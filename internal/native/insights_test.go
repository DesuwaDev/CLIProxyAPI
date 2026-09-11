package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/history"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestNativeExactAnalyticsAndStableSlowPagination(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	at := time.Now().Add(-time.Minute)
	publish := func(ms int, failed bool, stream bool, ttft time.Duration) {
		r.HandleUsage(context.Background(), usage.Record{Provider: "openai", Model: "model", RequestedAt: at, Latency: time.Duration(ms) * time.Millisecond, Stream: stream, TTFT: ttft, Failed: failed, Detail: usage.Detail{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}})
	}
	for _, ms := range []int{100, 200, 200, 400, 1000} {
		publish(ms, false, true, 10*time.Millisecond)
	}
	publish(0, false, false, 0)
	publish(9000, true, true, time.Second)
	w := request(t, g, "GET", "history/analytics", "")
	requireOK(t, w)
	var a struct {
		Duration history.Percentiles `json:"duration"`
		TTFT     history.Percentiles `json:"ttft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Duration.Samples != 5 || a.TTFT.Samples != 5 || a.Duration.P50 == nil || *a.Duration.P50 != 200 || *a.Duration.P95 != 1000 || *a.TTFT.P99 != 10 {
		t.Fatalf("unknown/failed samples leaked into percentiles: %s", w.Body.String())
	}
	path := "history/events?sort=latency&limit=2&failed=false&min_latency=100"
	read := func(cursor string) ([]history.Row, string) {
		w := request(t, g, "GET", path+"&cursor="+url.QueryEscape(cursor), "")
		requireOK(t, w)
		var result struct {
			Events []history.Row `json:"events"`
			Cursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Events, result.Cursor
	}
	rows, cursor := read("")
	if len(rows) != 2 || rows[0].LatencyMS != 1000 || rows[1].LatencyMS != 400 || cursor == "" {
		t.Fatal("incorrect first page")
	}
	publish(150, false, false, 0)
	seen := map[string]bool{}
	for _, e := range rows {
		seen[e.ID] = true
	}
	for cursor != "" {
		rows, cursor = read(cursor)
		for _, e := range rows {
			if seen[e.ID] || e.LatencyMS == 150 {
				t.Fatal("duplicate or later insertion crossed snapshot")
			}
			seen[e.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("lost tied rows: %d", len(seen))
	}
	w = request(t, g, "GET", fmt.Sprintf("history/analytics?from=%d&to=%d", at.Add(-2*time.Hour).UnixMilli(), at.Add(-time.Hour).UnixMilli()), "")
	requireOK(t, w)
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Duration.Samples != 0 || a.Duration.P50 != nil || a.TTFT.P99 != nil {
		t.Fatal("empty percentile must be null")
	}
}

func TestNativeRecentPaginationUsesRequestTimeAndSnapshot(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	at := time.Now().Add(-time.Hour)
	publish := func(offset int) {
		r.HandleUsage(context.Background(), usage.Record{Provider: "openai", Model: "chronology", RequestedAt: at.Add(time.Duration(offset) * time.Minute), Detail: usage.Detail{InputTokens: 1, TotalTokens: 1}})
	}
	for _, offset := range []int{4, 2, 4, 1, 3} {
		publish(offset)
	}
	read := func(cursor string) ([]history.Row, string) {
		w := request(t, g, "GET", "history/events?sort=recent&limit=2&cursor="+url.QueryEscape(cursor), "")
		requireOK(t, w)
		var result struct {
			Events []history.Row `json:"events"`
			Cursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Events, result.Cursor
	}
	rows, cursor := read("")
	if len(rows) != 2 || rows[0].TimestampMS != at.Add(4*time.Minute).UnixMilli() || rows[1].TimestampMS != rows[0].TimestampMS || rows[0].Sequence <= rows[1].Sequence {
		t.Fatal("request-time ordering or tie break failed")
	}
	publish(0)
	seen := map[string]bool{}
	lastTime, lastSeq := int64(1<<62), int64(1<<62)
	count := 0
	for {
		for _, row := range rows {
			if seen[row.ID] || row.TimestampMS > lastTime || (row.TimestampMS == lastTime && row.Sequence >= lastSeq) {
				t.Fatal("duplicate or unordered request")
			}
			if row.TimestampMS == at.UnixMilli() {
				t.Fatal("late insertion crossed snapshot")
			}
			seen[row.ID] = true
			lastTime, lastSeq = row.TimestampMS, row.Sequence
			count++
		}
		if cursor == "" {
			break
		}
		rows, cursor = read(cursor)
	}
	if count != 5 {
		t.Fatalf("lost requests: %d", count)
	}
}
