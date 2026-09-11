package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/risk"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type riskExecutor struct {
	auth.ProviderExecutor
	calls atomic.Int32
}

func (e *riskExecutor) Identifier() string { return "codex" }
func (e *riskExecutor) Execute(context.Context, *auth.Auth, ex.Request, ex.Options) (ex.Response, error) {
	e.calls.Add(1)
	return ex.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *riskExecutor) ExecuteStream(context.Context, *auth.Auth, ex.Request, ex.Options) (*ex.StreamResult, error) {
	e.calls.Add(1)
	out := make(chan ex.StreamChunk, 1)
	out <- ex.StreamChunk{Payload: []byte("data: done\n\n")}
	close(out)
	return &ex.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}
func TestNativeRiskPreventsExecutionWithoutFailoverOrHealthChanges(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	configResponse := request(t, g, "GET", "risk/config", "")
	var cfg risk.Config
	if err := json.Unmarshal(configResponse.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "block"
	cfg.Strategy = "keywords"
	cfg.Rules = []risk.Rule{{ID: "canary", Pattern: "risk-canary", Enabled: true}}
	raw, _ := json.Marshal(cfg)
	requireOK(t, request(t, g, "PUT", "risk/config", string(raw)))
	m := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	m.SetExecutionPolicy(r)
	upstream := &riskExecutor{}
	m.RegisterExecutor(upstream)
	ctx := auth.WithSkipPersist(context.Background())
	model := "native-risk-policy-model"
	for _, id := range []string{"native-risk-a", "native-risk-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := m.Register(ctx, &auth.Auth{ID: id, Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
	req := ex.Request{Model: model, Payload: []byte(`{"input":"risk-canary"}`)}
	for _, streaming := range []bool{false, true} {
		var err error
		if streaming {
			_, err = m.ExecuteStream(ctx, []string{"codex"}, req, ex.Options{Stream: true})
		} else {
			_, err = m.Execute(ctx, []string{"codex"}, req, ex.Options{})
		}
		var rejection *risk.Rejection
		if !errors.As(err, &rejection) || rejection.StatusCode() != 403 {
			t.Fatalf("expected policy rejection: %v", err)
		}
	}
	if upstream.calls.Load() != 0 {
		t.Fatal("blocked request reached upstream")
	}
	var events int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM native_risk_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("unexpected failover / duplicated review: %d", events)
	}
	for _, a := range m.List() {
		if a.LastError != nil || a.Unavailable || a.Failed != 0 || len(a.ModelStates) != 0 {
			t.Fatalf("policy changed credential state: %s %+v", a.ID, a)
		}
	}
	// Per-turn execution checks also apply when a long-lived session is reused.
	options := ex.Options{Stream: true, Metadata: map[string]any{ex.ExecutionSessionMetadataKey: "same-websocket-session", ex.CallerScopeMetadataKey: "caller"}}
	req.Payload = []byte(`{"input":"allowed"}`)
	stream, err := m.ExecuteStream(ctx, []string{"codex"}, req, options)
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	req.Payload = []byte(`{"input":"risk-canary second turn"}`)
	if _, err = m.ExecuteStream(ctx, []string{"codex"}, req, options); err == nil {
		t.Fatal("second session turn bypassed guard")
	}
	requireOK(t, request(t, g, "PUT", "modules/risk", `{"enabled":false}`))
	if _, err = m.Execute(ctx, []string{"codex"}, req, ex.Options{}); err != nil {
		t.Fatalf("disabled module still blocked: %v", err)
	}
	if upstream.calls.Load() != 2 {
		t.Fatalf("allowed calls=%d", upstream.calls.Load())
	}
	// Structured error bodies do not echo the blocked input.
	if strings.Contains((&risk.Rejection{Code: "native_risk_blocked", Status: 403}).Error(), "risk-canary") {
		t.Fatal("input leaked")
	}
}
