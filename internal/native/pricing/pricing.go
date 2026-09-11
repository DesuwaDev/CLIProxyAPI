// Package pricing estimates costs from explicit, versioned local price rules.
package pricing

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type Rule struct {
	ID         string  `json:"id"`
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	Tier       string  `json:"tier"`
	MinContext int64   `json:"min_context"`
	Input      float64 `json:"input_per_million"`
	Output     float64 `json:"output_per_million"`
	CacheRead  float64 `json:"cache_read_per_million"`
	CacheWrite float64 `json:"cache_write_per_million"`
}

// UnmarshalJSON distinguishes explicit zero prices from accidentally omitted rates.
func (r *Rule) UnmarshalJSON(data []byte) error {
	type plain Rule
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"input_per_million", "output_per_million", "cache_read_per_million", "cache_write_per_million"} {
		raw, exists := fields[key]
		if !exists || string(raw) == "null" {
			return fmt.Errorf("explicit %s is required", key)
		}
	}
	*r = Rule(value)
	return nil
}

type Module struct {
	catalog catalogState
	db      *sql.DB
	mu      sync.RWMutex
	rules   []Rule
}

func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "pricing", `CREATE TABLE native_prices (id TEXT PRIMARY KEY, payload TEXT NOT NULL);`); err != nil {
		return nil, err
	}
	m := &Module{db: db, rules: []Rule{}}
	rows, err := db.Query("SELECT payload FROM native_prices ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		var rule Rule
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &rule); err != nil {
			return nil, err
		}
		m.rules = append(m.rules, rule)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = m.initCatalog(); err != nil {
		return nil, err
	}
	return m, nil
}

func Validate(rules []Rule) error {
	if len(rules) > 2000 {
		return fmt.Errorf("at most 2000 price rules are allowed")
	}
	ids, matches := map[string]bool{}, map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || len(r.ID) > 128 || r.Model == "" || len(r.Model) > 256 || r.Provider == "" || len(r.Provider) > 128 || r.Tier == "" || len(r.Tier) > 64 || r.MinContext < 0 {
			return fmt.Errorf("each rule requires id, provider, model, tier and a non-negative min_context")
		}
		key := fmt.Sprintf("%q/%q/%q/%d", r.Provider, r.Model, r.Tier, r.MinContext)
		if ids[r.ID] || matches[key] {
			return fmt.Errorf("duplicate rule ID or matching conditions")
		}
		ids[r.ID], matches[key] = true, true
		for _, v := range []float64{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e6 {
				return fmt.Errorf("prices must be finite USD per million tokens between 0 and 1000000")
			}
		}
	}
	return nil
}

// Estimate uses non-overlapping upstream token buckets. Unknown accounting or
// unmatched tiers stay unpriced. Historical events retain their original estimate.
func (m *Module) Estimate(e *event.Event) {
	if !e.Generate || !e.Tokens.Valid() || e.Tokens.Quality != usage.TokenAccountingQualityComplete {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	best, bestScore := -1, -1
	for i, r := range m.rules {
		if r.Model != e.Model || (r.Provider != "*" && r.Provider != e.Provider) || (r.Tier != "*" && modelTier(r.Tier, e.Model, e.Provider) != effectiveTier(e)) || e.Tokens.Input.TotalTokens < r.MinContext {
			continue
		}
		score := 0
		if r.Provider == e.Provider {
			score += 2
		}
		if modelTier(r.Tier, e.Model, e.Provider) == effectiveTier(e) {
			score++
		}
		if best < 0 || score > bestScore || (score == bestScore && r.MinContext > m.rules[best].MinContext) {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		m.estimateCatalog(e)
		return
	}
	r := m.rules[best]
	cost := (float64(e.Tokens.Input.UncachedTokens)*r.Input + float64(e.Tokens.Input.CacheReadTokens)*r.CacheRead + float64(e.Tokens.Input.CacheWriteTokens)*r.CacheWrite + float64(e.Tokens.Output.TotalTokens)*r.Output) / 1e6
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		return
	}
	e.CostUSD, e.PricingRule = &cost, r.ID
}

func (m *Module) Register(g *gin.RouterGroup) {
	m.registerCatalog(g)
	g.GET("", func(c *gin.Context) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		c.JSON(200, gin.H{"rules": m.rules, "currency": "USD", "historical_costs": "snapshot"})
	})
	g.PUT("", func(c *gin.Context) {
		var input struct {
			Rules *[]Rule `json:"rules"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if input.Rules == nil {
			c.JSON(400, gin.H{"error": "rules array is required"})
			return
		}
		rules := *input.Rules
		if err := Validate(rules); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// Serialize price updates. Event estimation finishes before opening its transaction.
		m.mu.Lock()
		defer m.mu.Unlock()
		tx, err := m.db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.Exec("DELETE FROM native_prices"); err == nil {
			for _, r := range rules {
				b, _ := json.Marshal(r)
				if _, err = tx.Exec("INSERT INTO native_prices(id,payload) VALUES(?,?)", r.ID, string(b)); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			httpx.Error(c, err)
			return
		}
		m.rules = append([]Rule{}, rules...)
		c.JSON(200, gin.H{"rules": m.rules})
	})
}
