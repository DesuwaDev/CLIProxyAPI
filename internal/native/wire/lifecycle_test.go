package wire

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func localH2Conn(t *testing.T, handler http.HandlerFunc) *h2Conn {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tc, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := newH2Conn(tc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func lifecycleContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestH2ConcurrentStreamOpen(t *testing.T) {
	c := localH2Conn(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	ctx := lifecycleContext(t)
	const count = 32
	start := make(chan struct{})
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			<-start
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/test", nil)
			resp, err := c.roundTrip(req, nil, nil)
			if err == nil {
				var body []byte
				body, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err == nil && string(body) != "ok" {
					err = fmt.Errorf("unexpected response: %q", body)
				}
			}
			results <- err
		}()
	}
	close(start)
	for i := 0; i < count; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if c.isClosed() {
		t.Fatal("concurrent requests closed the shared connection")
	}
}

func TestH2EarlyResponseAndResetStopUpload(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint("reset=", reset), func(t *testing.T) {
			ctx := lifecycleContext(t)
			client, peer := net.Pipe()
			t.Cleanup(func() { _ = peer.Close() })
			ready := make(chan struct{})
			go func() {
				defer peer.Close()
				if _, err := io.CopyN(io.Discard, peer, int64(len(http2.ClientPreface))); err != nil {
					return
				}
				fr := http2.NewFramer(peer, peer)
				// Consume the preface before advertising a zero upload window.
				for i := 0; i < 2; i++ {
					if _, err := fr.ReadFrame(); err != nil {
						return
					}
				}
				_ = fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0})
				for {
					f, err := fr.ReadFrame()
					if err != nil {
						return
					}
					switch f := f.(type) {
					case *http2.SettingsFrame:
						if f.IsAck() {
							close(ready)
						}
					case *http2.HeadersFrame:
						if reset {
							_ = fr.WriteRSTStream(f.StreamID, http2.ErrCodeCancel)
							continue
						}
						var block bytes.Buffer
						enc := hpack.NewEncoder(&block)
						_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: "413"})
						_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, BlockFragment: block.Bytes(), EndHeaders: true})
						_ = fr.WriteData(f.StreamID, true, []byte("too large"))
					}
				}
			}()
			c, err := newH2Conn(client)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://localhost/test", nil)
			resp, err := c.roundTrip(req, bytes.Repeat([]byte("x"), 128<<10), nil)
			if ctx.Err() != nil {
				t.Fatal("upload waited for cancellation instead of handling the upstream result")
			}
			if reset {
				if err == nil || !strings.Contains(err.Error(), "reset by peer") {
					t.Fatalf("expected peer reset, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != 413 || string(body) != "too large" {
				t.Fatalf("early response was lost: status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
		})
	}
}

func TestH2ClosingUnreadBodiesRestoresConnectionWindow(t *testing.T) {
	c := localH2Conn(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
	})
	ctx := lifecycleContext(t)
	// Six unread responses exceed the entire initial connection receive window.
	for i := 0; i < 6; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/test", nil)
		resp, err := c.roundTrip(req, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		st := resp.Body.(*streamBody).st
		select {
		case <-st.done:
		case <-ctx.Done():
			t.Fatalf("receive window stalled at response %d", i+1)
		}
		_ = resp.Body.Close()
		_ = resp.Body.Close() // Returning credit must be idempotent.
	}
	if c.isClosed() {
		t.Fatal("discarding response bodies closed the connection")
	}
}

func TestTransportRetiresConnectionsAndReenables(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(fmt.Sprint("pause=", pause), func(t *testing.T) {
			ctx := lifecycleContext(t)
			finish := make(chan struct{})
			c := localH2Conn(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				select {
				case <-finish:
					_, _ = io.WriteString(w, "complete")
				case <-r.Context().Done():
				}
			})
			p := newTransportPool()
			defer p.closeAll()
			policy := Policy{Mode: "codex"}
			tr := p.get("account", "http://old-proxy:8080", policy)
			tr.conns = []*h2Conn{c}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/test", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			if pause {
				p.setEnabled(false)
				if p.get("account", "", policy) != nil {
					t.Fatal("disabled pool accepted a new request")
				}
			} else {
				p.get("account", "http://new-proxy:8080", policy)
			}
			if c.isClosed() || tr.pick() != nil {
				t.Fatal("retired connection must finish existing streams and reject new ones")
			}
			close(finish)
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || string(body) != "complete" {
				t.Fatalf("active response was interrupted: %q %v", body, err)
			}
			select {
			case <-c.done:
			case <-ctx.Done():
				t.Fatal("retired connection did not close after its last response")
			}
			p.setEnabled(true)
			select {
			case <-p.stop:
				t.Fatal("module toggle permanently stopped the idle reaper")
			default:
			}
			if p.get("account", "http://new-proxy:8080", policy) == nil {
				t.Fatal("pool did not resume after enabling")
			}
		})
	}
}
