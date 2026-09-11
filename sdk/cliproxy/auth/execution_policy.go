package auth

import (
	"context"
	"errors"
	"net/http"

	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ExecutionPolicy is an optional host-owned admission and request-context extension.
// It runs per provider attempt; release owns the full lifetime of streaming chunks.
type ExecutionPolicy interface {
	BeforeExecute(context.Context, *Auth, ex.Request, ex.Options) (context.Context, func(), error)
}

// ExecutionObserver receives terminal errors before downstream delivery.
type ExecutionObserver interface {
	ObserveExecution(context.Context, *Auth, ex.Request, ex.Options, error)
}

func (m *Manager) observeExecution(ctx context.Context, a *Auth, r ex.Request, o ex.Options, err error) {
	if state := m.executionPolicy.Load(); state != nil && !m.HomeEnabled() {
		if observer, ok := state.policy.(ExecutionObserver); ok {
			observer.ObserveExecution(ctx, a, r, o, err)
		}
	}
}

type executionPolicyState struct{ policy ExecutionPolicy }

func (m *Manager) SetExecutionPolicy(p ExecutionPolicy) {
	if p == nil {
		m.executionPolicy.Store(nil)
	} else {
		m.executionPolicy.Store(&executionPolicyState{p})
	}
}

type localAdmissionError struct{ cause error }

func (e *localAdmissionError) Error() string   { return e.cause.Error() }
func (e *localAdmissionError) Unwrap() error   { return e.cause }
func (e *localAdmissionError) StatusCode() int { return statusCodeFromError(e.cause) }
func (e *localAdmissionError) Headers() http.Header {
	var h interface{ Headers() http.Header }
	if errors.As(e.cause, &h) {
		return h.Headers()
	}
	return nil
}
func isLocalAdmissionError(err error) bool {
	var target *localAdmissionError
	return errors.As(err, &target)
}

const localAdmissionCode = "local_admission_rejected"

func (m *Manager) prepareExecutionPolicy(ctx context.Context, a *Auth, r ex.Request, o ex.Options) (context.Context, func(), error) {
	state := m.executionPolicy.Load()
	if state == nil || m.HomeEnabled() {
		return ctx, func() {}, nil
	}
	next, release, err := state.policy.BeforeExecute(ctx, a, r, o)
	if err != nil {
		if release != nil {
			release()
		}
		return ctx, nil, &localAdmissionError{err}
	}
	if next == nil {
		next = ctx
	}
	if release == nil {
		release = func() {}
	}
	return next, release, nil
}
func (m *Manager) executeAdmitted(ctx context.Context, p ProviderExecutor, a *Auth, r ex.Request, o ex.Options, count bool) (ex.Response, error) {
	ctx, release, err := m.prepareExecutionPolicy(ctx, a, r, o)
	if err != nil {
		return ex.Response{}, err
	}
	defer release()
	if count {
		return p.CountTokens(ctx, a, r, o)
	}
	response, err := p.Execute(ctx, a, r, o)
	m.observeExecution(ctx, a, r, o, err)
	return response, err
}
func (m *Manager) streamAdmitted(ctx context.Context, p ProviderExecutor, a *Auth, r ex.Request, o ex.Options) (*ex.StreamResult, error) {
	if m.executionPolicy.Load() == nil || m.HomeEnabled() {
		return p.ExecuteStream(ctx, a, r, o)
	}
	ctx, release, err := m.prepareExecutionPolicy(ctx, a, r, o)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	stream, err := p.ExecuteStream(ctx, a, r, o)
	m.observeExecution(ctx, a, r, o, err)
	if err != nil || stream == nil || stream.Chunks == nil {
		return stream, err
	}
	out := make(chan ex.StreamChunk)
	transferred = true
	go func() {
		defer close(out)
		defer release()
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				m.observeExecution(ctx, a, r, o, chunk.Err)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				for range stream.Chunks {
				}
				return
			}
		}
	}()
	return &ex.StreamResult{Headers: stream.Headers, Chunks: out}, nil
}
