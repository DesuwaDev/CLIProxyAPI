// Package risk implements optional request admission auditing without changing credentials.
package risk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type Rule struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Regex   bool   `json:"regex"`
	Enabled bool   `json:"enabled"`
}
type Endpoint struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Protocol   string `json:"protocol"`
	Enabled    bool   `json:"enabled"`
	Token      string `json:"token,omitempty"`
	ClearToken bool   `json:"clear_token,omitempty"`
	HasToken   bool   `json:"has_token"`
	Ciphertext string `json:"ciphertext,omitempty"`
}
type Config struct {
	Version        int64              `json:"version"`
	Mode           string             `json:"mode"`
	Strategy       string             `json:"strategy"`
	Providers      []string           `json:"providers"`
	ModelFilter    string             `json:"model_filter"`
	Models         []string           `json:"models"`
	LatestTurnOnly bool               `json:"latest_turn_only"`
	FailClosed     bool               `json:"fail_closed"`
	RecordPass     bool               `json:"record_pass"`
	Concurrency    int                `json:"concurrency"`
	ChunkSize      int                `json:"chunk_size"`
	MaxInputChars  int                `json:"max_input_chars"`
	HashBlock      bool               `json:"hash_block"`
	HashTTL        int                `json:"hash_ttl_seconds"`
	SessionBlock   bool               `json:"session_block"`
	SessionTTL     int                `json:"session_ttl_seconds"`
	Rules          []Rule             `json:"rules"`
	Endpoints      []Endpoint         `json:"endpoints"`
	Thresholds     map[string]float64 `json:"thresholds"`
}

func defaults() Config {
	return Config{Version: 1, Mode: "off", Strategy: "api", Providers: []string{"codex"}, ModelFilter: "all", Models: []string{}, FailClosed: true, Concurrency: 16, ChunkSize: 12000, MaxInputChars: 1000000, HashBlock: true, HashTTL: 86400, SessionTTL: 3600, Rules: []Rule{}, Endpoints: []Endpoint{}, Thresholds: map[string]float64{"cyber": 0.8, "jailbreak": 0.8}}
}
func validate(c Config) error {
	if c.Mode != "off" && c.Mode != "observe" && c.Mode != "block" {
		return fmt.Errorf("mode must be off, observe or block")
	}
	if c.Strategy != "keywords" && c.Strategy != "api" && c.Strategy != "both" {
		return fmt.Errorf("invalid strategy")
	}
	if c.ModelFilter != "all" && c.ModelFilter != "include" && c.ModelFilter != "exclude" {
		return fmt.Errorf("invalid model filter")
	}
	if c.Concurrency < 1 || c.Concurrency > 64 || c.ChunkSize < 256 || c.ChunkSize > 100000 || c.MaxInputChars < c.ChunkSize || c.MaxInputChars > 4000000 {
		return fmt.Errorf("invalid concurrency or input limits")
	}
	if c.HashTTL < 60 || c.HashTTL > 2592000 || c.SessionTTL < 60 || c.SessionTTL > 2592000 {
		return fmt.Errorf("block TTL must be between 60 and 2592000 seconds")
	}
	if len(c.Rules) > 256 || len(c.Endpoints) > 16 || len(c.Models) > 200 || len(c.Providers) > 32 || len(c.Thresholds) > 64 {
		return fmt.Errorf("too many configuration entries")
	}
	ids := map[string]bool{}
	activeRules, activeEndpoints := 0, 0
	for _, r := range c.Rules {
		if r.ID == "" || len(r.ID) > 80 || ids["r:"+r.ID] || r.Pattern == "" || len(r.Pattern) > 2048 {
			return fmt.Errorf("invalid or duplicate rule")
		}
		ids["r:"+r.ID] = true
		if r.Regex {
			if _, err := regexp.Compile("(?i)" + r.Pattern); err != nil {
				return fmt.Errorf("invalid rule expression: %s", r.ID)
			}
		}
		if r.Enabled {
			activeRules++
		}
	}
	for _, e := range c.Endpoints {
		if e.ID == "" || len(e.ID) > 80 || ids["e:"+e.ID] || len(e.Name) > 160 || len(e.Model) > 200 || len(e.URL) > 2048 || len(e.Token) > 4096 {
			return fmt.Errorf("invalid endpoint")
		}
		ids["e:"+e.ID] = true
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("endpoint requires a plain HTTP(S) URL without credentials or query")
		}
		if e.Protocol != "guard_json" && e.Protocol != "qwen3guard" && e.Protocol != "moderations" {
			return fmt.Errorf("unsupported audit protocol")
		}
		if e.Protocol != "moderations" && strings.TrimSpace(e.Model) == "" {
			return fmt.Errorf("audit model is required")
		}
		if strings.ContainsAny(e.Token, "\r\n") {
			return fmt.Errorf("invalid endpoint token")
		}
		if e.Enabled {
			activeEndpoints++
		}
	}
	for k, v := range c.Thresholds {
		if k == "" || len(k) > 100 || v < 0 || v > 1 {
			return fmt.Errorf("invalid category threshold")
		}
	}
	if c.Mode != "off" {
		if c.Strategy != "api" && activeRules == 0 {
			return fmt.Errorf("enable at least one local rule")
		}
		if c.Strategy != "keywords" && activeEndpoints == 0 {
			return fmt.Errorf("enable at least one audit endpoint")
		}
	}
	return nil
}
func public(c Config) Config {
	raw, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(raw, &out)
	for i := range out.Endpoints {
		e := &out.Endpoints[i]
		e.HasToken = e.Ciphertext != ""
		e.Ciphertext = ""
		e.Token = ""
		e.ClearToken = false
	}
	return out
}
func (m *Module) seal(value string) (string, error) {
	key, err := os.ReadFile(m.keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return "", err
		}
		var f *os.File
		f, err = os.OpenFile(m.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", err
		}
		_, err = f.Write(key)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return "", err
	}
	aead, err := makeAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), nil)), nil
}
func makeAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("audit encryption key is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (m *Module) token(e Endpoint) (string, error) {
	if e.Ciphertext == "" {
		return "", nil
	}
	key, err := os.ReadFile(m.keyPath)
	if err != nil {
		return "", fmt.Errorf("audit token key unavailable")
	}
	a, err := makeAEAD(key)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil || len(b) < a.NonceSize() {
		return "", fmt.Errorf("audit token invalid")
	}
	out, err := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], nil)
	return string(out), err
}
