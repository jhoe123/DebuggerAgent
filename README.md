# DebuggerAgent

> AI Root-Cause Investigator for the **Google Cloud Rapid Agent Hackathon** — **Dynatrace track**.
> Captures a production issue from **Dynatrace**, uses **Gemini 3** to correlate it to source
> code and explain the root cause, and **proposes a human-gated patch** (no auto-merge/deploy).

- **Context:** [PROJECT.md](PROJECT.md) · **Progress/tasks:** [TASKS.md](TASKS.md)
- **Stack:** React + TypeScript (frontend) · Go + ADK Go (backend agent, Gemini 3) ·
  Dynatrace MCP server · Cloud Run.

## How it works

```
Dynatrace problem ──MCP──▶ Go/ADK agent (Gemini 3) ──▶ root-cause summary + proposed diff
                                                   └─▶ developer Approves ─▶ patch to branch/file
```

## Prerequisites

- Go 1.24+, Node 20+, `git`
- A **Google Cloud** project with billing + Vertex AI / Gemini Enterprise Agent Platform enabled
- A **Dynatrace** tenant (15-day free trial is enough)
- `gcloud` CLI; (optional) `gh` CLI for pushing the repo

## Setup

1. **Clone & configure env**
   ```bash
   cp .env.example .env
   # fill in GCP + Dynatrace values (see comments in .env.example)
   ```

2. **Google Cloud / Gemini** (APIs `aiplatform` + `run` already enabled on `emogent-demo-2026`)
   ```bash
   gcloud config set project emogent-demo-2026
   gcloud auth application-default login           # opens a browser — run in your own terminal
   gcloud auth application-default set-quota-project emogent-demo-2026
   ```
   Confirmed model: **`gemini-3.1-pro-preview`**, served from the **`global`** location
   (not `us-central1`). Already set in `.env`.

3. **Dynatrace**
   - Sign up for the free SaaS trial at <https://www.dynatrace.com/trial/>; note your tenant
     URL, e.g. `https://abc12345.apps.dynatrace.com` → `DT_ENVIRONMENT`.
   - Create a **Platform token** (Dynatrace → **Settings → Access Tokens / Platform tokens →
     Generate new token**) and put it in `DT_PLATFORM_TOKEN`. Recommended scopes for this
     project (grant all to avoid tool failures):
     ```
     app-engine:apps:run
     storage:buckets:read   storage:logs:read     storage:metrics:read
     storage:spans:read     storage:events:read   storage:entities:read
     storage:bizevents:read storage:system:read   storage:smartscape:read
     davis-copilot:nl2dql:execute   davis-copilot:dql2nl:execute
     davis:analyzers:read           davis:analyzers:execute
     ```
   - Verify the MCP server can reach your tenant (Node already trusts the Avast CA via
     `NODE_EXTRA_CA_CERTS`):
     ```bash
     DT_ENVIRONMENT=... DT_PLATFORM_TOKEN=... npx -y @dynatrace-oss/dynatrace-mcp-server@latest
     ```

4. **Seed a production problem (T3)** — emit a real exception from `demo_app` to Dynatrace via
   OpenTelemetry:
   - Create a Dynatrace **API token** (Settings → Access Tokens → **API tokens** → Generate;
     token starts `dt0c01...`) with the **Ingest OpenTelemetry traces** (`openTelemetryTrace.ingest`)
     scope. Put it in `DT_API_TOKEN` in `.env`.
   - Run the instrumented demo app (derives the OTLP endpoint from `DT_ENVIRONMENT`):
     ```powershell
     pwsh scripts/run_demo.ps1
     ```
   - In another shell, trigger the bug a few times:
     ```powershell
     1..5 | % { curl "http://localhost:9090/checkout?index=99" }
     ```
   - Within ~1–2 min the exception (with stack trace referencing `demo_app/main.go`) is queryable
     in Dynatrace; the agent finds it via `list_exceptions` / `execute_dql`.

## Run (local)

```bash
# backend (serves API on :8080)
cd backend && go run ./cmd/server

# frontend (Vite dev server)
cd frontend && npm install && npm run dev
```

Open the frontend, pick the latest Dynatrace problem, let the agent investigate, review the
root-cause summary and proposed diff, then **Approve** to write the patch to a branch/file.

## Deploy (Cloud Run)

```bash
gcloud run deploy debugger-agent --source . --region "$GOOGLE_CLOUD_LOCATION" --allow-unauthenticated
```
Set the env vars (`DT_*`, `GEMINI_MODEL`, etc.) on the service; use Secret Manager for tokens.

## For hackathon judges

- **Hosted URL:** _(add after T9 deploy)_
- **Test flow:** open the URL → select the seeded problem → click **Investigate** → review the
  AI root-cause summary + proposed patch → click **Approve** to see the diff written (the agent
  never merges or deploys).
- No login required (or use the provided test credentials, if any).

## Pushing to GitHub (manual — `gh` not installed in this environment)

```bash
git remote add origin https://github.com/<you>/DebuggerAgent.git
git push -u origin main
```
Ensure the repo is **public** and the `LICENSE` file is present (both required by the rules).

## Troubleshooting

**`CERTIFICATE_VERIFY_FAILED` / `unable to get local issuer certificate` from gcloud or npx.**
Avast Antivirus' Web/Mail Shield does TLS interception (re-signs HTTPS with its own root CA).
gcloud and Node use their own CA bundles and don't see the Windows store. Fix (already applied
on this machine): export the Windows root store and point both tools at it.
```powershell
$pem = Join-Path $HOME ".gcloud-ca\win-roots.pem"
$certs = Get-ChildItem Cert:\LocalMachine\Root, Cert:\CurrentUser\Root
# ...write each cert as a PEM block to $pem...
gcloud config set core/custom_ca_certs_file "$pem"
setx NODE_EXTRA_CA_CERTS "$pem"
```
Alternative: disable Avast's HTTPS scanning (Menu → Settings → Protection → Core Shields →
Web Shield → uncheck "Enable HTTPS scanning"). To undo the gcloud change:
`gcloud config unset core/custom_ca_certs_file`.

**`webidl.util.markAsUncloneable is not a function` when starting the MCP server.**
The Dynatrace MCP server bundles a newer `undici` that needs Node ≥ 20.17 (use 22/24).
System Node 20.12 crashes on load. Fix: install a newer Node (a portable zip from
nodejs.org works without admin) and set `MCP_NODE_BIN` in `.env` to that `node.exe`.
Verify the connection with: `node scripts/mcp_check.mjs` (does an MCP handshake + lists tools).

## Author

Jhoemar Pagao — <jhoemar.pagao@gmail.com>

## License

[MIT](LICENSE) © 2026 Jhoemar Pagao.
