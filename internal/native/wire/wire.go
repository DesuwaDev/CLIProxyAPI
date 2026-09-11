// Package wire owns the opt-in Codex OAuth transport profile that reproduces the
// wire behaviour of the official Codex CLI (reqwest + rustls + hyper h2) instead
// of CPA's default Go/uTLS Chrome transport. It is compiled into the native
// management runtime and consumed by the Codex executors through the SDK
// transport hook; it never changes scheduling, credentials or request bodies
// beyond the documented compression step.
//
// Profile values are taken from a local capture of codex-cli 0.154.0
// (see _diagnostics/codex-wire-20260911): the TLS ClientHello, the HTTP/2
// connection preface (SETTINGS + WINDOW_UPDATE), request header order, hpack
// behaviour, and zstd request-body framing.
package wire

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

// Policy is the persisted per-credential wire profile selection.
type Policy struct {
	// Mode is "off" (CPA default transport) or "codex" (official CLI profile).
	Mode string `json:"mode"`
	// Compress sends JSON bodies as Content-Encoding: zstd like the CLI does.
	Compress bool `json:"compress"`
	// Cookies keeps upstream Set-Cookie values per credential and replays them.
	Cookies bool `json:"cookies"`
	// RoutingHint synthesizes x-codex-routing-hint from the request model when
	// the downstream client did not send one.
	RoutingHint bool `json:"routing_hint"`
}

func (p Policy) normalized() Policy {
	if p.Mode != "codex" {
		return Policy{Mode: "off"}
	}
	return p
}

// Module stores policies and owns the per-credential transports.
type Module struct {
	db       *sql.DB
	mu       sync.RWMutex
	policies map[string]Policy
	Eligible func(string) bool
	pool     *transportPool
}

func validMode(mode string) bool { return mode == "off" || mode == "codex" }

// New opens the module schema and loads persisted policies.
func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "wire", `CREATE TABLE native_wire_policies (target TEXT PRIMARY KEY,mode TEXT NOT NULL,compress INTEGER NOT NULL DEFAULT 1,cookies INTEGER NOT NULL DEFAULT 1,routing_hint INTEGER NOT NULL DEFAULT 1);`); err != nil {
		return nil, err
	}
	m := &Module{db: db, policies: map[string]Policy{}, pool: newTransportPool()}
	rows, err := db.Query("SELECT target,mode,compress,cookies,routing_hint FROM native_wire_policies")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target string
		var p Policy
		var compress, cookies, hint int
		if err = rows.Scan(&target, &p.Mode, &compress, &cookies, &hint); err != nil {
			return nil, err
		}
		if !validMode(p.Mode) {
			return nil, fmt.Errorf("invalid persisted wire policy mode")
		}
		p.Compress, p.Cookies, p.RoutingHint = compress != 0, cookies != 0, hint != 0
		m.policies[target] = p.normalized()
	}
	return m, rows.Err()
}

// Get returns the effective policy for a credential; off when unset.
func (m *Module) Get(target string) Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.policies[target]; ok {
		return p
	}
	return Policy{Mode: "off"}
}

// Register mounts GET/PUT /:target under the module group.
func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/profile", func(c *gin.Context) { c.JSON(200, gin.H{"profile": ProfileSummary()}) })
	g.GET("/:target", func(c *gin.Context) {
		target := c.Param("target")
		c.JSON(200, gin.H{"policy": m.Get(target), "connections": m.pool.stats(target)})
	})
	g.PUT("/:target", func(c *gin.Context) {
		var input Policy
		if !httpx.Decode(c, &input) {
			return
		}
		if !validMode(input.Mode) {
			c.JSON(400, gin.H{"error": "mode must be off or codex"})
			return
		}
		target := c.Param("target")
		if target == "" || len(target) > 256 || m.Eligible == nil || !m.Eligible(target) {
			c.JSON(400, gin.H{"error": "wire profile requires an existing Codex OAuth credential"})
			return
		}
		input = input.normalized()
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, err := m.db.ExecContext(c.Request.Context(), "INSERT INTO native_wire_policies(target,mode,compress,cookies,routing_hint) VALUES(?,?,?,?,?) ON CONFLICT(target) DO UPDATE SET mode=excluded.mode,compress=excluded.compress,cookies=excluded.cookies,routing_hint=excluded.routing_hint", target, input.Mode, boolInt(input.Compress), boolInt(input.Cookies), boolInt(input.RoutingHint)); err != nil {
			httpx.Error(c, err)
			return
		}
		m.policies[target] = input
		// A mode change must not reuse connections negotiated under another profile.
		m.pool.drop(target)
		c.JSON(200, gin.H{"policy": input})
	})
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Transport returns the round tripper for one credential attempt, or nil when
// the policy is off. proxyURL is the credential/global proxy setting already
// resolved by the caller.
func (m *Module) Transport(target, proxyURL string) http.RoundTripper {
	p := m.Get(target)
	if p.Mode != "codex" {
		return nil
	}
	if transport := m.pool.get(target, proxyURL, p); transport != nil {
		return transport
	}
	return nil
}

// SetEnabled retires active connections on disable without destroying the pool.
func (m *Module) SetEnabled(enabled bool) { m.pool.setEnabled(enabled) }

// Close shuts every pooled connection.
func (m *Module) Close() { m.pool.closeAll() }

// WebsocketDialer returns the TLS dialer for gorilla's NetDialTLSContext, or
// nil when the policy is off.
func (m *Module) WebsocketDialer(target, proxyURL string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if m.Get(target).Mode != "codex" {
		return nil
	}
	return websocketTLSDialer(proxyURL)
}

// HeaderTransform returns the header-only normalization applied to both the
// HTTP and WebSocket send paths: one hyphenated session-id (the CLI never
// sends session_id or conversation_id) and a routing hint derived from the
// requested model when the client did not send one. It returns nil when off.
func (m *Module) HeaderTransform(target, model, serviceTier string) func(http.Header) {
	p := m.Get(target)
	if p.Mode != "codex" {
		return nil
	}
	hint := ""
	if p.RoutingHint {
		hint = routingHint(model, serviceTier)
	}
	return func(h http.Header) {
		if h == nil {
			return
		}
		normalizeWebsocketHeaders(h)
		if hint != "" && headerGet(h, "X-Codex-Routing-Hint") == "" {
			h.Set("X-Codex-Routing-Hint", hint)
		}
	}
}

func headerGet(h http.Header, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}
