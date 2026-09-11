package wire

import (
	"crypto/x509"
	"net/http"
	"sync"
)

// Standalone helpers let diagnostics and tests use the transport without the
// SQLite-backed module.

var (
	extraRootsMu sync.RWMutex
	extraRoots   *x509.CertPool
)

// SetExtraRootCA adds a trusted root for the profile TLS verifier. Intended
// for the local capture listener only; production traffic uses system roots.
func SetExtraRootCA(cert *x509.Certificate) {
	extraRootsMu.Lock()
	defer extraRootsMu.Unlock()
	if extraRoots == nil {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		extraRoots = pool
	}
	extraRoots.AddCert(cert)
}

func rootCAs() *x509.CertPool {
	extraRootsMu.RLock()
	defer extraRootsMu.RUnlock()
	return extraRoots
}

// StandaloneTransport is a single-credential transport without persistence.
type StandaloneTransport struct{ t *credentialTransport }

// NewStandaloneTransport builds a transport for one proxy/policy pair.
func NewStandaloneTransport(proxyURL string, policy Policy) *StandaloneTransport {
	return &StandaloneTransport{t: &credentialTransport{target: "standalone", proxyURL: proxyURL, policy: policy.normalized(), jar: newCookieJar()}}
}

// RoundTrip implements http.RoundTripper.
func (s *StandaloneTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.t.RoundTrip(req)
}

// Stats reports connection and request counters.
func (s *StandaloneTransport) Stats() map[string]any {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	return map[string]any{"open": len(s.t.conns), "requests": s.t.requests, "reused": s.t.reused, "dials": s.t.dials, "compressed": s.t.compressed, "cookies": s.t.jar.count(), "last_error": s.t.lastError}
}

// Close closes pooled connections.
func (s *StandaloneTransport) Close() { s.t.closeConns() }
