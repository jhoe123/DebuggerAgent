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

2. **Google Cloud / Gemini**
   ```bash
   gcloud auth application-default login
   gcloud config set project "$GOOGLE_CLOUD_PROJECT"
   gcloud services enable aiplatform.googleapis.com run.googleapis.com
   ```
   Confirm the exact **Gemini 3** model id available in your region and set `GEMINI_MODEL`.

3. **Dynatrace**
   - Sign up for the free SaaS trial; note your tenant URL (`DT_ENVIRONMENT`).
   - Create a **platform token** with scopes to read problems, read logs, and run DQL/Grail
     queries. Put it in `DT_PLATFORM_TOKEN`.
   - Verify the MCP server can reach your tenant:
     ```bash
     npx -y @dynatrace-oss/dynatrace-mcp-server
     # with DT_ENVIRONMENT and DT_PLATFORM_TOKEN exported
     ```

4. **Seed a production problem** (see [demo_app/](demo_app/)): run the instrumented buggy
   service and trigger the failing endpoint so a **Problem** appears in Dynatrace.

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

## License

[MIT](LICENSE).
