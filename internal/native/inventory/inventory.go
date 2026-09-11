// Package inventory owns credential annotations and durable lifetime usage counters.
package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type Profile struct {
	Name      string   `json:"name"`
	Notes     string   `json:"notes"`
	Tags      []string `json:"tags"`
	Disabled  bool     `json:"disabled"`
	ExpiresMS int64    `json:"expires_ms"`
	Models    []string `json:"models"`
	BudgetUSD *float64 `json:"budget_usd"`
	Version   int64    `json:"version"`
}
type Totals struct {
	Requests int64   `json:"requests"`
	Failures int64   `json:"failures"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost_usd"`
	Unpriced int64   `json:"unpriced"`
	FirstMS  int64   `json:"first_ms"`
	LastMS   int64   `json:"last_ms"`
}
type Item struct {
	Scope   string   `json:"scope"`
	Target  string   `json:"target"`
	Profile Profile  `json:"profile"`
	Totals  Totals   `json:"totals"`
	Account *Account `json:"account,omitempty"`
}
type Module struct {
	DB       *sql.DB
	mu       sync.RWMutex
	profiles map[string]Profile
	Accounts func() map[string]Account
	now      func() time.Time
}

func New(db *sql.DB) (*Module, error) {
	err := storage.Migrate(db, "inventory", `
 CREATE TABLE native_inventory_windows(target TEXT PRIMARY KEY,payload TEXT NOT NULL);
 CREATE TABLE native_profiles(scope TEXT NOT NULL,target TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(scope,target));
 CREATE TABLE native_usage_totals(scope TEXT NOT NULL,target TEXT NOT NULL,requests INTEGER NOT NULL,failures INTEGER NOT NULL,tokens INTEGER NOT NULL,cost_usd REAL NOT NULL,unpriced INTEGER NOT NULL,first_ms INTEGER NOT NULL,last_ms INTEGER NOT NULL,PRIMARY KEY(scope,target));
 INSERT INTO native_usage_totals SELECT 'credential',account,COUNT(*),SUM(failed),SUM(total_tokens),COALESCE(SUM(cost_usd),0),SUM(cost_usd IS NULL),MIN(timestamp_ms),MAX(timestamp_ms) FROM native_events WHERE account<>'' GROUP BY account;
 INSERT INTO native_usage_totals SELECT 'client',key_hash,COUNT(*),SUM(failed),SUM(total_tokens),COALESCE(SUM(cost_usd),0),SUM(cost_usd IS NULL),MIN(timestamp_ms),MAX(timestamp_ms) FROM native_events WHERE key_hash<>'' GROUP BY key_hash;
 INSERT INTO native_profiles SELECT 'client',key_hash,json_object('name',label,'notes','','tags',json('[]'),'models',json('[]'),'version',1) FROM native_aliases;
 `)
	if err != nil {
		return nil, err
	}
	m := &Module{DB: db, profiles: map[string]Profile{}, now: time.Now}
	rows, err := db.Query("SELECT scope,target,payload FROM native_profiles")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var scope, target, raw string
		if err = rows.Scan(&scope, &target, &raw); err != nil {
			return nil, err
		}
		var p Profile
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		if err = validate(p); err != nil {
			return nil, err
		}
		m.profiles[scope+":"+target] = p
	}
	return m, rows.Err()
}
func validate(p Profile) error {
	if len(p.Name) > 160 || len(p.Notes) > 4000 || len(p.Tags) > 30 || len(p.Models) > 200 || p.ExpiresMS < 0 || p.ExpiresMS > 253402300799000 {
		return fmt.Errorf("invalid profile")
	}
	for _, v := range p.Tags {
		if len(v) > 80 {
			return fmt.Errorf("tag too long")
		}
	}
	for _, v := range p.Models {
		if v == "" || len(v) > 256 {
			return fmt.Errorf("invalid allowed model")
		}
	}
	if p.BudgetUSD != nil && (math.IsNaN(*p.BudgetUSD) || math.IsInf(*p.BudgetUSD, 0) || *p.BudgetUSD < 0 || *p.BudgetUSD > 1e12) {
		return fmt.Errorf("invalid budget")
	}
	return nil
}
func (m *Module) Profile(scope, target string) Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.profiles[scope+":"+target]; ok {
		return p
	}
	return Profile{Tags: []string{}, Models: []string{}}
}
func (m *Module) Save(ctx context.Context, scope, target string, p Profile) error {
	if err := validate(p); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Version != m.profiles[scope+":"+target].Version {
		return fmt.Errorf("profile changed; reload before saving")
	}
	p.Version++
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO native_profiles(scope,target,payload) VALUES(?,?,?) ON CONFLICT(scope,target) DO UPDATE SET payload=excluded.payload", scope, target, string(raw)); err != nil {
		return err
	}
	// Keep the original history alias API compatible with richer profiles.
	if scope == "client" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO native_aliases(key_hash,label) VALUES(?,?) ON CONFLICT(key_hash) DO UPDATE SET label=excluded.label", target, p.Name); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	m.profiles[scope+":"+target] = p
	return nil
}
func Record(tx *sql.Tx, e event.Event) error {
	for _, target := range []struct{ scope, id string }{{"credential", e.Account}, {"client", e.KeyHash}} {
		if target.id == "" {
			continue
		}
		cost := 0.0
		unpriced := 1
		if e.CostUSD != nil {
			cost = *e.CostUSD
			unpriced = 0
		}
		_, err := tx.Exec(`INSERT INTO native_usage_totals(scope,target,requests,failures,tokens,cost_usd,unpriced,first_ms,last_ms) VALUES(?,?,1,?,?,?,?,?,?)
 ON CONFLICT(scope,target) DO UPDATE SET requests=requests+1,failures=failures+excluded.failures,tokens=tokens+excluded.tokens,cost_usd=cost_usd+excluded.cost_usd,unpriced=unpriced+excluded.unpriced,first_ms=MIN(first_ms,excluded.first_ms),last_ms=MAX(last_ms,excluded.last_ms)`, target.scope, target.id, e.Failed, e.Tokens.TotalTokens, cost, unpriced, e.TimestampMS, e.TimestampMS)
		if err != nil {
			return err
		}
	}
	return nil
}

type Rejection struct {
	Code     string
	Terminal bool
}

func (e *Rejection) Error() string        { return e.Code }
func (e *Rejection) StatusCode() int      { return 403 }
func (e *Rejection) IsRequestStop() bool  { return e.Terminal }
func (e *Rejection) DirectResponse() bool { return true }
func (e *Rejection) ResponseBody() []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "request_policy_error", "code": e.Code, "message": "The key or credential policy does not permit this request."}})
	return b
}
func (m *Module) Check(scope, target, model string) error {
	p := m.Profile(scope, target)
	if p.Disabled {
		return &Rejection{"native_profile_disabled", scope == "client"}
	}
	if p.ExpiresMS > 0 && p.ExpiresMS <= m.now().UnixMilli() {
		return &Rejection{"native_profile_expired", scope == "client"}
	}
	if model != "" && len(p.Models) > 0 {
		for _, allowed := range p.Models {
			if model == allowed {
				return nil
			}
		}
		return &Rejection{"native_model_not_allowed", scope == "client"}
	}
	return nil
}
