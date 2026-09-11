package headers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexidentity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

const versionReleaseURL = "https://api.github.com/repos/openai/codex/releases/latest"
const versionReleaseListURL = "https://api.github.com/repos/openai/codex/releases?per_page=30"
const versionSyncInterval = 6 * time.Hour
const versionDownloadLimit = 16 * 1024 * 1024

var errVersionSyncBusy = errors.New("Codex version synchronization is already running")

type VersionSettings struct {
	ManualVersion string `json:"manual_version"`
	Automatic     bool   `json:"automatic"`
}

type VersionStatus struct {
	Settings       VersionSettings `json:"settings"`
	BuiltinVersion string          `json:"builtin_version"`
	SyncedVersion  string          `json:"synced_version"`
	Effective      string          `json:"effective_version"`
	EffectiveFrom  string          `json:"effective_source"`
	Source         string          `json:"source"`
	IntervalHours  int             `json:"interval_hours"`
	LastCheckedMS  int64           `json:"last_checked_ms"`
	LastSyncedMS   int64           `json:"last_synced_ms"`
	LastError      string          `json:"last_error"`
	Syncing        bool            `json:"syncing"`
}

type versionState struct {
	settings            VersionSettings
	synced              string
	checkedMS, syncedMS int64
	lastError           string
	busy                bool
	syncMu              sync.Mutex
	wake                chan struct{}
	client              *http.Client
	cancel              context.CancelFunc
	paused              bool
}

func (m *Module) initVersions() error {
	if err := storage.Migrate(m.db, "codex_client_version", `CREATE TABLE native_codex_client_version (
		id INTEGER PRIMARY KEY CHECK(id=1), manual_version TEXT NOT NULL DEFAULT '',
		automatic INTEGER NOT NULL DEFAULT 0, synced_version TEXT NOT NULL DEFAULT '',
		checked_ms INTEGER NOT NULL DEFAULT 0, synced_ms INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT ''); INSERT INTO native_codex_client_version(id) VALUES(1);`); err != nil {
		return err
	}
	v := &m.versions
	v.wake = make(chan struct{}, 1)
	v.client = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("unexpected redirect from official release API")
	}}
	if err := m.db.QueryRow(`SELECT manual_version,automatic,synced_version,checked_ms,synced_ms,last_error FROM native_codex_client_version WHERE id=1`).Scan(
		&v.settings.ManualVersion, &v.settings.Automatic, &v.synced, &v.checkedMS, &v.syncedMS, &v.lastError); err != nil {
		return err
	}
	if v.settings.ManualVersion != "" && codexidentity.NormalizeVersion(v.settings.ManualVersion) != v.settings.ManualVersion || v.synced != "" && codexidentity.StableVersion(v.synced) != v.synced {
		return fmt.Errorf("invalid persisted Codex client version")
	}
	return nil
}

func (m *Module) VersionStatus() VersionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := &m.versions
	effective, source := codexidentity.DefaultVersion, "builtin"
	if v.synced != "" && !codexidentity.NewerStable(effective, v.synced) {
		effective, source = v.synced, "synced"
	}
	if v.settings.ManualVersion != "" {
		effective, source = v.settings.ManualVersion, "manual"
	}
	return VersionStatus{Settings: v.settings, BuiltinVersion: codexidentity.DefaultVersion,
		SyncedVersion: v.synced, Effective: effective, EffectiveFrom: source, Source: versionReleaseURL,
		IntervalHours: int(versionSyncInterval / time.Hour), LastCheckedMS: v.checkedMS, LastSyncedMS: v.syncedMS,
		LastError: v.lastError, Syncing: v.busy}
}

func (m *Module) saveVersionSettings(ctx context.Context, settings VersionSettings) error {
	if strings.TrimSpace(settings.ManualVersion) != "" {
		settings.ManualVersion = codexidentity.NormalizeVersion(settings.ManualVersion)
		if settings.ManualVersion == "" {
			return fmt.Errorf("use a version such as 0.154.0 or 0.155.0-alpha.1")
		}
	} else {
		settings.ManualVersion = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.db.ExecContext(ctx, `UPDATE native_codex_client_version SET manual_version=?,automatic=? WHERE id=1`, settings.ManualVersion, settings.Automatic); err != nil {
		return err
	}
	m.versions.settings = settings
	if !settings.Automatic && m.versions.cancel != nil {
		m.versions.cancel()
	}
	select {
	case m.versions.wake <- struct{}{}:
	default:
	}
	return nil
}

func (m *Module) registerVersions(g *gin.RouterGroup) {
	g.GET("/client-version", func(c *gin.Context) { c.JSON(200, m.VersionStatus()) })
	g.PUT("/client-version", func(c *gin.Context) {
		var input struct {
			ManualVersion string `json:"manual_version"`
			Automatic     *bool  `json:"automatic"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if input.Automatic == nil || strings.TrimSpace(input.ManualVersion) != "" && codexidentity.NormalizeVersion(input.ManualVersion) == "" {
			c.JSON(400, gin.H{"error": "automatic is required; manual_version must be empty or a valid Codex version"})
			return
		}
		if err := m.saveVersionSettings(c.Request.Context(), VersionSettings{input.ManualVersion, *input.Automatic}); err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(200, m.VersionStatus())
	})
	g.POST("/client-version/sync", func(c *gin.Context) {
		if err := m.SyncVersion(c.Request.Context()); err != nil {
			status := 502
			if errors.Is(err, errVersionSyncBusy) {
				status = 409
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, m.VersionStatus())
	})
}

// RunVersions is separate from inference. It never downloads binaries or changes models.
func (m *Module) RunVersions(ctx context.Context, enabled func() bool) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s := m.VersionStatus()
		if enabled() && s.Settings.Automatic && (s.LastCheckedMS == 0 || time.Since(time.UnixMilli(s.LastCheckedMS)) >= versionSyncInterval) {
			_ = m.syncVersion(ctx, true)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.versions.wake:
		}
	}
}

func (m *Module) SyncVersion(ctx context.Context) (result error) {
	return m.syncVersion(ctx, false)
}

// PauseVersionSync cancels scheduled I/O when the parent feature is disabled.
func (m *Module) PauseVersionSync(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions.paused = !enabled
	if !enabled && m.versions.cancel != nil {
		m.versions.cancel()
	}
	select {
	case m.versions.wake <- struct{}{}:
	default:
	}
}

func (m *Module) syncVersion(ctx context.Context, automatic bool) (result error) {
	v := &m.versions
	if !v.syncMu.TryLock() {
		return errVersionSyncBusy
	}
	defer v.syncMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if automatic && (!v.settings.Automatic || v.paused) {
		m.mu.Unlock()
		return nil
	}
	if automatic {
		v.cancel = cancel
	}
	v.busy = true
	v.checkedMS = time.Now().UnixMilli()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		v.busy = false
		v.cancel = nil
		v.lastError = ""
		if result != nil {
			v.lastError = result.Error()
		}
		if _, err := m.db.Exec(`UPDATE native_codex_client_version SET checked_ms=?,last_error=? WHERE id=1`, v.checkedMS, v.lastError); err != nil && result == nil {
			result = fmt.Errorf("could not persist Codex version sync status")
			v.lastError = result.Error()
		}
	}()
	latest, err := m.fetchStableVersion(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Settings may have changed during the download. Only the sync cache is written.
	if codexidentity.NewerStable(latest, v.synced) {
		now := time.Now().UnixMilli()
		if _, err := m.db.ExecContext(ctx, `UPDATE native_codex_client_version SET synced_version=?,synced_ms=? WHERE id=1`, latest, now); err != nil {
			return fmt.Errorf("could not persist Codex version; previous value retained")
		}
		v.synced, v.syncedMS = latest, now
	}
	return nil
}

type codexRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func stableRelease(release codexRelease) string {
	if release.Draft || release.Prerelease || !strings.HasPrefix(release.Tag, "rust-v") {
		return ""
	}
	raw := strings.TrimPrefix(release.Tag, "rust-v")
	version := codexidentity.StableVersion(raw)
	if version != raw {
		return ""
	}
	return version
}

func (m *Module) fetchStableVersion(ctx context.Context) (string, error) {
	var release codexRelease
	err := m.fetchReleases(ctx, versionReleaseURL, &release)
	if err == nil {
		if version := stableRelease(release); version != "" {
			return version, nil
		}
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("Codex version sync cancelled; previous value retained")
	}
	var releases []codexRelease
	if err := m.fetchReleases(ctx, versionReleaseListURL, &releases); err != nil {
		return "", err
	}
	best := ""
	for _, release := range releases {
		if version := stableRelease(release); codexidentity.NewerStable(version, best) {
			best = version
		}
	}
	if best == "" {
		return "", fmt.Errorf("official release API returned no stable Codex version; previous value retained")
	}
	return best, nil
}

func (m *Module) fetchReleases(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid official release URL")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CLIProxyAPI-version-sync")
	resp, err := m.versions.client.Do(req)
	if err != nil {
		return fmt.Errorf("official release download failed or was cancelled; previous version retained")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("official release API returned HTTP %d; previous version retained", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, versionDownloadLimit+1))
	if err != nil || len(raw) > versionDownloadLimit || json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("invalid or oversized official release response; previous version retained")
	}
	return nil
}
