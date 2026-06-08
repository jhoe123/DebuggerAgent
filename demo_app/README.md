# ShopFlow — demo storefront

ShopFlow is the stand-in "production" service the agent investigates. It is a small
e-commerce app — a Go API (`:9090`) that also serves a React/TypeScript storefront — and
it is **deliberately seeded with a variety of production issues** that surface in
Dynatrace via OpenTelemetry. Each storefront action either is healthy traffic or, with a
single click, triggers one specific problem.

The Go service reports to Dynatrace as **`checkout-demo`** (OTLP/HTTP traces). Tracing is
optional: with no `OTEL_*` env set the app still runs (logs `tracing disabled`), so the
API, tests, and the agent's build/deploy gate all work without Dynatrace credentials.

## Scenarios

Every action returns the active span's trace id (response header `X-Trace-Id` and, for
JSON responses, a `traceId` field) so you can find the exact trace in Dynatrace.

| Action (storefront) | Endpoint | Signal in Dynatrace | Issue? |
|---|---|---|---|
| Browse products | `GET /api/catalog` | healthy baseline span | no |
| Recommended for you | `GET /api/recommend` | multi-span distributed trace (`catalog.lookup`→`inventory.check`→`pricing.calc`) | no |
| Search catalog | `GET /api/search?q=…` | slow operation / high & variable p95 | **yes** |
| Place order | `GET /checkout?index=99` | exception + stack trace (index-out-of-range panic → 500) | **yes** |
| Sales report | `GET /report?n=200` | slow operation (~650ms p95) | **yes** |
| Pay now | `POST /api/pay` | failing **external dependency** (payment gateway 502 ~30% / 2s timeout) | **yes** |
| Apply gift card | `GET /api/giftcard?code=PROMO2026` | exception (malformed code → recorded error → 400) | **yes** |
| Add to cart | `GET /api/flaky` | intermittent failure-rate spike (~30% 500) | **yes** |
| Run promo engine | `GET /api/cpu?ms=1500` | CPU spike (bounded ≤5000ms) | **yes** |
| Build wishlist cache | `GET /api/mem?mb=16` | memory growth (bounded ≤256MB; `?reset=true` frees it) | **yes** |

`/healthz`, `/checkout`, and `/report` are kept as bare paths because the agent's
remediation pipeline calls them directly. The two original seeded bugs (`/checkout`,
`/report`) are unchanged so the investigate → patch → test → deploy flow keeps working.

## Run

### Integrated (storefront + API + Dynatrace)
From the repo root, with `.env` configured (`DT_ENVIRONMENT`, `DT_API_TOKEN`):

```pwsh
pwsh scripts/run_demo.ps1        # builds the storefront, sets OTLP env, runs :9090
pwsh scripts/run_demo.ps1 -SkipWeb   # skip the frontend build (API-only iteration)
```

Then open <http://localhost:9090/> and click the labeled actions.

### Frontend dev loop (fast inner loop)
Run the API and the Vite dev server separately:

```pwsh
# terminal A — API on :9090 (no frontend build needed)
cd demo_app; go run .

# terminal B — storefront on :5174, proxies /api, /checkout, /report -> :9090
npm --prefix demo_app/web install   # first time
npm --prefix demo_app/web run dev
```

## Notes
- **Static serving:** the Go server serves the built SPA from `DEMO_WEB_DIR` (default
  `web/dist`). If it isn't built, `/` shows a short hint and the API stays live — so the
  build/deploy gate never depends on the frontend being built.
- **Build artifacts** (`web/node_modules`, `web/dist`) are gitignored.
- **No new Go modules:** all scenarios use the standard library plus the OpenTelemetry
  packages already in `go.mod`.
