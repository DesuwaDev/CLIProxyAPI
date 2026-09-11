package inventory

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

type Window struct {
	ID          string  `json:"id"`
	UsedPercent float64 `json:"used_percent"`
	StartMS     int64   `json:"start_ms"`
	ResetMS     int64   `json:"reset_ms"`
	ObservedMS  int64   `json:"observed_ms"`
}
type Account struct {
	FileName string   `json:"file_name"`
	Label    string   `json:"label"`
	Provider string   `json:"provider"`
	Windows  []Window `json:"windows"`
}
type Forecast struct {
	Window             Window   `json:"window"`
	Usage              Totals   `json:"usage"`
	EstimatedTotal     *float64 `json:"estimated_total_usd"`
	EstimatedRemaining *float64 `json:"estimated_remaining_usd"`
	Coverage           string   `json:"coverage"`
	Reason             string   `json:"reason"`
}

func Windows(signals map[string]string, observed time.Time) []Window {
	headers := http.Header{}
	for k, v := range signals {
		headers.Set(k, v)
	}
	out := []Window{}
	if observed.IsZero() {
		return out
	}
	for _, id := range []string{"primary", "secondary"} {
		prefix := "X-Codex-" + id + "-"
		used, e1 := strconv.ParseFloat(headers.Get(prefix+"Used-Percent"), 64)
		minutes, e2 := strconv.ParseInt(headers.Get(prefix+"Window-Minutes"), 10, 64)
		reset, e3 := strconv.ParseInt(headers.Get(prefix+"Reset-At"), 10, 64)
		if e3 != nil {
			seconds, err := strconv.ParseInt(headers.Get(prefix+"Reset-After-Seconds"), 10, 64)
			if err == nil && seconds >= 0 && seconds <= 60*86400 {
				reset = observed.Unix() + seconds
				e3 = nil
			}
		}
		if e1 != nil || e2 != nil || e3 != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 || minutes < 1 || minutes > 60*24*60 || reset < 1 || reset > observed.Unix()+60*86400 {
			continue
		}
		out = append(out, Window{ID: id, UsedPercent: used, StartMS: (reset - minutes*60) * 1000, ResetMS: reset * 1000, ObservedMS: observed.UnixMilli()})
	}
	// Claude OAuth windows use utilization as a fraction in response headers.
	for _, v := range []struct {
		id, prefix string
		seconds    int64
	}{{"five_hour", "anthropic-ratelimit-unified-5h-", 18000}, {"seven_day", "anthropic-ratelimit-unified-7d-", 604800}} {
		fraction, err := strconv.ParseFloat(headers.Get(v.prefix+"utilization"), 64)
		reset, errReset := strconv.ParseInt(headers.Get(v.prefix+"reset"), 10, 64)
		if err != nil || errReset != nil || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 || fraction > 1 || reset < 1 || reset > observed.Unix()+60*86400 {
			continue
		}
		out = append(out, Window{ID: v.id, UsedPercent: fraction * 100, StartMS: (reset - v.seconds) * 1000, ResetMS: reset * 1000, ObservedMS: observed.UnixMilli()})
	}
	return out
}
func Estimate(w Window, t Totals, firstMS, nowMS int64) Forecast {
	f := Forecast{Window: w, Usage: t, Coverage: "partial"}
	// Observations cannot establish usage outside this CPA or during collection gaps.
	_ = firstMS
	switch {
	case w.ResetMS <= nowMS:
		f.Reason = "expired_snapshot"
	case nowMS-w.ObservedMS > 15*60*1000:
		f.Reason = "stale_snapshot"
	case w.UsedPercent <= 0 || w.UsedPercent > 100:
		f.Reason = "no_quota_progress"
	case t.Requests == 0 || t.Cost <= 0:
		f.Reason = "no_priced_usage"
	case t.Unpriced > 0:
		f.Reason = "unpriced_requests"
	default:
		total := t.Cost * 100 / w.UsedPercent
		if math.IsInf(total, 0) || math.IsNaN(total) {
			f.Reason = "invalid_estimate"
			return f
		}
		remaining := math.Max(0, total-t.Cost)
		f.EstimatedTotal = &total
		f.EstimatedRemaining = &remaining
		f.Reason = "proportional_estimate"
	}
	return f
}
