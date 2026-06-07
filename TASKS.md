# DebuggerAgent — Task Tracker

> **Single source of truth for progress.** Update **Current State** every session.
> Context lives in [PROJECT.md](PROJECT.md); setup/run in [README.md](README.md).
> Status legend: `[ ]` todo · `[~]` in progress · `[x]` done · `[!]` blocked.

## Current State

- **Phase:** T1 done; credential-free slices of T5 and T7 done (committed).
  - **T5a:** `read_source` + `propose_patch` tools (pure Go) implemented + unit tests pass.
  - **T7a:** React+TS UI (ProblemList / Investigation / DiffViewer) built against a mock API;
    `tsc` + `vite build` verified. Falls back to mock until the backend is live.
- **T2 DONE & verified.** GCP: Avast TLS fixed (gcloud + Node trust `~/.gcloud-ca/win-roots.pem`);
  project `emogent-demo-2026` billing + `aiplatform` + `run` on; **`gemini-3.1-pro-preview` @
  `global` generates**; ADC login done. Dynatrace: tenant `ney49045`, platform token in `.env`,
  **MCP server connects (20 tools)**. Node note: MCP requires Node ≥ 20.17 → using portable
  **Node 24** via `MCP_NODE_BIN` (system Node 20.12 is too old).
- **T4 DONE & verified end-to-end.** ADK Go v1.4.0 agent (Gemini 3.1 @ Vertex) with the Dynatrace
  MCP toolset + `read_source`/`propose_patch` function tools. `go run ./cmd/agentcheck` builds the
  agent, launches the MCP server under Node 24, calls `list_problems` live, and returns structured
  JSON (tenant currently has 0 problems → that's T3). Key gotcha solved: prepend Node 24 to the
  MCP child's PATH so npx doesn't spawn the server under system Node 20.12.
- **T3 + T6 DONE & verified live end-to-end.** Seeded a real exception (checkout-demo, 5x) into
  Dynatrace via OTel. Backend `/api` endpoints all work against live services:
  - `GET /api/problems` → lists the error via direct MCP `execute_dql` (spans).
  - `POST /api/investigate` → agent finds the exception, reads `main.go`, returns root cause at
    `main.go:99` + a correct bounds-check patch (~70s; Gemini 3.1-pro + MCP round-trips).
  - `POST /api/approve-patch` → writes patched file + `.diff` to `.patches/` (no merge/deploy).
  - Frontend uses the live API automatically (Vite proxies `/api` → :8080; mock only as fallback).
- **Next action (me):** **T9** Cloud Run deploy + **T10/T11/T12** (README/judge notes, demo video,
  Devpost). Optional polish: investigate latency, SSE streaming.
- **Deadline:** **2026-06-11 14:00 PDT.**

## Tasks

| ID | Task | Depends on | Status |
|----|------|-----------|--------|
| T1 | Scaffold repo + LICENSE + PROJECT.md/TASKS.md/README + initial commit (done) + push **public** GitHub (manual) | — | [~] |
| T2 | Cloud/Dynatrace setup: GCP project+billing+APIs, Gemini 3 access, Dynatrace trial + platform token, run + verify MCP server | — | [x] |
| T3 | `demo_app/` (Go) that throws a real exception; instrument with OTel→Dynatrace; trigger so an exception appears | T2 | [x] |
| T4 | Backend skeleton: Go module, ADK Go agent w/ Gemini 3, connect **Dynatrace MCP** toolset (static token) | T2 | [x] |
| T5 | Implement `read_source` + `propose_patch` tools; agent prompt for fetch→correlate→summarize→diff | T3, T4 | [x] |
| T6 | Backend REST endpoints (list problems, investigate, approve-patch) | T5 | [x] |
| T7 | React+TS frontend: chat, problem list, root-cause panel, **diff viewer + Approve** (live + mock fallback) | T6 | [x] |
| T8 | Layer in selected high-value adds (confidence score, suggested regression test, severity ranking, NL follow-up, incident export) | T7 | [ ] |
| T9 | Containerize + **deploy to Cloud Run**; public hosted URL; judge test instructions | T7 | [ ] |
| T10| README finalize (setup/run/judge), `.env.example`, verify license detectable | T9 | [ ] |
| T11| Record **≤3-min demo video**, upload public to YouTube/Vimeo | T9 | [ ] |
| T12| **File Devpost submission**: hosted URL, description, repo, video, **Dynatrace track** | T10, T11 | [ ] |

## Feature backlog (pull into T8 only if time allows; do not threaten deadline)

- Confidence score + alternative root-cause hypotheses.
- Suggested regression test that would catch the bug.
- Severity/impact ranking of open problems (Dynatrace affected-users data).
- Natural-language follow-up Q&A → DQL via MCP.
- Exportable incident summary ("on-call report" markdown).
- Grail cost-awareness badge (bytes scanned per DQL).
- Deep links to offending file/line and the Dynatrace problem.
- **Stretch (defer):** multi-problem dashboard, recurring-incident detection, SLO after-patch
  preview, "World Cup traffic spike" themed demo scenario.

## Submission compliance checklist (all true before T12)

- [ ] Agent **functional**, powered by **Gemini + Agent Builder/ADK** (no other LLMs).
- [ ] Uses **Dynatrace** partner product (MCP server) meaningfully.
- [ ] **Public** repo with **detectable license** + setup/run instructions.
- [ ] **Hosted project URL** reachable by judges (+ test data/credentials & judging notes).
- [ ] **Text description** of features/functionality on Devpost.
- [ ] **Demo video ≤3 min**, public, YouTube/Vimeo, English.
- [ ] **Dynatrace track** selected on Devpost form.
- [ ] Eligibility: above age of majority; not an excluded territory.

## Session log

- 2026-06-07: Plan approved. Stack: React+TS / Go + ADK Go; Dynatrace track; CI/CD dropped.
- 2026-06-08: T1 — scaffolded repo (docs, LICENSE, .env.example, backend/frontend/demo_app stubs).
- 2026-06-08: T5a/T7a — pure-Go tools (read_source/propose_patch) + tests; React+TS UI on mock API
  (build verified). LICENSE/README attributed to Jhoemar Pagao.
- 2026-06-08: T2 done — GCP (Gemini 3.1 @ global, ADC, Avast TLS fix) + Dynatrace MCP verified
  (20 tools). Portable Node 24 required for the MCP server (`MCP_NODE_BIN`).
- 2026-06-08: T4 done — ADK Go v1.4.0 agent (Gemini 3.1, Dynatrace MCP toolset, read_source +
  propose_patch). Smoke test (`cmd/agentcheck`) runs live end-to-end. Node-24-on-PATH fix for the
  MCP child process.
- 2026-06-08: T3+T5+T6+T7 done — seeded real OTel exception in Dynatrace; backend `/api`
  (problems via direct MCP DQL, investigate via agent, approve-patch) verified live; UI live+mock.
  Verified: investigate returns root cause @ main.go:99 + correct patch; approve writes to .patches/.
