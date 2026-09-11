package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
)

type outboundTransformKey struct{}
type outboundHeaderTransformKey struct{}
type OutboundTransform func(http.Header, []byte) ([]byte, error)
type OutboundHeaderTransform func(http.Header)

func WithOutboundTransform(ctx context.Context, fn OutboundTransform) context.Context {
	return context.WithValue(ctx, outboundTransformKey{}, fn)
}
func HasOutboundTransform(ctx context.Context) bool {
	return HasOutboundIdentityTransform(ctx) || headerTransform(ctx) != nil
}

// Identity transforms supersede the legacy identity-confuse feature; header-only
// policies must not disable it or replace the fingerprint module's body transform.
func HasOutboundIdentityTransform(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	fn, _ := ctx.Value(outboundTransformKey{}).(OutboundTransform)
	return fn != nil
}

func WithOutboundHeaderTransform(ctx context.Context, fn OutboundHeaderTransform) context.Context {
	return context.WithValue(ctx, outboundHeaderTransformKey{}, fn)
}

func headerTransform(ctx context.Context) OutboundHeaderTransform {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(outboundHeaderTransformKey{}).(OutboundHeaderTransform)
	return fn
}

// OutboundHeaderIdentity detects changes to stable handshake fields without
// retaining raw routing hints or forcing reconnects for every turn/request ID.
func OutboundHeaderIdentity(ctx context.Context, h http.Header) string {
	if headerTransform(ctx) == nil {
		return ""
	}
	values := []string{}
	for _, name := range []string{"User-Agent", "Originator", "Version", "X-Codex-Beta-Features", "X-Openai-Internal-Codex-Responses-Lite", "X-Codex-Routing-Hint"} {
		values = append(values, h.Get(name))
	}
	raw, _ := json.Marshal(values)
	hash := sha256.Sum256(raw)
	return "headers:" + hex.EncodeToString(hash[:])
}
func TransformOutbound(ctx context.Context, h http.Header, body []byte) ([]byte, error) {
	if ctx == nil {
		return body, nil
	}
	if fn := headerTransform(ctx); fn != nil {
		fn(h)
	}
	fn, _ := ctx.Value(outboundTransformKey{}).(OutboundTransform)
	if fn == nil {
		return body, nil
	}
	return fn(h, body)
}
func TransformOutboundHTTP(req *http.Request, body []byte) ([]byte, error) {
	if !HasOutboundTransform(req.Context()) {
		return body, nil
	}
	next, err := TransformOutbound(req.Context(), req.Header, body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(next))
	req.ContentLength = int64(len(next))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(next)), nil }
	return next, nil
}
