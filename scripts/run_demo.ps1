# Runs the OTel-instrumented ShopFlow demo app, exporting traces/exceptions to Dynatrace.
# Reads DT_ENVIRONMENT + DT_API_TOKEN from .env, derives the OTLP endpoint, builds the
# React storefront (so the Go server can serve it), and starts the server on :9090.
# Open http://localhost:9090/ for the storefront, then click the labeled actions to
# generate problems the agent can investigate. Use -SkipWeb to skip the frontend build
# (e.g. when iterating on the Go API only).
param([switch]$SkipWeb)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$cfg = @{}
Get-Content (Join-Path $root ".env") | ForEach-Object {
  if ($_ -match '^\s*([A-Z0-9_]+)\s*=\s*(.*)$') { $cfg[$matches[1]] = $matches[2].Trim() }
}
if (-not $cfg["DT_API_TOKEN"]) {
  throw "DT_API_TOKEN not set in .env. Create a Dynatrace API token (dt0c01...) with the 'Ingest OpenTelemetry traces' (openTelemetryTrace.ingest) scope."
}
# apps.dynatrace.com (platform UI) -> live.dynatrace.com (ingest)
$tenant = $cfg["DT_ENVIRONMENT"] -replace '\.apps\.', '.live.'
$endpoint = "$tenant/api/v2/otlp"

# Build the storefront so demo_app serves it at "/". Skippable for API-only iteration.
$webDir = Join-Path $root "demo_app/web"
if (-not $SkipWeb -and (Test-Path (Join-Path $webDir "package.json"))) {
  Push-Location $webDir
  try {
    if (-not (Test-Path "node_modules")) {
      Write-Host "Installing storefront deps (npm install)..."
      npm install
    }
    Write-Host "Building storefront (npm run build)..."
    npm run build
  } finally { Pop-Location }
}

$env:OTEL_EXPORTER_OTLP_ENDPOINT = $endpoint
$env:OTEL_EXPORTER_OTLP_HEADERS  = "Authorization=Api-Token " + $cfg["DT_API_TOKEN"]
$env:OTEL_SERVICE_NAME           = "checkout-demo"

Write-Host "OTLP endpoint: $endpoint"
Write-Host "Storefront:    http://localhost:9090/      (click the labeled actions)"
Write-Host "API:           http://localhost:9090/api/* (e.g. /api/catalog)"
Write-Host "Starting demo_app on :9090  (Ctrl+C to stop)"
Push-Location (Join-Path $root "demo_app")
try { go run . } finally { Pop-Location }
