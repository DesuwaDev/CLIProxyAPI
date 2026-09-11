package headers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexidentity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type versionTransport func(*http.Request) (*http.Response, error)

func (f versionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func versionResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func versionTestModule(t *testing.T) *Module {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "version.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestVersionSyncFiltersReleasesAndPreservesSettings(t *testing.T) {
	m := versionTestModule(t)
	if s := m.VersionStatus(); s.Effective != codexidentity.DefaultVersion || s.Settings.Automatic {
		t.Fatal("invalid defaults")
	}
	if err := m.saveVersionSettings(context.Background(), VersionSettings{"v0.153.4", true}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	m.versions.client.Transport = versionTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "" {
			t.Fatal("sync leaked credentials or changed host")
		}
		if strings.HasSuffix(r.URL.Path, "/latest") {
			return versionResponse(200, `{"tag_name":"rusty-v8-v99.0.0"}`)
		}
		return versionResponse(200, `[{"tag_name":"rust-v0.99.0"},{"tag_name":"rust-v0.155.0"},{"tag_name":"rust-v999.0.0","draft":true},{"tag_name":"rust-v888.0.0","prerelease":true},{"tag_name":"rust-v777.0.0-alpha.1"},{"tag_name":"rust-vv666.0.0"}]`)
	})
	if err := m.SyncVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := m.VersionStatus(); requests != 2 || s.SyncedVersion != "0.155.0" || s.Effective != "0.153.4" || s.LastCheckedMS == 0 {
		t.Fatalf("bad sync: %+v, requests=%d", s, requests)
	}
	out := http.Header{"User-Agent": {codexidentity.DefaultUserAgent}, "Version": {"old"}, "Via": {"keep-with-account-cleanup-off"}}
	m.Transform("account", nil)(out)
	if out.Get("Version") != "0.153.4" || out.Get("Via") == "" {
		t.Fatal("global version should work independently of cleanup")
	}
	m.modes["account"] = "client"
	in := http.Header{"User-Agent": {"codex_exec/0.150.0 (Windows)"}, "Originator": {"codex_exec"}}
	m.Transform("account", in)(out)
	if out.Get("User-Agent") != in.Get("User-Agent") || out.Get("Version") != "" {
		t.Fatal("manual version overrode preserved client identity")
	}
	if err := m.saveVersionSettings(context.Background(), VersionSettings{}); err != nil {
		t.Fatal(err)
	}
	m.versions.client.Transport = versionTransport(func(r *http.Request) (*http.Response, error) { return versionResponse(429, `{}`) })
	if err := m.SyncVersion(context.Background()); err == nil {
		t.Fatal("expected sync failure")
	}
	if s := m.VersionStatus(); s.Effective != "0.155.0" || s.LastError == "" {
		t.Fatalf("failure lost cache: %+v", s)
	}
	m.versions.client.Transport = versionTransport(func(r *http.Request) (*http.Response, error) {
		return versionResponse(200, `{"tag_name":"rust-v0.154.0"}`)
	})
	if err := m.SyncVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(m.db)
	if err != nil {
		t.Fatal(err)
	}
	if s := restored.VersionStatus(); s.Effective != "0.155.0" || s.Settings.Automatic || s.LastError != "" || s.LastCheckedMS == 0 {
		t.Fatalf("downgraded or failed persistence: %+v", s)
	}
}

func TestVersionSyncConcurrentEditsAndShutdown(t *testing.T) {
	m := versionTestModule(t)
	started, finish := make(chan struct{}), make(chan struct{})
	m.versions.client.Transport = versionTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-finish
		return versionResponse(200, `{"tag_name":"rust-v0.155.0"}`)
	})
	done := make(chan error, 1)
	go func() { done <- m.SyncVersion(context.Background()) }()
	<-started
	if !errors.Is(m.SyncVersion(context.Background()), errVersionSyncBusy) {
		t.Fatal("duplicate sync started")
	}
	if err := m.saveVersionSettings(context.Background(), VersionSettings{"0.152.0", false}); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Transform("account", nil)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := m.VersionStatus(); s.Effective != "0.152.0" || s.Settings.Automatic {
		t.Fatal("late sync overwrote manual edit")
	}
	if err := m.saveVersionSettings(context.Background(), VersionSettings{"0.153.0", false}); err != nil {
		t.Fatal(err)
	}
	h := http.Header{"User-Agent": {codexidentity.DefaultUserAgent}, "Version": {"old"}}
	snapshot(h)
	if h.Get("Version") != "0.152.0" {
		t.Fatal("in-flight request changed policy")
	}
	// Exercise lifecycle cancellation without adding sleeps or network timeouts.
	started = make(chan struct{})
	m.versions.checkedMS = 0
	m.versions.client.Transport = versionTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	if err := m.saveVersionSettings(context.Background(), VersionSettings{"", true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() { m.RunVersions(ctx, func() bool { return true }); close(workerDone) }()
	<-started
	cancel()
	<-workerDone
	if m.VersionStatus().Syncing {
		t.Fatal("worker still marked busy after shutdown")
	}
}
