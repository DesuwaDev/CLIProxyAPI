package history

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
)

type Filter struct {
	Where    string
	Args     []any
	Limit    int
	Before   int64
	From     int64
	To       int64
	Sort     string
	Cursor   *pageCursor
	Snapshot *int64
}

func ParseFilter(c *gin.Context) (Filter, error) {
	if _, err := url.ParseQuery(c.Request.URL.RawQuery); err != nil {
		return Filter{}, fmt.Errorf("malformed query string")
	}
	now := time.Now().UnixMilli()
	f := Filter{Where: "timestamp_ms>=? AND timestamp_ms<?", Limit: 100}
	from, to := now-int64(24*time.Hour/time.Millisecond), now+1
	for _, p := range []struct {
		name string
		dest *int64
	}{{"from", &from}, {"to", &to}, {"before", &f.Before}} {
		if raw := c.Query(p.name); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 {
				return f, fmt.Errorf("%s must be a non-negative integer", p.name)
			}
			*p.dest = v
		}
	}
	if to <= from || to-from > int64(366*24*time.Hour/time.Millisecond) {
		return f, fmt.Errorf("select a time range of at most 366 days")
	}
	f.From, f.To = from, to
	f.Args = []any{from, to}
	if err := parseAdvanced(c, &f); err != nil {
		return f, err
	}
	for _, name := range []string{"provider", "model", "account", "key_hash", "request_id", "trace_id"} {
		if v := c.Query(name); v != "" {
			if len(v) > 1024 {
				return f, fmt.Errorf("filter is too long")
			}
			f.Where += " AND " + name + "=?"
			f.Args = append(f.Args, v)
		}
	}
	if v := c.Query("failed"); v != "" {
		if v != "true" && v != "false" {
			return f, fmt.Errorf("failed must be true or false")
		}
		f.Where += " AND failed=?"
		f.Args = append(f.Args, v == "true")
	}
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return f, fmt.Errorf("limit must be between 1 and 500")
		}
		f.Limit = n
	}
	return f, nil
}

type Row struct {
	Sequence int64 `json:"sequence"`
	event.Event
}
type Aggregate struct {
	Group      string  `json:"group"`
	Attempts   int64   `json:"attempts"`
	Failures   int64   `json:"failures"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	Reasoning  int64   `json:"reasoning_tokens"`
	Total      int64   `json:"total_tokens"`
	Cost       float64 `json:"estimated_cost_usd"`
	Unpriced   int64   `json:"unpriced_attempts"`
	Latency    float64 `json:"average_latency_ms"`
}

func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/events", m.events)
	g.GET("/analytics", m.analytics)
	g.GET("/models", m.models)
	g.GET("/trace/:id", m.trace)
	g.GET("/summary", m.summary)
	g.GET("/export", m.export)
	g.GET("/aliases", m.aliases)
	g.PUT("/aliases", m.putAlias)
}

func filter(c *gin.Context) (Filter, bool) {
	f, err := ParseFilter(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return f, false
	}
	return f, true
}

func (m *Module) rows(c *gin.Context, f Filter) ([]Row, error) {
	where, args := f.Where, append([]any{}, f.Args...)
	order, err := m.pageWhere(c, f, &where, &args)
	if err != nil {
		return nil, err
	}
	args = append(args, f.Limit+1)
	rows, err := m.DB.QueryContext(c.Request.Context(), "SELECT seq,payload FROM native_events WHERE "+where+" ORDER BY "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []Row{}
	for rows.Next() {
		var row Row
		var raw string
		if err = rows.Scan(&row.Sequence, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &row.Event); err != nil {
			return nil, err
		}
		// V1 stored positive timings but had no explicit observation flags.
		if row.LatencyMS > 0 {
			row.LatencyObserved = true
		}
		if row.TTFTObservedMS == nil && row.Stream && row.TTFTMS > 0 {
			value := row.TTFTMS
			row.TTFTObservedMS = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (m *Module) events(c *gin.Context) {
	f, ok := filter(c)
	if !ok {
		return
	}
	rows, err := m.rows(c, f)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	next := int64(0)
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		next = rows[len(rows)-1].Sequence
	}
	cursor := ""
	if next > 0 {
		cursor = encodeCursor(f, rows[len(rows)-1])
	}
	c.JSON(200, gin.H{"events": rows, "next_before": next, "next_cursor": cursor, "snapshot": *f.Snapshot, "count_semantics": "provider_attempts"})
}

func (m *Module) summary(c *gin.Context) {
	f, ok := filter(c)
	if !ok {
		return
	}
	groups := map[string]string{"": "''", "model": "model", "provider": "provider", "account": "account", "key": "key_hash", "status": "CAST(status_code AS TEXT)", "hour": "CAST(timestamp_ms/3600000*3600000 AS TEXT)", "day": "CAST(timestamp_ms/86400000*86400000 AS TEXT)"}
	expr, ok := groups[c.Query("group")]
	if !ok {
		c.JSON(400, gin.H{"error": "Unknown grouping dimension."})
		return
	}
	query := `SELECT ` + expr + `,COUNT(*),COALESCE(SUM(failed),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_write_tokens),0),COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(total_tokens),0),COALESCE(SUM(cost_usd),0),COALESCE(SUM(cost_usd IS NULL),0),COALESCE(AVG(latency_ms),0) FROM native_events WHERE ` + f.Where
	if c.Query("group") != "" {
		query += " GROUP BY " + expr
	}
	query += " ORDER BY COUNT(*) DESC LIMIT 1001"
	rows, err := m.DB.QueryContext(c.Request.Context(), query, f.Args...)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := []Aggregate{}
	for rows.Next() {
		var a Aggregate
		if err = rows.Scan(&a.Group, &a.Attempts, &a.Failures, &a.Input, &a.Output, &a.CacheRead, &a.CacheWrite, &a.Reasoning, &a.Total, &a.Cost, &a.Unpriced, &a.Latency); err != nil {
			httpx.Error(c, err)
			return
		}
		items = append(items, a)
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	truncated := len(items) > 1000
	if truncated {
		items = items[:1000]
	}
	c.JSON(200, gin.H{"groups": items, "truncated": truncated, "count_semantics": "provider_attempts", "timezone": "UTC"})
}

// Export is cursor-paged like events, so a slow download never holds a DB lock.
func (m *Module) export(c *gin.Context) {
	f, ok := filter(c)
	if !ok {
		return
	}
	rows, err := m.rows(c, f)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		c.Header("X-Next-Before", strconv.FormatInt(rows[len(rows)-1].Sequence, 10))
		c.Header("X-Next-Cursor", encodeCursor(f, rows[len(rows)-1]))
	}
	exposed := c.Writer.Header().Get("Access-Control-Expose-Headers")
	if exposed != "" {
		exposed += ", "
	}
	c.Header("Access-Control-Expose-Headers", exposed+"X-Next-Before, X-Next-Cursor")
	c.Header("Content-Disposition", `attachment; filename="cpa-requests.jsonl"`)
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	enc := json.NewEncoder(c.Writer)
	for _, row := range rows {
		if err = enc.Encode(row.Event); err != nil {
			return
		}
	}
}

func (m *Module) aliases(c *gin.Context) {
	rows, err := m.DB.QueryContext(c.Request.Context(), "SELECT key_hash,label FROM native_aliases ORDER BY label")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	result := map[string]string{}
	for rows.Next() {
		var key, label string
		if err = rows.Scan(&key, &label); err != nil {
			httpx.Error(c, err)
			return
		}
		result[key] = label
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(200, result)
}

func (m *Module) putAlias(c *gin.Context) {
	var input struct {
		KeyHash string `json:"key_hash"`
		Label   string `json:"label"`
	}
	if !httpx.Decode(c, &input) {
		return
	}
	if len(input.KeyHash) != 64 || strings.Trim(input.KeyHash, "0123456789abcdef") != "" || len(input.Label) > 128 {
		c.JSON(400, gin.H{"error": "Use a SHA-256 key identifier and a label up to 128 bytes."})
		return
	}
	var err error
	if m.AliasUpdater != nil {
		err = m.AliasUpdater(c.Request.Context(), input.KeyHash, strings.TrimSpace(input.Label))
	} else {
		_, err = m.DB.ExecContext(c.Request.Context(), "INSERT INTO native_aliases(key_hash,label) VALUES(?,?) ON CONFLICT(key_hash) DO UPDATE SET label=excluded.label", input.KeyHash, strings.TrimSpace(input.Label))
	}
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(200, gin.H{"saved": true})
}
