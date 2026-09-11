package executor

import (
	"context"
	"net/http"
	"testing"
)

func TestHeaderHandshakeIdentityTracksPolicyAndStableFields(t *testing.T) {
	ctx := WithOutboundHeaderTransform(context.Background(), func(http.Header) {})
	h := http.Header{"User-Agent": {"client"}, "Originator": {"codex_exec"}}
	one := OutboundHeaderIdentity(ctx, h)
	if one == "" || OutboundHeaderIdentity(context.Background(), h) != "" {
		t.Fatal("policy enable/disable cannot invalidate an existing handshake")
	}
	h.Set("X-Client-Request-Id", "next-request")
	h.Set("X-Codex-Turn-Metadata", "next-turn")
	if OutboundHeaderIdentity(ctx, h) != one {
		t.Fatal("per-turn changes must not force a reconnect")
	}
	h.Set("User-Agent", "updated-client")
	if OutboundHeaderIdentity(ctx, h) == one {
		t.Fatal("a changed client identity must invalidate the handshake")
	}
}
