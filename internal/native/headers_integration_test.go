package native

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestHeaderAndFingerprintModulesComposeAndDisableIndependently(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	r.SetIdentityLookup(func(id string) (Identity, bool) {
		return Identity{ID: id, Provider: "codex", Fingerprint: true, Headers: true}, id == "account"
	})
	requireOK(t, request(t, g, "PUT", "headers/account", `{"mode":"client"}`))
	a := &auth.Auth{ID: "account", Index: "account", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "synthetic"}}
	body := []byte(`{"model":"test-model","input":[],"client_metadata":{"session_id":"incoming-session","thread_id":"incoming-thread"}}`)
	opts := ex.Options{Headers: http.Header{"User-Agent": {"real-client"}, "Originator": {"codex_exec"}}, OriginalRequest: body}
	apply := func() (context.Context, http.Header, []byte) {
		t.Helper()
		ctx, release, err := r.BeforeExecute(context.Background(), a, ex.Request{Model: "test-model", Payload: body}, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		h := http.Header{"User-Agent": {"executor-default"}, "Originator": {"codex-tui"}}
		b, err := ex.TransformOutbound(ctx, h, body)
		if err != nil {
			t.Fatal(err)
		}
		return ctx, h, b
	}
	ctx, h, b := apply()
	if ex.HasOutboundIdentityTransform(ctx) || !ex.HasOutboundTransform(ctx) || h.Get("User-Agent") != "real-client" || string(b) != string(body) {
		t.Fatal("header-only policy changed the body or superseded legacy identity handling")
	}
	requireOK(t, request(t, g, "PUT", "fingerprint/account", `{"mode":"session"}`))
	ctx, h, b = apply()
	if !ex.HasOutboundIdentityTransform(ctx) || h.Get("User-Agent") != "real-client" || h.Get("Thread-Id") == "" || h.Get("Thread-Id") != gjson.GetBytes(b, "client_metadata.thread_id").String() {
		t.Fatal("header and fingerprint transforms did not compose")
	}
	requireOK(t, request(t, g, "PUT", "modules/headers", `{"enabled":false}`))
	_, h, b = apply()
	if h.Get("User-Agent") != "executor-default" || h.Get("Thread-Id") == "" {
		t.Fatal("disabling headers also disabled fingerprinting")
	}
	requireOK(t, request(t, g, "PUT", "modules/headers", `{"enabled":true}`))
	a.Metadata = map[string]any{"api_key": "synthetic"}
	ctx, h, _ = apply()
	if ex.HasOutboundTransform(ctx) || h.Get("User-Agent") != "executor-default" {
		t.Fatal("OAuth-only policy applied to API-key credential")
	}
}
