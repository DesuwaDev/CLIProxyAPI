package wire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// transportPool owns one connection set per credential. A credential maps to
// one upstream account, so its connections are never shared with another
// credential, mirroring one CLI process per account.
type transportPool struct {
	mu      sync.Mutex
	entries map[string]*credentialTransport
	stop    chan struct{}
	once    sync.Once
	paused  bool
	closed  bool
}

func newTransportPool() *transportPool {
	p := &transportPool{entries: map[string]*credentialTransport{}, stop: make(chan struct{})}
	go p.reap()
	return p
}

func (p *transportPool) get(target, proxyURL string, policy Policy) *credentialTransport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused || p.closed {
		return nil
	}
	if t := p.entries[target]; t != nil {
		t.mu.Lock()
		if t.proxyURL != proxyURL || t.policy != policy {
			t.drainConnsLocked()
		}
		t.proxyURL = proxyURL
		t.policy = policy
		t.mu.Unlock()
		return t
	}
	t := &credentialTransport{target: target, proxyURL: proxyURL, policy: policy, jar: newCookieJar()}
	p.entries[target] = t
	return t
}

func (p *transportPool) jar(target string) *cookieJar {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.entries[target]; t != nil {
		return t.jar
	}
	return nil
}

func (p *transportPool) drop(target string) {
	p.mu.Lock()
	t := p.entries[target]
	p.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		t.drainConnsLocked()
		t.mu.Unlock()
	}
}

// setEnabled is reversible; only closeAll stops the idle reaper permanently.
func (p *transportPool) setEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.paused = !enabled
	for _, t := range p.entries {
		t.mu.Lock()
		t.disabled = !enabled
		if !enabled {
			t.drainConnsLocked()
		}
		t.mu.Unlock()
	}
}

func (p *transportPool) stats(target string) map[string]any {
	p.mu.Lock()
	t := p.entries[target]
	p.mu.Unlock()
	if t == nil {
		return map[string]any{"open": 0, "cookies": 0}
	}
	t.mu.Lock()
	open := 0
	for _, c := range t.conns {
		if !c.isClosed() {
			open++
		}
	}
	requests, reused, compressed, dials := t.requests, t.reused, t.compressed, t.dials
	lastErr := t.lastError
	t.mu.Unlock()
	return map[string]any{"open": open, "cookies": t.jar.count(), "requests": requests, "reused": reused, "compressed": compressed, "dials": dials, "last_error": lastErr}
}

func (p *transportPool) closeAll() {
	p.once.Do(func() { close(p.stop) })
	p.mu.Lock()
	p.closed = true
	entries := make([]*credentialTransport, 0, len(p.entries))
	for _, t := range p.entries {
		t.mu.Lock()
		t.disabled = true
		t.generation++
		t.mu.Unlock()
		entries = append(entries, t)
	}
	p.mu.Unlock()
	for _, t := range entries {
		t.closeConns()
	}
}

func (p *transportPool) reap() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			entries := make([]*credentialTransport, 0, len(p.entries))
			for _, t := range p.entries {
				entries = append(entries, t)
			}
			p.mu.Unlock()
			for _, t := range entries {
				t.reapIdle()
			}
		}
	}
}

// credentialTransport is the http.RoundTripper for one credential.
type credentialTransport struct {
	target     string
	mu         sync.Mutex
	proxyURL   string
	policy     Policy
	conns      []*h2Conn
	jar        *cookieJar
	generation uint64
	disabled   bool

	requests, reused, compressed, dials int64
	lastError                           string
}

// Called with t.mu held. A generation also retires dials started on the old route.
func (t *credentialTransport) drainConnsLocked() {
	t.generation++
	for _, c := range t.conns {
		c.drain()
	}
}

func (t *credentialTransport) closeConns() {
	t.mu.Lock()
	conns := t.conns
	t.conns = nil
	t.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (t *credentialTransport) reapIdle() {
	t.mu.Lock()
	var keep []*h2Conn
	var closeList []*h2Conn
	for _, c := range t.conns {
		if c.isClosed() {
			continue
		}
		if since, idle := c.idleSince(); idle && time.Since(since) > idleTimeout {
			closeList = append(closeList, c)
			continue
		}
		keep = append(keep, c)
	}
	t.conns = keep
	t.mu.Unlock()
	for _, c := range closeList {
		_ = c.Close()
	}
}

func (t *credentialTransport) pick() *h2Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.disabled {
		return nil
	}
	var keep []*h2Conn
	var chosen *h2Conn
	for _, c := range t.conns {
		if c.isClosed() {
			continue
		}
		keep = append(keep, c)
		if chosen == nil && c.canTakeNewRequest() {
			chosen = c
		}
	}
	t.conns = keep
	return chosen
}

func (t *credentialTransport) dial(ctx context.Context, target *url.URL) (*h2Conn, error) {
	t.mu.Lock()
	if t.disabled {
		t.mu.Unlock()
		return nil, errConnClosed
	}
	proxyURL := t.proxyURL
	generation := t.generation
	t.dials++
	t.mu.Unlock()
	host := target.Hostname()
	port := target.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)
	dialer, err := resolveDialer(proxyURL, target)
	if err != nil {
		return nil, err
	}
	raw, err := dialTCP(ctx, dialer, addr)
	if err != nil {
		return nil, err
	}
	uconn, err := handshake(ctx, raw, host, []string{"h2", "http/1.1"})
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if proto := uconn.ConnectionState().NegotiatedProtocol; proto != "h2" {
		_ = uconn.Close()
		return nil, fmt.Errorf("wire: upstream negotiated %q instead of h2", proto)
	}
	conn, err := newH2Conn(uconn)
	if err != nil {
		_ = uconn.Close()
		return nil, err
	}
	t.mu.Lock()
	if t.disabled || t.generation != generation {
		t.mu.Unlock()
		_ = conn.Close()
		return nil, errConnClosed
	}
	t.conns = append(t.conns, conn)
	t.mu.Unlock()
	return conn, nil
}

// RoundTrip implements http.RoundTripper.
func (t *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" {
		return nil, errors.New("wire: only https upstreams are supported")
	}
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		data, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("wire: read request body: %w", err)
		}
		body = data
	}
	t.mu.Lock()
	policy := t.policy
	t.requests++
	t.mu.Unlock()

	header := req.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	// Derive the hint from the plain JSON body before any compression.
	if policy.RoutingHint && headerGet(header, "X-Codex-Routing-Hint") == "" && strings.HasSuffix(req.URL.Path, "/responses") && header.Get("Content-Encoding") == "" {
		if hint := routingHintFromBody(body); hint != "" {
			header.Set("X-Codex-Routing-Hint", hint)
		}
	}
	if policy.Compress && shouldCompress(req, body) {
		compressed, err := compressBody(body)
		if err != nil {
			return nil, err
		}
		body = compressed
		header.Set("Content-Encoding", "zstd")
		t.mu.Lock()
		t.compressed++
		t.mu.Unlock()
	}
	if policy.Cookies {
		if cookie := t.jar.header(req.URL.Hostname()); cookie != "" {
			header.Set("Cookie", cookie)
		}
	}
	contentLength := -1
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		contentLength = len(body)
	}
	fields := orderedHTTPHeaders(header, contentLength)

	ctx := req.Context()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn := t.pick()
		reused := conn != nil
		if conn == nil {
			dialed, err := t.dial(ctx, req.URL)
			if err != nil {
				if errors.Is(err, errConnClosed) && ctx.Err() == nil {
					lastErr = err
					continue
				}
				t.recordError(err)
				return nil, err
			}
			conn = dialed
		}
		resp, err := conn.roundTrip(req, bytes.Clone(body), fields)
		if err == nil {
			if reused {
				t.mu.Lock()
				t.reused++
				t.mu.Unlock()
			}
			if policy.Cookies {
				t.jar.store(req.URL, resp)
			}
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
		// Retry once on a connection that closed underneath us before the
		// request was accepted; any other failure is surfaced unchanged.
		if !errors.Is(err, errConnClosed) && !errors.Is(err, errGoAway) && !errors.Is(err, errStreamsMaxed) {
			t.recordError(err)
			return nil, err
		}
		log.Debugf("wire: retrying codex request after connection error: %v", err)
	}
	t.recordError(lastErr)
	return nil, lastErr
}

func (t *credentialTransport) recordError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.lastError = err.Error()
	t.mu.Unlock()
}
