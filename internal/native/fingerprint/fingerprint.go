// Package fingerprint owns optional, account-scoped Codex request identities.
package fingerprint

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type Policy struct {
	Mode string `json:"mode"`
	Seed string `json:"seed"`
}
type Module struct {
	db       *sql.DB
	mu       sync.RWMutex
	policies map[string]Policy
	Eligible func(string) bool
}

func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "fingerprint", `CREATE TABLE native_fingerprints (target TEXT PRIMARY KEY,payload TEXT NOT NULL);`); err != nil {
		return nil, err
	}
	m := &Module{db: db, policies: map[string]Policy{}}
	rows, err := db.Query("SELECT target,payload FROM native_fingerprints")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target, raw string
		var p Policy
		if err = rows.Scan(&target, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		switch p.Mode {
		case "off", "device", "session", "full":
		default:
			return nil, fmt.Errorf("invalid persisted fingerprint mode")
		}
		if _, err = uuid.Parse(p.Seed); err != nil {
			return nil, fmt.Errorf("invalid persisted fingerprint seed")
		}
		m.policies[target] = p
	}
	return m, rows.Err()
}
func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/:target", func(c *gin.Context) {
		m.mu.RLock()
		p := m.policies[c.Param("target")]
		m.mu.RUnlock()
		if p.Mode == "" {
			p.Mode = "off"
		}
		c.JSON(200, gin.H{"mode": p.Mode, "configured": p.Seed != ""})
	})
	g.PUT("/:target", func(c *gin.Context) {
		target := c.Param("target")
		var input struct {
			Mode string `json:"mode"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		switch input.Mode {
		case "off", "device", "session", "full":
		default:
			c.JSON(400, gin.H{"error": "mode must be off, device, session or full"})
			return
		}
		if len(target) == 0 || len(target) > 256 || m.Eligible == nil || !m.Eligible(target) {
			c.JSON(400, gin.H{"error": "fingerprint convergence requires an existing Codex OAuth credential"})
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		p := m.policies[target]
		if p.Seed == "" {
			p.Seed = uuid.NewString()
		}
		p.Mode = input.Mode
		raw, _ := json.Marshal(p)
		if _, err := m.db.ExecContext(c.Request.Context(), "INSERT INTO native_fingerprints(target,payload) VALUES(?,?) ON CONFLICT(target) DO UPDATE SET payload=excluded.payload", target, string(raw)); err != nil {
			httpx.Error(c, err)
			return
		}
		m.policies[target] = p
		c.JSON(200, gin.H{"mode": p.Mode, "configured": true})
	})
}

// Transform resolves one identity set per attempt. It does not change connection
// pools, routing keys, tool IDs or explicit prompt cache keys.
func (m *Module) Transform(target, caller string, original []byte, headers http.Header) ex.OutboundTransform {
	m.mu.RLock()
	p := m.policies[target]
	m.mu.RUnlock()
	if p.Mode == "" || p.Mode == "off" {
		return nil
	}
	seed, err := uuid.Parse(p.Seed)
	if err != nil {
		return nil
	}
	derive := func(value string) string { return uuid.NewSHA1(seed, []byte(value)).String() }
	installation := derive("installation")
	session := derive("session")
	thread := derive("thread")
	bodySession := gjson.GetBytes(original, "client_metadata.session_id").String()
	originalSession := bodySession
	originalThread := gjson.GetBytes(original, "client_metadata.thread_id").String()
	if originalSession == "" {
		originalSession = headers.Get("Session-Id")
		if originalSession == "" {
			originalSession = headers.Get("Session_id")
		}
	}
	if originalThread == "" {
		originalThread = headers.Get("Thread-Id")
	}
	if p.Mode == "session" {
		if originalSession == "" && originalThread == "" {
			thread = uuid.NewString()
		} else {
			identity, _ := json.Marshal([]string{caller, originalSession, originalThread})
			thread = derive("client-thread:" + string(identity))
		}
	}
	turn := uuid.NewString()
	started := time.Now().UnixMilli()
	return func(h http.Header, body []byte) ([]byte, error) {
		var err error
		if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
			return nil, fmt.Errorf("fingerprint convergence requires a JSON object")
		}
		fields := map[string]any{"installation_id": installation}
		values := map[string]string{"x-codex-installation-id": installation}
		if p.Mode != "device" {
			fields["session_id"] = session
			fields["thread_id"] = thread
			fields["turn_id"] = turn
			fields["window_id"] = thread + ":0"
			fields["turn_started_at_unix_ms"] = started
			values["session_id"] = session
			values["thread_id"] = thread
			values["turn_id"] = turn
			values["x-codex-window-id"] = thread + ":0"
		}
		// Merge raw JSON fields without rounding unknown numeric metadata.
		merge := func(raw string) string {
			obj := map[string]json.RawMessage{}
			if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
				obj = map[string]json.RawMessage{}
			}
			for k, v := range fields {
				b, _ := json.Marshal(v)
				obj[k] = b
			}
			out, _ := json.Marshal(obj)
			return string(out)
		}
		next := body
		if v := gjson.GetBytes(next, "client_metadata"); !v.IsObject() {
			next, err = sjson.SetRawBytes(next, "client_metadata", []byte("{}"))
			if err != nil {
				return nil, err
			}
		}
		for k, v := range values {
			next, err = sjson.SetBytes(next, "client_metadata."+k, v)
			if err != nil {
				return nil, err
			}
		}
		metadata := merge(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
		next, err = sjson.SetBytes(next, "client_metadata.x-codex-turn-metadata", metadata)
		if err != nil {
			return nil, err
		}
		if p.Mode != "device" && bodySession != "" && gjson.GetBytes(body, "prompt_cache_key").String() == bodySession {
			next, err = sjson.SetBytes(next, "prompt_cache_key", session)
			if err != nil {
				return nil, err
			}
		}
		h.Set("X-Codex-Installation-Id", installation)
		h.Set("X-Codex-Turn-Metadata", merge(h.Get("X-Codex-Turn-Metadata")))
		if p.Mode != "device" {
			h.Set("Session-Id", session)
			h.Set("Session_id", session)
			h.Set("Thread-Id", thread)
			h.Set("X-Client-Request-Id", thread)
			h.Set("X-Codex-Window-Id", thread+":0")
		}
		return next, nil
	}
}
