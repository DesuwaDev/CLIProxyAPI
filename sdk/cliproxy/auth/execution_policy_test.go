package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type admissionFixture struct {
	released  atomic.Int32
	rejectAll bool
}

func (p *admissionFixture) BeforeExecute(ctx context.Context, a *Auth, _ ex.Request, _ ex.Options) (context.Context, func(), error) {
	if p.rejectAll || a.ID == "policy-a" {
		return ctx, nil, &Error{Code: "local_limit", Message: "busy", HTTPStatus: 429}
	}
	return ctx, func() { p.released.Add(1) }, nil
}

type policyExecutor struct {
	ProviderExecutor
	chunks chan ex.StreamChunk
	calls  atomic.Int32
}

func (e *policyExecutor) Identifier() string { return "policy-test" }
func (e *policyExecutor) Execute(_ context.Context, a *Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
	e.calls.Add(1)
	return ex.Response{Payload: []byte(a.ID)}, nil
}
func (e *policyExecutor) ExecuteStream(context.Context, *Auth, ex.Request, ex.Options) (*ex.StreamResult, error) {
	e.calls.Add(1)
	return &ex.StreamResult{Headers: http.Header{"X-Test": {"kept"}}, Chunks: e.chunks}, nil
}

func TestExecutionPolicyFallbackNeutralityAndStreamLifetime(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	p := &admissionFixture{}
	m.SetExecutionPolicy(p)
	executor := &policyExecutor{chunks: make(chan ex.StreamChunk)}
	m.RegisterExecutor(executor)
	ctx := WithSkipPersist(context.Background())
	model := "native-policy-model"
	for _, id := range []string{"policy-a", "policy-b"} {
		a := &Auth{ID: id, Provider: "policy-test"}
		registry.GetGlobalRegistry().RegisterClient(id, "policy-test", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := m.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := m.Execute(ctx, []string{"policy-test"}, ex.Request{Model: model}, ex.Options{})
	if err != nil || string(resp.Payload) != "policy-b" {
		t.Fatalf("fallback: %s %v", resp.Payload, err)
	}
	a, _ := m.GetByID("policy-a")
	if a.Unavailable || a.LastError != nil || !a.NextRetryAfter.IsZero() || len(a.ModelStates) != 0 {
		t.Fatal("local rejection altered credential health")
	}
	p.rejectAll = true
	_, err = m.Execute(ctx, []string{"policy-test"}, ex.Request{Model: model}, ex.Options{})
	if statusCodeFromError(err) != 429 {
		t.Fatalf("all busy must return 429: %v", err)
	}
	p.rejectAll = false
	p.released.Store(0)
	stream, err := m.streamAdmitted(ctx, executor, &Auth{ID: "policy-b"}, ex.Request{}, ex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.released.Load() != 0 || stream.Headers.Get("X-Test") != "kept" {
		t.Fatal("stream released before completion")
	}
	go func() { executor.chunks <- ex.StreamChunk{Payload: []byte("one")}; close(executor.chunks) }()
	if string((<-stream.Chunks).Payload) != "one" {
		t.Fatal("lost stream chunk")
	}
	for range stream.Chunks {
	}
	if p.released.Load() != 1 {
		t.Fatal("stream close did not release exactly once")
	}
	// No real sleeps: cancellation drains an upstream producer before releasing.
	executor.chunks = make(chan ex.StreamChunk)
	cancelCtx, cancel := context.WithCancel(ctx)
	stream, err = m.streamAdmitted(cancelCtx, executor, &Auth{ID: "policy-b"}, ex.Request{}, ex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	go func() { executor.chunks <- ex.StreamChunk{}; close(executor.chunks) }()
	select {
	case <-stream.Chunks:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled stream leaked")
	}
	for range stream.Chunks {
	}
	if p.released.Load() != 2 {
		t.Fatal("cancel did not release")
	}
}
