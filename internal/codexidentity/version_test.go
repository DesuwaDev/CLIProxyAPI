package codexidentity

import (
	"net/http"
	"strings"
	"testing"
)

func TestVersionDeclarationsStayConsistent(t *testing.T) {
	for _, version := range []string{"0.155.0", "0.156.0-alpha.4"} {
		h := http.Header{"User-Agent": {DefaultUserAgent}, "version": {"0.1.0"}, "Originator": {"codex-tui"}, "Authorization": {"Bearer selected"}}
		RewriteHeaders(h, version)
		if strings.Count(h.Get("User-Agent"), version) != 2 || h.Get("Version") != version || h.Get("Originator") != "codex-tui" || h.Get("Authorization") != "Bearer selected" {
			t.Fatalf("inconsistent declarations: %#v", h)
		}
		h.Del("Version")
		RewriteHeaders(h, version)
		if h.Get("Version") != "" {
			t.Fatal("invented HTTP Version header")
		}
	}
	h := http.Header{"User-Agent": {"custom-agent/1.0"}, "Version": {"unchanged"}}
	RewriteHeaders(h, "0.155.0")
	if h.Get("User-Agent") != "custom-agent/1.0" || h.Get("Version") != "unchanged" {
		t.Fatal("rewrote unrelated client")
	}
	for _, invalid := range []string{"1", "1.2", "01.2.3", "1.2.3\r\nX-Injected: yes", strings.Repeat("1", 65)} {
		if NormalizeVersion(invalid) != "" {
			t.Fatalf("accepted invalid version %q", invalid)
		}
	}
	if NormalizeVersion(" rust-v0.154.0 ") != DefaultVersion || !NewerStable("0.100.0", "0.99.0") || NewerStable("0.154.0-alpha.9", "0.153.0") {
		t.Fatal("version normalization/comparison failed")
	}
}
