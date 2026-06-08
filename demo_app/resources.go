package main

import (
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// These scenarios stress host/process resources rather than emitting application
// errors. They are all BOUNDED (capped work, capped retention, resettable) so a demo
// click can never OOM or wedge the box the pipeline also runs on.

const (
	maxCPUMillis     = 5000 // /api/cpu busy-loop ceiling
	maxRetainedMB    = 256  // /api/mem total retention ceiling
	bytesPerMebibyte = 1 << 20
)

// cpuHandler is a CPU-SATURATION scenario: it busy-loops for the requested duration
// (default 1500ms, capped at 5s), driving process CPU up so Dynatrace shows a CPU
// spike on the host/process. One click is enough.
func cpuHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/cpu")
	defer span.End()

	ms := parseIndex(r.URL.Query().Get("ms"))
	if ms <= 0 {
		ms = 1500
	}
	if ms > maxCPUMillis {
		ms = maxCPUMillis
	}
	span.SetAttributes(attribute.Int("cpu.ms", ms))

	// BUG: the "promo engine" recomputes pricing in a hot busy-loop instead of caching
	// it, pegging a core. Bounded by the deadline so it can't run away.
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	var sink float64
	for time.Now().Before(deadline) {
		for i := 0; i < 1_000_000; i++ {
			sink += math.Sqrt(float64(i))
		}
	}
	span.SetAttributes(attribute.Float64("cpu.sink", sink))
	writeSceneJSON(w, span, http.StatusOK, map[string]any{"burnedMs": ms})
}

// retained holds chunks the /api/mem scenario "forgets" to free. Guarded by retainedMu.
var (
	retainedMu sync.Mutex
	retained   [][]byte
)

func retainedMB() int {
	retainedMu.Lock()
	defer retainedMu.Unlock()
	return len(retained)
}

// memHandler is a MEMORY-GROWTH scenario: each call appends `mb` MiB to a global slice
// that is never freed, so RSS climbs across clicks (visible in Dynatrace process
// memory). Total retention is capped at maxRetainedMB; `reset=true` frees it all so the
// demo is repeatable.
func memHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/mem")
	defer span.End()

	if r.URL.Query().Get("reset") == "true" {
		retainedMu.Lock()
		retained = nil
		retainedMu.Unlock()
		span.SetAttributes(attribute.Bool("mem.reset", true), attribute.Int("mem.retainedMB", 0))
		writeSceneJSON(w, span, http.StatusOK, map[string]any{"reset": true, "retainedMB": 0})
		return
	}

	mb := parseIndex(r.URL.Query().Get("mb"))
	if mb <= 0 {
		mb = 16
	}

	// BUG: the wishlist "cache" appends to a package-level slice and never evicts, so it
	// grows unbounded in real life. Here it's capped at maxRetainedMB as a safety net.
	retainedMu.Lock()
	for i := 0; i < mb && len(retained) < maxRetainedMB; i++ {
		chunk := make([]byte, bytesPerMebibyte)
		for j := range chunk {
			chunk[j] = byte(j) // touch pages so they're actually resident
		}
		retained = append(retained, chunk)
	}
	total := len(retained)
	retainedMu.Unlock()

	span.SetAttributes(attribute.Int("mem.addedMB", mb), attribute.Int("mem.retainedMB", total))
	writeSceneJSON(w, span, http.StatusOK, map[string]any{"addedMB": mb, "retainedMB": total, "capMB": maxRetainedMB})
}

// flakyHandler is an INTERMITTENT-FAILURE scenario: ~30% of calls fail with HTTP 500
// (recorded as a span error), the rest succeed. Repeated clicks produce a failure-rate
// spike in Dynatrace rather than a hard outage.
func flakyHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/flaky")
	defer span.End()

	// BUG: an unprotected shared resource intermittently rejects requests under
	// contention; ~30% fail. The fix is to make the access safe/retryable.
	if rand.Float64() < 0.30 {
		span.SetStatus(codes.Error, "add to cart failed (transient contention)")
		span.SetAttributes(attribute.Bool("cart.failed", true))
		writeSceneJSON(w, span, http.StatusInternalServerError, map[string]any{"ok": false, "error": "transient contention adding to cart"})
		return
	}
	writeSceneJSON(w, span, http.StatusOK, map[string]any{"ok": true})
}
