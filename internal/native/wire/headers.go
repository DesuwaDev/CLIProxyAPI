package wire

import (
	"net/http"
	"sort"
	"strings"
)

// Header order as emitted by codex-cli 0.154.0 for POST /backend-api/codex/responses:
// request-specific x-codex headers first (builder order), then content
// negotiation, then credentials, then reqwest default headers, then the cookie
// store and hyper's content-length.
var httpHeaderOrder = []string{
	"x-codex-beta-features",
	"x-codex-window-id",
	"x-codex-turn-metadata",
	"x-openai-internal-codex-responses-lite",
	"x-openai-internal-codex-residency",
	"x-codex-routing-hint",
	"x-client-request-id",
	"session-id",
	"thread-id",
	"accept",
	"content-encoding",
	"content-type",
	"authorization",
	"chatgpt-account-id",
	"originator",
	"user-agent",
	"version",
	"cookie",
	"content-length",
}

// WebSocket upgrade order (HTTP/1.1) from the same capture. gorilla writes the
// first five itself; the ordered connection wrapper rewrites the block.
var websocketHeaderOrder = []string{
	"Host",
	"Connection",
	"Upgrade",
	"Sec-WebSocket-Version",
	"Sec-WebSocket-Key",
	"chatgpt-account-id",
	"authorization",
	"user-agent",
	"originator",
	"openai-beta",
	"version",
	"x-codex-beta-features",
	"x-client-request-id",
	"session-id",
	"thread-id",
	"x-codex-window-id",
	"x-codex-turn-metadata",
	"x-openai-internal-codex-residency",
	"x-codex-routing-hint",
	"sec-websocket-extensions",
}

// hop-by-hop or HTTP/1-only fields that must never appear in an HTTP/2 block.
var h2Forbidden = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"transfer-encoding": true,
	"upgrade":           true,
	"te":                true,
	"host":              true,
}

type headerField struct{ name, value string }

// orderedHTTPHeaders flattens h into lowercase fields in profile order. Fields
// not in the profile keep a deterministic position: x-* fields after thread-id
// (the builder group), everything else before content-length.
func orderedHTTPHeaders(h http.Header, contentLength int) []headerField {
	lower := map[string][]string{}
	for k, vv := range h {
		name := strings.ToLower(strings.TrimSpace(k))
		if name == "" || h2Forbidden[name] || name == "content-length" {
			continue
		}
		lower[name] = append(lower[name], vv...)
	}
	rank := map[string]int{}
	for i, n := range httpHeaderOrder {
		rank[n] = i
	}
	threadRank := rank["thread-id"]
	cookieRank := rank["cookie"]
	var extra []string
	for name := range lower {
		if _, ok := rank[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	out := make([]headerField, 0, len(lower)+1)
	emit := func(name string) {
		for _, v := range lower[name] {
			out = append(out, headerField{name, v})
		}
		delete(lower, name)
	}
	for _, name := range httpHeaderOrder {
		if name == "content-length" {
			continue
		}
		if _, ok := lower[name]; ok {
			emit(name)
		}
		if rank[name] == threadRank {
			for _, e := range extra {
				if strings.HasPrefix(e, "x-") {
					emit(e)
				}
			}
		}
		if rank[name] == cookieRank {
			for _, e := range extra {
				if !strings.HasPrefix(e, "x-") {
					emit(e)
				}
			}
		}
	}
	// Anything still present had a profile rank but was skipped above (cannot
	// happen) or is an extra that matched no bucket; flush deterministically.
	var rest []string
	for name := range lower {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		emit(name)
	}
	if contentLength >= 0 {
		out = append(out, headerField{"content-length", itoa(contentLength)})
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// websocketRequestHeaderOrder is the httpwire order callback for the upgrade.
func websocketRequestHeaderOrder(_, _ string) []string { return websocketHeaderOrder }

// normalizeWebsocketHeaders aligns CPA's handshake header names with the CLI:
// the CLI sends session-id (hyphen) and no conversation_id mirror.
func normalizeWebsocketHeaders(h http.Header) {
	if h == nil {
		return
	}
	var sessionID string
	for k, vv := range h {
		switch strings.ToLower(k) {
		case "session_id", "session-id":
			if sessionID == "" && len(vv) > 0 {
				sessionID = strings.TrimSpace(vv[0])
			}
			delete(h, k)
		case "conversation_id":
			delete(h, k)
		}
	}
	if sessionID != "" {
		// Canonical key so existing Header.Get callers still see it; the wire
		// layers lowercase (HTTP/2) or reorder-and-recase (WebSocket) on send.
		h.Set("Session-Id", sessionID)
	}
}
