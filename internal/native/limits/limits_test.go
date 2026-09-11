package limits

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRollingAdmissionEditsAndPersistence(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "limits.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	m, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	g := gin.New()
	m.Register(g.Group("/limits"))
	put := func(body string) {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("PUT", "/limits/credential/a", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("save: %s", w.Body.String())
		}
	}
	ctx := context.Background()
	release, err := m.Acquire(ctx, "credential", "a")
	if err != nil {
		t.Fatal(err)
	}
	put(`{"rpm":2,"concurrency":1}`)
	if _, err = m.Acquire(ctx, "credential", "a"); err == nil {
		t.Fatal("new limit ignored an existing active request")
	}
	release()
	release()
	release, err = m.Acquire(ctx, "credential", "a")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = m.Acquire(ctx, "credential", "a"); err == nil {
		t.Fatal("rolling RPM not enforced")
	}
	var rejected *Rejected
	if !errors.As(err, &rejected) || rejected.Kind != "rpm" || rejected.Headers().Get("Retry-After") != "60" {
		t.Fatalf("bad rejection: %v", err)
	}
	now = now.Add(time.Minute)
	release, err = m.Acquire(ctx, "credential", "a")
	if err != nil {
		t.Fatal("60s boundary", err)
	}
	release()
	put(`{"rpm":0,"concurrency":3}`)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	hold := make(chan struct{})
	ready := make(chan struct{}, 30)
	for range 30 {
		wg.Go(func() {
			done, err := m.Acquire(ctx, "credential", "a")
			if err == nil {
				admitted.Add(1)
			}
			ready <- struct{}{}
			if err == nil {
				<-hold
				done()
			}
		})
	}
	for range 30 {
		<-ready
	}
	if admitted.Load() != 3 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	close(hold)
	wg.Wait()
	reopened, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.policies["credential:a"].Concurrency != 3 || len(reopened.buckets) != 0 {
		t.Fatal("policy must persist but runtime counters reset")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = reopened.Acquire(canceled, "credential", "a"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission")
	}
}
