// codex_wire_selftest dials chatgpt.com (or -host) once with the wire profile,
// requests /backend-api/codex/models without credentials, and prints the TLS
// version, negotiated ALPN, HTTP status and the first response headers. It
// proves the ClientHello and HTTP/2 preface are accepted by the real edge
// without sending any account material. Optional -capture points it at the
// local codex_wire_capture listener to diff the emitted ClientHello.
package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/wire"
)

func main() {
	host := flag.String("host", "chatgpt.com", "upstream host")
	path := flag.String("path", "/backend-api/codex/models?client_version=0.154.0", "request path")
	proxyURL := flag.String("proxy", "", "proxy url (socks5://, http://) or empty for environment")
	caFile := flag.String("ca", "", "extra trusted CA PEM (for the local capture listener)")
	post := flag.Bool("post", false, "send a Responses-shaped POST with a dummy JSON body instead of GET")
	flag.Parse()

	if *caFile != "" {
		pemBytes, err := os.ReadFile(*caFile)
		if err != nil {
			fail(err)
		}
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			fail(fmt.Errorf("no PEM block in %s", *caFile))
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			fail(err)
		}
		wire.SetExtraRootCA(cert)
	}
	rt := wire.NewStandaloneTransport(*proxyURL, wire.Policy{Mode: "codex", Compress: true, Cookies: true, RoutingHint: true})
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var req *http.Request
	var err error
	if *post {
		body := strings.Repeat(`{"model":"gpt-6-astra","instructions":"diagnostic","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with exactly OK"}]}],"stream":true}`, 1)
		body = body[:len(body)-1] + `,"padding":"` + strings.Repeat("x", 27000) + `"}`
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://"+*host+*path, strings.NewReader(body))
		if err != nil {
			fail(err)
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Codex-Beta-Features", "remote_compaction_v2")
		req.Header.Set("X-Codex-Window-Id", "00000000-0000-4000-8000-000000000000:0")
		req.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"00000000-0000-4000-8000-000000000001"}`)
		req.Header.Set("X-Openai-Internal-Codex-Responses-Lite", "true")
		req.Header.Set("X-Client-Request-Id", "00000000-0000-4000-8000-000000000002")
		req.Header.Set("Session-Id", "00000000-0000-4000-8000-000000000003")
		req.Header.Set("Thread-Id", "00000000-0000-4000-8000-000000000004")
		req.Header.Set("Authorization", "Bearer diagnostic-placeholder")
		req.Header.Set("Chatgpt-Account-Id", "00000000-0000-4000-8000-000000000005")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://"+*host+*path, nil)
		if err != nil {
			fail(err)
		}
		req.Header.Set("Accept", "*/*")
	}
	req.Header.Set("Originator", "codex_exec")
	req.Header.Set("User-Agent", "codex_exec/0.154.0 (Windows 10.0.26200; x86_64) unknown (codex_exec; 0.154.0)")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	fmt.Printf("status: %d proto: %s\n", resp.StatusCode, resp.Proto)
	for _, k := range []string{"Server", "Cf-Ray", "Content-Type", "Set-Cookie", "Openai-Processing-Ms", "X-Request-Id"} {
		if v := resp.Header.Get(k); v != "" {
			if k == "Set-Cookie" {
				v = "<present>"
			}
			fmt.Printf("%s: %s\n", k, v)
		}
	}
	fmt.Printf("body[:%d]: %q\n", len(body), body)
	fmt.Printf("stats: %v\n", rt.Stats())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
