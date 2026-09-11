// Package diagnostics observes downstream outcomes without consuming request bodies.
package diagnostics

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type Request struct {
	ID                 string `json:"id"`
	RequestID          string `json:"request_id"`
	StartedMS          int64  `json:"started_ms"`
	EndedMS            int64  `json:"ended_ms"`
	Endpoint           string `json:"endpoint"`
	KeyHash            string `json:"key_hash"`
	Status             int    `json:"http_status"`
	DurationMS         int64  `json:"duration_ms"`
	Outcome            string `json:"outcome"`
	Stream             bool   `json:"stream"`
	StreamError        bool   `json:"stream_error"`
	InspectionComplete bool   `json:"inspection_complete"`
}

type Module struct {
	db      *sql.DB
	queue   chan Request
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	Failed  atomic.Int64
	Dropped atomic.Int64
}

func New(db *sql.DB) (*Module, error) {
	if err := storage.Migrate(db, "diagnostics", `CREATE TABLE native_requests (id TEXT PRIMARY KEY,request_id TEXT NOT NULL,started_ms INTEGER NOT NULL,ended_ms INTEGER NOT NULL,endpoint TEXT NOT NULL,key_hash TEXT NOT NULL,http_status INTEGER NOT NULL,duration_ms INTEGER NOT NULL,outcome TEXT NOT NULL,payload TEXT NOT NULL); CREATE INDEX native_requests_time ON native_requests(started_ms); CREATE INDEX native_requests_id ON native_requests(request_id);`); err != nil {
		return nil, err
	}
	m := &Module{db: db, queue: make(chan Request, 1024), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		for r := range m.queue {
			b, err := json.Marshal(r)
			if err == nil {
				_, err = m.db.Exec(`INSERT INTO native_requests(id,request_id,started_ms,ended_ms,endpoint,key_hash,http_status,duration_ms,outcome,payload) VALUES(?,?,?,?,?,?,?,?,?,?)`, r.ID, r.RequestID, r.StartedMS, r.EndedMS, r.Endpoint, r.KeyHash, r.Status, r.DurationMS, r.Outcome, string(b))
			}
			if err != nil {
				m.Failed.Add(1)
			}
		}
	}()
	return m, nil
}

func (m *Module) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.queue)
	}
	m.mu.Unlock()
	<-m.done
}
func (m *Module) enqueue(r Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	select {
	case m.queue <- r:
	default:
		m.Dropped.Add(1)
	}
}

func proxyPost(c *gin.Context) bool {
	if c.Request.Method != "POST" || c.GetHeader("Upgrade") != "" {
		return false
	}
	for _, prefix := range []string{"/v1", "/v1beta", "/openai/v1", "/backend-api/codex"} {
		if c.Request.URL.Path == prefix || strings.HasPrefix(c.Request.URL.Path, prefix+"/") {
			return true
		}
	}
	return false
}

func (m *Module) Middleware(enabled func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled() || !proxyPost(c) {
			c.Next()
			return
		}
		started := time.Now()
		id := uuid.NewString()
		c.Request = c.Request.WithContext(logging.WithObservationID(c.Request.Context(), id))
		original := c.Writer
		writer := &observerWriter{ResponseWriter: original, scan: streamScan{complete: true}}
		c.Writer = writer
		defer func() {
			// Never swallow panics; the host recovery middleware retains ownership.
			panicValue := recover()
			status := writer.Status()
			if panicValue != nil && !writer.Written() {
				status = 500
			}
			writer.scan.finish()
			outcome := "http_success"
			switch {
			case panicValue != nil:
				outcome = "panic"
			case c.Request.Context().Err() != nil:
				outcome = "client_cancelled"
			case writer.writeFailed:
				outcome = "write_error"
			case writer.scan.failed:
				outcome = "stream_error"
			case c.Writer.Status() >= 400:
				outcome = "http_error"
			case writer.stream && !writer.scan.complete:
				outcome = "stream_unknown"
			}
			path := c.FullPath()
			if path == "" {
				// Unmatched paths can contain arbitrary caller data; retain only the outcome.
				path = "(unmatched route)"
			}
			if len(path) > 256 {
				path = path[:256]
			}
			m.enqueue(Request{ID: id, RequestID: logging.GetGinRequestID(c), StartedMS: started.UnixMilli(), EndedMS: time.Now().UnixMilli(), Endpoint: c.Request.Method + " " + path, KeyHash: event.KeyHash(c.GetString("userApiKey")), Status: status, DurationMS: time.Since(started).Milliseconds(), Outcome: outcome, Stream: writer.stream, StreamError: writer.scan.failed, InspectionComplete: writer.scan.complete})
			c.Writer = original
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		c.Next()
	}
}

func (m *Module) Register(g *gin.RouterGroup) {
	g.GET("/requests", m.list)
	g.GET("/requests/:id", func(c *gin.Context) {
		var raw string
		err := m.db.QueryRowContext(c.Request.Context(), "SELECT payload FROM native_requests WHERE id=?", c.Param("id")).Scan(&raw)
		if err == sql.ErrNoRows {
			c.JSON(404, gin.H{"error": "Request observation unavailable"})
			return
		}
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.Data(200, "application/json", []byte(raw))
	})
}

func (m *Module) list(c *gin.Context) {
	from, to, err := timeRange(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	where := "started_ms>=? AND started_ms<?"
	args := []any{from, to}
	if id := c.Query("request_id"); id != "" {
		where += " AND request_id=?"
		args = append(args, id)
	}
	if outcome := c.Query("outcome"); outcome != "" {
		where += " AND outcome=?"
		args = append(args, outcome)
	}
	var summary struct {
		Total   int64   `json:"total"`
		Success int64   `json:"success"`
		Errors  int64   `json:"errors"`
		Unknown int64   `json:"unknown"`
		Average float64 `json:"average_ms"`
	}
	if err = m.db.QueryRowContext(c.Request.Context(), "SELECT COUNT(*),COALESCE(SUM(outcome='http_success'),0),COALESCE(SUM(outcome IN ('http_error','stream_error','write_error','panic')),0),COALESCE(SUM(outcome IN ('stream_unknown','client_cancelled')),0),COALESCE(AVG(duration_ms),0) FROM native_requests WHERE "+where, args...).Scan(&summary.Total, &summary.Success, &summary.Errors, &summary.Unknown, &summary.Average); err != nil {
		httpx.Error(c, err)
		return
	}
	if before := c.Query("before"); before != "" {
		cursor, parseErr := strconv.ParseInt(before, 10, 64)
		if parseErr != nil || cursor <= 0 {
			c.JSON(400, gin.H{"error": "Invalid request cursor"})
			return
		}
		where += " AND rowid<?"
		args = append(args, cursor)
	}
	rows, err := m.db.QueryContext(c.Request.Context(), "SELECT rowid,payload FROM native_requests WHERE "+where+" ORDER BY rowid DESC LIMIT 101", args...)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer func() { _ = rows.Close() }()
	type item struct {
		Sequence int64 `json:"sequence"`
		Request
	}
	items := []item{}
	for rows.Next() {
		var v item
		var raw string
		if err = rows.Scan(&v.Sequence, &raw); err == nil {
			err = json.Unmarshal([]byte(raw), &v.Request)
		}
		if err != nil {
			httpx.Error(c, err)
			return
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	next := int64(0)
	if len(items) > 100 {
		items = items[:100]
		next = items[99].Sequence
	}
	c.JSON(200, gin.H{"summary": summary, "requests": items, "next_before": next, "scope": "downstream_http_post", "dropped_this_run": m.Dropped.Load(), "failed_writes_this_run": m.Failed.Load()})
}
