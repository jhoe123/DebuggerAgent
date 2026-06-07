# DebuggerAgent — Task Tracker

> **Single source of truth for progress.** Update **Current State** every session.
> Context lives in [PROJECT.md](PROJECT.md); setup/run in [README.md](README.md).
> Status legend: `[ ]` todo · `[~]` in progress · `[x]` done · `[!]` blocked.

## Current State

- **Phase:** T1 done; credential-free slices of T5 and T7 done (committed).
  - **T5a:** `read_source` + `propose_patch` tools (pure Go) implemented + unit tests pass.
  - **T7a:** React+TS UI (ProblemList / Investigation / DiffViewer) built against a mock API;
    `tsc` + `vite build` verified. Falls back to mock until the backend is live.
- **Next action (you):** push to a **public GitHub repo** (README → "Pushing to GitHub"), and
  do **T2** (GCP + Dynatrace accounts). Then I wire T3/T4/T6 against live services.
- **Blockers:** T2/T3/T4/T6 need your GCP + Dynatrace credentials (I can't create those).
- **Deadline:** **2026-06-11 14:00 PDT.**

## Tasks

| ID | Task | Depends on | Status |
|----|------|-----------|--------|
| T1 | Scaffold repo + LICENSE + PROJECT.md/TASKS.md/README + initial commit (done) + push **public** GitHub (manual) | — | [~] |
| T2 | Cloud/Dynatrace setup: GCP project+billing+APIs, Gemini 3 access, ADK Go install, Dynatrace trial + platform token, run + verify MCP server | — | [ ] |
| T3 | `demo_app/` (Go) that throws a real exception; instrument with Dynatrace; trigger so a **Problem** appears | T2 | [ ] |
| T4 | Backend skeleton: Go module, ADK Go agent w/ Gemini 3, connect **Dynatrace MCP** toolset (static token) | T2 | [ ] |
| T5 | Implement `read_source` + `propose_patch` tools; agent prompt for fetch→correlate→summarize→diff | T3, T4 | [ ] |
| T6 | Backend REST/SSE endpoints (list problems, investigate, approve-patch) | T5 | [ ] |
| T7 | React+TS frontend: chat, problem list, root-cause panel, **diff viewer + Approve** | T6 | [ ] |
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
