// Package native composes optional management modules inside the CPA process.
// Feature packages depend only on storage/events, never on the HTTP proxy server.
package native

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/accounts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/fingerprint"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/headers"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/history"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/inventory"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/limits"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/pricing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/risk"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/wire"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type feature struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	register func(*gin.RouterGroup)
	changed  func(bool)
}

type Runtime struct {
	inventory           *inventory.Module
	risk                *risk.Module
	limits              *limits.Module
	fingerprint         *fingerprint.Module
	headers             *headers.Module
	wire                *wire.Module
	globalProxyURL      string
	identityLookup      func(string) (Identity, bool)
	diagnostics         *diagnostics.Module
	db                  *sql.DB
	mu                  sync.RWMutex
	features            map[string]*feature
	closed              bool
	prices              *pricing.Module
	accounts            *accounts.Module
	cancel              context.CancelFunc
	catalogDone         chan struct{}
	versionDone         chan struct{}
	done                chan struct{}
	retention           int
	written             atomic.Int64
	failed              atomic.Int64
	lastWrite           atomic.Int64
	maintenanceFailures atomic.Int64
}

func Open(cfg config.NativeManagementConfig, configPath string, snapshots func() []accounts.Snapshot) (*Runtime, error) {
	if cfg.RetentionDays < 0 || cfg.RetentionDays > 3650 {
		return nil, fmt.Errorf("native-management.retention-days must be between 1 and 3650 (0 uses 90)")
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 90
	}
	path := cfg.Database
	if path == "" {
		path = filepath.Join("data", "native-management.sqlite")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	h, err := history.New(db)
	if err != nil {
		return nil, err
	}
	p, err := pricing.New(db)
	if err != nil {
		return nil, err
	}
	a, err := accounts.New(db, snapshots)
	if err != nil {
		return nil, err
	}
	l, err := limits.New(db)
	if err != nil {
		return nil, err
	}
	fp, err := fingerprint.New(db)
	if err != nil {
		return nil, err
	}
	hp, err := headers.New(db)
	if err != nil {
		return nil, err
	}
	wp, err := wire.New(db)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			wp.Close()
		}
	}()
	d, err := diagnostics.New(db)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			d.Close()
		}
	}()
	directory, err := inventory.New(db)
	if err != nil {
		return nil, err
	}
	guard, err := risk.New(db, path+".risk.key")
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			guard.Close()
		}
	}()
	r := &Runtime{inventory: directory, risk: guard, limits: l, fingerprint: fp, headers: hp, wire: wp, diagnostics: d, db: db, features: map[string]*feature{}, prices: p, accounts: a, done: make(chan struct{}), retention: cfg.RetentionDays}
	h.AliasUpdater = func(ctx context.Context, key, label string) error {
		p := directory.Profile("client", key)
		p.Name = label
		return directory.Save(ctx, "client", key, p)
	}
	guard.Enabled = func() bool { return r.enabled("risk") }
	// This is the only feature registry. Adding/removing a module does not change the host.
	for _, f := range []*feature{{Name: "history", register: h.Register}, {Name: "pricing", register: p.Register}, {Name: "accounts", register: a.Register}, {Name: "diagnostics", register: d.Register}, {Name: "limits", register: l.Register}, {Name: "fingerprint", register: fp.Register}, {Name: "headers", register: hp.Register, changed: hp.PauseVersionSync}, {Name: "wire", register: wp.Register, changed: wp.SetEnabled}, {Name: "risk", register: guard.Register}, {Name: "inventory", register: directory.Register}} {
		enabled, exists := cfg.Modules[f.Name]
		if !exists {
			enabled = true
		}
		if _, err = db.Exec("INSERT INTO native_settings(name,enabled) VALUES(?,?) ON CONFLICT(name) DO NOTHING", f.Name, enabled); err != nil {
			return nil, err
		}
		if err = db.QueryRow("SELECT enabled FROM native_settings WHERE name=?", f.Name).Scan(&f.Enabled); err != nil {
			return nil, err
		}
		r.features[f.Name] = f
		if f.changed != nil {
			f.changed(f.Enabled)
		}
	}
	for name := range cfg.Modules {
		if r.features[name] == nil {
			return nil, fmt.Errorf("unknown native module %q", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.catalogDone = make(chan struct{})
	go func() {
		defer close(r.catalogDone)
		r.prices.RunCatalog(ctx, func() bool { return r.enabled("pricing") })
	}()
	r.versionDone = make(chan struct{})
	go func() {
		defer close(r.versionDone)
		r.headers.RunVersions(ctx, func() bool { return r.enabled("headers") })
	}()
	go r.maintain(ctx)
	ok = true
	return r, nil
}

func (r *Runtime) enabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f := r.features[name]
	return !r.closed && f != nil && f.Enabled
}

// HandleUsage runs on the existing CPA usage dispatcher, never the request goroutine.
// Backpressure stays in that dispatcher rather than dropping records in a second queue.
func (r *Runtime) HandleUsage(ctx context.Context, record usage.Record) {
	historyEnabled, accountsEnabled, inventoryEnabled := r.enabled("history"), r.enabled("accounts"), r.enabled("inventory")
	if !historyEnabled && !accountsEnabled && !inventoryEnabled {
		return
	}
	e := event.FromUsage(ctx, record)
	if (historyEnabled || inventoryEnabled) && r.enabled("pricing") {
		r.prices.Estimate(&e)
	}
	// Request contexts may already be canceled when asynchronous events arrive.
	tx, err := r.db.Begin()
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		if historyEnabled {
			err = history.Insert(tx, e)
		}
		if err == nil && inventoryEnabled {
			err = inventory.Record(tx, e)
		}
		if err == nil && accountsEnabled {
			err = accounts.Record(tx, e)
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		r.failed.Add(1)
		log.WithError(err).Error("native management could not persist a usage event")
		return
	}
	r.written.Add(1)
	r.lastWrite.Store(time.Now().UnixMilli())
}

func (r *Runtime) Register(g *gin.RouterGroup) {
	n := g.Group("/native")
	n.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	r.registerIdentity(n)
	n.GET("/status", func(c *gin.Context) {
		r.mu.RLock()
		defer r.mu.RUnlock()
		items := []feature{}
		for _, name := range []string{"history", "pricing", "accounts", "diagnostics", "limits", "fingerprint", "headers", "wire", "risk", "inventory"} {
			if f := r.features[name]; f != nil {
				items = append(items, *f)
			}
		}
		c.JSON(200, gin.H{"modules": items, "written_this_run": r.written.Load(), "failed_writes_this_run": r.failed.Load(), "last_write_ms": r.lastWrite.Load(), "maintenance_failures_this_run": r.maintenanceFailures.Load(), "retention_days": r.retention, "diagnostic_dropped_this_run": r.diagnostics.Dropped.Load(), "diagnostic_failed_writes_this_run": r.diagnostics.Failed.Load(), "automatic_credential_changes": false, "count_semantics": "provider_attempts", "settings_apply": "module toggles are immediate; YAML startup settings require restart"})
	})
	n.PUT("/modules/:name", func(c *gin.Context) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if input.Enabled == nil {
			c.JSON(400, gin.H{"error": "enabled is required"})
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		f := r.features[c.Param("name")]
		if f == nil {
			c.JSON(404, gin.H{"error": "Unknown module"})
			return
		}
		if _, err := r.db.ExecContext(c.Request.Context(), "UPDATE native_settings SET enabled=? WHERE name=?", *input.Enabled, f.Name); err != nil {
			httpx.Error(c, err)
			return
		}
		f.Enabled = *input.Enabled
		if f.changed != nil {
			f.changed(f.Enabled)
		}
		c.JSON(200, f)
	})
	for name, f := range r.features {
		group := n.Group("/" + name)
		group.Use(func(c *gin.Context) {
			if !r.enabled(name) {
				c.AbortWithStatusJSON(409, gin.H{"error": "Module is disabled"})
				return
			}
			c.Next()
		})
		f.register(group)
	}
}

func (r *Runtime) maintain(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if r.enabled("accounts") {
			if _, err := r.accounts.Inspect(ctx); err != nil && ctx.Err() == nil {
				r.maintenanceError(err)
			}
		}
		cutoff := time.Now().AddDate(0, 0, -r.retention).UnixMilli()
		for _, q := range []string{
			"DELETE FROM native_risk_events WHERE seq IN (SELECT seq FROM native_risk_events WHERE timestamp_ms<? LIMIT 10000)",
			"DELETE FROM native_requests WHERE rowid IN (SELECT rowid FROM native_requests WHERE started_ms<? LIMIT 10000)",
			"DELETE FROM native_events WHERE seq IN (SELECT seq FROM native_events WHERE timestamp_ms<? LIMIT 10000)",
			"DELETE FROM native_account_snapshots WHERE seq IN (SELECT seq FROM native_account_snapshots WHERE checked_at_ms<? LIMIT 10000)",
			"DELETE FROM native_account_actions WHERE last_seen_ms<? AND status IN ('resolved','ignored')",
		} {
			if _, err := r.db.ExecContext(ctx, q, cutoff); err != nil && ctx.Err() == nil {
				r.maintenanceError(err)
			}
		}
		if _, err := r.db.ExecContext(ctx, "DELETE FROM native_risk_blocks WHERE expires_ms<?", time.Now().UnixMilli()); err != nil && ctx.Err() == nil {
			r.maintenanceError(err)
		}
		if _, err := r.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil && ctx.Err() == nil {
			r.maintenanceError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) maintenanceError(err error) {
	r.maintenanceFailures.Add(1)
	log.WithError(err).Warn("native management maintenance failed")
}

// Close must run after CPA's usage dispatcher has drained.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	<-r.done
	<-r.catalogDone
	<-r.versionDone
	r.risk.Close()
	r.diagnostics.Close()
	r.wire.Close()
	return r.db.Close()
}

// Middleware installs optional request observation without changing inference routes.
func (r *Runtime) Middleware() gin.HandlerFunc {
	return r.diagnostics.Middleware(func() bool { return r.enabled("diagnostics") })
}
