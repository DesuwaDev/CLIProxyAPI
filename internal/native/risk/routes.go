package risk

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
)

func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/config", func(c *gin.Context) { c.JSON(200, public(m.snapshot())) })
	g.PUT("/config", func(c *gin.Context) {
		var input Config
		if !httpx.Decode(c, &input) {
			return
		}
		output, err := m.Save(c.Request.Context(), input)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, output)
	})
	g.GET("/status", func(c *gin.Context) {
		m.statusMu.Lock()
		status := make(map[string]EndpointStatus, len(m.status))
		for k, v := range m.status {
			status[k] = v
		}
		active := m.active
		m.statusMu.Unlock()
		rows, err := m.db.QueryContext(c.Request.Context(), "SELECT decision,COUNT(*),SUM(blocked) FROM native_risk_events WHERE timestamp_ms>=? GROUP BY decision", m.now().Add(-24*time.Hour).UnixMilli())
		if err != nil {
			httpx.Error(c, err)
			return
		}
		counts := map[string]int64{}
		var blocked int64
		for rows.Next() {
			var k string
			var n, b int64
			if err = rows.Scan(&k, &n, &b); err != nil {
				break
			}
			counts[k] = n
			blocked += b
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, gin.H{"mode": m.snapshot().Mode, "active": active, "queued": len(m.queue), "queue_capacity": cap(m.queue), "dropped": m.Dropped.Load(), "failed_writes": m.FailedWrites.Load(), "endpoints": status, "last_24h": counts, "blocked_24h": blocked})
	})
	g.GET("/events", func(c *gin.Context) {
		before := int64(0)
		if c.Query("before") != "" {
			var err error
			before, err = strconv.ParseInt(c.Query("before"), 10, 64)
			if err != nil || before < 1 {
				c.JSON(400, gin.H{"error": "invalid cursor"})
				return
			}
		}
		query := "SELECT seq,payload FROM native_risk_events WHERE 1=1"
		args := []any{}
		if before > 0 {
			query += " AND seq<?"
			args = append(args, before)
		}
		if v := c.Query("decision"); v != "" {
			query += " AND decision=?"
			args = append(args, v)
		}
		if v := c.Query("model"); v != "" {
			query += " AND model=?"
			args = append(args, v)
		}
		query += " ORDER BY seq DESC LIMIT 101"
		rows, err := m.db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		defer func() { _ = rows.Close() }()
		events := []Event{}
		next := int64(0)
		more := false
		for rows.Next() {
			var seq int64
			var raw string
			if err = rows.Scan(&seq, &raw); err != nil {
				httpx.Error(c, err)
				return
			}
			if len(events) == 100 {
				more = true
				break
			}
			var e Event
			if err = json.Unmarshal([]byte(raw), &e); err != nil {
				httpx.Error(c, err)
				return
			}
			events = append(events, e)
			next = seq
		}
		if err = rows.Err(); err != nil {
			httpx.Error(c, err)
			return
		}
		if !more {
			next = 0
		}
		c.JSON(200, gin.H{"events": events, "next": next})
	})
	g.GET("/blocks", func(c *gin.Context) {
		rows, err := m.db.QueryContext(c.Request.Context(), "SELECT kind,target,expires_ms,reason FROM native_risk_blocks WHERE expires_ms>? ORDER BY expires_ms DESC LIMIT 501", m.now().UnixMilli())
		if err != nil {
			httpx.Error(c, err)
			return
		}
		defer func() { _ = rows.Close() }()
		items := []gin.H{}
		for rows.Next() {
			var kind, target, reason string
			var expires int64
			if err = rows.Scan(&kind, &target, &expires, &reason); err != nil {
				httpx.Error(c, err)
				return
			}
			items = append(items, gin.H{"kind": kind, "target": target, "expires_ms": expires, "reason": reason})
		}
		if err = rows.Err(); err != nil {
			httpx.Error(c, err)
			return
		}
		truncated := len(items) > 500
		if truncated {
			items = items[:500]
		}
		c.JSON(200, gin.H{"blocks": items, "truncated": truncated})
	})
	g.DELETE("/blocks/:kind/:target", func(c *gin.Context) {
		if c.Param("kind") != "hash" && c.Param("kind") != "session" {
			c.JSON(400, gin.H{"error": "invalid block kind"})
			return
		}
		result, err := m.db.ExecContext(c.Request.Context(), "DELETE FROM native_risk_blocks WHERE kind=? AND target=?", c.Param("kind"), c.Param("target"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		n, _ := result.RowsAffected()
		c.JSON(200, gin.H{"deleted": n})
	})
	g.POST("/test", func(c *gin.Context) {
		var input struct {
			Text       string `json:"text"`
			EndpointID string `json:"endpoint_id"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		cfg := m.snapshot()
		if len([]rune(input.Text)) > cfg.MaxInputChars {
			c.JSON(400, gin.H{"error": "test input too large"})
			return
		}
		if input.EndpointID != "" {
			found := false
			for _, e := range cfg.Endpoints {
				if e.ID == input.EndpointID {
					e.Enabled = true
					cfg.Endpoints = []Endpoint{e}
					cfg.Strategy = "api"
					found = true
					break
				}
			}
			if !found {
				c.JSON(404, gin.H{"error": "endpoint not found"})
				return
			}
		}
		if !m.acquire(cfg.Concurrency) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "audit capacity full"})
			return
		}
		defer m.release()
		result := m.scan(c.Request.Context(), cfg, input.Text)
		c.JSON(200, result)
	})
}
