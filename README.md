# PatchPilot

> **Detect → diagnose → patch → verify.** AI Root-Cause Investigator for the **Google Cloud
> Rapid Agent Hackathon** — **Dynatrace track**. Captures a production issue from **Dynatrace**,
> uses **Gemini 3** to correlate it to source code and explain the root cause, and **proposes a
> human-gated patch** (no auto-merge/deploy).

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

## Configurable autonomy: auto-remediation pipeline + Test Console (local)

Autonomy is a **setting**. By default the agent is human-gated (approve → patch written to a
branch, never merged/deployed). Opt-in **auto-remediation** runs a full pipeline:

```
Apply → Test → Build → Deploy → Verify     (deploy is GATED on tests passing)
```

A committed regression test (`demo_app/checkout_test.go`) fails on the seeded bug and passes once
the fix is applied, so even autopilot won't ship a broken patch. The pipeline applies the patch to
the service source, runs the test, rebuilds, restarts, and verifies `/checkout?index=99` goes from
HTTP 500 to fixed. Logic lives in [`backend/internal/democtl`](backend/internal/democtl).

A **Test Console** (clearly labeled "testing only") lets you trigger the incident, reset the demo
source to its committed buggy state (`git checkout`), and see status — for repeatable demos.

These controls are **gated by `ENABLE_TEST_CONSOLE`, which defaults ON locally** so the full app
works out of the box. The backend OWNS demo_app (git-resets, builds, runs, restarts it) when on, so
the **hosted Cloud Run image explicitly pins `ENABLE_TEST_CONSOLE=false`** (see Dockerfile) — the
public demo stays human-gated. Set `ENABLE_TEST_CONSOLE=false` to disable locally too.

```bash
# Test Console is on by default — just run:
cd backend && go run ./cmd/server
cd frontend && npm run dev
```

The UI then shows the Test Console + an auto-remediation panel (autonomy toggle + per-stage
checkboxes) with a live Apply→Test→Build→Deploy→Verify stream.

## Auto-Instrument with Dynatrace (proactive observability)

Where investigation is *reactive* (a production error → one fix), **Auto-Instrument** is
*proactive*: a second agent **scans the service for OpenTelemetry gaps** — handlers without a
span, operations without error recording, missing useful attributes, or a missing tracer/exporter
bootstrap — and proposes a list of candidates. Each is shown with a kind badge, `file:line`, a
rationale, and an expandable diff hunk; long lists are grouped by file with select-all, kind and
text filters. You **selectively apply** some/all.

```
(generate) → Apply → Test/Vet → Debug (auto-repair) → Build → Deploy → Verify (/healthz)
```

Applying writes the chosen instrumentation, runs `go vet ./...` (a deterministic gate that
compiles every package — including tests — and statically checks the AI's edits without being
blocked by `demo_app`'s deliberately-failing seeded-bug tests), rebuilds, restarts, and verifies
`/healthz`. If a stage fails the agent is fed the compiler/vet output to **fix its own edits** (up
to 2 attempts); if it still can't go green the source is **rolled back via `git checkout`**.

- **Scan** is read-only and **hosted-safe** (`POST /api/instrument/scan`).
- **Apply** writes/builds/runs source and is **local-only**, gated by `ENABLE_TEST_CONSOLE`
  (`POST /api/instrument/apply`) — the same boundary as auto-remediation. The Apply buttons are
  disabled in the hosted UI; scanning still works.

Logic: the instrumenter agent in [`backend/internal/agent/instrument.go`](backend/internal/agent/instrument.go)
and the pipeline in [`backend/internal/democtl`](backend/internal/democtl); UI in
[`frontend/src/components/Instrumentation.tsx`](frontend/src/components/Instrumentation.tsx).

## Performance monitoring (not just errors)

The agent surfaces **two kinds** of Dynatrace problems: `error` (exception spans) and
`performance` (operations whose **p95 span duration** exceeds a threshold). `ListProblems` runs an
error DQL and a latency DQL (`percentile(duration, 95)`), tagging each problem with a `kind` and a
`metric` chip (e.g. `p95 657 ms`). Investigating a performance problem routes the agent to a
latency query + an optimization patch (instead of an exception trace + bounds-check), and the
auto-remediation pipeline verifies the **latency dropped** (gated on a latency regression test).
The bundled `demo_app` ships a seeded slow endpoint (`/report`, ~657 ms) alongside the buggy
`/checkout` so both scenarios can be demoed.

## Patch & change history

Every proposed patch, human approval, and pipeline run is recorded in an audit log (with the
**affected files** and, for pipelines, the before→after verify like `500 -> 400`). The UI shows it
under **Patch & change history**; the data comes from `GET /api/history` (read-only, always on,
hosted-safe). Storage is an in-memory ring buffer with best-effort append to
`<PATCH_OUTPUT_DIR>/history.jsonl` (note: Cloud Run `/tmp` is ephemeral).

## Slack notifications (optional)

Set `SLACK_WEBHOOK_URL` (a Slack Incoming Webhook) and a background poller posts a single,
**consolidated rolling digest** of all active bugs — re-posted only when the bug set changes, so
recurring occurrences don't spam the channel. Tune the cadence with `SLACK_POLL_INTERVAL` (default
`60s`). The webhook is a **secret**: keep it in `.env` (gitignored) or pass it via Secret Manager
on Cloud Run — never commit it.

> Cloud Run scales to zero, so the poller only runs while an instance is warm. For a reliable live
> demo, run the backend locally with the webhook set, or deploy with `--min-instances=1`.

## Git source — branch-per-fix + confirm-to-merge (optional)

Instead of patching the bundled `demo_app` in place, PatchPilot can manage fixes in a **real Git
repository**: it clones the repo, makes the clone the active source the agent reads/patches, opens
an **isolated branch per fix**, commits the fix there, and — **only when you click "Confirm
fixed"** — merges that branch into a configured **working branch** and deletes it. The agent,
pipeline, and autopilot may commit to a fix branch, but **a merge into the working branch only ever
happens via the human confirm action** — preserving PatchPilot's human-in-the-loop boundary.

Configure it in **Settings → Git source**:

- **Repository URL** + **Working branch** (e.g. `main`) and a **fix branch prefix**.
- **Auth token (HTTPS PAT)** — a secret; it is injected per git call (never written to
  `.git/config` or the URL) and **never returned by the API** (only a masked preview / "token set").
- Toggles: **Create a branch per fix**, **Push to remote** (permission gate — off ⇒ all
  branch/commit/merge work stays local), and **Merge & delete branch on confirm**.
- Click **Connect / Clone**. The clone becomes the active source root.

Endpoints: `GET/POST /api/git-source`, `POST /api/git-source/connect`, `POST /api/git-source/branch`,
`POST /api/confirm-fix` (the merge gate), `POST /api/git-source/cleanup`. The mutating ones need the
`git` binary (installed in the Docker image) and the feature flag, which is **on by default** — set
`ENABLE_GIT_SOURCE=false` to disable. It stays harmless until a repo is configured, and pushing
still requires a token + "Push to remote"; see the `GIT_SOURCE_*` block in `.env.example`. On hosted
Cloud Run, deploy with
`--min-instances=1 --max-instances=1` so the clone + branch state stay on one instance; with push
enabled, the pushed branches on the remote are the durable record across cold starts.

**Default / published demo source:** the bundled ShopFlow `demo_app` is published as a standalone
public repo — **https://github.com/jhoe123/patchpilot-demo-app** — and is the **default**
`GIT_SOURCE_REPO_URL`, so PatchPilot points at it out of the box. To republish it (or publish your
own), create an empty public repo on GitHub and run:

```powershell
pwsh scripts/publish_demo_repo.ps1 -RemoteUrl https://github.com/<you>/patchpilot-demo-app.git
```

> **Tested with:** `https://github.com/jhoe123/patchpilot-demo-app` (the published ShopFlow demo).
> Due to limited AI/model access during development, the Git source + confirm-to-merge flow was
> validated against **this single repository only**. Other hosts (GitLab/Bitbucket/self-hosted) and
> branch layouts are best-effort. The merge path assumes a conflict-free `--no-ff` merge; conflicts
> are aborted and reported for manual resolution (out of scope).

## Deploy (Cloud Run)

The root `Dockerfile` ships a Node 24 runtime (to run the Dynatrace MCP server) plus the Go
server, the built React app, and `demo_app`. Vertex auth uses the Cloud Run runtime service
account (grant it `roles/aiplatform.user`); the Dynatrace token is passed as an env var.

```bash
gcloud run deploy patchpilot --source . --region us-central1 --allow-unauthenticated \
  --memory 1Gi --cpu 1 --timeout 300 \
  --set-env-vars "GOOGLE_CLOUD_PROJECT=<project>,GEMINI_MODEL=gemini-3.5-flash,DT_ENVIRONMENT=<tenant>,DT_PLATFORM_TOKEN=<token>"
```
`GOOGLE_CLOUD_LOCATION=global`, `WEB_DIR`, `SOURCE_ROOT`, and `PATCH_OUTPUT_DIR=/tmp/patches`
are baked into the image. For production use Secret Manager (`--set-secrets`) for the token.

## Testing

- **Hosted URL:** https://patchpilot-460077240357.us-central1.run.app — **no login required.**
- **Test flow:** open the URL → select the **checkout-demo** problem (a real `index out of range`
  exception captured from Dynatrace) → click **Investigate with AI** → the agent queries Dynatrace
  spans, reads the source, and returns the root cause at `main.go:99` with a proposed patch
  (~30s) → click **Approve patch** to write the diff to a branch/file. The agent **never merges
  or deploys** — a human always approves.
- **What's Gemini-powered:** the investigation (root-cause reasoning + patch) runs on an ADK Go
  agent backed by **Gemini 3.5 Flash** on Vertex AI, using the **Dynatrace MCP server** for all
  telemetry access.

## Pushing to GitHub (manual — `gh` not installed in this environment)

```bash
git remote add origin https://github.com/<you>/PatchPilot.git
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
