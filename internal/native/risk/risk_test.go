package risk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

func fixture(t *testing.T) *Module {
	t.Helper()
	path := filepath.Join(t.TempDir(), "risk.sqlite")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(db, path+".key")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(); _ = db.Close() })
	return m
}
func save(t *testing.T, m *Module, c Config) Config {
	t.Helper()
	c.Version = m.snapshot().Version
	out, err := m.Save(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestRiskReviewFailoverSecretsAndModes(t *testing.T) {
	m := fixture(t)
	var calls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer reviewer-secret-canary" {
			t.Error("reviewer token missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"safe":false,"category_scores":{"cyber":0.99}}`}}}})
	}))
	defer good.Close()
	c := defaults()
	c.Mode = "block"
	c.Endpoints = []Endpoint{{ID: "bad", URL: bad.URL, Protocol: "guard_json", Model: "test", Enabled: true}, {ID: "good", URL: good.URL, Protocol: "guard_json", Model: "test", Enabled: true, Token: "reviewer-secret-canary"}}
	out := save(t, m, c)
	if !out.Endpoints[1].HasToken || out.Endpoints[1].Token != "" || out.Endpoints[1].Ciphertext != "" {
		t.Fatal("token not write-only")
	}
	var persisted string
	if err := m.db.QueryRow("SELECT payload FROM native_risk_config").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "reviewer-secret-canary") {
		t.Fatal("plaintext token persisted")
	}
	input := Input{Body: []byte(`{"input":"private-prompt-canary"}`), Provider: "codex", Model: "test", KeyHash: "caller-a", Session: "session"}
	err := m.Check(context.Background(), input)
	var rejected *Rejection
	if !errors.As(err, &rejected) || rejected.Status != 403 || calls.Load() != 1 {
		t.Fatalf("not blocked: %v calls=%d", err, calls.Load())
	}
	if err = m.Check(context.Background(), input); err == nil || calls.Load() != 1 {
		t.Fatal("flagged hash was not reused")
	}
	rows, err := m.db.Query("SELECT payload FROM native_risk_events")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		if strings.Contains(raw, "private-prompt-canary") || strings.Contains(raw, "reviewer-secret-canary") {
			t.Error("secret persisted in event")
		}
	}
	_ = rows.Close()
	// Observe never rejects, including known blocked content.
	c = m.snapshot()
	c.Mode = "observe"
	save(t, m, c)
	if err = m.Check(context.Background(), input); err != nil {
		t.Fatalf("observe rejected: %v", err)
	}
	c = m.snapshot()
	c.Mode = "off"
	save(t, m, c)
	if err = m.Check(context.Background(), Input{Body: []byte("not json"), Provider: "codex"}); err != nil {
		t.Fatal("off mode touched body")
	}
}
func TestRiskInputCompletenessAndStrictVerdicts(t *testing.T) {
	body := []byte(`{"instructions":"review canary","input":[{"role":"user","content":"old turn"},{"role":"assistant","content":"old answer"},{"role":"user","content":[{"type":"input_text","text":"new turn"},{"type":"input_image","image_url":"data:image/png;base64,skip-me"}]},{"type":"function_call","arguments":"{\"command\":\"tool-canary\"}"},{"type":"function_call_output","output":"result-canary"}]}`)
	text, err := Extract(body, true, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"review canary", "new turn", "tool-canary", "result-canary"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %s", s)
		}
	}
	for _, s := range []string{"old turn", "old answer", "skip-me"} {
		if strings.Contains(text, s) {
			t.Errorf("unexpected %s", s)
		}
	}
	if _, err = Extract(body, false, 10); err == nil {
		t.Fatal("oversize input was silently truncated")
	}

	encoded := []byte(`{"contents":[{"parts":[{"functionCall":{"args":{"command":"\u0061udit-tool-canary"}}},{"functionResponse":{"response":{"result":"\u0061udit-result-canary"}}}]}],"input":[{"type":"function_call_output","output":"\u0061udit-output-canary"}]}`)
	extracted, extractErr := Extract(encoded, false, 10000)
	if extractErr != nil {
		t.Fatal(extractErr)
	}
	for _, expected := range []string{"audit-tool-canary", "audit-result-canary", "audit-output-canary"} {
		if !strings.Contains(extracted, expected) {
			t.Fatalf("escaped tool payload omitted: %s", expected)
		}
	}
	for _, value := range []string{`{"choices":[]}`, `{"choices":[{"message":{"content":"{}"}}]}`, `{"results":[{"flagged":false}]}`} {
		protocol := "guard_json"
		if strings.Contains(value, "results") {
			protocol = "moderations"
		}
		if _, err = parseResponse([]byte(value), protocol, map[string]float64{}); err == nil {
			t.Fatalf("invalid verdict accepted: %s", value)
		}
	}
	if r, err := parseQwen("Safety: Unsafe\nCategories: Non-violent Illegal Acts", nil); err != nil || r.Decision != "block" {
		t.Fatalf("Qwen: %+v %v", r, err)
	}
}
func TestRiskFailClosedSessionIsolationAndExpiry(t *testing.T) {
	m := fixture(t)
	now := time.Unix(1800000000, 0)
	m.now = func() time.Time { return now }
	c := defaults()
	c.Mode = "block"
	c.Strategy = "keywords"
	c.Rules = []Rule{{ID: "r", Pattern: "local-block-canary", Enabled: true}}
	c.SessionBlock = true
	c.HashBlock = false
	save(t, m, c)
	a := Input{Provider: "codex", Model: "test", KeyHash: "key-a", Session: "shared", Body: []byte(`{"input":"safe"}`)}
	m.ObserveUpstream(context.Background(), a, errors.New(`{"error":{"code":"cyber_policy"}}`))
	if err := m.Check(context.Background(), a); err == nil {
		t.Fatal("session not blocked")
	}
	b := a
	b.KeyHash = "key-b"
	if err := m.Check(context.Background(), b); err != nil {
		t.Fatal("session leaked across keys")
	}
	now = now.Add(time.Duration(c.SessionTTL+1) * time.Second)
	if err := m.Check(context.Background(), a); err != nil {
		t.Fatal("session did not expire")
	}
	a.Body = []byte("invalid")
	if err := m.Check(context.Background(), a); err == nil {
		t.Fatal("invalid review input failed open")
	}
	c = m.snapshot()
	c.FailClosed = false
	save(t, m, c)
	if err := m.Check(context.Background(), a); err != nil {
		t.Fatal("configured fail-open ignored")
	}
	// No exact error code, no session block.
	a.Body = []byte(`{"input":"safe"}`)
	m.ObserveUpstream(context.Background(), a, errors.New(`{"error":{"message":"cyber_policy"}}`))
	if err := m.Check(context.Background(), a); err != nil {
		t.Fatal("substring caused session block")
	}
}
