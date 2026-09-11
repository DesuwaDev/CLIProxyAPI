// chparse decodes a raw TLS ClientHello (hex) into a structured JSON summary:
// version, cipher suites, compression, extension order with types, supported
// groups, key share groups, signature algorithms, ALPN, supported versions, and
// PSK modes. It exists to turn a capture into a reviewable spec and a JA3-style
// string, not to talk to any network.
package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type ext struct {
	Type uint16 `json:"type"`
	Name string `json:"name"`
	Len  int    `json:"len"`
	Data string `json:"data,omitempty"`
}

var extNames = map[uint16]string{0: "server_name", 5: "status_request", 10: "supported_groups", 11: "ec_point_formats", 13: "signature_algorithms", 16: "alpn", 18: "sct", 21: "padding", 23: "extended_master_secret", 27: "compress_certificate", 28: "record_size_limit", 35: "session_ticket", 41: "pre_shared_key", 42: "early_data", 43: "supported_versions", 44: "cookie", 45: "psk_key_exchange_modes", 49: "post_handshake_auth", 50: "signature_algorithms_cert", 51: "key_share", 17513: "application_settings", 17613: "application_settings_new", 65037: "encrypted_client_hello", 65281: "renegotiation_info", 34: "delegated_credentials", 65445: "ech_outer"}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: chparse <hexfile>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		panic(err)
	}
	// handshake header
	if b[0] != 1 {
		panic("not a ClientHello")
	}
	p := 4
	out := map[string]any{}
	out["legacy_version"] = fmt.Sprintf("%04x", binary.BigEndian.Uint16(b[p:]))
	p += 2
	p += 32 // random
	sidLen := int(b[p])
	p++
	out["session_id_len"] = sidLen
	p += sidLen
	csLen := int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	var suites []string
	var ja3Suites []string
	for i := 0; i < csLen; i += 2 {
		v := binary.BigEndian.Uint16(b[p+i:])
		suites = append(suites, fmt.Sprintf("0x%04x", v))
		ja3Suites = append(ja3Suites, fmt.Sprint(v))
	}
	p += csLen
	out["cipher_suites"] = suites
	compLen := int(b[p])
	p++
	out["compression"] = hex.EncodeToString(b[p : p+compLen])
	p += compLen
	extLen := int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	end := p + extLen
	var exts []ext
	var ja3Ext, ja3Groups, ja3Points []string
	for p < end {
		t := binary.BigEndian.Uint16(b[p:])
		l := int(binary.BigEndian.Uint16(b[p+2:]))
		data := b[p+4 : p+4+l]
		e := ext{Type: t, Name: extNames[t], Len: l}
		switch t {
		case 10:
			n := int(binary.BigEndian.Uint16(data))
			var g []string
			for i := 2; i < 2+n; i += 2 {
				v := binary.BigEndian.Uint16(data[i:])
				g = append(g, fmt.Sprintf("0x%04x", v))
				ja3Groups = append(ja3Groups, fmt.Sprint(v))
			}
			e.Data = strings.Join(g, ",")
		case 11:
			for i := 1; i < len(data); i++ {
				ja3Points = append(ja3Points, fmt.Sprint(data[i]))
			}
			e.Data = hex.EncodeToString(data)
		case 13, 50:
			n := int(binary.BigEndian.Uint16(data))
			var g []string
			for i := 2; i < 2+n; i += 2 {
				g = append(g, fmt.Sprintf("0x%04x", binary.BigEndian.Uint16(data[i:])))
			}
			e.Data = strings.Join(g, ",")
		case 16:
			n := int(binary.BigEndian.Uint16(data))
			var g []string
			for i := 2; i < 2+n; {
				l := int(data[i])
				g = append(g, string(data[i+1:i+1+l]))
				i += 1 + l
			}
			e.Data = strings.Join(g, ",")
		case 43:
			n := int(data[0])
			var g []string
			for i := 1; i < 1+n; i += 2 {
				g = append(g, fmt.Sprintf("0x%04x", binary.BigEndian.Uint16(data[i:])))
			}
			e.Data = strings.Join(g, ",")
		case 45:
			e.Data = hex.EncodeToString(data[1:])
		case 51:
			n := int(binary.BigEndian.Uint16(data))
			var g []string
			for i := 2; i < 2+n; {
				grp := binary.BigEndian.Uint16(data[i:])
				kl := int(binary.BigEndian.Uint16(data[i+2:]))
				g = append(g, fmt.Sprintf("0x%04x(len=%d)", grp, kl))
				i += 4 + kl
			}
			e.Data = strings.Join(g, ",")
		case 0:
			e.Data = "sni"
		case 27:
			e.Data = hex.EncodeToString(data)
		case 28:
			e.Data = hex.EncodeToString(data)
		case 65281:
			e.Data = hex.EncodeToString(data)
		default:
			if l <= 32 {
				e.Data = hex.EncodeToString(data)
			}
		}
		exts = append(exts, e)
		ja3Ext = append(ja3Ext, fmt.Sprint(t))
		p += 4 + l
	}
	out["extensions"] = exts
	ja3 := fmt.Sprintf("771,%s,%s,%s,%s", strings.Join(ja3Suites, "-"), strings.Join(ja3Ext, "-"), strings.Join(ja3Groups, "-"), strings.Join(ja3Points, "-"))
	out["ja3"] = ja3
	out["ja3_md5"] = fmt.Sprintf("%x", md5.Sum([]byte(ja3)))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
