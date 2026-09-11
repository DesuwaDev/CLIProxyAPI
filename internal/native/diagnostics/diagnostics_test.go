package diagnostics

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

func TestDownstreamStreamingObservationPreservesWireAndPrivacy(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "observations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	g := gin.New()
	g.Use(gin.Recovery(), m.Middleware(func() bool { return true }))
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"private-response-canary\"}}]}\r\n\r\nevent: error\ndata: {\"error\":{\"message\":\"private-error-canary\"}}\n\n"
	var id string
	g.POST("/v1/chat/completions", func(c *gin.Context) {
		id = logging.ObservationID(c.Request.Context())
		c.Set("userApiKey", "private-key-canary")
		c.Header("Content-Type", "text/event-stream")
		for i := 0; i < len(payload); i += 7 {
			end := min(i+7, len(payload))
			if _, errWrite := c.Writer.Write([]byte(payload[i:end])); errWrite != nil {
				t.Fatal(errWrite)
			}
			c.Writer.Flush()
		}
	})
	g.POST("/v1/panic", func(c *gin.Context) { panic("test panic") })
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("private-prompt-canary")))
	if w.Code != 200 || w.Body.String() != payload || id == "" {
		t.Fatalf("wire or correlation changed: HTTP %d", w.Code)
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "/v1/panic", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	m.Close()
	var raw string
	if err = db.QueryRow("SELECT payload FROM native_requests WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var r Request
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "stream_error" || r.Status != 200 || !r.StreamError || !r.InspectionComplete || r.KeyHash != event.KeyHash("private-key-canary") {
		t.Fatalf("bad observation: %+v", r)
	}
	if strings.Contains(raw, "canary") {
		t.Fatal("private content persisted")
	}
	var status int
	if err = db.QueryRow("SELECT http_status FROM native_requests WHERE outcome='panic'").Scan(&status); err != nil || status != 500 {
		t.Fatalf("panic observation: %d %v", status, err)
	}
	scan := streamScan{complete: true}
	scan.feed([]byte("data: " + strings.Repeat("x", 65537) + "\n\nevent: error\n\n"))
	scan.finish()
	if scan.complete || !scan.failed || len(scan.line) != 0 {
		t.Fatal("oversized data must mark incomplete without hiding later errors")
	}
	if m.Failed.Load() != 0 || m.Dropped.Load() != 0 {
		t.Fatal("unexpected observation loss")
	}
}
