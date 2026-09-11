package wire

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"

	tls "github.com/refraction-networking/utls"
)

// Profile values below reproduce the ClientHello emitted by codex-cli 0.154.0
// (reqwest 0.12 / rustls 0.23 / aws-lc-rs) as captured locally on 2026-09-11.
// rustls shuffles extension order per connection; the extension *set* is fixed.

var codexCipherSuites = []uint16{
	tls.TLS_AES_256_GCM_SHA384,
	tls.TLS_AES_128_GCM_SHA256,
	tls.TLS_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.FAKE_TLS_EMPTY_RENEGOTIATION_INFO_SCSV,
}

var codexSignatureAlgorithms = []tls.SignatureScheme{
	tls.ECDSAWithP384AndSHA384,
	tls.ECDSAWithP256AndSHA256,
	tls.ECDSAWithP521AndSHA512,
	tls.Ed25519,
	tls.PSSWithSHA512,
	tls.PSSWithSHA384,
	tls.PSSWithSHA256,
	tls.PKCS1WithSHA512,
	tls.PKCS1WithSHA384,
	tls.PKCS1WithSHA256,
}

var codexCurves = []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}

// codexClientHelloSpec builds a fresh spec. alpn is nil for the WebSocket
// handshake (the CLI's tungstenite path negotiates no ALPN) and
// {"h2","http/1.1"} for the reqwest HTTP path.
func codexClientHelloSpec(alpn []string) *tls.ClientHelloSpec {
	exts := []tls.TLSExtension{
		&tls.SNIExtension{},
		&tls.StatusRequestExtension{},
		&tls.SupportedCurvesExtension{Curves: append([]tls.CurveID(nil), codexCurves...)},
		&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
		&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]tls.SignatureScheme(nil), codexSignatureAlgorithms...)},
		&tls.ExtendedMasterSecretExtension{},
		&tls.SessionTicketExtension{},
		&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
		&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
		&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519MLKEM768}, {Group: tls.X25519}}},
	}
	if len(alpn) > 0 {
		exts = append(exts, &tls.ALPNExtension{AlpnProtocols: append([]string(nil), alpn...)})
	}
	shuffle(exts)
	return &tls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CipherSuites:       append([]uint16(nil), codexCipherSuites...),
		CompressionMethods: []uint8{0},
		Extensions:         exts,
	}
}

// shuffle applies a Fisher-Yates permutation using crypto/rand, matching the
// per-connection randomization rustls performs.
func shuffle(exts []tls.TLSExtension) {
	var buf [8]byte
	for i := len(exts) - 1; i > 0; i-- {
		if _, err := rand.Read(buf[:]); err != nil {
			return
		}
		j := int(binary.LittleEndian.Uint64(buf[:]) % uint64(i+1))
		exts[i], exts[j] = exts[j], exts[i]
	}
}

// handshake performs the profile TLS handshake over conn for host. It never
// resumes sessions: the captured client opened every connection with a full
// handshake and no pre_shared_key extension.
func handshake(ctx context.Context, conn net.Conn, host string, alpn []string) (*tls.UConn, error) {
	cfg := &tls.Config{ServerName: host, OmitEmptyPsk: true, SessionTicketsDisabled: true, RootCAs: rootCAs()}
	uconn := tls.UClient(conn, cfg, tls.HelloCustom)
	if err := uconn.ApplyPreset(codexClientHelloSpec(alpn)); err != nil {
		return nil, fmt.Errorf("wire: apply codex ClientHello: %w", err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("wire: tls handshake: %w", err)
	}
	return uconn, nil
}

// ProfileSummary describes the compiled-in profile for the management UI.
func ProfileSummary() map[string]any {
	return map[string]any{
		"client":         "codex-cli 0.154.0 (reqwest 0.12.28 / rustls 0.23.36 / hyper 1.8.1 / h2 0.4.16)",
		"captured":       "2026-09-11 local capture",
		"tls_ja4_like":   "t13d1110h2_" + fmt.Sprintf("%d ciphers, %d extensions, groups x25519mlkem768/x25519/p256/p384", len(codexCipherSuites), 11),
		"http2_settings": "ENABLE_PUSH=0 INITIAL_WINDOW_SIZE=2097152 MAX_FRAME_SIZE=16384 MAX_HEADER_LIST_SIZE=16384; WINDOW_UPDATE 5177345",
		"body":           "POST /responses zstd (libzstd level 3 frame shape, window 2 MiB, no checksum, no content size)",
		"cookies":        "per-credential jar for the upstream host",
		"websocket":      "same TLS profile without ALPN; HTTP/1.1 upgrade header order from the CLI",
	}
}
