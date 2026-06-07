// Command demo_app is the stand-in "production" service the agent investigates.
//
// /checkout has a deliberately seeded bug (unbounded slice index). It is
// instrumented with OpenTelemetry: when the bug panics, the handler records an
// exception (with stack trace) on the active span and exports it to Dynatrace via
// OTLP. The DebuggerAgent then finds that exception and correlates it to this file.
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
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("checkout-demo")

func main() {
	ctx := context.Background()
	shutdown, err := initTracer(ctx)
	if err != nil {
		log.Printf("tracing disabled: %v", err)
	} else {
		defer func() { _ = shutdown(context.Background()) }()
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/checkout", checkoutHandler)
	http.HandleFunc("/report", reportHandler)

	addr := ":9090"
	log.Printf("demo_app (buggy, OTel-instrumented) listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// initTracer wires an OTLP/HTTP exporter from standard OTEL_* env vars. If no
// endpoint is configured, it returns an error so the app runs without tracing.
func initTracer(ctx context.Context) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT not set")
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	svc := os.Getenv("OTEL_SERVICE_NAME")
	if svc == "" {
		svc = "checkout-demo"
	}
	// Schemaless avoids a schema-URL conflict with resource.Default().
	res, err := resource.Merge(resource.Default(),
		resource.NewSchemaless(semconv.ServiceName(svc)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	log.Printf("OTel tracing enabled (service=%s)", svc)
	return tp.Shutdown, nil
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
