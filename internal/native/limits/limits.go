// Package limits owns process-local admission controls, backed by persistent policies.
package limits

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type Policy struct {
	RPM         int `json:"rpm"`
	Concurrency int `json:"concurrency"`
}

func (p *Policy) UnmarshalJSON(raw []byte) error {
	var input struct {
		RPM         *int `json:"rpm"`
		Concurrency *int `json:"concurrency"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	if input.RPM == nil || input.Concurrency == nil {
		return fmt.Errorf("rpm and concurrency are both required; zero means unlimited")
	}
	p.RPM, p.Concurrency = *input.RPM, *input.Concurrency
	return nil
}

type bucket struct {
	starts   []time.Time
	active   int
	rejected int64
}
type Module struct {
	db       *sql.DB
	mu       sync.Mutex
	policies map[string]Policy
	buckets  map[string]*bucket
	now      func() time.Time
}
type Rejected struct {
	Kind  string
	After time.Duration
}

func (e *Rejected) Error() string              { return "local " + e.Kind + " limit reached" }
func (e *Rejected) StatusCode() int            { return 429 }
func (e *Rejected) RetryAfter() *time.Duration { value := e.After; return &value }
func (e *Rejected) Headers() http.Header {
	return http.Header{"Retry-After": {strconv.Itoa(max(1, int((e.After+time.Second-1)/time.Second)))}}
}

func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "limits", `CREATE TABLE native_limits (scope TEXT NOT NULL,target TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(scope,target));`); err != nil {
		return nil, err
	}
	m := &Module{db: db, policies: map[string]Policy{}, buckets: map[string]*bucket{}, now: time.Now}
	rows, err := db.Query("SELECT scope,target,payload FROM native_limits")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var scope, target, raw string
		var p Policy
		if err = rows.Scan(&scope, &target, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		m.policies[scope+":"+target] = p
	}
	return m, rows.Err()
}

// Acquire atomically checks a rolling 60-second window and active executions.
// Rejections consume no RPM and never modify credential health or provider cooldowns.
func (m *Module) Acquire(ctx context.Context, scope, target string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scope + ":" + target
	p := m.policies[key]
	if target == "" {
		return func() {}, nil
	}
	b := m.buckets[key]
	if b == nil {
		b = &bucket{}
		m.buckets[key] = b
	}
	now := m.now()
	cut := now.Add(-time.Minute)
	n := 0
	for n < len(b.starts) && !b.starts[n].After(cut) {
		n++
	}
	b.starts = append(b.starts[:0], b.starts[n:]...)
	reject := func(kind string, after time.Duration) (func(), error) {
		b.rejected++
		return nil, &Rejected{kind, after}
	}
	if p.Concurrency > 0 && b.active >= p.Concurrency {
		return reject("concurrency", time.Second)
	}
	if p.RPM > 0 && len(b.starts) >= p.RPM {
		return reject("rpm", b.starts[0].Add(time.Minute).Sub(now))
	}
	if len(b.starts) >= 100000 {
		b.starts = append(b.starts[:0], b.starts[len(b.starts)-99999:]...)
	}
	b.starts = append(b.starts, now)
	b.active++
	var once sync.Once
	return func() { once.Do(func() { m.mu.Lock(); defer m.mu.Unlock(); b.active-- }) }, nil
}
func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/:scope/:target", func(c *gin.Context) {
		scope, target, ok := identity(c)
		if !ok {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		key := scope + ":" + target
		b := m.buckets[key]
		active := 0
		var rejected int64
		if b != nil {
			active = b.active
			rejected = b.rejected
		}
		c.JSON(200, gin.H{"policy": m.policies[key], "active": active, "rejected_this_run": rejected, "window_seconds": 60, "scope": "single_process"})
	})
	g.PUT("/:scope/:target", func(c *gin.Context) {
		scope, target, ok := identity(c)
		if !ok {
			return
		}
		var p Policy
		if !httpx.Decode(c, &p) {
			return
		}
		if p.RPM < 0 || p.RPM > 100000 || p.Concurrency < 0 || p.Concurrency > 10000 {
			c.JSON(400, gin.H{"error": "rpm must be 0..100000 and concurrency 0..10000; zero means unlimited"})
			return
		}
		raw, _ := json.Marshal(p)
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, err := m.db.ExecContext(c.Request.Context(), "INSERT INTO native_limits(scope,target,payload) VALUES(?,?,?) ON CONFLICT(scope,target) DO UPDATE SET payload=excluded.payload", scope, target, string(raw)); err != nil {
			httpx.Error(c, err)
			return
		}
		m.policies[scope+":"+target] = p
		c.JSON(200, gin.H{"policy": p})
	})
}
func identity(c *gin.Context) (string, string, bool) {
	scope, target := c.Param("scope"), c.Param("target")
	if (scope != "client" && scope != "credential") || len(target) < 1 || len(target) > 256 {
		c.JSON(400, gin.H{"error": "invalid limit target"})
		return "", "", false
	}
	return scope, target, true
}
func (m *Module) CallerMiddleware(enabled func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled() {
			c.Next()
			return
		}
		key := c.GetString("userApiKey")
		if key == "" {
			c.Next()
			return
		}
		release, err := m.Acquire(c.Request.Context(), "client", event.KeyHash(key))
		if err != nil {
			if limited, ok := err.(*Rejected); ok {
				for k, v := range limited.Headers() {
					c.Header(k, v[0])
				}
				c.AbortWithStatusJSON(429, gin.H{"error": gin.H{"type": "rate_limit_error", "code": "local_" + limited.Kind + "_exceeded", "message": limited.Error()}})
			} else {
				c.AbortWithStatusJSON(499, gin.H{"error": fmt.Sprint(err)})
			}
			return
		}
		defer release()
		c.Next()
	}
}
