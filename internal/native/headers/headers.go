// Package headers owns opt-in Codex OAuth outbound header normalization.
package headers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexidentity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"golang.org/x/net/http/httpguts"
)

type Module struct {
	db       *sql.DB
	mu       sync.RWMutex
	modes    map[string]string
	versions versionState
	Eligible func(string) bool
}

func validMode(mode string) bool { return mode == "off" || mode == "clean" || mode == "client" }

func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "headers", `CREATE TABLE native_header_policies (target TEXT PRIMARY KEY,mode TEXT NOT NULL);`); err != nil {
		return nil, err
	}
	m := &Module{db: db, modes: map[string]string{}}
	rows, err := db.Query("SELECT target,mode FROM native_header_policies")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target, mode string
		if err = rows.Scan(&target, &mode); err != nil {
			return nil, err
		}
		if !validMode(mode) {
			return nil, fmt.Errorf("invalid persisted header policy mode")
		}
		m.modes[target] = mode
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := m.initVersions(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) mode(target string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mode := m.modes[target]; mode != "" {
		return mode
	}
	return "off"
}

func (m *Module) Register(g *gin.RouterGroup) {
	m.registerVersions(g)
	g.GET("/:target", func(c *gin.Context) {
		c.JSON(200, gin.H{"mode": m.mode(c.Param("target"))})
	})
	g.PUT("/:target", func(c *gin.Context) {
		var input struct {
			Mode string `json:"mode"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if !validMode(input.Mode) {
			c.JSON(400, gin.H{"error": "mode must be off, clean or client"})
			return
		}
		target := c.Param("target")
		if target == "" || len(target) > 256 || m.Eligible == nil || !m.Eligible(target) {
			c.JSON(400, gin.H{"error": "header normalization requires an existing Codex OAuth credential"})
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, err := m.db.ExecContext(c.Request.Context(), "INSERT INTO native_header_policies(target,mode) VALUES(?,?) ON CONFLICT(target) DO UPDATE SET mode=excluded.mode", target, input.Mode); err != nil {
			httpx.Error(c, err)
			return
		}
		m.modes[target] = input.Mode
		c.JSON(200, gin.H{"mode": input.Mode})
	})
}

// Transform snapshots policy and allowlisted input, excluding caller Authorization/Cookie.
func (m *Module) Transform(target string, incoming http.Header) ex.OutboundHeaderTransform {
	mode := m.mode(target)
	version := m.VersionStatus()
	versionOverride := version.Effective
	if version.EffectiveFrom == "builtin" {
		versionOverride = ""
	}
	if mode == "off" && versionOverride == "" {
		return nil
	}
	client := http.Header{}
	nominated := connectionNames(incoming)
	for _, name := range []string{"User-Agent", "Originator", "Version", "Chatgpt-Account-Id", "X-Codex-Routing-Hint"} {
		if value := singleValue(incoming, name); value != "" && !nominated[strings.ToLower(name)] {
			client.Set(name, value)
		}
	}
	return func(out http.Header) {
		if mode != "off" {
			clean(out)
		}
		if mode != "client" || client.Get("User-Agent") == "" || client.Get("Originator") == "" {
			codexidentity.RewriteHeaders(out, versionOverride)
			return
		}
		// Restore the pair together; absent/ambiguous identities keep executor defaults.
		for _, name := range []string{"User-Agent", "Originator", "Version"} {
			remove(out, name)
			if value := client.Get(name); value != "" {
				out.Set(name, value)
			}
		}
		// Opaque routing hints may belong to an account. Never mix them across the pool.
		if account := client.Get("Chatgpt-Account-Id"); account != "" && account == out.Get("Chatgpt-Account-Id") {
			if hint := client.Get("X-Codex-Routing-Hint"); hint != "" {
				remove(out, "X-Codex-Routing-Hint")
				out.Set("X-Codex-Routing-Hint", hint)
			}
		}
	}
}

func singleValue(h http.Header, name string) string {
	var values []string
	for key, vv := range h {
		if strings.EqualFold(key, name) {
			values = append(values, vv...)
		}
	}
	if len(values) != 1 || len(values[0]) > 4096 || !httpguts.ValidHeaderFieldValue(values[0]) {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func connectionNames(h http.Header) map[string]bool {
	names := map[string]bool{}
	for key, values := range h {
		if strings.EqualFold(key, "Connection") {
			for _, value := range values {
				for _, name := range strings.Split(value, ",") {
					names[strings.ToLower(strings.TrimSpace(name))] = true
				}
			}
		}
	}
	return names
}

func remove(h http.Header, name string) {
	for key := range h {
		if strings.EqualFold(key, name) {
			delete(h, key)
		}
	}
}

func clean(h http.Header) {
	nominated := connectionNames(h)
	for key := range h {
		name := strings.ToLower(key)
		// These belong to the selected upstream credential or executor, never the caller.
		if name == "authorization" || name == "chatgpt-account-id" || name == "content-type" || name == "accept" || name == "cookie" {
			continue
		}
		drop := nominated[name] || strings.HasPrefix(name, "x-forwarded-") || strings.HasPrefix(name, "sec-websocket-")
		switch name {
		case "connection", "proxy-connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "proxy-authenticate", "proxy-authorization",
			"forwarded", "via", "x-real-ip", "x-client-ip", "client-ip", "true-client-ip", "cf-connecting-ip", "cf-connecting-ipv6", "cf-ray", "cf-ipcountry", "cf-visitor", "cdn-loop",
			"content-length", "content-encoding":
			drop = true
		}
		if drop {
			delete(h, key)
		}
	}
}
