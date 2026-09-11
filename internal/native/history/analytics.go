package history

import (
	"database/sql"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
)

type Percentiles struct {
	Samples int64    `json:"samples"`
	Average *float64 `json:"average_ms"`
	P50     *int64   `json:"p50_ms"`
	P95     *int64   `json:"p95_ms"`
	P99     *int64   `json:"p99_ms"`
	Max     *int64   `json:"max_ms"`
}

func percentiles(c *gin.Context, db *sql.Tx, f Filter, column, condition string) (Percentiles, error) {
	// Column and condition are internal constants; all user filters remain bound values.
	q := `WITH samples AS (SELECT ` + column + ` AS v,ROW_NUMBER() OVER(ORDER BY ` + column + `) AS rn,COUNT(*) OVER() AS n FROM native_events WHERE ` + f.Where + ` AND ` + condition + `) SELECT COUNT(*),AVG(v),MAX(CASE WHEN rn=(n*50+99)/100 THEN v END),MAX(CASE WHEN rn=(n*95+99)/100 THEN v END),MAX(CASE WHEN rn=(n*99+99)/100 THEN v END),MAX(v) FROM samples`
	var p Percentiles
	err := db.QueryRowContext(c.Request.Context(), q, f.Args...).Scan(&p.Samples, &p.Average, &p.P50, &p.P95, &p.P99, &p.Max)
	return p, err
}
func (m *Module) analytics(c *gin.Context) {
	f, ok := filter(c)
	if !ok {
		return
	}
	// A single read snapshot keeps all metrics consistent with concurrent retention.
	tx, err := m.DB.BeginTx(c.Request.Context(), nil)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var ceiling int64
	if err := tx.QueryRowContext(c.Request.Context(), "SELECT COALESCE(MAX(seq),0) FROM native_events").Scan(&ceiling); err != nil {
		httpx.Error(c, err)
		return
	}
	f.Where += " AND seq<=?"
	f.Args = append(f.Args, ceiling)
	duration, err := percentiles(c, tx, f, "latency_ms", "failed=0 AND latency_observed=1")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	ttft, err := percentiles(c, tx, f, "ttft_ms", "failed=0 AND ttft_ms IS NOT NULL")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var attempts, tokens, input, cache int64
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT COUNT(*),COALESCE(SUM(total_tokens),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(cache_read_tokens),0) FROM native_events WHERE "+f.Where, f.Args...).Scan(&attempts, &tokens, &input, &cache); err != nil {
		httpx.Error(c, err)
		return
	}
	var cacheRate *float64
	if input > 0 {
		v := float64(cache) / float64(input)
		cacheRate = &v
	}
	type bucket struct {
		Label string `json:"label"`
		Count int64  `json:"count"`
	}
	ranges := []int64{100, 500, 1000, 3000, 10000, 30000}
	buckets := []bucket{}
	selects := ""
	low := int64(0)
	for i, high := range ranges {
		if i > 0 {
			selects += ","
		}
		selects += fmt.Sprintf("COALESCE(SUM(latency_ms>=%d AND latency_ms<%d),0)", low, high)
		buckets = append(buckets, bucket{Label: fmt.Sprintf("%d–%d ms", low, high)})
		low = high
	}
	selects += ",COALESCE(SUM(latency_ms>=30000),0)"
	buckets = append(buckets, bucket{Label: "≥30000 ms"})
	dest := []any{}
	for i := range buckets {
		dest = append(dest, &buckets[i].Count)
	}
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT "+selects+" FROM native_events WHERE "+f.Where+" AND failed=0 AND latency_observed=1", f.Args...).Scan(dest...); err != nil {
		httpx.Error(c, err)
		return
	}
	minutes := float64(f.To-f.From) / 60000
	// Release the connection before writing to a potentially slow client.
	if err = tx.Rollback(); err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(200, gin.H{"duration": duration, "ttft": ttft, "histogram": buckets, "rpm": float64(attempts) / minutes, "tpm": float64(tokens) / minutes, "cache_read_rate": cacheRate, "rate_scope": "selected_window_provider_attempts", "latency_scope": "successful_observed_attempts", "percentile_method": "nearest_rank", "snapshot": ceiling})
}
