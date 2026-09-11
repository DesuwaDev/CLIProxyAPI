package executor

import (
	"context"
	"net"
	"net/http"
)

type wireTransportKey struct{}

// WireTransport carries an optional host-selected upstream transport for one
// provider attempt. Executors that support it use RoundTripper for HTTPS calls
// and DialTLS for WebSocket upgrades; a nil field keeps the executor default.
type WireTransport struct {
	RoundTripper http.RoundTripper
	DialTLS      func(ctx context.Context, network, addr string) (net.Conn, error)
}

// WithWireTransport attaches a transport selection to ctx.
func WithWireTransport(ctx context.Context, t *WireTransport) context.Context {
	if ctx == nil || t == nil {
		return ctx
	}
	return context.WithValue(ctx, wireTransportKey{}, t)
}

// WireTransportFrom returns the attached transport selection, if any.
func WireTransportFrom(ctx context.Context) *WireTransport {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(wireTransportKey{}).(*WireTransport)
	return t
}
