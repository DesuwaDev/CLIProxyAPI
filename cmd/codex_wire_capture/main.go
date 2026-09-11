// codex_wire_capture is a diagnostic-only TLS listener. It accepts connections
// from a Codex CLI pointed at it, records the raw ClientHello bytes, completes a
// TLS handshake with a throwaway self-signed certificate (the CLI must trust it via
// SSL_CERT_FILE), then records the HTTP/2 preface, SETTINGS, WINDOW_UPDATE and the
// first HEADERS frame (decoded header names, non-sensitive values, exact order).
// It never stores Authorization/Cookie values and never forwards traffic upstream.
package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type record struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

var sensitive = map[string]bool{
	"authorization":         true,
	"cookie":                true,
	"chatgpt-account-id":    true,
	"x-codex-turn-metadata": true,
	"x-codex-turn-state":    true,
	"session-id":            true,
	"session_id":            true,
	"thread-id":             true,
	"x-client-request-id":   true,
	"x-codex-window-id":     true,
	"conversation_id":       true,
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "listen address")
	out := flag.String("out", "capture.jsonl", "output jsonl")
	certOut := flag.String("cert", "ca.pem", "write self-signed cert here (for SSL_CERT_FILE)")
	host := flag.String("host", "chatgpt.com", "certificate SAN")
	wait := flag.Duration("wait", 120*time.Second, "how long to accept connections")
	flag.Parse()

	cert, pemBytes := selfSigned(*host)
	if err := os.WriteFile(*certOut, pemBytes, 0o600); err != nil {
		fail(err)
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	write := func(ev string, data any) { _ = enc.Encode(record{Event: ev, Data: data}) }

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "listening on %s, cert at %s\n", *listen, *certOut)
	deadline := time.Now().Add(*wait)
	_ = ln.(*net.TCPListener).SetDeadline(deadline)
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		go handle(conn, cert, write)
	}
	time.Sleep(500 * time.Millisecond)
}

func handle(raw net.Conn, cert tls.Certificate, write func(string, any)) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	write("connection", map[string]any{"remote": raw.RemoteAddr().String()})
	br := bufio.NewReaderSize(raw, 1<<16)
	hdr, err := br.Peek(5)
	if err != nil || hdr[0] != 0x16 {
		write("not_tls", nil)
		return
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	rec, err := br.Peek(5 + recLen)
	if err != nil {
		write("peek_error", err.Error())
		return
	}
	ch := append([]byte(nil), rec[5:]...)
	write("client_hello", map[string]any{
		"hex":            hex.EncodeToString(ch),
		"len":            len(ch),
		"record_version": fmt.Sprintf("%02x%02x", hdr[1], hdr[2]),
	})

	tlsConn := tls.Server(&peekedConn{Conn: raw, r: br}, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		write("handshake_error", err.Error())
		return
	}
	st := tlsConn.ConnectionState()
	write("tls_negotiated", map[string]any{
		"version": st.Version,
		"cipher":  st.CipherSuite,
		"alpn":    st.NegotiatedProtocol,
		"sni":     st.ServerName,
	})
	if st.NegotiatedProtocol != "h2" {
		rd := bufio.NewReader(tlsConn)
		var lines []string
		for {
			line, err := rd.ReadString(10)
			if err != nil {
				break
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				break
			}
			lines = append(lines, redactLine(trimmed))
		}
		write("http1_request_head", lines)
		return
	}

	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(tlsConn, preface); err != nil || string(preface) != http2.ClientPreface {
		write("bad_preface", nil)
		return
	}
	fr := http2.NewFramer(tlsConn, tlsConn)
	dec := hpack.NewDecoder(4096, nil)
	var frames []any
	var headers []map[string]any
	var headerBlock bytes.Buffer
	_ = fr.WriteSettings()
	_ = fr.WriteSettingsAck()
	emit := func(hf hpack.HeaderField) {
		v := hf.Value
		if sensitive[strings.ToLower(hf.Name)] {
			v = "<redacted>"
		}
		headers = append(headers, map[string]any{"name": hf.Name, "value": v, "sensitive_bit": hf.Sensitive})
	}
	var body bytes.Buffer
	var dataFrames []map[string]any
	finish := func() {
		dec.SetEmitFunc(emit)
		_, _ = dec.Write(headerBlock.Bytes())
		_ = dec.Close()
		write("h2_request", map[string]any{"frames": frames, "headers": headers})
	}
	readBody := func(streamID uint32) {
		for j := 0; j < 4096; j++ {
			frm, err := fr.ReadFrame()
			if err != nil {
				write("body_frame_error", err.Error())
				break
			}
			switch f := frm.(type) {
			case *http2.DataFrame:
				body.Write(f.Data())
				dataFrames = append(dataFrames, map[string]any{"len": len(f.Data()), "padded": f.Header().Flags.Has(http2.FlagDataPadded), "end_stream": f.StreamEnded()})
				if f.StreamEnded() {
					write("h2_body", map[string]any{"data_frames": dataFrames, "total": body.Len(), "zstd": zstdSummary(body.Bytes())})
					return
				}
			case *http2.WindowUpdateFrame:
				dataFrames = append(dataFrames, map[string]any{"window_update": f.Increment, "stream": f.StreamID})
			case *http2.SettingsFrame:
				dataFrames = append(dataFrames, map[string]any{"settings_ack": f.IsAck()})
			case *http2.RSTStreamFrame:
				write("h2_rst", map[string]any{"code": f.ErrCode.String()})
				return
			default:
				dataFrames = append(dataFrames, map[string]any{"other": frm.Header().Type.String()})
			}
		}
	}
	for i := 0; i < 32; i++ {
		frm, err := fr.ReadFrame()
		if err != nil {
			write("frame_error", err.Error())
			break
		}
		switch f := frm.(type) {
		case *http2.SettingsFrame:
			var s []map[string]any
			_ = f.ForeachSetting(func(st http2.Setting) error {
				s = append(s, map[string]any{"id": st.ID.String(), "val": st.Val})
				return nil
			})
			frames = append(frames, map[string]any{"type": "SETTINGS", "ack": f.IsAck(), "settings": s})
		case *http2.WindowUpdateFrame:
			frames = append(frames, map[string]any{"type": "WINDOW_UPDATE", "stream": f.StreamID, "increment": f.Increment})
		case *http2.PingFrame:
			frames = append(frames, map[string]any{"type": "PING"})
		case *http2.HeadersFrame:
			headerBlock.Write(f.HeaderBlockFragment())
			frames = append(frames, map[string]any{
				"type":        "HEADERS",
				"stream":      f.StreamID,
				"end_stream":  f.StreamEnded(),
				"end_headers": f.HeadersEnded(),
				"priority":    f.HasPriority(),
				"padded":      f.Header().Flags.Has(http2.FlagHeadersPadded),
			})
			if f.HeadersEnded() {
				finish()
				if !f.StreamEnded() {
					readBody(f.StreamID)
				}
				return
			}
		case *http2.ContinuationFrame:
			headerBlock.Write(f.HeaderBlockFragment())
			if f.HeadersEnded() {
				finish()
				readBody(f.StreamID)
				return
			}
		default:
			frames = append(frames, map[string]any{"type": frm.Header().Type.String()})
		}
	}
	write("h2_partial", frames)
}

// zstdSummary decodes the zstd frame header flags without exposing content.
func zstdSummary(b []byte) map[string]any {
	if len(b) < 6 || b[0] != 0x28 || b[1] != 0xb5 || b[2] != 0x2f || b[3] != 0xfd {
		return map[string]any{"is_zstd": false}
	}
	fhd := b[4]
	out := map[string]any{
		"is_zstd":            true,
		"fcs_flag":           int(fhd >> 6),
		"single_segment":     fhd&0x20 != 0,
		"content_checksum":   fhd&0x04 != 0,
		"dictionary_id_flag": int(fhd & 0x03),
	}
	p := 5
	if fhd&0x20 == 0 {
		out["window_descriptor"] = fmt.Sprintf("0x%02x", b[p])
		p++
	}
	p += int(fhd & 0x03)
	switch fhd >> 6 {
	case 0:
		if fhd&0x20 != 0 {
			out["frame_content_size"] = int(b[p])
		}
	case 1:
		out["frame_content_size"] = int(binary.LittleEndian.Uint16(b[p:])) + 256
	case 2:
		out["frame_content_size"] = int(binary.LittleEndian.Uint32(b[p:]))
	case 3:
		out["frame_content_size"] = int(binary.LittleEndian.Uint64(b[p:]))
	}
	// First block header: 3 bytes little endian: last(1) type(2) size(21)
	if len(b) > p+3 {
		bh := uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16
		out["first_block"] = map[string]any{"last": bh&1 == 1, "type": int((bh >> 1) & 3), "size": int(bh >> 3)}
	}
	return out
}

func redactLine(line string) string {
	idx := strings.IndexByte(line, ':')
	if idx > 0 && sensitive[strings.ToLower(strings.TrimSpace(line[:idx]))] {
		return line[:idx] + ": <redacted>"
	}
	return line
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func selfSigned(host string) (tls.Certificate, []byte) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fail(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "codex-wire-capture throwaway CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		fail(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		fail(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fail(err)
	}
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		fail(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		fail(err)
	}
	chainPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	cert, err := tls.X509KeyPair(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER}))
	if err != nil {
		fail(err)
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
