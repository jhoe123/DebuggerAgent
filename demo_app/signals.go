package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// product is one catalog item in the storefront.
type product struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Category string  `json:"category"`
}

// catalog is the storefront's (tiny, in-memory) product list.
var catalog = []product{
	{ID: "sku-1", Name: "Aurora Running Shoes", Price: 89.00, Category: "footwear"},
	{ID: "sku-2", Name: "Trailhead Backpack", Price: 120.00, Category: "bags"},
	{ID: "sku-3", Name: "Nimbus Wireless Earbuds", Price: 59.50, Category: "audio"},
	{ID: "sku-4", Name: "Solstice Sunglasses", Price: 45.00, Category: "accessories"},
	{ID: "sku-5", Name: "Vertex Water Bottle", Price: 18.00, Category: "accessories"},
	{ID: "sku-6", Name: "Horizon Smart Watch", Price: 199.00, Category: "audio"},
}

// tiers/regions feed business-context span attributes so Dynatrace can facet by them.
var (
	userTiers = []string{"free", "pro", "enterprise"}
	regions   = []string{"us-east", "us-west", "eu-west", "ap-south"}
)

// businessAttrs returns user.tier / geo.region / cart.value, taken from the request
// when present or randomized otherwise, so spans carry realistic business context.
func businessAttrs(r *http.Request) []attribute.KeyValue {
	tier := r.URL.Query().Get("tier")
	if tier == "" {
		tier = userTiers[rand.Intn(len(userTiers))]
	}
	region := r.URL.Query().Get("region")
	if region == "" {
		region = regions[rand.Intn(len(regions))]
	}
	cart := 0.0
	for _, p := range catalog[:1+rand.Intn(len(catalog))] {
		cart += p.Price
	}
	return []attribute.KeyValue{
		attribute.String("user.tier", tier),
		attribute.String("geo.region", region),
		attribute.Float64("cart.value", cart),
	}
}

// catalogHandler is healthy baseline traffic: a fast product listing. Useful so the
// service has normal (non-error, low-latency) spans alongside the seeded problems.
func catalogHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/catalog")
	defer span.End()
	span.SetAttributes(attribute.Int("catalog.count", len(catalog)))
	writeSceneJSON(w, span, http.StatusOK, map[string]any{
		"products": catalog,
		"count":    len(catalog),
	})
}

// recommendHandler builds a multi-span distributed-trace waterfall: a parent span with
// sequential child spans (catalog.lookup -> inventory.check -> pricing.calc), each a
// small unit of work. It shows up in Dynatrace as a rich service-flow / span tree.
func recommendHandler(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "GET /api/recommend")
	defer span.End()
	span.SetAttributes(businessAttrs(r)...)

	picks := childStep(ctx, "catalog.lookup", 18*time.Millisecond, func() []product {
		return catalog[:3]
	})
	childStep(ctx, "inventory.check", 25*time.Millisecond, func() any {
		span.SetAttributes(attribute.Bool("inventory.in_stock", true))
		return nil
	})
	total := childStep(ctx, "pricing.calc", 15*time.Millisecond, func() float64 {
		sum := 0.0
		for _, p := range picks {
			sum += p.Price
		}
		return sum
	})

	span.SetAttributes(attribute.Int("recommend.count", len(picks)))
	writeSceneJSON(w, span, http.StatusOK, map[string]any{
		"recommended": picks,
		"bundlePrice": total,
	})
}

// childStep runs fn inside a named child span after a small simulated delay, so the
// parent trace shows a realistic waterfall. Generic over the work's return type.
func childStep[T any](ctx context.Context, name string, delay time.Duration, fn func() T) T {
	_, span := tracer.Start(ctx, name)
	defer span.End()
	time.Sleep(delay)
	return fn()
}

// searchHandler is a SLOW OPERATION scenario: response time scales with the result set
// and ~20% of calls hit a tail-latency branch, so /api/search surfaces as a high (and
// variable) p95 span in Dynatrace. One click (default n) is enough to make it slow.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/search")
	defer span.End()

	q := r.URL.Query().Get("q")
	if q == "" {
		q = "shoes"
	}
	n := parseIndex(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 40
	}
	if n > 200 {
		n = 200 // bound the work so a click can't wedge the process
	}
	span.SetAttributes(attribute.String("search.query", q), attribute.Int("search.n", n))

	// BUG: an unindexed scan does ~8ms of work per candidate row instead of a single
	// indexed lookup, so latency grows with n. The fix is to query an index in one pass.
	for i := 0; i < n; i++ {
		time.Sleep(8 * time.Millisecond)
	}
	tail := false
	if rand.Float64() < 0.20 {
		tail = true
		time.Sleep(400 * time.Millisecond) // tail-latency branch (cold cache)
	}

	results := len(catalog)
	span.SetAttributes(attribute.Int("search.results", results), attribute.Bool("search.tail_latency", tail))
	writeSceneJSON(w, span, http.StatusOK, map[string]any{
		"query":   q,
		"results": catalog,
		"count":   results,
	})
}

// payHandler is an EXTERNAL-DEPENDENCY FAILURE scenario. It records an outbound
// client-kind span for a call to a simulated payment gateway that fails ~30% of the
// time (502) and occasionally times out (~2s). Dynatrace shows the failing dependency
// edge in the service flow plus the error on the outbound span.
func payHandler(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "POST /api/pay")
	defer span.End()
	span.SetAttributes(businessAttrs(r)...)

	code, detail := callPaymentGateway(ctx)
	if code != http.StatusOK {
		span.SetStatus(codes.Error, detail)
		writeSceneJSON(w, span, http.StatusBadGateway, map[string]any{
			"ok":     false,
			"error":  detail,
			"gateway": "paygw.example.com",
		})
		return
	}
	writeSceneJSON(w, span, http.StatusOK, map[string]any{"ok": true, "authCode": "AUTH-" + strconv.Itoa(rand.Intn(1_000_000))})
}

// callPaymentGateway models an outbound HTTP call to a 3rd-party payment provider as a
// CLIENT span with standard dependency attributes. No real network call is made; the
// span (and its error) is what Dynatrace correlates into the service flow.
func callPaymentGateway(ctx context.Context) (int, string) {
	_, span := tracer.Start(ctx, "POST paygw.example.com/charge", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String("http.request.method", "POST"),
		attribute.String("server.address", "paygw.example.com"),
		attribute.String("url.full", "https://paygw.example.com/v1/charge"),
	)

	roll := rand.Float64()
	switch {
	case roll < 0.10:
		time.Sleep(2 * time.Second) // upstream timeout
		err := fmt.Errorf("payment gateway timed out after 2s")
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetAttributes(attribute.Int("http.response.status_code", 504))
		span.SetStatus(codes.Error, err.Error())
		return http.StatusGatewayTimeout, err.Error()
	case roll < 0.30:
		err := fmt.Errorf("payment gateway returned 502 Bad Gateway")
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetAttributes(attribute.Int("http.response.status_code", 502))
		span.SetStatus(codes.Error, err.Error())
		return http.StatusBadGateway, err.Error()
	default:
		time.Sleep(40 * time.Millisecond)
		span.SetAttributes(attribute.Int("http.response.status_code", 200))
		return http.StatusOK, ""
	}
}

// giftcardHandler is an EXCEPTION scenario distinct from the checkout panic: it parses a
// gift-card code, and on malformed input records the error (with stack trace) on the
// span and returns 400. The default storefront button sends an invalid code so it
// triggers in one click.
func giftcardHandler(w http.ResponseWriter, r *http.Request) {
	_, span := tracer.Start(r.Context(), "GET /api/giftcard")
	defer span.End()

	code := r.URL.Query().Get("code")
	span.SetAttributes(attribute.String("giftcard.code", code))

	amount, err := parseGiftcard(code)
	if err != nil {
		// BUG: codes from the new promo campaign don't match the expected GIFT-<amount>
		// format and were never validated, so redemption throws. Records an exception
		// (with stack trace) on the span -> exported to Dynatrace.
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		writeSceneJSON(w, span, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeSceneJSON(w, span, http.StatusOK, map[string]any{"ok": true, "credited": amount})
}

// parseGiftcard expects "GIFT-<amount>", e.g. GIFT-25. It returns a descriptive error
// for malformed codes (which the scenario relies on to surface an exception).
func parseGiftcard(code string) (int, error) {
	rest, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(code)), "GIFT-")
	if !ok {
		return 0, fmt.Errorf("malformed gift card code %q: expected GIFT-<amount>", code)
	}
	amount, err := strconv.Atoi(rest)
	if err != nil {
		return 0, fmt.Errorf("malformed gift card amount in %q: %w", code, err)
	}
	return amount, nil
}
