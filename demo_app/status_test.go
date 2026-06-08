package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatusHandlerConsistent verifies the self-check returns a well-formed payload whose
// overall verdict agrees with the per-bug flags. It is deliberately patch-state-agnostic
// (it does not assert "buggy" vs "patched"), so it passes on both the seeded-bug source
// and the patched source — unlike the checkout/report gates, which assert a specific state.
func TestStatusHandlerConsistent(t *testing.T) {
	rec := httptest.NewRecorder()
	statusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got HTTP %d, want 200", rec.Code)
	}

	var v buildVerdict
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode /api/status: %v (body=%q)", err, rec.Body.String())
	}

	if v.App == "" {
		t.Errorf("expected a non-empty app display name")
	}

	ids := map[string]bool{}
	fixed := 0
	for _, b := range v.Bugs {
		ids[b.ID] = true
		if b.Fixed {
			fixed++
		}
		if b.Detail == "" {
			t.Errorf("bug %q missing detail: %+v", b.ID, b)
		}
	}
	if !ids["checkout"] || !ids["report"] {
		t.Errorf("expected checkout+report bugs, got ids %v", ids)
	}

	want := "partially-patched"
	switch fixed {
	case 0:
		want = "buggy"
	case len(v.Bugs):
		want = "patched"
	}
	if v.Verdict != want {
		t.Errorf("verdict %q inconsistent with %d/%d fixed (want %q)", v.Verdict, fixed, len(v.Bugs), want)
	}
}
