package wire

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// cookieJar is a minimal per-credential jar for the single upstream host. It
// keeps name/value/path/expiry and ignores domain widening, which is enough for
// Cloudflare and ChatGPT edge cookies and avoids pulling a public-suffix list.
type cookieJar struct {
	mu      sync.Mutex
	cookies map[string]storedCookie // key: host|name
}

type storedCookie struct {
	host    string
	name    string
	value   string
	path    string
	expires time.Time // zero = session cookie
	secure  bool
}

func newCookieJar() *cookieJar { return &cookieJar{cookies: map[string]storedCookie{}} }

func (j *cookieJar) store(u *url.URL, resp *http.Response) {
	if j == nil || u == nil || resp == nil {
		return
	}
	host := strings.ToLower(u.Hostname())
	now := time.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range resp.Cookies() {
		if c == nil || c.Name == "" {
			continue
		}
		key := host + "|" + c.Name
		expired := false
		var exp time.Time
		if c.MaxAge < 0 {
			expired = true
		} else if c.MaxAge > 0 {
			exp = now.Add(time.Duration(c.MaxAge) * time.Second)
		} else if !c.Expires.IsZero() {
			exp = c.Expires
			if exp.Before(now) {
				expired = true
			}
		}
		if expired {
			delete(j.cookies, key)
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		j.cookies[key] = storedCookie{host: host, name: c.Name, value: c.Value, path: path, expires: exp, secure: c.Secure}
	}
}

// header renders the Cookie header value for host, dropping expired entries.
func (j *cookieJar) header(host string) string {
	if j == nil {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	now := time.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	var parts []string
	for key, c := range j.cookies {
		if c.host != host {
			continue
		}
		if !c.expires.IsZero() && c.expires.Before(now) {
			delete(j.cookies, key)
			continue
		}
		parts = append(parts, c.name+"="+c.value)
	}
	return strings.Join(parts, "; ")
}

func (j *cookieJar) count() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.cookies)
}
