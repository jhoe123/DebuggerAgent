package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These assert the FIXED behavior. They FAIL on the seeded (buggy) source — an
// out-of-range index panics and yields HTTP 500 — and PASS once the bounds-check
// patch is applied. The auto-remediation pipeline runs them as the deploy gate.

func TestCheckoutOutOfRangeReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	checkoutHandler(w, httptest.NewRequest(http.MethodGet, "/checkout?index=99", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("index=99: expected HTTP 400 (bounds-checked), got %d", w.Code)
	}
}

func TestCheckoutValidReturns200(t *testing.T) {
	w := httptest.NewRecorder()
	checkoutHandler(w, httptest.NewRequest(http.MethodGet, "/checkout?index=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("index=1: expected HTTP 200, got %d", w.Code)
	}
}
