package pricing

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"math"
	"testing"
)

func TestCatalogActualTierDynamicBoundaryAndMissingRates(t *testing.T) {
	raw := []byte(`{"m":{"litellm_provider":"openai","input_cost_per_token":0.000004,"output_cost_per_token":0.00002,"cache_read_input_token_cost":0.0000004,"input_cost_per_token_priority":0.000008,"output_cost_per_token_priority":0.00004,"cache_read_input_token_cost_priority":0.0000008,"input_cost_per_token_above_272k_tokens":0.000008,"output_cost_per_token_above_272k_tokens":0.00003,"cache_read_input_token_cost_above_272k_tokens":0.0000008,"input_cost_per_token_above_272k_tokens_priority":0.000016,"output_cost_per_token_priority_above_272k_tokens":0.00006,"cache_read_input_token_cost_above_272k_tokens_priority":0.0000016},"missing":{"litellm_provider":"openai","input_cost_per_token":0.000002,"output_cost_per_token":0.00001,"input_cost_per_token_priority":0.000005,"output_cost_per_token_priority":0.00003,"input_cost_per_token_above_272k_tokens":0.000004},"ttl":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"cache_creation_input_token_cost":0.00000375,"cache_creation_input_token_cost_above_1hr":0.000006}}`)
	data, err := parseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	m := &Module{catalog: catalogState{data: data}}
	for _, tc := range []struct {
		model, request, response string
		input, cache             int64
		want                     *float64
	}{
		{"m", "fast", "", 272000, 1000, ptr((271000*8.0 + 1000*0.8 + 100*40) / 1e6)},
		{"m", "priority", "fast", 272001, 1000, ptr((271001*16.0 + 1000*1.6 + 100*60) / 1e6)},
		{"m", "fast", "default", 272001, 1000, ptr((271001*8.0 + 1000*0.8 + 100*30) / 1e6)},
		{"m", "auto", "priority", 272000, 0, ptr((272000*8.0 + 100*40) / 1e6)},
		{"missing", "priority", "", 272001, 0, nil},
		{"m", "unrecognized", "", 1000, 0, nil},
	} {
		e := event.Event{Provider: "codex", Model: tc.model, ServiceTier: tc.request, ResponseTier: tc.response, Generate: true, Tokens: usage.NewSubsetTokenBreakdown(tc.input, tc.cache, 0, 100, 0, tc.input+100)}
		m.Estimate(&e)
		if (e.CostUSD == nil) != (tc.want == nil) {
			t.Fatalf("%+v cost=%v", tc, e.CostUSD)
		}
		if tc.want != nil && math.Abs(*e.CostUSD-*tc.want) > 1e-10 {
			t.Fatalf("%+v cost=%g", tc, *e.CostUSD)
		}
	}
	e := event.Event{Provider: "codex", Model: "ttl", Generate: true, Tokens: usage.NewSubsetTokenBreakdown(1000, 0, 100, 0, 0, 1000)}
	m.Estimate(&e)
	if e.CostUSD != nil {
		t.Fatal("ambiguous write TTL must remain unpriced")
	}
	e.CacheWriteTTLObserved, e.CacheWrite5m, e.CacheWrite1h = true, 40, 60
	m.Estimate(&e)
	wantTTL := (900*3.0 + 40*3.75 + 60*6.0) / 1e6
	if e.CostUSD == nil || math.Abs(*e.CostUSD-wantTTL) > 1e-12 {
		t.Fatal("mixed 5m/1h cache writes priced incorrectly", e.CostUSD)
	}
	if modelTier("priority", "claude-opus-5", "anthropic") == modelTier("fast", "claude-opus-5", "anthropic") {
		t.Fatal("Anthropic priority commitments must not be treated as fast speed")
	}
	m.rules = []Rule{{ID: "manual-fast", Provider: "*", Model: "m", Tier: "priority", Input: 10, Output: 20}}
	e = event.Event{Provider: "codex", Model: "m", ServiceTier: "auto", ResponseTier: "fast", Generate: true, Tokens: usage.NewSubsetTokenBreakdown(1000, 0, 0, 0, 0, 1000)}
	m.Estimate(&e)
	if e.PricingRule != "manual-fast" {
		t.Fatal("manual actual-tier override did not win")
	}
}
