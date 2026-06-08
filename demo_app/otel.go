package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// initObservability wires an OTLP/HTTP trace exporter from standard OTEL_* env vars.
// If no endpoint is configured it returns an error so the app runs without tracing
// (so `go run .`, tests, and the deploy gate all work with no Dynatrace creds).
//
// Scope is TRACES only — this demo deliberately exercises Dynatrace via distributed
// tracing, exceptions, and span-derived latency/error signals; it does not ship the
// metrics or logs SDKs.
func initObservability(ctx context.Context) (func(context.Context) error, error) {
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

// traceID returns the active span's trace id (or "" if there is no recording span),
// so handlers can surface it to the storefront and an operator can find the trace
// in Dynatrace.
func traceID(span trace.Span) string {
	sc := span.SpanContext()
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

// writeSceneJSON stamps the trace id on both the X-Trace-Id header and the JSON body,
// then writes the response. Header must be set before WriteHeader, so do it here.
func writeSceneJSON(w http.ResponseWriter, span trace.Span, status int, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	if tid := traceID(span); tid != "" {
		w.Header().Set("X-Trace-Id", tid)
		payload["traceId"] = tid
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
