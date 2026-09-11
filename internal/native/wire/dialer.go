package wire

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// resolveDialer returns the TCP dialer for one credential: explicit proxy URL
// (socks5/http/https via proxyutil), "direct", or the process environment
// proxy for the target when nothing is configured (matching CPA's default).
func resolveDialer(proxyURL string, target *url.URL) (proxy.ContextDialer, error) {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" && target != nil {
		if envURL, err := http.ProxyFromEnvironment(&http.Request{URL: target}); err == nil && envURL != nil {
			trimmed = envURL.String()
		}
	}
	if trimmed == "" {
		return proxy.Direct, nil
	}
	dialer, mode, err := proxyutil.BuildDialer(trimmed)
	if err != nil {
		return nil, fmt.Errorf("wire: proxy dialer: %w", err)
	}
	if mode == proxyutil.ModeInherit || dialer == nil {
		return proxy.Direct, nil
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("wire: proxy dialer does not support context cancellation")
	}
	return contextDialer, nil
}

func dialTCP(ctx context.Context, dialer proxy.ContextDialer, addr string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("wire: dial upstream: %w", err)
	}
	return conn, nil
}

// websocketTLSDialer returns a gorilla NetDialTLSContext implementation that
// performs the profile handshake (no ALPN) and reorders the HTTP/1.1 upgrade
// header block like the CLI. Proxying is handled here, so the caller must clear
// the websocket.Dialer Proxy field.
func websocketTLSDialer(proxyURL string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, errSplit := net.SplitHostPort(addr)
		if errSplit != nil {
			host = addr
		}
		dialer, err := resolveDialer(proxyURL, &url.URL{Scheme: "https", Host: addr})
		if err != nil {
			return nil, err
		}
		raw, err := dialTCP(ctx, dialer, addr)
		if err != nil {
			return nil, err
		}
		uconn, err := handshake(ctx, raw, host, nil)
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return httpwire.NewOrderedRequestConn(uconn, websocketRequestHeaderOrder), nil
	}
}
