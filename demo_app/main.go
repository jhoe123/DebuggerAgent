// Command demo_app is the stand-in "production" service the agent investigates.
//
// It exposes a deliberately buggy endpoint so that, once instrumented with
// Dynatrace (OneAgent or OpenTelemetry), hitting it produces a real Problem with
// a stack trace the agent can correlate back to this source (see TASKS.md T3).
//
// Keep this app TINY so stack-trace → file/line correlation stays reliable.
package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// /checkout has the seeded bug: it indexes a slice using an unvalidated
	// query parameter count, causing an index-out-of-range panic for count>=len(items).
	http.HandleFunc("/checkout", checkoutHandler)

	addr := ":9090"
	log.Printf("demo_app (buggy) listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func checkoutHandler(w http.ResponseWriter, r *http.Request) {
	items := []string{"apple", "banana", "cherry"}
	// BUG (T3): no bounds check — e.g. /checkout?index=5 panics with index out of range.
	idx := parseIndex(r.URL.Query().Get("index"))
	selected := items[idx]
	fmt.Fprintf(w, "checked out: %s\n", selected)
}

func parseIndex(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
