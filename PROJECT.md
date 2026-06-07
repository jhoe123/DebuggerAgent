# DebuggerAgent — Project Context

> **Purpose of this file:** give any developer or AI assistant full context to pick up
> the project and continue. Pair it with [TASKS.md](TASKS.md) (the live task tracker) and
> [README.md](README.md) (setup/run/judge instructions). Update the **Current State** in
> TASKS.md every session.

## What we're building

An **AI Root-Cause Investigator** agent for the **Google Cloud Rapid Agent Hackathon**
([rapid-agent.devpost.com](https://rapid-agent.devpost.com/)), **Dynatrace track**.

The agent:
1. Captures a real production issue from **Dynatrace** (problem, logs, traces) via the
   **Dynatrace MCP server**.
2. Uses **Gemini 3** (via Google's **Agent Development Kit / Gemini Enterprise Agent
   Platform**) to correlate the stack trace to **source code** and explain the root cause.
3. **Proposes a human-gated patch** (a reviewable diff). A developer explicitly approves
   before the patch is written to a branch/file.

**Out of scope (deliberately):** CI/CD automation, autonomous merging, autonomous deploys.
The human-in-the-loop boundary is intentional and is the credibility story for judges.

## Why these choices

- **Dynatrace track** because the idea maps cleanly onto observability → the Dynatrace MCP
  server provides problems/logs/traces/DQL off the shelf, removing the hardest plumbing.
- **Gemini + Agent Builder/ADK** is mandatory per contest rules (other LLMs prohibited).
- **Human-gated patch** because judges/devs distrust AI that merges code autonomously.
- **Tight, seeded demo** because the deadline is ~4 days — we optimize for a compelling
  ≤3-min video, not a production system.

## Tech stack

| Layer | Choice |
|-------|--------|
| Frontend | **React + TypeScript** (Vite): chat, problem list, root-cause panel, diff viewer, Approve |
| Backend | **Go**, hosting the **ADK Go** agent (Gemini 3) + REST/SSE API |
| Agent tools | Dynatrace **MCP** toolset (problems/logs/spans/DQL) + `read_source` + `propose_patch` |
| Demo app | Small **Go** service that throws a real exception (Dynatrace-instrumented) |
| Hosting | **Cloud Run** (one container serves Go API + built React assets) → public judge URL |

**Fallback** if ADK Go's MCP wiring is immature: Go backend calls **Vertex AI Gemini**
directly + a Go **MCP client** to Dynatrace. Still Gemini-powered → still compliant. Note it
in the README if used.

## Architecture

```
Dynatrace tenant (problem + logs + traces/spans + DQL)
        │  MCP (static platform token)
        ▼
  Dynatrace MCP server (npx @dynatrace-oss/dynatrace-mcp-server)
        │  tools
        ▼
  Go backend = ADK Go agent (Gemini 3)
     tools: [Dynatrace MCP] + read_source(path) + propose_patch(file, diff)
        │  REST + SSE (stream reasoning)
        ▼
  React + TS SPA  →  Cloud Run public URL
        │  developer clicks "Approve"
        ▼
  patch written to a branch/file ONLY (never merges, never deploys)
```

## Repo layout

```
DebuggerAgent/
  PROJECT.md          # this file — context
  TASKS.md            # live task tracker + Current State
  README.md           # setup / run / judge instructions
  LICENSE             # MIT (detectable, required by rules)
  .env.example        # all required keys/env (copy to .env)
  backend/            # Go + ADK Go agent
    cmd/server/       # HTTP/SSE entrypoint
    internal/agent/   # agent def, tools (read_source, propose_patch), MCP wiring
  frontend/           # React + TS (Vite)
  demo_app/           # instrumented buggy Go service (the "production" app)
  deploy/             # Dockerfile, Cloud Run config
```

## Key constraints / compliance (full checklist in TASKS.md §Compliance)

- Functional agent **powered by Gemini + Agent Builder/ADK**; no other LLMs.
- Must **meaningfully use** a Dynatrace partner product (the MCP server).
- **Public** repo + **detectable license**; **hosted URL**; **≤3-min public video**;
  Devpost form with **Dynatrace track** selected.
- **Deadline: 2026-06-11 14:00 PDT.**

## Decision log

- 2026-06-07: Chose Dynatrace track; dropped CI/CD from scope; React+TS / Go stack;
  ADK Go primary with direct-Vertex fallback; MIT license; Cloud Run hosting.
