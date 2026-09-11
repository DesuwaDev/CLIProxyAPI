package risk

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type Input struct {
	Body     []byte
	Provider string
	Model    string
	Account  string
	KeyHash  string
	Session  string
	TraceID  string
}
type Event struct {
	ID            string `json:"id"`
	TimestampMS   int64  `json:"timestamp_ms"`
	TraceID       string `json:"trace_id"`
	Account       string `json:"account"`
	KeyHash       string `json:"key_hash"`
	SessionHash   string `json:"session_hash"`
	InputHash     string `json:"input_hash"`
	InputChars    int    `json:"input_chars"`
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	Mode          string `json:"mode"`
	ConfigVersion int64  `json:"config_version"`
	Blocked       bool   `json:"blocked"`
	Result        Result `json:"result"`
}
type job struct {
	config Config
	event  Event
	text   string
}
type Module struct {
	db           *sql.DB
	keyPath      string
	mu           sync.RWMutex
	config       Config
	client       *http.Client
	statusMu     sync.Mutex
	status       map[string]EndpointStatus
	active       int
	queue        chan job
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	Enabled      func() bool
	Dropped      atomic.Int64
	FailedWrites atomic.Int64
	now          func() time.Time
}

func New(db *sql.DB, keyPath string) (*Module, error) {
	err := storage.Migrate(db, "risk", `
 CREATE TABLE native_risk_config(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL);
 CREATE TABLE native_risk_events(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,timestamp_ms INTEGER NOT NULL,decision TEXT NOT NULL,blocked INTEGER NOT NULL,model TEXT NOT NULL,key_hash TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX native_risk_events_time ON native_risk_events(timestamp_ms,seq);
 CREATE TABLE native_risk_blocks(kind TEXT NOT NULL,target TEXT NOT NULL,expires_ms INTEGER NOT NULL,reason TEXT NOT NULL,PRIMARY KEY(kind,target));
 CREATE INDEX native_risk_blocks_expiry ON native_risk_blocks(expires_ms);
 `)
	if err != nil {
		return nil, err
	}
	c := defaults()
	var raw string
	err = db.QueryRow("SELECT payload FROM native_risk_config WHERE id=1").Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
	}
	if err = validate(c); err != nil {
		return nil, fmt.Errorf("invalid stored risk configuration: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Module{db: db, keyPath: keyPath, config: c, status: map[string]EndpointStatus{}, queue: make(chan job, 64), ctx: ctx, cancel: cancel, now: time.Now}
	// Caller cancellation owns connected I/O. Redirects never forward audit credentials.
	m.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < 4; i++ {
		m.wg.Add(1)
		go m.worker()
	}
	return m, nil
}
func (m *Module) Close()           { m.cancel(); m.wg.Wait() }
func (m *Module) snapshot() Config { m.mu.RLock(); defer m.mu.RUnlock(); return m.config }
func (m *Module) Save(ctx context.Context, c Config) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Version != m.config.Version {
		return Config{}, fmt.Errorf("configuration changed; reload before saving")
	}
	if err := validate(c); err != nil {
		return Config{}, err
	}
	for i := range c.Endpoints {
		e := &c.Endpoints[i]
		e.Ciphertext = ""
		for _, old := range m.config.Endpoints {
			if old.ID == e.ID {
				e.Ciphertext = old.Ciphertext
				break
			}
		}
		if e.ClearToken {
			e.Ciphertext = ""
		}
		if e.Token != "" {
			var err error
			e.Ciphertext, err = m.seal(e.Token)
			if err != nil {
				return Config{}, err
			}
		}
		e.Token = ""
		e.ClearToken = false
		e.HasToken = e.Ciphertext != ""
	}
	c.Version++
	raw, err := json.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	if _, err = m.db.ExecContext(ctx, "INSERT INTO native_risk_config(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(raw)); err != nil {
		return Config{}, err
	}
	m.config = c
	return public(c), nil
}
func applicable(c Config, i Input) bool {
	if c.Mode == "off" {
		return false
	}
	found := len(c.Providers) == 0
	for _, p := range c.Providers {
		if p == i.Provider {
			found = true
		}
	}
	if !found {
		return false
	}
	found = false
	for _, model := range c.Models {
		if model == i.Model {
			found = true
		}
	}
	return c.ModelFilter == "all" || (c.ModelFilter == "include" && found) || (c.ModelFilter == "exclude" && !found)
}
func (m *Module) newEvent(c Config, i Input) Event {
	session := ""
	// A session identifier without an authenticated caller must never block others.
	if i.Session != "" && i.KeyHash != "" {
		session = digest(i.KeyHash + "\x00" + i.Session)
	}
	return Event{ID: uuid.NewString(), TimestampMS: m.now().UnixMilli(), TraceID: i.TraceID, Account: i.Account, KeyHash: i.KeyHash, SessionHash: session, Model: i.Model, Provider: i.Provider, Mode: c.Mode, ConfigVersion: c.Version}
}
func (m *Module) Check(ctx context.Context, i Input) error {
	if m.Enabled != nil && !m.Enabled() {
		return nil
	}
	c := m.snapshot()
	if !applicable(c, i) {
		return nil
	}
	e := m.newEvent(c, i)
	if c.SessionBlock && e.SessionHash != "" {
		blocked, err := m.blocked(ctx, "session", e.SessionHash)
		if err != nil {
			return m.finish(c, e, Result{Decision: "error", Source: "session", ErrorCode: "audit_storage_unavailable"})
		}
		if blocked {
			return m.finish(c, e, Result{Decision: "block", Source: "session", Category: "cyber_policy_session_blocked"})
		}
	}
	text, err := Extract(i.Body, c.LatestTurnOnly, c.MaxInputChars)
	if err != nil {
		return m.finish(c, e, Result{Decision: "error", Source: "input", ErrorCode: err.Error()})
	}
	e.InputHash = digest(text)
	e.InputChars = len([]rune(text))
	if c.HashBlock {
		target := fmt.Sprintf("%d:%s", c.Version, e.InputHash)
		blocked, err := m.blocked(ctx, "hash", target)
		if err != nil {
			return m.finish(c, e, Result{Decision: "error", Source: "hash", ErrorCode: "audit_storage_unavailable"})
		}
		if blocked {
			return m.finish(c, e, Result{Decision: "block", Source: "hash", Category: "flagged_input_hash"})
		}
	}
	if c.Mode == "observe" {
		select {
		case m.queue <- job{c, e, text}:
		default:
			m.Dropped.Add(1)
		}
		return nil
	}
	if !m.acquire(c.Concurrency) {
		return m.finish(c, e, Result{Decision: "error", Source: "capacity", ErrorCode: "audit_capacity_full"})
	}
	defer m.release()
	result := m.scan(ctx, c, text)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return m.finish(c, e, result)
}
func (m *Module) acquire(limit int) bool {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.active >= limit {
		return false
	}
	m.active++
	return true
}
func (m *Module) release() { m.statusMu.Lock(); m.active--; m.statusMu.Unlock() }
func (m *Module) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case j := <-m.queue:
			if m.ctx.Err() != nil {
				return
			}
			// Queued work is discarded on a policy edit or module disable.
			if m.snapshot().Version != j.config.Version || (m.Enabled != nil && !m.Enabled()) {
				m.Dropped.Add(1)
				continue
			}
			if !m.acquire(j.config.Concurrency) {
				m.Dropped.Add(1)
				continue
			}
			result := m.scan(m.ctx, j.config, j.text)
			m.release()
			_ = m.finish(j.config, j.event, result)
		}
	}
}
func (m *Module) finish(c Config, e Event, result Result) error {
	e.Result = result
	e.Blocked = c.Mode == "block" && (result.Decision == "block" || (result.Decision == "error" && c.FailClosed))
	if result.Decision == "block" && c.HashBlock && e.InputHash != "" && result.Source != "hash" && result.Source != "session" {
		if err := m.putBlock("hash", fmt.Sprintf("%d:%s", c.Version, e.InputHash), c.HashTTL, result.Category); err != nil {
			m.FailedWrites.Add(1)
		}
	}
	if c.RecordPass || result.Decision != "allow" {
		raw, err := json.Marshal(e)
		if err == nil {
			_, err = m.db.Exec("INSERT INTO native_risk_events(id,timestamp_ms,decision,blocked,model,key_hash,payload) VALUES(?,?,?,?,?,?,?)", e.ID, e.TimestampMS, result.Decision, e.Blocked, e.Model, e.KeyHash, string(raw))
		}
		if err != nil {
			m.FailedWrites.Add(1)
		}
	}
	if e.Blocked {
		code, status := "native_risk_blocked", 403
		if result.Decision == "error" {
			code = "native_risk_unavailable"
			status = 503
		}
		return &Rejection{Code: code, Status: status}
	}
	return nil
}

type Rejection struct {
	Code   string
	Status int
}

func (e *Rejection) Error() string        { return e.Code }
func (e *Rejection) StatusCode() int      { return e.Status }
func (e *Rejection) IsRequestStop() bool  { return true }
func (e *Rejection) DirectResponse() bool { return true }
func (e *Rejection) ResponseBody() []byte {
	message := "Request blocked by the configured safety policy."
	if e.Status == 503 {
		message = "Request safety review is unavailable. Please retry later."
	}
	raw, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "request_policy_error", "code": e.Code, "message": message}})
	return raw
}
func (m *Module) blocked(ctx context.Context, kind, target string) (bool, error) {
	var expires int64
	err := m.db.QueryRowContext(ctx, "SELECT expires_ms FROM native_risk_blocks WHERE kind=? AND target=?", kind, target).Scan(&expires)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return expires > m.now().UnixMilli(), err
}
func (m *Module) putBlock(kind, target string, ttl int, reason string) error {
	_, err := m.db.Exec("INSERT INTO native_risk_blocks(kind,target,expires_ms,reason) VALUES(?,?,?,?) ON CONFLICT(kind,target) DO UPDATE SET expires_ms=MAX(expires_ms,excluded.expires_ms),reason=excluded.reason", kind, target, m.now().Add(time.Duration(ttl)*time.Second).UnixMilli(), reason)
	return err
}

// ObserveUpstream only accepts a structured cyber_policy code, never substring matches.
func (m *Module) ObserveUpstream(ctx context.Context, i Input, err error) {
	if err == nil || (m.Enabled != nil && !m.Enabled()) {
		return
	}
	c := m.snapshot()
	if !applicable(c, i) {
		return
	}
	raw := err.Error()
	if len(raw) > 65536 {
		return
	}
	var body struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return
	}
	if !strings.EqualFold(body.Code, "cyber_policy") && !strings.EqualFold(body.Error.Code, "cyber_policy") && !strings.EqualFold(body.Error.Type, "cyber_policy") {
		return
	}
	e := m.newEvent(c, i)
	e.Result = Result{Decision: "upstream_block", Source: "upstream", Category: "cyber_policy"}
	e.Blocked = false
	if c.SessionBlock && e.SessionHash != "" {
		if err := m.putBlock("session", e.SessionHash, c.SessionTTL, "cyber_policy"); err != nil {
			m.FailedWrites.Add(1)
		}
	}
	rawEvent, _ := json.Marshal(e)
	if _, err := m.db.ExecContext(context.WithoutCancel(ctx), "INSERT INTO native_risk_events(id,timestamp_ms,decision,blocked,model,key_hash,payload) VALUES(?,?,?,?,?,?,?)", e.ID, e.TimestampMS, e.Result.Decision, false, e.Model, e.KeyHash, string(rawEvent)); err != nil {
		m.FailedWrites.Add(1)
	}
}
