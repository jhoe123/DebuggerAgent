package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestReportLatencyUnderThreshold asserts the FIXED behavior: /report?n=200
// responds well under 100ms. It FAILS on the seeded (buggy) source — buildReport's
// per-item sleep makes n=200 take ~600ms — and PASSES once the per-item blocking
// call is removed. The auto-remediation pipeline runs this as the perf deploy gate.
func TestReportLatencyUnderThreshold(t *testing.T) {
	w := httptest.NewRecorder()
	start := time.Now()
	reportHandler(w, httptest.NewRequest(http.MethodGet, "/report?n=200", nil))
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("/report?n=200: expected HTTP 200, got %d", w.Code)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("/report?n=200 took %v, want < 100ms (performance bug still present?)", elapsed)
	}
}
