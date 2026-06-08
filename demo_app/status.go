package main

// status.go implements a behavioral self-check the storefront uses to show whether the
// *running binary* still has the seeded bugs or has been patched. It exercises the two
// canonical patchable bugs in-process and reports a verdict — so a tester can tell at a
// glance that a patch is actually live (the storefront otherwise looks identical).
//
// This file is intentionally separate from main.go: the agent's remediation pipeline
// overwrites demo_app/main.go (and ResetSource does `git checkout -- demo_app/main.go`),
// but never touches status.go, so the self-check survives both apply and reset.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

// bugStatus is the per-bug self-check result surfaced to the storefront. It carries only
// the bug id (matching a storefront scenario), the fixed flag, and a runtime detail — the
// human-readable label is resolved on the client from its scenario registry, so labels
// aren't duplicated/hardcoded here.
type bugStatus struct {
	ID     string `json:"id"`    // matches the storefront scenario id (checkout / report)
	Fixed  bool   `json:"fixed"` // true once the running binary no longer exhibits the bug
	Detail string `json:"detail"`
}

// buildVerdict is the overall self-check payload returned by GET /api/status. App is the
// demo app's display name (DEMO_APP_NAME) so the storefront brand isn't hardcoded either.
type buildVerdict struct {
	App     string      `json:"app"`
	Verdict string      `json:"verdict"` // "buggy" | "partially-patched" | "patched"
	Bugs    []bugStatus `json:"bugs"`
}

// appDisplayName is the configurable demo-app name (DEMO_APP_NAME), shown as the storefront
// brand. Defaults to "ShopFlow" so the standalone demo keeps its identity when unset.
func appDisplayName() string {
	if v := strings.TrimSpace(os.Getenv("DEMO_APP_NAME")); v != "" {
		return v
	}
	return "ShopFlow"
}

// statusHandler runs the self-check and returns the verdict as JSON. It deliberately does
// NOT use writeSceneJSON (which stamps X-Trace-Id from a span) — this is a meta endpoint,
// not storefront traffic, so it stays out of the Dynatrace service flow.
func statusHandler(w http.ResponseWriter, r *http.Request) {
	bugs := []bugStatus{probeCheckout(), probeReport()}

	fixedCount := 0
	for _, b := range bugs {
		if b.Fixed {
			fixedCount++
		}
	}
	verdict := "partially-patched"
	switch fixedCount {
	case 0:
		verdict = "buggy"
	case len(bugs):
		verdict = "patched"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildVerdict{App: appDisplayName(), Verdict: verdict, Bugs: bugs})
}

// probeCheckout calls the real checkoutHandler with the out-of-range index the storefront
// uses. The buggy code panics (recovered → HTTP 500); the patched code bounds-checks and
// returns 400. The panic IS the bug, so we must drive the actual handler — this mirrors
// checkout_test.go's discriminator exactly, so the banner can't disagree with the gate.
func probeCheckout() bugStatus {
	rec := httptest.NewRecorder()
	checkoutHandler(rec, httptest.NewRequest(http.MethodGet, "/checkout?index=99", nil))
	fixed := rec.Code == http.StatusBadRequest
	detail := fmt.Sprintf("HTTP %d on out-of-range checkout", rec.Code)
	if fixed {
		detail = fmt.Sprintf("HTTP %d — out-of-range checkout handled", rec.Code)
	}
	return bugStatus{ID: "checkout", Fixed: fixed, Detail: detail}
}

// probeReport times buildReport directly (not reportHandler) so the perf probe emits no
// span. The buggy build sleeps 3ms/item (~120ms for 40 rows); the patched build drops the
// per-item I/O (<5ms). The < 1ms/item threshold matches report_test.go's gate semantics.
func probeReport() bugStatus {
	const rows = 40
	start := time.Now()
	buildReport(rows)
	elapsed := time.Since(start)
	fixed := elapsed < rows*time.Millisecond
	return bugStatus{
		ID:     "report",
		Fixed:  fixed,
		Detail: fmt.Sprintf("%d rows in %dms", rows, elapsed.Milliseconds()),
	}
}
