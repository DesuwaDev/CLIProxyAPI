package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

const DefaultCatalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
const catalogLimit = 16 * 1024 * 1024

type CatalogSettings struct {
	Source        string `json:"source"`
	Automatic     bool   `json:"automatic"`
	IntervalHours int    `json:"interval_hours"`
}
type catalogRate struct {
	HasCacheTTL  bool     `json:"has_cache_ttl,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h"`
	Tier         string   `json:"tier,omitempty"`
	Minimum      int64    `json:"min_context"`
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheRead    *float64 `json:"cache_read"`
	CacheWrite   *float64 `json:"cache_write"`
}
type catalogData struct {
	Source    string                   `json:"source"`
	Models    map[string][]catalogRate `json:"models"`
	UpdatedMS int64                    `json:"updated_ms"`
	Hash      string                   `json:"hash"`
	Skipped   int                      `json:"skipped"`
}
type catalogState struct {
	settings  CatalogSettings
	data      catalogData
	attemptMS int64
	lastError string
	busy      bool
	syncMu    sync.Mutex
}

func (m *Module) initCatalog() error {
	// v2 changes payload semantics (service-tier and TTL dimensions). The version
	// barrier prevents older binaries from treating tiered rates as standard rates.
	if err := storage.Migrate(m.db, "price_catalog", `CREATE TABLE native_price_catalog (id INTEGER PRIMARY KEY CHECK(id=1), settings TEXT NOT NULL, payload TEXT NOT NULL);`, `SELECT 1;`); err != nil {
		return err
	}
	m.catalog.settings = CatalogSettings{Source: DefaultCatalogURL, IntervalHours: 24}
	m.catalog.data.Models = map[string][]catalogRate{}
	settings, _ := json.Marshal(m.catalog.settings)
	payload, _ := json.Marshal(m.catalog.data)
	if _, err := m.db.Exec("INSERT INTO native_price_catalog(id,settings,payload) VALUES(1,?,?) ON CONFLICT(id) DO NOTHING", string(settings), string(payload)); err != nil {
		return err
	}
	var a, b string
	if err := m.db.QueryRow("SELECT settings,payload FROM native_price_catalog WHERE id=1").Scan(&a, &b); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(a), &m.catalog.settings); err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), &m.catalog.data)
}
func validateCatalogSettings(s CatalogSettings) error {
	u, err := url.Parse(s.Source)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("use an HTTP(S) pricing URL without user credentials or fragments")
	}
	if s.IntervalHours < 1 || s.IntervalHours > 168 {
		return fmt.Errorf("interval_hours must be between 1 and 168")
	}
	return nil
}
func (m *Module) registerCatalog(g *gin.RouterGroup) {
	g.GET("/catalog/model", func(c *gin.Context) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		rates := m.catalog.data.Models[c.Query("model")]
		if rates == nil {
			rates = []catalogRate{}
		}
		c.JSON(200, gin.H{"model": c.Query("model"), "rates": rates, "source": m.catalog.data.Source, "updated_ms": m.catalog.data.UpdatedMS, "hash": m.catalog.data.Hash})
	})
	g.GET("/catalog", func(c *gin.Context) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		c.JSON(200, gin.H{"settings": m.catalog.settings, "models": len(m.catalog.data.Models), "updated_ms": m.catalog.data.UpdatedMS, "hash": m.catalog.data.Hash, "skipped": m.catalog.data.Skipped, "last_attempt_ms": m.catalog.attemptMS, "last_error": m.catalog.lastError, "syncing": m.catalog.busy, "priority": "local_rules_then_catalog", "loaded_source": m.catalog.data.Source})
	})
	g.PUT("/catalog", func(c *gin.Context) {
		var input CatalogSettings
		if !httpx.Decode(c, &input) {
			return
		}
		if err := validateCatalogSettings(input); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		raw, _ := json.Marshal(input)
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, err := m.db.ExecContext(c.Request.Context(), "UPDATE native_price_catalog SET settings=? WHERE id=1", string(raw)); err != nil {
			httpx.Error(c, err)
			return
		}
		m.catalog.settings = input
		c.JSON(200, gin.H{"saved": true})
	})
	g.POST("/catalog/sync", func(c *gin.Context) {
		if err := m.SyncCatalog(c.Request.Context()); err != nil {
			c.JSON(502, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"synced": true})
	})
}

// RunCatalog owns automatic refresh. Disabling pricing also suspends scheduled I/O.
func (m *Module) RunCatalog(ctx context.Context, enabled func() bool) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		m.mu.RLock()
		s := m.catalog.settings
		last := m.catalog.attemptMS
		if last < m.catalog.data.UpdatedMS {
			last = m.catalog.data.UpdatedMS
		}
		m.mu.RUnlock()
		if enabled() && s.Automatic && (last == 0 || time.Since(time.UnixMilli(last)) >= time.Duration(s.IntervalHours)*time.Hour) {
			_ = m.SyncCatalog(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Module) SyncCatalog(ctx context.Context) (result error) {
	if !m.catalog.syncMu.TryLock() {
		return fmt.Errorf("price catalog synchronization is already running")
	}
	defer m.catalog.syncMu.Unlock()
	m.mu.Lock()
	settings := m.catalog.settings
	m.catalog.busy = true
	m.catalog.attemptMS = time.Now().UnixMilli()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.catalog.busy = false
		if result != nil {
			m.catalog.lastError = result.Error()
		} else {
			m.catalog.lastError = ""
		}
		m.mu.Unlock()
	}()
	if err := validateCatalogSettings(settings); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, settings.Source, nil)
	if err != nil {
		return fmt.Errorf("invalid pricing source")
	}
	req.Header.Set("Accept", "application/json")
	// No inference transports or global HTTP defaults are changed. Cancellation belongs to the caller.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("invalid redirect")
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pricing download failed or was cancelled; previous catalog retained")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return fmt.Errorf("pricing source returned HTTP %d; previous catalog retained", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, catalogLimit+1))
	if err != nil {
		return fmt.Errorf("pricing download incomplete; previous catalog retained")
	}
	if len(raw) > catalogLimit {
		return fmt.Errorf("price catalog exceeds 16 MiB")
	}
	data, err := parseCatalog(raw)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	data.Hash = hex.EncodeToString(digest[:])
	data.Source = settings.Source
	data.UpdatedMS = time.Now().UnixMilli()
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("invalid catalog values")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.catalog.settings.Source != settings.Source {
		return fmt.Errorf("pricing source changed while downloading; result discarded")
	}
	if _, err = m.db.ExecContext(ctx, "UPDATE native_price_catalog SET payload=? WHERE id=1", string(payload)); err != nil {
		return fmt.Errorf("cannot persist pricing catalog; previous catalog retained")
	}
	m.catalog.data = data
	return nil
}
