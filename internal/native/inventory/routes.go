package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
)

func validTarget(c *gin.Context) (string, string, bool) {
	scope, target := c.Param("scope"), c.Param("target")
	if (scope != "client" && scope != "credential") || target == "" || len(target) > 256 {
		c.JSON(400, gin.H{"error": "invalid profile target"})
		return "", "", false
	}
	return scope, target, true
}
func (m *Module) totals(ctx context.Context, scope, target string) (Totals, error) {
	var t Totals
	err := m.DB.QueryRowContext(ctx, "SELECT requests,failures,tokens,cost_usd,unpriced,first_ms,last_ms FROM native_usage_totals WHERE scope=? AND target=?", scope, target).Scan(&t.Requests, &t.Failures, &t.Tokens, &t.Cost, &t.Unpriced, &t.FirstMS, &t.LastMS)
	if err == sql.ErrNoRows {
		err = nil
	}
	return t, err
}
func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("", m.list)
	g.GET("/:scope/:target", func(c *gin.Context) {
		scope, target, ok := validTarget(c)
		if !ok {
			return
		}
		totals, err := m.totals(c.Request.Context(), scope, target)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		item := Item{Scope: scope, Target: target, Profile: m.Profile(scope, target), Totals: totals}
		if scope == "credential" && m.Accounts != nil {
			if a, ok := m.Accounts()[target]; ok {
				item.Account = &a
			}
		}
		forecasts, err := m.forecasts(c.Request.Context(), item)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, gin.H{"item": item, "forecasts": forecasts})
	})
	g.PUT("/:scope/:target", func(c *gin.Context) {
		scope, target, ok := validTarget(c)
		if !ok {
			return
		}
		var p Profile
		if !httpx.Decode(c, &p) {
			return
		}
		if err := m.Save(c.Request.Context(), scope, target, p); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, m.Profile(scope, target))
	})
	g.PUT("/windows/:target", func(c *gin.Context) {
		var input struct {
			Windows []Window `json:"windows"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		target := c.Param("target")
		if m.Accounts == nil {
			c.JSON(404, gin.H{"error": "account not found"})
			return
		}
		if _, ok := m.Accounts()[target]; !ok {
			c.JSON(404, gin.H{"error": "account not found"})
			return
		}
		if len(input.Windows) > 16 {
			c.JSON(400, gin.H{"error": "too many windows"})
			return
		}
		now := m.now().UnixMilli()
		for i := range input.Windows {
			w := &input.Windows[i]
			if w.ObservedMS <= 0 || w.ObservedMS > now+15000 {
				c.JSON(400, gin.H{"error": "invalid observation time"})
				return
			}
			if w.ID == "" || len(w.ID) > 128 || math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) || w.UsedPercent < 0 || w.UsedPercent > 100 || w.StartMS <= 0 || w.StartMS > w.ObservedMS || w.ResetMS <= w.StartMS || w.ResetMS-w.StartMS > 60*86400000 || w.ResetMS > now+60*86400000 {
				c.JSON(400, gin.H{"error": "invalid quota window"})
				return
			}
		}
		raw, _ := json.Marshal(input.Windows)
		if _, err := m.DB.ExecContext(c.Request.Context(), "INSERT INTO native_inventory_windows(target,payload) VALUES(?,?) ON CONFLICT(target) DO UPDATE SET payload=excluded.payload WHERE COALESCE(json_extract(excluded.payload,'$[0].observed_ms'),0)>=COALESCE(json_extract(native_inventory_windows.payload,'$[0].observed_ms'),0)", target, string(raw)); err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, gin.H{"saved": true})
	})
}
func (m *Module) list(c *gin.Context) {
	rows, err := m.DB.QueryContext(c.Request.Context(), "SELECT scope,target,requests,failures,tokens,cost_usd,unpriced,first_ms,last_ms FROM native_usage_totals ORDER BY last_ms DESC LIMIT 5001")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items := map[string]Item{}
	for rows.Next() {
		var i Item
		if err = rows.Scan(&i.Scope, &i.Target, &i.Totals.Requests, &i.Totals.Failures, &i.Totals.Tokens, &i.Totals.Cost, &i.Totals.Unpriced, &i.Totals.FirstMS, &i.Totals.LastMS); err != nil {
			break
		}
		items[i.Scope+":"+i.Target] = i
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		httpx.Error(c, err)
		return
	}
	m.mu.RLock()
	for key, p := range m.profiles {
		item := items[key]
		if item.Target == "" {
			parts := strings.SplitN(key, ":", 2)
			item.Scope, item.Target = parts[0], parts[1]
		}
		item.Profile = p
		items[key] = item
	}
	m.mu.RUnlock()
	if m.Accounts != nil {
		for id, a := range m.Accounts() {
			key := "credential:" + id
			item := items[key]
			item.Scope = "credential"
			item.Target = id
			item.Account = &a
			items[key] = item
		}
	}
	out := []Item{}
	for _, i := range items {
		if i.Profile.Tags == nil {
			i.Profile.Tags = []string{}
		}
		if i.Profile.Models == nil {
			i.Profile.Models = []string{}
		}
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Totals.LastMS != out[j].Totals.LastMS {
			return out[i].Totals.LastMS > out[j].Totals.LastMS
		}
		return out[i].Scope+":"+out[i].Target < out[j].Scope+":"+out[j].Target
	})
	if len(out) > 5000 {
		out = out[:5000]
	}
	c.JSON(200, gin.H{"items": out, "truncated": len(items) > 5000, "count_semantics": "provider_attempts", "coverage": "recorded_since_first_seen"})
}
func (m *Module) forecasts(ctx context.Context, item Item) ([]Forecast, error) {
	out := []Forecast{}
	if item.Scope != "credential" {
		return out, nil
	}
	windows := []Window{}
	if item.Account != nil {
		windows = item.Account.Windows
	}
	var raw string
	err := m.DB.QueryRowContext(ctx, "SELECT payload FROM native_inventory_windows WHERE target=?", item.Target).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		var saved []Window
		if err = json.Unmarshal([]byte(raw), &saved); err != nil {
			return nil, err
		}
		if len(saved) > 0 && (len(windows) == 0 || saved[0].ObservedMS > windows[0].ObservedMS) {
			windows = saved
		}
	}
	for _, w := range windows {
		var t Totals
		err = m.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(failed),0),COALESCE(SUM(total_tokens),0),COALESCE(SUM(cost_usd),0),COALESCE(SUM(cost_usd IS NULL),0),COALESCE(MIN(timestamp_ms),0),COALESCE(MAX(timestamp_ms),0) FROM native_events WHERE account=? AND timestamp_ms>=? AND timestamp_ms<=?`, item.Target, w.StartMS, w.ObservedMS).Scan(&t.Requests, &t.Failures, &t.Tokens, &t.Cost, &t.Unpriced, &t.FirstMS, &t.LastMS)
		if err != nil {
			return nil, err
		}
		out = append(out, Estimate(w, t, item.Totals.FirstMS, m.now().UnixMilli()))
	}
	return out, nil
}
