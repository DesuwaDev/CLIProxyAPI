package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCatalogFallbackOverrideAndFailurePersistence(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"m":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"input_cost_per_token_above_128k_tokens":0.000003},"unsupported":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"input_cost_per_token_above_64k_tokens":0.000003}}`))
	}))
	defer server.Close()
	db, err := storage.Open(filepath.Join(t.TempDir(), "prices.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	m.catalog.settings.Source = server.URL
	if m.catalog.settings.Automatic {
		t.Fatal("automatic downloads must default off")
	}
	if err = m.SyncCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	hash := m.catalog.data.Hash
	if len(m.catalog.data.Models) != 2 || m.catalog.data.Skipped != 0 {
		t.Fatal("dynamic context thresholds must be supported")
	}
	estimate := func(input, cache int64, tier string) *float64 {
		e := event.Event{Model: "m", ServiceTier: tier, Generate: true, Tokens: usage.NewSubsetTokenBreakdown(input, cache, 0, 0, 0, input)}
		m.Estimate(&e)
		return e.CostUSD
	}
	for _, tc := range []struct {
		input int64
		want  float64
	}{{128000, 0.128}, {128001, 0.384003}} {
		got := estimate(tc.input, 0, "")
		if got == nil || math.Abs(*got-tc.want) > 1e-10 {
			t.Fatalf("threshold %d: %v", tc.input, got)
		}
	}
	if estimate(100, 20, "") != nil || estimate(100, 0, "priority") != nil {
		t.Fatal("unknown cache/tier must not become a standard estimate")
	}
	m.rules = []Rule{{ID: "override", Provider: "*", Model: "m", Tier: "*", Input: 9, CacheRead: 2}}
	got := estimate(100, 20, "priority")
	if got == nil || math.Abs(*got-0.00076) > 1e-12 {
		t.Fatal("local override did not win")
	}
	fail.Store(true)
	if err = m.SyncCatalog(context.Background()); err == nil {
		t.Fatal("expected source failure")
	}
	if m.catalog.data.Hash != hash {
		t.Fatal("failed sync replaced catalog")
	}
	reopened, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.catalog.data.Hash != hash {
		t.Fatal("catalog cache did not persist")
	}
}
