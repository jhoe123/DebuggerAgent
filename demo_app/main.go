// Command demo_app is the "ShopFlow" storefront — the stand-in "production" service the
// agent investigates. It is a small e-commerce app (Go API + a React/TS storefront the
// Go server serves) deliberately seeded with a variety of production issues that surface
// in Dynatrace via OpenTelemetry: exceptions with stack traces, slow operations, a
// failing external dependency, intermittent failures, and CPU/memory pressure.
//
// The original two seeded bugs live here and are unchanged so the agent's investigate →
// patch → test → deploy flow keeps working:
//   - /checkout  : unbounded slice index → panic (exception recorded on the span).
//   - /report    : per-item blocking call → high latency (slow operation).
// The remaining scenarios live in signals.go and resources.go and are reached under /api/*.
//
// Run (OTLP env configured — see scripts/run_demo.ps1):
//
//	OTEL_EXPORTER_OTLP_ENDPOINT=https://<env>.live.dynatrace.com/api/v2/otlp
//	OTEL_EXPORTER_OTLP_HEADERS="Authorization=Api-Token dt0c01...."
//	OTEL_SERVICE_NAME=checkout-demo
//	go run .
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("checkout-demo")

func main() {
	ctx := context.Background()
	shutdown, err := initObservability(ctx)
	if err != nil {
		log.Printf("tracing disabled: %v", err)
	} else {
		defer func() { _ = shutdown(context.Background()) }()
	}

	mux := http.NewServeMux()

	// Liveness + the two legacy seeded-bug routes the agent's democtl pipeline calls
	// directly (Trigger / Remediate / reachable). Keep these bare paths stable.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/checkout", checkoutHandler)
	mux.HandleFunc("/report", reportHandler)

	// Storefront API — each endpoint triggers a specific Dynatrace signal (see
	// signals.go / resources.go for the framing of each).
	mux.HandleFunc("GET /api/catalog", catalogHandler)
	mux.HandleFunc("GET /api/recommend", recommendHandler)
	mux.HandleFunc("GET /api/search", searchHandler)
	mux.HandleFunc("POST /api/pay", payHandler)
	mux.HandleFunc("GET /api/giftcard", giftcardHandler)
	mux.HandleFunc("GET /api/flaky", flakyHandler)
	mux.HandleFunc("GET /api/cpu", cpuHandler)
	mux.HandleFunc("GET /api/mem", memHandler)

	// Serve the built storefront SPA at "/" (registered last). Degrades gracefully when
	// the frontend isn't built so the API stays usable headless (e.g. for the pipeline).
	mux.Handle("/", storefrontHandler())

	addr := ":9090"
	log.Printf("demo_app (ShopFlow storefront, OTel-instrumented) listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func checkoutHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /checkout")
	defer span.End()
	defer func() {
		if rec := recover(); rec != nil {
			err := fmt.Errorf("%v", rec)
			// Records an exception event (with stack trace) on the span → exported to Dynatrace.
			span.RecordError(err, trace.WithStackTrace(true))
			span.SetStatus(codes.Error, err.Error())
			log.Printf("checkout panic: %v\n%s", rec, debug.Stack())
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}()

	items := []string{"apple", "banana", "cherry"}
	idx := parseIndex(r.URL.Query().Get("index"))
	span.SetAttributes(attribute.Int("checkout.index", idx))
	// BUG: no bounds check — e.g. /checkout?index=5 panics with index out of range.
	selected := items[idx]
	fmt.Fprintf(w, "checked out: %s\n", selected)
}

func parseIndex(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}

// reportHandler builds a summary report. It is OTel-instrumented with its own span
// ("GET /report") so its duration is queryable in Dynatrace. It has a deliberately
// seeded PERFORMANCE bug: buildReport is slow (see below), so /report shows up as a
// high-latency operation the agent can detect, explain, and optimize.
func reportHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /report")
	defer span.End()

	n := parseIndex(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 200
	}
	span.SetAttributes(attribute.Int("report.n", n))
	total := buildReport(n)
	fmt.Fprintf(w, "report: %d rows, checksum %d\n", n, total)
}

// buildReport aggregates n rows.
//
// PERF BUG: it does a per-item blocking call (simulating an unbatched/N+1 lookup),
// so latency scales linearly with n and dominates /report's response time. The fix
// is to drop the per-item sleep and aggregate in a single in-memory pass.
func buildReport(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		time.Sleep(3 * time.Millisecond) // BUG: per-item blocking I/O — batch this instead.
		total += i
	}
	return total
}

// storefrontHandler serves the built React storefront from DEMO_WEB_DIR (default
// "web/dist", resolved relative to the working dir). If the frontend isn't built it
// returns a helpful message at "/" instead of 500, so the API and /healthz stay usable
// headless (the remediation pipeline never builds the frontend).
func storefrontHandler() http.Handler {
	dir := os.Getenv("DEMO_WEB_DIR")
	if dir == "" {
		dir = "web/dist"
	}
	if _, err := os.Stat(dir + "/index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "ShopFlow storefront UI is not built.\n"+
				"Build it with: npm --prefix web install && npm --prefix web run build\n"+
				"The API is live: try /api/catalog or /healthz.\n")
		})
	}
	return spaFileServer(dir)
}

// spaFileServer serves static files and falls back to index.html for client routes.
// Mirrors the agent backend's handler: API paths must 404 (not fall back to the SPA).
func spaFileServer(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(dir + r.URL.Path); err != nil && !strings.HasPrefix(r.URL.Path, "/assets") {
			http.ServeFile(w, r, dir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	})
}
