package wire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(t.TempDir() + "/wire.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPolicyPersistsAndNormalizes(t *testing.T) {
	db := openTestDB(t)
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Get("acct"); got.Mode != "off" {
		t.Fatalf("default mode = %q, want off", got.Mode)
	}
	if _, err = db.Exec("INSERT INTO native_wire_policies(target,mode,compress,cookies,routing_hint) VALUES('acct','codex',1,0,1)"); err != nil {
		t.Fatal(err)
	}
	m2, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	got := m2.Get("acct")
	if got.Mode != "codex" || !got.Compress || got.Cookies || !got.RoutingHint {
		t.Fatalf("persisted policy = %+v", got)
	}
	if m2.Transport("other", "") != nil {
		t.Fatal("off credential must not get a transport")
	}
	if m2.Transport("acct", "") == nil {
		t.Fatal("codex credential must get a transport")
	}
	m.Close()
	m2.Close()
}

func TestRoutingHint(t *testing.T) {
	cases := map[string]string{
		`{"model":"gpt-6-astra"}`:                           "model=gpt-6-astra",
		`{"model":"gpt-6-astra","service_tier":"priority"}`: "model=gpt-6-astra;tier=priority",
		`{"model":"gpt-6-astra","service_tier":"default"}`:  "model=gpt-6-astra",
		`{"model":"bad=model"}`:                             "",
		`{}`:                                                "",
	}
	for body, want := range cases {
		if got := routingHintFromBody([]byte(body)); got != want {
			t.Fatalf("hint(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestZstdFrameMatchesLibzstdShape(t *testing.T) {
	body := bytes.Repeat([]byte(`{"input":[{"role":"user","content":"hello world"}],"model":"gpt-6-astra"}`), 400)
	out, err := compressBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatal("missing zstd magic")
	}
	if out[4] != 0x00 {
		t.Fatalf("frame header descriptor = %#x, want 0x00 (no checksum, no single segment, no FCS)", out[4])
	}
	if out[5] != observedWindowDescriptor {
		t.Fatalf("window descriptor = %#x, want %#x", out[5], observedWindowDescriptor)
	}
	bh := uint32(out[6]) | uint32(out[7])<<8 | uint32(out[8])<<16
	if (bh>>1)&3 != 2 {
		t.Fatalf("first block type = %d, want 2 (compressed)", (bh>>1)&3)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	back, err := dec.DecodeAll(out, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, body) {
		t.Fatal("round trip mismatch")
	}
	small, err := compressBody([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if back, err = dec.DecodeAll(small, nil); err != nil || string(back) != `{"a":1}` {
		t.Fatalf("small round trip: %v %q", err, back)
	}
}

func TestOrderedHTTPHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "codex_exec/0.154.0")
	h.Set("Authorization", "Bearer x")
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("Originator", "codex_exec")
	h.Set("Chatgpt-Account-Id", "acc")
	h.Set("X-Codex-Beta-Features", "remote_compaction_v2")
	h.Set("Session-Id", "s")
	h.Set("Thread-Id", "t")
	h.Set("X-Client-Request-Id", "r")
	h.Set("X-Codex-Window-Id", "w")
	h.Set("X-Codex-Turn-Metadata", "{}")
	h.Set("X-Openai-Internal-Codex-Responses-Lite", "true")
	h.Set("X-Codex-Routing-Hint", "model=gpt-6-astra")
	h.Set("Content-Encoding", "zstd")
	h.Set("Connection", "Keep-Alive")
	h.Set("Version", "0.154.0")
	fields := orderedHTTPHeaders(h, 123)
	var names []string
	for _, f := range fields {
		names = append(names, f.name)
	}
	want := []string{"x-codex-beta-features", "x-codex-window-id", "x-codex-turn-metadata", "x-openai-internal-codex-responses-lite", "x-codex-routing-hint", "x-client-request-id", "session-id", "thread-id", "accept", "content-encoding", "content-type", "authorization", "chatgpt-account-id", "originator", "user-agent", "version", "content-length"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v\nwant  %v", names, want)
	}
	if fields[len(fields)-1].value != "123" {
		t.Fatalf("content-length = %q", fields[len(fields)-1].value)
	}
}

func TestNormalizeWebsocketHeaders(t *testing.T) {
	h := http.Header{"Session_id": {"abc"}, "Conversation_id": {"abc"}, "Originator": {"codex_exec"}}
	normalizeWebsocketHeaders(h)
	if h.Get("session-id") != "abc" || len(h["Session_id"]) != 0 || len(h["Conversation_id"]) != 0 {
		t.Fatalf("normalized = %v", h)
	}
}

func TestClientHelloSpecShape(t *testing.T) {
	spec := codexClientHelloSpec([]string{"h2", "http/1.1"})
	if len(spec.CipherSuites) != 10 || spec.CipherSuites[len(spec.CipherSuites)-1] != 0x00ff {
		t.Fatalf("cipher suites = %x", spec.CipherSuites)
	}
	if len(spec.Extensions) != 11 {
		t.Fatalf("extension count = %d, want 11", len(spec.Extensions))
	}
	if len(codexClientHelloSpec(nil).Extensions) != 10 {
		t.Fatal("websocket spec must omit ALPN")
	}
}

// TestH2RoundTripAgainstGoServer drives the custom HTTP/2 client against Go's
// server: preface acceptance, header decoding, zstd body delivery, streamed
// response bodies, flow control on a large response and connection reuse.
func TestH2RoundTripAgainstGoServer(t *testing.T) {
	var gotEncoding, gotHint, gotCookie string
	var gotBody []byte
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			gotEncoding = r.Header.Get("Content-Encoding")
			gotHint = r.Header.Get("X-Codex-Routing-Hint")
			gotCookie = r.Header.Get("Cookie")
			gotBody, _ = io.ReadAll(r.Body)
			http.SetCookie(w, &http.Cookie{Name: "__cf_bm", Value: "abc", Path: "/", Secure: true, MaxAge: 1800})
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			for i := 0; i < 3; i++ {
				_, _ = io.WriteString(w, "data: {\"i\":1}\n\n")
				fl.Flush()
			}
		case "/big":
			w.WriteHeader(200)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 3<<20))
		default:
			w.WriteHeader(404)
		}
	}))
	srv.TLS = &tls.Config{NextProtos: []string{"h2"}, Certificates: []tls.Certificate{selfSignedCert(t)}}
	srv.StartTLS()
	defer srv.Close()

	// The test server is Go TLS; the h2Conn is placed on a plain Go client TLS
	// connection to isolate the HTTP/2 layer from the rustls handshake profile.
	dialH2 := func() *h2Conn {
		raw, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		tc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
		if err = tc.Handshake(); err != nil {
			t.Fatal(err)
		}
		c, err := newH2Conn(tc)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tr := &credentialTransport{target: "t", policy: Policy{Mode: "codex", Compress: true, Cookies: true, RoutingHint: true}, jar: newCookieJar()}
	tr.conns = []*h2Conn{dialH2()}

	body := []byte(`{"model":"gpt-6-astra","input":"hi","stream":true}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/backend-api/codex/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || strings.Count(string(data), "data:") != 3 {
		t.Fatalf("resp %d err=%v body=%q", resp.StatusCode, err, data)
	}
	if gotEncoding != "zstd" || gotHint != "model=gpt-6-astra" {
		t.Fatalf("server saw encoding=%q hint=%q", gotEncoding, gotHint)
	}
	dec, _ := zstd.NewReader(nil)
	plain, err := dec.DecodeAll(gotBody, nil)
	if err != nil || !bytes.Equal(plain, body) {
		t.Fatalf("server body decode: %v", err)
	}
	if gotCookie != "" {
		t.Fatalf("first request must not carry a cookie, got %q", gotCookie)
	}
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/backend-api/codex/responses", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := tr.RoundTrip(req2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()
	if gotCookie != "__cf_bm=abc" {
		t.Fatalf("cookie replay = %q", gotCookie)
	}
	if tr.reused != 2 || tr.dials != 0 || tr.compressed != 2 {
		t.Fatalf("stats reused=%d dials=%d compressed=%d", tr.reused, tr.dials, tr.compressed)
	}
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/big", nil)
	resp3, err := tr.RoundTrip(req3)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp3.Body)
	_ = resp3.Body.Close()
	if err != nil || n != 3<<20 {
		t.Fatalf("big body n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req4, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/big", nil)
	resp4, err := tr.RoundTrip(req4)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, _ = io.Copy(io.Discard, resp4.Body)
	_ = resp4.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tr.conns[0].canTakeNewRequest() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	req5, _ := http.NewRequest(http.MethodGet, srv.URL+"/missing", nil)
	resp5, err := tr.RoundTrip(req5)
	if err != nil || resp5.StatusCode != 404 {
		t.Fatalf("after cancel: %v %v", err, resp5)
	}
	_ = resp5.Body.Close()
	tr.closeConns()
}

// TestH2PrefaceBytes verifies the raw SETTINGS/WINDOW_UPDATE sequence and the
// hpack pseudo-header order using an in-memory peer.
func TestH2PrefaceBytes(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	var seen []string
	var headerBlock []byte
	go func() {
		defer close(done)
		buf := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(server, buf); err != nil || string(buf) != http2.ClientPreface {
			t.Errorf("preface = %q err=%v", buf, err)
			return
		}
		fr := http2.NewFramer(server, server)
		_ = fr.WriteSettings()
		for len(seen) < 4 {
			f, err := fr.ReadFrame()
			if err != nil {
				t.Errorf("read: %v", err)
				return
			}
			// Framer reuses frame objects; extract everything before the next read.
			switch fr := f.(type) {
			case *http2.SettingsFrame:
				var ids []string
				_ = fr.ForeachSetting(func(s http2.Setting) error {
					ids = append(ids, s.ID.String()+"="+itoa(int(s.Val)))
					return nil
				})
				if fr.IsAck() {
					seen = append(seen, "SETTINGS_ACK")
				} else {
					seen = append(seen, "SETTINGS "+strings.Join(ids, " "))
				}
			case *http2.WindowUpdateFrame:
				seen = append(seen, "WINDOW_UPDATE "+itoa(int(fr.StreamID))+" "+itoa(int(fr.Increment)))
			case *http2.HeadersFrame:
				headerBlock = append(headerBlock, fr.HeaderBlockFragment()...)
				seen = append(seen, "HEADERS")
			default:
				seen = append(seen, f.Header().Type.String())
			}
		}
	}()
	c, err := newH2Conn(client)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com:443/backend-api/codex/models", nil)
	go func() { _, _ = c.roundTrip(req, nil, orderedHTTPHeaders(http.Header{"Accept": {"*/*"}}, -1)) }()
	<-done
	// Unblock the client read loop and the pending round trip before asserting.
	_ = server.Close()
	_ = c.Close()
	if len(seen) < 4 {
		t.Fatalf("frames = %v", seen)
	}
	if seen[0] != "SETTINGS ENABLE_PUSH=0 INITIAL_WINDOW_SIZE=2097152 MAX_FRAME_SIZE=16384 MAX_HEADER_LIST_SIZE=16384" {
		t.Fatalf("frame0 = %q", seen[0])
	}
	if seen[1] != "WINDOW_UPDATE 0 "+itoa(connectionWindowUpdate) {
		t.Fatalf("frame1 = %q", seen[1])
	}
	if !strings.Contains(strings.Join(seen, ","), "HEADERS") {
		t.Fatalf("no HEADERS in %v", seen)
	}
	var names []string
	dec := hpack.NewDecoder(4096, func(f hpack.HeaderField) { names = append(names, f.Name+"="+f.Value) })
	if _, err = dec.Write(headerBlock); err != nil {
		t.Fatal(err)
	}
	if strings.Join(names[:4], ",") != ":method=GET,:scheme=https,:authority=chatgpt.com,:path=/backend-api/codex/models" {
		t.Fatalf("pseudo headers = %v", names)
	}
}

func TestCookieJarExpiry(t *testing.T) {
	jar := newCookieJar()
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Add("Set-Cookie", "a=1; Path=/; Max-Age=60")
	resp.Header.Add("Set-Cookie", "b=2; Path=/; Max-Age=0")
	u, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/x", nil)
	jar.store(u.URL, resp)
	if got := jar.header("chatgpt.com"); got != "a=1" {
		t.Fatalf("cookie header = %q", got)
	}
	if jar.header("other.com") != "" {
		t.Fatal("cookies must not leak across hosts")
	}
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestH2HpackEncoderMatchesRustPolicy checks the representation choices that
// distinguish the h2 crate from Go's encoder and that Go's decoder reads them back.
func TestH2HpackEncoderMatchesRustPolicy(t *testing.T) {
	enc := newH2HpackEncoder()
	fields := []hpack.HeaderField{
		{Name: ":method", Value: "POST"},
		{Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: "chatgpt.com"},
		{Name: ":path", Value: "/backend-api/codex/responses"},
		{Name: "accept", Value: "text/event-stream"},
		{Name: "content-length", Value: "28137"},
		{Name: "x-codex-beta-features", Value: "remote_compaction_v2"},
	}
	block := enc.encode(nil, fields)
	// :method POST -> indexed static 3 (0x83); :scheme https -> 0x87
	if block[0] != 0x83 || block[1] != 0x87 {
		t.Fatalf("static indexed prefix = %x", block[:2])
	}
	// :authority -> literal with incremental indexing, static name 1 (0x41), Huffman value (0x80 | len)
	if block[2] != 0x41 || block[3]&0x80 == 0 {
		t.Fatalf(":authority representation = %x", block[2:4])
	}
	var got []string
	dec := hpack.NewDecoder(4096, func(f hpack.HeaderField) { got = append(got, f.Name+"="+f.Value) })
	if _, err := dec.Write(block); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(fields) || got[3] != ":path=/backend-api/codex/responses" || got[5] != "content-length=28137" {
		t.Fatalf("decoded = %v", got)
	}
	// :path and content-length must not have been inserted; :method/:scheme are
	// static full matches, so exactly :authority, accept and x-codex-beta-features remain.
	if len(enc.dyn) != 3 {
		t.Fatalf("dynamic table size = %d, want 3", len(enc.dyn))
	}
	// Second block: repeated headers are fully indexed from the dynamic table.
	block2 := enc.encode(nil, fields[:5])
	if block2[2]&0x80 == 0 || block2[4]&0x80 == 0 {
		t.Fatalf("second block should index :authority and accept: %x", block2)
	}
	got = nil
	if _, err := dec.Write(block2); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[2] != ":authority=chatgpt.com" {
		t.Fatalf("decoded2 = %v", got)
	}
}
