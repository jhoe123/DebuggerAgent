# Runs the OTel-instrumented demo app, exporting traces/exceptions to Dynatrace.
# Reads DT_ENVIRONMENT + DT_API_TOKEN from .env, derives the OTLP endpoint, and
# starts the server on :9090. Then hit http://localhost:9090/checkout?index=99 to
# generate an exception the agent can investigate.
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

$env:OTEL_EXPORTER_OTLP_ENDPOINT = $endpoint
$env:OTEL_EXPORTER_OTLP_HEADERS  = "Authorization=Api-Token " + $cfg["DT_API_TOKEN"]
$env:OTEL_SERVICE_NAME           = "checkout-demo"

Write-Host "OTLP endpoint: $endpoint"
Write-Host "Starting demo_app on http://localhost:9090  (Ctrl+C to stop)"
Write-Host "Generate an exception:  curl `"http://localhost:9090/checkout?index=99`""
Push-Location (Join-Path $root "demo_app")
try { go run . } finally { Pop-Location }
