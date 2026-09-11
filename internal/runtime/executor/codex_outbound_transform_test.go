package executor

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexidentity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCodexOutboundTransformReachesHTTPAndWebsocket(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		for _, stream := range []bool{false, true} {
			t.Run(transport+map[bool]string{false: "-sync", true: "-stream"}[stream], func(t *testing.T) {
				type captured struct {
					h http.Header
					b []byte
				}
				received := make(chan captured, 1)
				completed := []byte(`{"type":"response.completed","response":{"id":"resp-native","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == "ws" {
						u := websocket.Upgrader{}
						conn, err := u.Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						_, b, err := conn.ReadMessage()
						if err != nil {
							t.Error(err)
							return
						}
						received <- captured{r.Header.Clone(), b}
						_ = conn.WriteMessage(websocket.TextMessage, completed)
					} else {
						b, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						received <- captured{r.Header.Clone(), b}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(append(append([]byte("data: "), completed...), []byte("\n\n")...))
					}
				}))
				defer server.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				var executor auth.ProviderExecutor = NewCodexExecutor(cfg)
				if transport == "ws" {
					executor = NewCodexWebsocketsExecutor(cfg)
				}
				credential := &auth.Auth{ID: "native-test", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-test", "base_url": server.URL}}
				ctx := ex.WithOutboundTransform(context.Background(), func(h http.Header, b []byte) ([]byte, error) {
					h.Set("X-Codex-Installation-Id", "native-device")
					h.Set("Session-Id", "native-session")
					return sjson.SetBytes(b, "client_metadata.session_id", "native-session")
				})
				ctx = ex.WithOutboundHeaderTransform(ctx, func(h http.Header) {
					h.Set("User-Agent", "codex_exec/0.153.4 (Windows) (codex_exec; 0.153.4)")
					h.Set("Version", "0.153.4")
					codexidentity.RewriteHeaders(h, codexidentity.DefaultVersion)
					h.Set("Originator", "codex_exec")
					h.Del("Connection")
				})
				req := ex.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[],"instructions":"test"}`)}
				opts := ex.Options{SourceFormat: translator.FromString("codex")}
				if stream {
					result, err := executor.ExecuteStream(ctx, credential, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, err := executor.Execute(ctx, credential, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				got := <-received
				if got.h.Get("User-Agent") != "codex_exec/"+codexidentity.DefaultVersion+" (Windows) (codex_exec; "+codexidentity.DefaultVersion+")" || got.h.Get("Version") != codexidentity.DefaultVersion || got.h.Get("Originator") != "codex_exec" {
					t.Fatal("executor overwrote the final header policy")
				}
				if got.h.Get("X-Codex-Installation-Id") != "native-device" || got.h.Get("Session-Id") != gjson.GetBytes(got.b, "client_metadata.session_id").String() {
					t.Fatal("outbound header/body transform missing")
				}
			})
		}
	}
}
