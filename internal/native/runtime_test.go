package native

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/history"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func openTestRuntime(t *testing.T, path string) (*Runtime, *gin.Engine) {
	t.Helper()
	r, err := Open(config.NativeManagementConfig{}, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := r.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	g := gin.New()
	r.Register(g.Group("/v0/management"))
	return r, g
}

func request(t *testing.T, g *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/v0/management/native/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	g.ServeHTTP(w, req)
	return w
}

func requireOK(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestNativePersistencePricingPaginationAndSecretExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with spaces", "config.yaml")
	r, g := openTestRuntime(t, path)
	prices := `{"rules":[{"id":"standard","provider":"openai","model":"test-model","tier":"default","input_per_million":2,"output_per_million":10,"cache_read_per_million":0.5,"cache_write_per_million":3}]}`
	requireOK(t, request(t, g, "PUT", "pricing", prices))
	record := usage.Record{Provider: "openai", Model: "test-model", AuthIndex: "account-1", APIKey: "secret-api-key-canary", Source: "secret-source-canary", RequestedAt: time.Now(), Detail: usage.Detail{InputTokens: 1000, CachedTokens: 200, CacheReadTokens: 200, OutputTokens: 100, ReasoningTokens: 20, TotalTokens: 1100}}
	ctx := logging.WithRequestID(context.Background(), "same-request-id")
	r.HandleUsage(ctx, record)
	record.Failed = true
	record.Fail = usage.Failure{StatusCode: 401, Body: `{"error":{"code":"invalid_token","message":"secret-failure-canary"}}`}
	r.HandleUsage(ctx, record)
	w := request(t, g, "GET", "history/events?limit=1", "")
	requireOK(t, w)
	var first struct {
		Events []history.Row `json:"events"`
		Next   int64         `json:"next_before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 1 || first.Next == 0 {
		t.Fatalf("bad pagination: %s", w.Body.String())
	}
	e := first.Events[0]
	if e.CostUSD == nil || math.Abs(*e.CostUSD-0.0027) > 1e-12 || e.Tokens.Input.UncachedTokens != 800 {
		t.Fatalf("cache/reasoning double-counted: %+v", e)
	}
	if e.KeyHash != event.KeyHash(record.APIKey) || e.FailureCode != "invalid_token" {
		t.Fatalf("bad identity or classification: %+v", e)
	}
	requireOK(t, request(t, g, "PUT", "pricing", strings.ReplaceAll(prices, `"input_per_million":2`, `"input_per_million":20`)))
	w = request(t, g, "GET", "history/summary", "")
	requireOK(t, w)
	var totals struct {
		Groups []history.Aggregate `json:"groups"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &totals)
	if len(totals.Groups) != 1 || totals.Groups[0].Attempts != 2 || math.Abs(totals.Groups[0].Cost-0.0054) > 1e-12 {
		t.Fatalf("historical prices changed or retry deduplicated: %s", w.Body.String())
	}
	w = request(t, g, "GET", "history/export?limit=1", "")
	requireOK(t, w)
	if w.Header().Get("X-Next-Before") == "" || !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "X-Next-Before") {
		t.Fatal("export cursor must be visible to the original panel on custom connections")
	}
	for _, secret := range []string{record.APIKey, record.Source, "secret-failure-canary"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret present in export")
		}
		var count int
		if err := r.db.QueryRow("SELECT COUNT(*) FROM native_events WHERE instr(payload,?)>0", secret).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("secret present in database")
		}
	}
	requireOK(t, request(t, g, "PUT", "modules/pricing", `{"enabled":false}`))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r2, g2 := openTestRuntime(t, path)
	if r2.enabled("pricing") {
		t.Fatal("module toggle did not survive restart")
	}
	w = request(t, g2, "GET", "history/summary", "")
	requireOK(t, w)
	_ = json.Unmarshal(w.Body.Bytes(), &totals)
	if totals.Groups[0].Attempts != 2 {
		t.Fatal("history did not survive restart")
	}
	if got := request(t, g2, "GET", "history/summary?group=model;DROP%20TABLE%20native_events", "").Code; got != 400 {
		t.Fatalf("invalid group status=%d", got)
	}
}

func TestNativeConcurrentDeliveryDrainsBeforeDatabaseClose(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	m := usage.NewManager(10)
	m.Register(r)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				m.Publish(context.Background(), usage.Record{Provider: "openai", Model: "m", RequestedAt: time.Now(), Detail: usage.Detail{InputTokens: 1, TotalTokens: 1}})
			}
		}()
	}
	wg.Wait()
	m.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	w := request(t, g, "GET", "history/summary", "")
	requireOK(t, w)
	var totals struct {
		Groups []history.Aggregate `json:"groups"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &totals)
	if len(totals.Groups) != 1 || totals.Groups[0].Attempts != 200 || totals.Groups[0].Total != 200 || r.failed.Load() != 0 {
		t.Fatalf("lost events: %s", w.Body.String())
	}
}

func TestNativeDisabledHistoryAndConservativeAccountQueue(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	requireOK(t, request(t, g, "PUT", "modules/history", `{"enabled":false}`))
	r.HandleUsage(context.Background(), usage.Record{AuthIndex: "account", Failed: true, Fail: usage.Failure{StatusCode: 403, Body: `{"error":{"message":"blocked in region"}}`}})
	if got := request(t, g, "GET", "history/events", "").Code; got != 409 {
		t.Fatalf("disabled history status=%d", got)
	}
	w := request(t, g, "GET", "accounts/actions", "")
	requireOK(t, w)
	if !strings.Contains(w.Body.String(), "authorization_review") || !strings.Contains(w.Body.String(), `"automatic_credential_changes":false`) {
		t.Fatalf("unsafe policy: %s", w.Body.String())
	}
	var count int
	_ = r.db.QueryRow("SELECT COUNT(*) FROM native_events").Scan(&count)
	if count != 0 {
		t.Fatal("disabled history still recorded events")
	}
}
