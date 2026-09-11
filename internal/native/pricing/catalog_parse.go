package pricing

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
)

var openAIReasoningModel = regexp.MustCompile("^(?:openai/)?o[0-9]")
var aboveTokens = regexp.MustCompile(`_above_([0-9]+)(k|m)?_tokens`)
var priceNames = []string{"input_cost_per_token", "output_cost_per_token", "cache_read_input_token_cost", "cache_creation_input_token_cost"}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "auto", "default", "standard":
		return "standard"
	case "fast":
		return "fast"
	case "batches", "batch":
		return "batch"
	default:
		return strings.ToLower(strings.TrimSpace(tier))
	}
}
func modelTier(tier, model, provider string) string {
	tier = normalizeTier(tier)
	openai := provider == "codex" || provider == "openai" || provider == "azure" || strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "chatgpt-") || openAIReasoningModel.MatchString(model)
	if tier == "priority" && openai {
		return "fast"
	}
	return tier
}
func effectiveTier(e *event.Event) string {
	if e.ResponseTier != "" {
		return modelTier(e.ResponseTier, e.Model, e.Provider)
	}
	if e.ServiceTier != "" {
		return modelTier(e.ServiceTier, e.Model, e.Provider)
	}
	return modelTier(e.RequestedTier, e.Model, e.Provider)
}

func parseCatalog(raw []byte) (catalogData, error) {
	data := catalogData{Models: map[string][]catalogRate{}}
	var entries map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return data, fmt.Errorf("price catalog must be a JSON object")
	}
	if len(entries) > 30000 {
		return data, fmt.Errorf("too many catalog entries")
	}
	for model, body := range entries {
		var fields map[string]json.RawMessage
		if model == "" || len(model) > 256 || json.Unmarshal(body, &fields) != nil || fields == nil {
			data.Skipped++
			continue
		}
		var provider string
		_ = json.Unmarshal(fields["litellm_provider"], &provider)
		// Normalize both suffix orders without inheriting prices between service tiers.
		type key struct {
			tier      string
			minimum   int64
			component int
			hour      bool
		}
		values := map[key]*float64{}
		unsafeContext := false
		tiers := map[string]bool{}
		thresholds := map[int64]bool{0: true}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			component := -1
			suffix := ""
			for i, prefix := range priceNames {
				if strings.HasPrefix(name, prefix) {
					component = i
					suffix = strings.TrimPrefix(name, prefix)
					break
				}
			}
			if component < 0 {
				continue
			}
			minimum := int64(0)
			if match := aboveTokens.FindStringSubmatch(suffix); match != nil {
				n, err := strconv.ParseInt(match[1], 10, 64)
				if err != nil || n <= 0 || n > 1e9 {
					unsafeContext = true
					continue
				}
				if match[2] == "k" {
					n *= 1000
				} else if match[2] == "m" {
					n *= 1000000
				}
				minimum = n + 1
				suffix = strings.Replace(suffix, match[0], "", 1)
			}
			hour := strings.Contains(suffix, "_above_1hr")
			suffix = strings.Replace(suffix, "_above_1hr", "", 1)
			tier := modelTier(strings.TrimPrefix(suffix, "_"), model, provider)
			switch tier {
			case "standard", "fast", "priority", "flex", "batch":
			default:
				if strings.Contains(suffix, "_above_") {
					unsafeContext = true
				}
				continue
			}
			var v float64
			var value *float64
			if string(fields[name]) != "null" && json.Unmarshal(fields[name], &v) == nil && v >= 0 && v <= 1 {
				v *= 1e6
				value = &v
			}
			k := key{tier, minimum, component, hour}
			// Explicit fast fields supersede the legacy priority alias deterministically.
			if _, exists := values[k]; !exists || strings.HasSuffix(name, "_fast") {
				values[k] = value
			}
			tiers[tier] = true
			thresholds[minimum] = true
		}
		if len(values) == 0 || unsafeContext {
			data.Skipped++
			continue
		}
		mins := make([]int64, 0, len(thresholds))
		for n := range thresholds {
			mins = append(mins, n)
		}
		sort.Slice(mins, func(i, j int) bool { return mins[i] < mins[j] })
		rates := []catalogRate{}
		for _, tier := range []string{"standard", "fast", "priority", "flex", "batch"} {
			if !tiers[tier] {
				continue
			}
			previous := [4]*float64{}
			var previousHour *float64
			previousHasTTL := false
			for _, minimum := range mins {
				current := previous
				currentHour := previousHour
				currentHasTTL := previousHasTTL
				found := false
				for i := range current {
					if v, ok := values[key{tier, minimum, i, false}]; ok {
						current[i] = v
						found = true
					}
				}
				// A model's long-context boundary also applies to other service levels.
				// Missing rates there mean unsupported/unknown, not the short-context price.
				if _, ok := values[key{tier, minimum, 3, true}]; ok {
					found = true
				}
				if minimum > 0 && !found {
					current = [4]*float64{}
					currentHour = nil
					currentHasTTL = false
				}
				if hour, ok := values[key{tier, minimum, 3, true}]; ok {
					currentHour = hour
					currentHasTTL = true
				}
				rates = append(rates, catalogRate{HasCacheTTL: currentHasTTL, CacheWrite1h: currentHour, Tier: tier, Minimum: minimum, Input: current[0], Output: current[1], CacheRead: current[2], CacheWrite: current[3]})
				previous = current
				previousHour = currentHour
				previousHasTTL = currentHasTTL
			}
		}
		hasTokenRates := false
		for _, rate := range rates {
			if rate.Input != nil && rate.Output != nil {
				hasTokenRates = true
				break
			}
		}
		if !hasTokenRates {
			data.Skipped++
			continue
		}
		data.Models[model] = rates
	}
	if len(data.Models) == 0 {
		return data, fmt.Errorf("no supported token prices found; previous catalog retained")
	}
	return data, nil
}

// Called under m.mu.RLock only after no local rule matched.
func (m *Module) estimateCatalog(e *event.Event) {
	rates := m.catalog.data.Models[e.Model]
	var selected *catalogRate
	tier := effectiveTier(e)
	for i := range rates {
		r := &rates[i]
		if modelTier(r.Tier, e.Model, e.Provider) == tier && r.Minimum <= e.Tokens.Input.TotalTokens && (selected == nil || r.Minimum > selected.Minimum) {
			selected = r
		}
	}
	if selected == nil {
		return
	}
	pairs := []struct {
		tokens int64
		price  *float64
	}{{e.Tokens.Input.UncachedTokens, selected.Input}, {e.Tokens.Output.TotalTokens, selected.Output}, {e.Tokens.Input.CacheReadTokens, selected.CacheRead}}
	if e.Tokens.Input.CacheWriteTokens > 0 {
		if e.CacheWriteTTLObserved && e.CacheWrite5m >= 0 && e.CacheWrite1h >= 0 && e.CacheWrite5m <= e.Tokens.Input.CacheWriteTokens && e.CacheWrite1h == e.Tokens.Input.CacheWriteTokens-e.CacheWrite5m {
			pairs = append(pairs, struct {
				tokens int64
				price  *float64
			}{e.CacheWrite5m, selected.CacheWrite}, struct {
				tokens int64
				price  *float64
			}{e.CacheWrite1h, selected.CacheWrite1h})
		} else {
			if (selected.HasCacheTTL || selected.CacheWrite1h != nil) && (selected.CacheWrite1h == nil || selected.CacheWrite == nil || *selected.CacheWrite1h != *selected.CacheWrite) {
				return
			}
			pairs = append(pairs, struct {
				tokens int64
				price  *float64
			}{e.Tokens.Input.CacheWriteTokens, selected.CacheWrite})
		}
	}
	cost := 0.0
	for _, p := range pairs {
		if p.tokens == 0 {
			continue
		}
		if p.price == nil {
			return
		}
		cost += float64(p.tokens) * *p.price / 1e6
	}
	e.CostUSD = &cost
	e.PricingRule = fmt.Sprintf("catalog:%s:%s:%s:%d", m.catalog.data.Hash, e.Model, tier, selected.Minimum)
}
