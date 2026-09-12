package cliproxy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/accounts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/inventory"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func (s *Service) startNativeManagement() error {
	if s.cfg == nil || !s.cfg.NativeManagement.Enabled {
		return nil
	}
	if s.cfg.Home.Enabled {
		return fmt.Errorf("native-management requires standalone CPA mode")
	}
	runtime, err := native.Open(s.cfg.NativeManagement, s.configPath, s.nativeAccountSnapshots)
	if err != nil {
		return fmt.Errorf("start native management: %w", err)
	}
	runtime.SetIdentityLookup(func(value string) (native.Identity, bool) {
		for _, a := range s.coreManager.List() {
			if a != nil && (a.Index == value || a.ID == value || a.FileName == value) {
				kind, _ := a.AccountInfo()
				codexOAuth := a.Provider == "codex" && kind == "oauth"
				return native.Identity{ID: a.Index, Provider: a.Provider, Fingerprint: codexOAuth, Headers: codexOAuth}, true
			}
		}
		return native.Identity{}, false
	})
	runtime.SetInventoryLookup(func() map[string]inventory.Account {
		out := map[string]inventory.Account{}
		for _, a := range s.coreManager.List() {
			if a == nil {
				continue
			}
			fileName := a.FileName
			if kind, _ := a.AccountInfo(); fileName == "" && kind == "oauth" && strings.HasSuffix(a.ID, ".json") {
				fileName = filepath.Base(a.ID)
			}
			out[a.Index] = inventory.Account{FileName: fileName, Label: a.Label, Provider: a.Provider, Windows: inventory.Windows(a.Quota.Signals, a.Quota.ObservedAt)}
		}
		return out
	})
	s.coreManager.SetExecutionPolicy(runtime)
	s.nativeManagement = runtime
	s.serverOptions = append(s.serverOptions, api.WithManagementExtension(runtime), api.WithMiddleware(runtime.Middleware()), api.WithAuthenticatedMiddleware(runtime.CallerMiddleware()))
	usage.RegisterNamedPlugin("cpa-native-management", runtime)
	log.Info("native management enabled on /management.html; credential automation is off")
	return nil
}

func (s *Service) stopNativeManagement(ctx context.Context) error {
	if s.nativeManagement == nil {
		return nil
	}
	runtime := s.nativeManagement
	if err := usage.DefaultManager().Wait(ctx); err != nil {
		// Do not close SQLite under an in-flight usage write if shutdown's budget expires.
		go func() {
			_ = usage.DefaultManager().Wait(context.Background())
			if errClose := runtime.Close(); errClose != nil {
				log.WithError(errClose).Error("close native management")
			}
		}()
		return fmt.Errorf("drain native management usage: %w", err)
	}
	return runtime.Close()
}

func (s *Service) nativeAccountSnapshots() []accounts.Snapshot {
	items := []accounts.Snapshot{}
	if s.coreManager == nil {
		return items
	}
	now := time.Now()
	for _, a := range s.coreManager.List() {
		if a == nil {
			continue
		}
		state := "unknown"
		switch {
		case a.Disabled:
			state = "disabled"
		case a.NextRetryAfter.After(now) || a.Quota.NextRecoverAt.After(now):
			state = "cooldown"
		case a.Unavailable:
			state = "unavailable"
		case a.LastError != nil:
			state = "review"
		case a.Success > 0:
			state = "observed_available"
		}
		next := a.NextRetryAfter
		if a.Quota.NextRecoverAt.After(next) {
			next = a.Quota.NextRecoverAt
		}
		item := accounts.Snapshot{Account: a.Index, Provider: a.Provider, Label: a.Label, State: state, Disabled: a.Disabled, QuotaExceeded: a.Quota.Exceeded}
		if !next.IsZero() {
			item.NextRetryMS = next.UnixMilli()
		}
		if !a.Quota.ObservedAt.IsZero() {
			item.QuotaObservedMS = a.Quota.ObservedAt.UnixMilli()
		}
		items = append(items, item)
	}
	return items
}
