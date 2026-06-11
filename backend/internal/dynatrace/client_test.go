package dynatrace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func errRow(svc, span, msg, count, latest string) map[string]any {
	return map[string]any{
		"service.name": svc, "span.name": span, "span.status_message": msg,
		"count": count, "latest": latest,
	}
}

func perfRow(svc, span string, p95ns float64, count string) map[string]any {
	return map[string]any{
		"service.name": svc, "span.name": span, "p95": p95ns, "count": count,
	}
}

func TestAggregateErrorRows(t *testing.T) {
	// Two rows for checkout (newest first, as Grail sorts latest desc) + one for cart.
	recs := []map[string]any{
		errRow("checkout", "POST /pay", "card declined", "7", "2026-06-11T10:00:00Z"),
		errRow("checkout", "GET /quote", "timeout", "3", "2026-06-11T09:00:00Z"),
		errRow("cart", "POST /add", "nil pointer", "2", "2026-06-11T08:00:00Z"),
	}
	got := aggregateErrorRows(recs, "https://env")
	if len(got) != 2 {
		t.Fatalf("want 2 problems, got %d", len(got))
	}
	co := got[0]
	if co.ID != "error:checkout" {
		t.Fatalf("want first problem error:checkout, got %s", co.ID)
	}
	if co.Occurrences != 10 || co.AffectedUsers != 10 {
		t.Errorf("counts should sum across rows: got occurrences=%d users=%d", co.Occurrences, co.AffectedUsers)
	}
	// Newest row supplies title/entity/timestamp.
	if co.Title != "card declined" || !strings.Contains(co.Entity, "POST /pay") || co.StartedAt != "2026-06-11T10:00:00Z" {
		t.Errorf("newest row should supply title/entity/startedAt: %+v", co)
	}
	if got[1].ID != "error:cart" || got[1].Occurrences != 2 {
		t.Errorf("unexpected second problem: %+v", got[1])
	}
}

func TestAggregatePerfRows(t *testing.T) {
	recs := []map[string]any{
		perfRow("search", "GET /search", 900e6, "5"), // 900ms — slowest, supplies title
		perfRow("search", "GET /suggest", 600e6, "4"), // 600ms — qualifies, count sums
		perfRow("search", "GET /ping", 100e6, "99"),   // below threshold — excluded entirely
		perfRow("cart", "GET /cart", 300e6, "8"),      // below threshold — no problem at all
	}
	got := aggregatePerfRows(recs, "https://env")
	if len(got) != 1 {
		t.Fatalf("want 1 problem, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.ID != "performance:search" {
		t.Fatalf("want performance:search, got %s", p.ID)
	}
	if p.Occurrences != 9 {
		t.Errorf("only qualifying rows should sum: want 9, got %d", p.Occurrences)
	}
	if p.Title != "Slow operation: GET /search" || p.Metric != "p95 900 ms" {
		t.Errorf("slowest row should supply title/metric: %+v", p)
	}
}

// fakeExec returns canned records per query kind (matched on the DQL filter) and
// counts calls so cache behavior is observable.
type fakeExec struct {
	calls    int
	errRecs  []map[string]any
	perfRecs []map[string]any
	errErr   error // error for the error-span query
	perfErr  error // error for the perf query
}

func (f *fakeExec) run(_ context.Context, dql string) ([]map[string]any, int64, error) {
	f.calls++
	if strings.Contains(dql, `span.status_code == "error"`) {
		return f.errRecs, 100, f.errErr
	}
	return f.perfRecs, 50, f.perfErr
}

func newTestClient(f *fakeExec) *Client {
	c := &Client{env: "https://env"}
	c.exec = f.run
	return c
}

func TestListProblemsCacheHit(t *testing.T) {
	f := &fakeExec{errRecs: []map[string]any{errRow("svc", "op", "boom", "1", "t1")}}
	c := newTestClient(f)
	ctx := context.Background()

	first, err := c.ListProblems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("first call should run both queries, got %d calls", f.calls)
	}
	second, err := c.ListProblems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Errorf("within TTL no new queries should run, got %d calls", f.calls)
	}
	if len(first) != 1 || len(second) != 1 || first[0].ID != second[0].ID {
		t.Errorf("cached result should match: %+v vs %+v", first, second)
	}
	// Returned slice is a copy — mutating it must not poison the cache.
	second[0].Title = "mutated"
	third, _ := c.ListProblems(ctx)
	if third[0].Title == "mutated" {
		t.Error("cache should return copies, not the shared slice")
	}
}

func TestListProblemsServesStaleOnError(t *testing.T) {
	f := &fakeExec{errRecs: []map[string]any{errRow("svc", "op", "boom", "1", "t1")}}
	c := newTestClient(f)
	ctx := context.Background()

	if _, err := c.ListProblems(ctx); err != nil {
		t.Fatal(err)
	}
	// Expire the TTL and make the error query fail: stale snapshot is served.
	c.cachedAt = time.Now().Add(-cacheTTL - time.Second)
	f.errErr = errors.New("grail down")
	got, err := c.ListProblems(ctx)
	if err != nil {
		t.Fatalf("stale snapshot should be served on transient failure: %v", err)
	}
	if len(got) != 1 || got[0].ID != "error:svc" {
		t.Errorf("unexpected stale result: %+v", got)
	}
	// Past maxStale the error propagates.
	c.cachedAt = time.Now().Add(-maxStale - time.Second)
	if _, err := c.ListProblems(ctx); err == nil {
		t.Error("error should propagate once the snapshot is too stale")
	}
}

func TestListProblemsKeepsLastPerfOnPerfError(t *testing.T) {
	f := &fakeExec{
		errRecs:  []map[string]any{errRow("svc", "op", "boom", "1", "t1")},
		perfRecs: []map[string]any{perfRow("slowsvc", "GET /x", 800e6, "3")},
	}
	c := newTestClient(f)
	ctx := context.Background()

	first, err := c.ListProblems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("want error+perf problems, got %+v", first)
	}
	// Perf query starts failing: last perf set is spliced in rather than vanishing.
	c.cachedAt = time.Now().Add(-cacheTTL - time.Second)
	f.perfErr = errors.New("percentile unsupported")
	got, err := c.ListProblems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hasPerf := false
	for _, p := range got {
		if p.ID == "performance:slowsvc" {
			hasPerf = true
		}
	}
	if !hasPerf {
		t.Errorf("perf problems should persist through perf-query failures: %+v", got)
	}
}

func TestListProblemsDeterministicOrder(t *testing.T) {
	f := &fakeExec{
		errRecs: []map[string]any{
			errRow("zeta", "op", "z", "1", "t1"),
			errRow("alpha", "op", "a", "1", "t1"),
		},
		perfRecs: []map[string]any{perfRow("mid", "GET /m", 700e6, "2")},
	}
	c := newTestClient(f)
	got, err := c.ListProblems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"error:alpha", "error:zeta", "performance:mid"}
	if len(got) != len(want) {
		t.Fatalf("want %d problems, got %+v", len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].ID)
		}
	}
}
