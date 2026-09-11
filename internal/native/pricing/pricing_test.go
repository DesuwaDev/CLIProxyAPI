package pricing

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPricingTierContextAndUnknownAccounting(t *testing.T) {
	m := &Module{rules: []Rule{
		{ID: "base", Provider: "codex", Model: "m", Tier: "auto", Input: 1, Output: 2},
		{ID: "long", Provider: "codex", Model: "m", Tier: "auto", MinContext: 1000, Input: 3, Output: 4},
	}}
	for _, tt := range []struct {
		name, tier string
		input      int64
		quality    usage.TokenAccountingQuality
		want       *float64
	}{
		{"base", "auto", 999, usage.TokenAccountingQualityComplete, ptr(999.0 / 1e6)},
		{"threshold", "auto", 1000, usage.TokenAccountingQualityComplete, ptr(3000.0 / 1e6)},
		{"unknown tier", "priority", 1000, usage.TokenAccountingQualityComplete, nil},
		{"unknown accounting", "auto", 1000, usage.TokenAccountingQualityUnclassified, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := event.Event{Provider: "codex", Model: "m", ServiceTier: tt.tier, Generate: true, Tokens: usage.NewSubsetTokenBreakdown(tt.input, 0, 0, 0, 0, tt.input)}
			if tt.quality != usage.TokenAccountingQualityComplete {
				e.Tokens = usage.NewUnclassifiedTokenBreakdown(tt.input)
			}
			m.Estimate(&e)
			if (e.CostUSD == nil) != (tt.want == nil) {
				t.Fatalf("cost=%v want=%v", e.CostUSD, tt.want)
			}
			if tt.want != nil && math.Abs(*e.CostUSD-*tt.want) > 1e-12 {
				t.Fatalf("cost=%g want=%g", *e.CostUSD, *tt.want)
			}
		})
	}
	var rule Rule
	if err := json.Unmarshal([]byte(`{"id":"missing-rates"}`), &rule); err == nil {
		t.Fatal("omitted prices must not silently become zero")
	}
}

func ptr(v float64) *float64 { return &v }
