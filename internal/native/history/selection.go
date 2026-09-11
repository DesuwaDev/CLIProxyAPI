package history

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
)

type pageCursor struct {
	Sequence  int64  `json:"s"`
	Timestamp int64  `json:"t,omitempty"`
	Latency   int64  `json:"l"`
	Snapshot  int64  `json:"m"`
	Sort      string `json:"o"`
}

func parseAdvanced(c *gin.Context, f *Filter) error {
	f.Sort = c.DefaultQuery("sort", "time")
	if f.Sort != "time" && f.Sort != "recent" && f.Sort != "latency" {
		return fmt.Errorf("sort must be time, recent or latency")
	}
	f.Snapshot = new(int64)
	for _, v := range []struct{ name, op string }{{"min_latency", ">="}, {"max_latency", "<="}} {
		if raw := c.Query(v.name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid latency filter")
			}
			f.Where += " AND latency_observed=1 AND latency_ms" + v.op + "?"
			f.Args = append(f.Args, n)
		}
	}
	if raw := c.Query("cursor"); raw != "" {
		if len(raw) > 512 {
			return fmt.Errorf("invalid cursor")
		}
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("invalid cursor")
		}
		var cursor pageCursor
		if json.Unmarshal(b, &cursor) != nil || cursor.Sequence <= 0 || cursor.Latency < 0 || cursor.Snapshot < cursor.Sequence || cursor.Sort != f.Sort || (f.Sort == "recent" && cursor.Timestamp <= 0) {
			return fmt.Errorf("invalid or incompatible cursor")
		}
		f.Cursor = &cursor
		*f.Snapshot = cursor.Snapshot
	}
	return nil
}
func (m *Module) pageWhere(c *gin.Context, f Filter, where *string, args *[]any) (string, error) {
	if *f.Snapshot == 0 {
		if err := m.DB.QueryRowContext(c.Request.Context(), "SELECT COALESCE(MAX(seq),0) FROM native_events").Scan(f.Snapshot); err != nil {
			return "", err
		}
	}
	*where += " AND seq<=?"
	*args = append(*args, *f.Snapshot)
	if f.Cursor != nil {
		if f.Sort == "latency" {
			*where += " AND (latency_ms<? OR (latency_ms=? AND seq<?))"
			*args = append(*args, f.Cursor.Latency, f.Cursor.Latency, f.Cursor.Sequence)
		} else if f.Sort == "recent" {
			*where += " AND (timestamp_ms<? OR (timestamp_ms=? AND seq<?))"
			*args = append(*args, f.Cursor.Timestamp, f.Cursor.Timestamp, f.Cursor.Sequence)
		} else {
			*where += " AND seq<?"
			*args = append(*args, f.Cursor.Sequence)
		}
	} else if f.Before > 0 {
		*where += " AND seq<?"
		*args = append(*args, f.Before)
	}
	if f.Sort == "latency" {
		return "latency_ms DESC,seq DESC", nil
	}
	if f.Sort == "recent" {
		return "timestamp_ms DESC,seq DESC", nil
	}
	return "seq DESC", nil
}
func encodeCursor(f Filter, row Row) string {
	b, _ := json.Marshal(pageCursor{Sequence: row.Sequence, Timestamp: row.TimestampMS, Latency: row.LatencyMS, Snapshot: *f.Snapshot, Sort: f.Sort})
	return base64.RawURLEncoding.EncodeToString(b)
}
func (m *Module) models(c *gin.Context) {
	f, ok := filter(c)
	if !ok {
		return
	}
	rows, err := m.DB.QueryContext(c.Request.Context(), "SELECT model,COUNT(*) FROM native_events WHERE "+f.Where+" GROUP BY model ORDER BY COUNT(*) DESC,model LIMIT 501", f.Args...)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	models := []string{}
	for rows.Next() {
		var model string
		var count int64
		if err = rows.Scan(&model, &count); err != nil {
			httpx.Error(c, err)
			return
		}
		models = append(models, model)
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	truncated := len(models) > 500
	if truncated {
		models = models[:500]
	}
	c.JSON(200, gin.H{"models": models, "truncated": truncated})
}
func (m *Module) trace(c *gin.Context) {
	if len(c.Param("id")) > 128 {
		c.JSON(400, gin.H{"error": "invalid trace ID"})
		return
	}
	rows, err := m.DB.QueryContext(c.Request.Context(), "SELECT seq,payload FROM native_events WHERE trace_id=? ORDER BY timestamp_ms,seq LIMIT 501", c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	events := []Row{}
	for rows.Next() {
		var row Row
		var raw string
		if err = rows.Scan(&row.Sequence, &raw); err == nil {
			err = json.Unmarshal([]byte(raw), &row.Event)
		}
		if err != nil {
			httpx.Error(c, err)
			return
		}
		events = append(events, row)
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	truncated := len(events) > 500
	if truncated {
		events = events[:500]
	}
	c.JSON(200, gin.H{"events": events, "truncated": truncated, "order": "attempt_start_then_sequence"})
}
