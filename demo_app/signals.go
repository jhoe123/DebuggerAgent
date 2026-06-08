package main

import (
	"context"
	"math/rand"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
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
