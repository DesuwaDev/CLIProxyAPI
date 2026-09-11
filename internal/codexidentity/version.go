// Package codexidentity keeps Codex version declarations consistent across transports.
package codexidentity

import (
	"net/http"
	"regexp"
	"strings"
)

// DefaultVersion was verified against the official stable release and npm latest.
const DefaultVersion = "0.154.0"
const DefaultUserAgent = "codex-tui/" + DefaultVersion + " (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; " + DefaultVersion + ")"

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)
var userAgentPattern = regexp.MustCompile(`^(codex-tui|codex_cli_rs|codex_exec|codex_vscode|codex_app_server|codex)/[^ \t]+(.*)$`)
var trailerPattern = regexp.MustCompile(`(\((?:codex-tui|codex_cli_rs|codex_exec|codex_vscode|codex_app_server|codex); )[^ ;)]+(\))`)

func NormalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "rust-"), "v")
	if len(value) > 64 || !versionPattern.MatchString(value) {
		return ""
	}
	return value
}

func StableVersion(value string) string {
	value = NormalizeVersion(value)
	if strings.Contains(value, "-") {
		return ""
	}
	return value
}

// NewerStable compares validated numeric components without integer overflow.
func NewerStable(a, b string) bool {
	a, b = StableVersion(a), StableVersion(b)
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range aa {
		if len(aa[i]) != len(bb[i]) {
			return len(aa[i]) > len(bb[i])
		}
		if aa[i] != bb[i] {
			return aa[i] > bb[i]
		}
	}
	return false
}

// RewriteHeaders changes only version declarations in recognized Codex UAs.
// In particular, it does not invent a Version header on requests without one.
func RewriteHeaders(h http.Header, version string) {
	version = NormalizeVersion(version)
	if version == "" {
		return
	}
	match := userAgentPattern.FindStringSubmatch(h.Get("User-Agent"))
	if match == nil {
		return
	}
	ua := match[1] + "/" + version + match[2]
	ua = trailerPattern.ReplaceAllString(ua, "${1}"+version+"${2}")
	setHeader(h, "User-Agent", ua)
	for key := range h {
		if strings.EqualFold(key, "Version") {
			setHeader(h, "Version", version)
			break
		}
	}
}

func setHeader(h http.Header, name, value string) {
	for key := range h {
		if strings.EqualFold(key, name) {
			delete(h, key)
		}
	}
	h.Set(name, value)
}
