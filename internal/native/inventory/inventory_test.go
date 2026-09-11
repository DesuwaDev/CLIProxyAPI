package inventory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/history"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestInventorySeedsAndPreservesCountersAcrossRetention(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "inventory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = history.New(db); err != nil {
		t.Fatal(err)
	}
	cost := 2.5
	e := event.Event{ID: "seed", TimestampMS: 1000, Account: "account", KeyHash: "key", Tokens: usage.TokenBreakdown{TotalTokens: 150}, CostUSD: &cost}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = history.Insert(tx, e); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	e.ID = "new"
	e.TimestampMS = 2000
	e.CostUSD = nil
	e.Failed = true
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = history.Insert(tx, e); err != nil {
		t.Fatal(err)
	}
	if err = Record(tx, e); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM native_events"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"credential", "client"} {
		target := "account"
		if scope == "client" {
			target = "key"
		}
		total, err := m.totals(context.Background(), scope, target)
		if err != nil || total.Requests != 2 || total.Tokens != 300 || total.Cost != 2.5 || total.Unpriced != 1 || total.Failures != 1 || total.FirstMS != 1000 || total.LastMS != 2000 {
			t.Fatalf("wrong totals: %+v %v", total, err)
		}
	}
	if _, err = New(db); err != nil {
		t.Fatal(err)
	}
	total, err := m.totals(context.Background(), "client", "key")
	if err != nil || total.Requests != 2 {
		t.Fatal("restart reseeded counters")
	}
	p := m.Profile("client", "key")
	p.Name = "name"
	p.Notes = "note"
	p.Models = []string{"allowed"}
	p.ExpiresMS = 5000
	m.now = func() time.Time { return time.UnixMilli(3000) }
	if err = m.Save(context.Background(), "client", "key", p); err != nil {
		t.Fatal(err)
	}
	if err = m.Check("client", "key", "other"); err == nil {
		t.Fatal("model restriction missing")
	}
	if err = m.Check("client", "key", "allowed"); err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.UnixMilli(6000) }
	if err = m.Check("client", "key", "allowed"); err == nil {
		t.Fatal("expiry missing")
	}
	if err = m.Save(context.Background(), "client", "key", p); err == nil {
		t.Fatal("stale profile overwrite accepted")
	}
}
func TestInventoryForecastHandlesUnpricedStaleAndZeroQuota(t *testing.T) {
	now := time.Unix(1800000000, 0)
	signals := map[string]string{"x-codex-primary-used-percent": "25", "x-codex-primary-window-minutes": "300", "x-codex-primary-reset-at": "1800003600"}
	windows := Windows(signals, now)
	if len(windows) != 1 || windows[0].StartMS != (1800003600-18000)*1000 {
		t.Fatalf("wrong window: %+v", windows)
	}
	total := Totals{Requests: 10, Cost: 12.5}
	forecast := Estimate(windows[0], total, 0, now.UnixMilli())
	if forecast.EstimatedTotal == nil || *forecast.EstimatedTotal != 50 || *forecast.EstimatedRemaining != 37.5 || forecast.Coverage != "partial" {
		t.Fatalf("wrong estimate: %+v", forecast)
	}
	total.Unpriced = 1
	if Estimate(windows[0], total, 0, now.UnixMilli()).EstimatedRemaining != nil {
		t.Fatal("unpriced usage treated as free")
	}
	total.Unpriced = 0
	w := windows[0]
	w.UsedPercent = 0
	if Estimate(w, total, 0, now.UnixMilli()).EstimatedTotal != nil {
		t.Fatal("division by zero")
	}
	w = windows[0]
	if Estimate(w, total, 0, now.Add(16*time.Minute).UnixMilli()).EstimatedTotal != nil {
		t.Fatal("stale quota extrapolated")
	}
	signals["x-codex-primary-used-percent"] = "NaN"
	if len(Windows(signals, now)) != 0 {
		t.Fatal("nonfinite quota accepted")
	}
}
