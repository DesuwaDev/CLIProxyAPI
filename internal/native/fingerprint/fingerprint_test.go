package fingerprint

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountModesIsolationAndStableSeed(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "fingerprint.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	m.Eligible = func(id string) bool { return id == "a" || id == "b" }
	g := gin.New()
	m.Register(g.Group("/fp"))
	set := func(id, mode string) {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("PUT", "/fp/"+id, strings.NewReader(`{"mode":"`+mode+`"}`)))
		if w.Code != 200 {
			t.Fatalf("mode: %s", w.Body.String())
		}
	}
	original := []byte(`{"client_metadata":{"session_id":"client-session","thread_id":"client-thread","custom":9007199254740993,"x-codex-turn-metadata":"{\"extra\":true}"},"prompt_cache_key":"custom-cache","previous_response_id":"resp-unchanged","input":[{"id":"tool-unchanged"}]}`)
	if m.Transform("a", "caller", original, nil) != nil {
		t.Fatal("default must be off")
	}
	apply := func(m *Module, id, caller string) (http.Header, []byte) {
		h := http.Header{"Session-Id": {"original-session"}, "Thread-Id": {"original-thread"}}
		transform := m.Transform(id, caller, original, h)
		if transform == nil {
			t.Fatal("missing transform")
		}
		b, err := transform(h, original)
		if err != nil {
			t.Fatal(err)
		}
		return h, b
	}
	set("a", "device")
	hd, b := apply(m, "a", "caller")
	if hd.Get("Session-Id") != "original-session" || hd.Get("Thread-Id") != "original-thread" || gjson.GetBytes(b, "client_metadata.session_id").String() != "client-session" {
		t.Fatal("device changed session")
	}
	installation := hd.Get("X-Codex-Installation-Id")
	set("a", "session")
	h1, b1 := apply(m, "a", "caller1")
	h2, _ := apply(m, "a", "caller2")
	h3, _ := apply(m, "a", "caller1")
	if h1.Get("Session-Id") != h2.Get("Session-Id") || h1.Get("Thread-Id") == h2.Get("Thread-Id") || h1.Get("Thread-Id") != h3.Get("Thread-Id") {
		t.Fatal("caller thread isolation unstable")
	}
	if installation != h1.Get("X-Codex-Installation-Id") {
		t.Fatal("mode switch rotated device")
	}
	if gjson.GetBytes(b1, "client_metadata.custom").Raw != "9007199254740993" || gjson.GetBytes(b1, "prompt_cache_key").String() != "custom-cache" || gjson.GetBytes(b1, "previous_response_id").String() != "resp-unchanged" || gjson.GetBytes(b1, "input.0.id").String() != "tool-unchanged" {
		t.Fatal("unrelated request fields changed")
	}
	if gjson.GetBytes(b1, "client_metadata.session_id").String() != h1.Get("Session-Id") || gjson.GetBytes(b1, "client_metadata.thread_id").String() != h1.Get("Thread-Id") {
		t.Fatal("body/header identity mismatch")
	}
	headerTurn := gjson.Get(h1.Get("X-Codex-Turn-Metadata"), "turn_id").String()
	if headerTurn == "" || headerTurn != gjson.GetBytes(b1, "client_metadata.turn_id").String() {
		t.Fatal("turn metadata mismatch")
	}
	set("b", "session")
	hb, _ := apply(m, "b", "caller1")
	if hb.Get("X-Codex-Installation-Id") == installation || hb.Get("Session-Id") == h1.Get("Session-Id") {
		t.Fatal("accounts share identities")
	}
	set("a", "full")
	hf, _ := apply(m, "a", "caller1")
	hg, _ := apply(m, "a", "caller2")
	if hf.Get("Thread-Id") != hg.Get("Thread-Id") {
		t.Fatal("full mode did not converge")
	}
	set("a", "off")
	if m.Transform("a", "caller", original, nil) != nil {
		t.Fatal("off still rewrites")
	}
	set("a", "session")
	reopened, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	hr, _ := apply(reopened, "a", "caller1")
	if hr.Get("X-Codex-Installation-Id") != installation || hr.Get("Thread-Id") != h1.Get("Thread-Id") {
		t.Fatal("restart changed identity")
	}
}
