package headers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

func TestHeaderPolicyPersistenceEligibilityAndNormalization(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "headers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	m.Eligible = func(id string) bool { return id == "account" }
	g := gin.New()
	m.Register(g.Group("/headers"))
	set := func(target, mode string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("PUT", "/headers/"+target, strings.NewReader(`{"mode":"`+mode+`"}`)))
		if w.Code != status {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	if m.Transform("account", nil) != nil {
		t.Fatal("new accounts must be off")
	}
	set("other", "client", 400)
	set("account", "unknown", 400)
	set("account", "client", 200)
	restored, err := New(db)
	if err != nil || restored.mode("account") != "client" {
		t.Fatalf("policy did not survive reload: %v", err)
	}
	incoming := http.Header{
		"user-agent": {"codex_exec/0.153.4 (Windows)"}, "originator": {"codex_exec"},
		"authorization": {"Bearer caller-secret"}, "cookie": {"caller-cookie"},
		"chatgpt-account-id": {"selected"}, "x-codex-routing-hint": {"opaque-hint"},
		"Content-Encoding": {"zstd"},
	}
	transform := restored.Transform("account", incoming)
	incoming["user-agent"][0] = "mutated-after-snapshot"
	out := http.Header{
		"User-Agent": {"old-macos"}, "Originator": {"codex-tui"}, "Version": {"injected-version"},
		"Authorization": {"Bearer selected-secret"}, "Chatgpt-Account-Id": {"selected"},
		"Cookie": {"selected-cookie"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
		"Connection": {"keep-alive, X-Leak, Authorization, Cookie"}, "X-Leak": {"proxy-data"},
		"x-forwarded-for": {"private-ip"}, "Cf-Connecting-Ip": {"private-ip"}, "Via": {"proxy"},
		"Content-Encoding": {"zstd"}, "Content-Length": {"12345"},
		"Sec-Websocket-Key": {"caller-handshake"}, "Proxy-Authorization": {"proxy-secret"},
	}
	transform(out)
	if out.Get("User-Agent") != "codex_exec/0.153.4 (Windows)" || out.Get("Originator") != "codex_exec" || out.Get("Version") != "" {
		t.Fatal("client identity pair or absence of Version was not preserved")
	}
	if out.Get("Authorization") != "Bearer selected-secret" || out.Get("Chatgpt-Account-Id") != "selected" || out.Get("Cookie") != "selected-cookie" {
		t.Fatal("selected upstream credentials were replaced")
	}
	if out.Get("X-Codex-Routing-Hint") != "opaque-hint" {
		t.Fatal("matching-account routing hint missing")
	}
	for _, name := range []string{"Connection", "X-Leak", "X-Forwarded-For", "Cf-Connecting-Ip", "Via", "Content-Encoding", "Content-Length", "Sec-Websocket-Key", "Proxy-Authorization"} {
		for key := range out {
			if strings.EqualFold(key, name) {
				t.Fatalf("unsafe/stale header retained: %s", key)
			}
		}
	}
	other := http.Header{"Chatgpt-Account-Id": {"different-account"}}
	transform(other)
	if other.Get("X-Codex-Routing-Hint") != "" {
		t.Fatal("routing hint crossed account boundary")
	}
	set("account", "clean", 200)
	cleaned := http.Header{"User-Agent": {"executor-default"}, "Originator": {"executor"}, "Via": {"proxy"}}
	m.Transform("account", incoming)(cleaned)
	if cleaned.Get("User-Agent") != "executor-default" || cleaned.Get("Via") != "" {
		t.Fatal("clean-only mode changed identity or retained proxy header")
	}
	set("account", "off", 200)
	if m.Transform("account", incoming) != nil || m.Transform("other", incoming) != nil {
		t.Fatal("disabled or unconfigured account changed headers")
	}
}

func TestAmbiguousAndHopByHopClientIdentityDoesNotReplaceDefaults(t *testing.T) {
	m := &Module{modes: map[string]string{"a": "client"}}
	for _, h := range []http.Header{
		{"User-Agent": {"client"}},
		{"User-Agent": {"a", "b"}, "Originator": {"codex_exec"}},
		{"User-Agent": {"a"}, "user-agent": {"b"}, "Originator": {"codex_exec"}},
		{"User-Agent": {"a\r\nX-Fake: yes"}, "Originator": {"codex_exec"}},
		{"User-Agent": {"a"}, "Originator": {"codex_exec"}, "Connection": {"User-Agent"}},
	} {
		out := http.Header{"User-Agent": {"default"}, "Originator": {"default"}}
		m.Transform("a", h)(out)
		if out.Get("User-Agent") != "default" || out.Get("Originator") != "default" {
			t.Fatal("incomplete, ambiguous or hop-by-hop identity was forwarded")
		}
	}
}
