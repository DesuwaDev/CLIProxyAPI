// Package accounts records runtime health evidence without modifying credentials.
package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

// Snapshot is an allowlist: the host must never pass Auth.Metadata or credentials.
type Snapshot struct {
	Account         string `json:"account"`
	Provider        string `json:"provider"`
	Label           string `json:"label"`
	State           string `json:"state"`
	Disabled        bool   `json:"disabled"`
	QuotaExceeded   bool   `json:"quota_exceeded"`
	NextRetryMS     int64  `json:"next_retry_ms"`
	QuotaObservedMS int64  `json:"quota_observed_ms"`
	CheckedAtMS     int64  `json:"checked_at_ms"`
}

type Module struct {
	db        *sql.DB
	snapshots func() []Snapshot
	inspectMu sync.Mutex
}

func New(db *sql.DB, snapshots func() []Snapshot) (*Module, error) {
	err := storage.Migrate(db, "accounts", `
	CREATE TABLE native_account_snapshots (seq INTEGER PRIMARY KEY AUTOINCREMENT,account TEXT NOT NULL,checked_at_ms INTEGER NOT NULL,payload TEXT NOT NULL);
	CREATE INDEX native_snapshots_account_time ON native_account_snapshots(account,checked_at_ms);
	CREATE TABLE native_account_actions (account TEXT NOT NULL,code TEXT NOT NULL,first_seen_ms INTEGER NOT NULL,last_seen_ms INTEGER NOT NULL,hits INTEGER NOT NULL,status TEXT NOT NULL,PRIMARY KEY(account,code));`)
	return &Module{db: db, snapshots: snapshots}, err
}

func Record(tx *sql.Tx, e event.Event) error {
	if e.Account == "" || !e.Failed {
		return nil
	}
	switch e.FailureCode {
	case "invalid_token", "token_expired", "token_revoked", "invalid_api_key", "invalid_grant", "account_deactivated", "workspace_deactivated", "authorization_review":
	default:
		return nil
	}
	_, err := tx.Exec(`INSERT INTO native_account_actions(account,code,first_seen_ms,last_seen_ms,hits,status) VALUES(?,?,?,?,1,'pending')
	ON CONFLICT(account,code) DO UPDATE SET last_seen_ms=MAX(last_seen_ms,excluded.last_seen_ms),hits=hits+1,
	status=CASE WHEN status='resolved' AND excluded.last_seen_ms>last_seen_ms THEN 'pending' ELSE status END`, e.Account, e.FailureCode, e.TimestampMS, e.TimestampMS)
	return err
}

func (m *Module) Inspect(ctx context.Context) ([]Snapshot, error) {
	m.inspectMu.Lock()
	defer m.inspectMu.Unlock()
	items := []Snapshot{}
	if m.snapshots != nil {
		items = m.snapshots()
	}
	now := time.Now().UnixMilli()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for i := range items {
		items[i].CheckedAtMS = now
		b, errJSON := json.Marshal(items[i])
		if errJSON != nil {
			return nil, errJSON
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO native_account_snapshots(account,checked_at_ms,payload) VALUES(?,?,?)", items[i].Account, now, string(b)); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit()
}

func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("", func(c *gin.Context) {
		items := []Snapshot{}
		if m.snapshots != nil {
			items = m.snapshots()
		}
		c.JSON(200, gin.H{"accounts": items, "source": "runtime_observations", "automatic_credential_changes": false})
	})
	g.POST("/inspect", func(c *gin.Context) {
		items, err := m.Inspect(c.Request.Context())
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, gin.H{"accounts": items, "source": "runtime_observations"})
	})
	g.GET("/history", func(c *gin.Context) {
		account := c.Query("account")
		if account == "" {
			c.JSON(400, gin.H{"error": "account is required"})
			return
		}
		rows, err := m.db.QueryContext(c.Request.Context(), "SELECT payload FROM native_account_snapshots WHERE account=? ORDER BY checked_at_ms DESC,seq DESC LIMIT 200", account)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		defer func() { _ = rows.Close() }()
		items := []Snapshot{}
		for rows.Next() {
			var raw string
			var s Snapshot
			if err = rows.Scan(&raw); err != nil {
				httpx.Error(c, err)
				return
			}
			if err = json.Unmarshal([]byte(raw), &s); err != nil {
				httpx.Error(c, err)
				return
			}
			items = append(items, s)
		}
		if err = rows.Err(); err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, gin.H{"snapshots": items})
	})
	g.GET("/actions", m.actions)
	g.PUT("/actions", func(c *gin.Context) {
		var input struct {
			Account string `json:"account"`
			Code    string `json:"code"`
			Status  string `json:"status"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if input.Status != "resolved" && input.Status != "ignored" && input.Status != "pending" {
			c.JSON(400, gin.H{"error": "status must be pending, ignored or resolved"})
			return
		}
		result, err := m.db.ExecContext(c.Request.Context(), "UPDATE native_account_actions SET status=? WHERE account=? AND code=?", input.Status, input.Account, input.Code)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			c.JSON(404, gin.H{"error": "Action not found"})
			return
		}
		c.JSON(200, gin.H{"saved": true})
	})
}

type Action struct {
	Account   string `json:"account"`
	Code      string `json:"code"`
	FirstSeen int64  `json:"first_seen_ms"`
	LastSeen  int64  `json:"last_seen_ms"`
	Hits      int64  `json:"hits"`
	Status    string `json:"status"`
}

func (m *Module) actions(c *gin.Context) {
	rows, err := m.db.QueryContext(c.Request.Context(), "SELECT account,code,first_seen_ms,last_seen_ms,hits,status FROM native_account_actions ORDER BY last_seen_ms DESC LIMIT 501")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := []Action{}
	for rows.Next() {
		var a Action
		if err = rows.Scan(&a.Account, &a.Code, &a.FirstSeen, &a.LastSeen, &a.Hits, &a.Status); err != nil {
			httpx.Error(c, err)
			return
		}
		items = append(items, a)
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	truncated := len(items) > 500
	if truncated {
		items = items[:500]
	}
	c.JSON(200, gin.H{"actions": items, "truncated": truncated, "automatic_credential_changes": false})
}
