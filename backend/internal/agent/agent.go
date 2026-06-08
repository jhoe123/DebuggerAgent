// Package agent defines the PatchPilot root-cause investigator: an ADK Go
// llmagent backed by Gemini 3.1 (Vertex AI) with three tools —
//
//   - the Dynatrace MCP toolset (problems, logs, spans, DQL) over stdio,
//   - read_source(path)          — reads files under SOURCE_ROOT for code correlation,
//   - propose_patch(...)         — records a human-reviewable diff; never merges/deploys.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/patchpilot/backend/internal/dynatrace"
	"github.com/patchpilot/backend/internal/tools"
)

type readSourceArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 1-based start line; 0 => beginning
	Limit  int    `json:"limit,omitempty"`  // max lines; 0 => to end
	Query  string `json:"query,omitempty"`  // search within the file instead of ranging
}
type readSourceResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated,omitempty"`
}
type proposePatchArgs struct {
	File           string `json:"file"`
	UnifiedDiff    string `json:"unified_diff"`
	PatchedContent string `json:"patched_content"`
	Rationale      string `json:"rationale"`
}
type proposePatchResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Service wraps the ADK runners and the agent's stores. It hosts three agents that
// share the same model and source sandbox: the investigator (runner/patches), the
// instrumenter (instrument/instStore — see instrument.go), and the builder
// (builder/artifacts — generates tests and build/deploy scripts; see builder.go).
type Service struct {
	runner  *runner.Runner
	patches *tools.PatchStore
	sandbox *tools.Sandbox // shared source sandbox (re-pointable via SetSourceRoot)

	instrument *runner.Runner
	instStore  *tools.InstrumentStore

	builder   *runner.Runner
	artifacts *tools.ArtifactStore
}

// newReadSourceTool builds the read_source FunctionTool over a sandbox. It is
// shared by both the investigator and the instrumenter agents.
func newReadSourceTool(sb *tools.Sandbox) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "read_source",
		Description: "Read a source file (path relative to the project root) to correlate a stack trace or error to the responsible code. " +
			"For large files prefer a WINDOW: pass offset (1-based start line) and limit (number of lines), or query to search for a symbol and get matching regions with context. " +
			"With no offset/limit/query it returns the whole file when small, otherwise a truncated head — total_lines tells you the file size so you can range further.",
	}, func(_ tool.Context, args readSourceArgs) (readSourceResult, error) {
		r, err := sb.ReadSource(args.Path, tools.ReadOptions{Offset: args.Offset, Limit: args.Limit, Query: args.Query})
		if err != nil {
			return readSourceResult{}, err
		}
		return readSourceResult{
			Path: r.Path, Content: r.Content, StartLine: r.StartLine, EndLine: r.EndLine,
			TotalLines: r.TotalLines, Truncated: r.Truncated,
		}, nil
	})
}

// New builds the agent (model + tools + MCP toolset) and its runner.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.GCPProject == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT not set")
	}
	model, err := gemini.NewModel(ctx, cfg.GeminiModel, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.GCPProject,
		Location: cfg.GCPLocation,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini model: %w", err)
	}

	sb, err := tools.NewSandbox(cfg.SourceRoot)
	if err != nil {
		return nil, err
	}
	patches := tools.NewPatchStore(sb, cfg.PatchOutputDir)

	readTool, err := newReadSourceTool(sb)
	if err != nil {
		return nil, fmt.Errorf("read_source tool: %w", err)
	}

	patchTool, err := functiontool.New(functiontool.Config{
		Name:        "propose_patch",
		Description: "Propose a human-reviewable fix. Provide the project-relative file path, a unified diff (for display), the FULL patched file content, and a short rationale. This does NOT apply, merge, or deploy anything — a human reviews and approves separately.",
	}, func(_ tool.Context, args proposePatchArgs) (proposePatchResult, error) {
		if err := patches.Propose(tools.PatchProposal{
			File:           args.File,
			UnifiedDiff:    args.UnifiedDiff,
			PatchedContent: args.PatchedContent,
			Rationale:      args.Rationale,
		}); err != nil {
			return proposePatchResult{OK: false, Message: err.Error()}, err
		}
		return proposePatchResult{OK: true, Message: "patch proposed; awaiting human approval"}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("propose_patch tool: %w", err)
	}

	dtToolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.CommandTransport{
			Command: dynatrace.MCPCommand(cfg.MCPNodeBin, cfg.DTEnvironment, cfg.DTPlatformTok),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dynatrace mcp toolset: %w", err)
	}

	ag, err := llmagent.New(llmagent.Config{
		Name:        "patchpilot_agent",
		Description: "Investigates Dynatrace production problems, correlates them to source code, and proposes human-gated fixes.",
		Model:       model,
		Instruction: systemPrompt,
		Tools:       []tool.Tool{readTool, patchTool},
		Toolsets:    []tool.Toolset{dtToolset},
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	r, err := runner.New(runner.Config{
		AppName:           "patchpilot",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	// Second agent: the instrumenter (scan source for telemetry gaps + apply edits).
	// Shares the model and source sandbox; see instrument.go.
	instStore := tools.NewInstrumentStore(sb)
	instTools, err := instrumentTools(sb, instStore)
	if err != nil {
		return nil, err
	}
	instAgent, err := llmagent.New(llmagent.Config{
		Name:        "instrumenter_agent",
		Description: "Reviews source code for Dynatrace/OpenTelemetry instrumentation gaps and applies human-selected edits.",
		Model:       model,
		Instruction: instrumentPrompt,
		Tools:       instTools,
	})
	if err != nil {
		return nil, fmt.Errorf("create instrumenter agent: %w", err)
	}
	ir, err := runner.New(runner.Config{
		AppName:           "patchpilot-instrument",
		Agent:             instAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create instrumenter runner: %w", err)
	}

	// Third agent: the builder (generates regression tests + build/deploy artifacts on
	// demand, reusing existing ones when present). Shares the model and source sandbox.
	artifacts := tools.NewArtifactStore(sb)
	bldTools, err := builderTools(sb, artifacts)
	if err != nil {
		return nil, err
	}
	bldAgent, err := llmagent.New(llmagent.Config{
		Name:        "builder_agent",
		Description: "Generates regression tests and build/deploy artifacts on demand, reusing existing ones when present.",
		Model:       model,
		Instruction: builderPrompt,
		Tools:       bldTools,
	})
	if err != nil {
		return nil, fmt.Errorf("create builder agent: %w", err)
	}
	br, err := runner.New(runner.Config{
		AppName:           "patchpilot-builder",
		Agent:             bldAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create builder runner: %w", err)
	}

	return &Service{
		runner: r, patches: patches, sandbox: sb,
		instrument: ir, instStore: instStore,
		builder: br, artifacts: artifacts,
	}, nil
}

// Patches exposes the patch store (used by the approve-patch endpoint).
func (s *Service) Patches() *tools.PatchStore { return s.patches }

// SetSourceRoot re-points the shared source sandbox at a new root (e.g. a connected
// Git source clone). All three agents and the patch store share this sandbox, so one
// update re-points every source read/patch path. Safe to call at runtime.
func (s *Service) SetSourceRoot(root string) error {
	if s.sandbox == nil {
		return fmt.Errorf("sandbox unavailable")
	}
	return s.sandbox.SetRoot(root)
}

// Investigate runs the agent. It streams text chunks to onText (for SSE) and
// returns the final aggregated text plus the proposed patch (if any).
// StepFunc receives live milestones during a run (for SSE streaming).
type StepFunc func(stage, status, message string)

// Investigate runs the agent and returns the final text + the proposed patch.
// onStep (optional) receives live milestones derived from the agent's tool calls.
func (s *Service) Investigate(ctx context.Context, sessionID, prompt string, onStep StepFunc) (string, *tools.PatchProposal, error) {
	final, err := s.run(ctx, sessionID, prompt, onStep)
	return final, s.patches.Latest(), err
}

// Ask runs a free-form follow-up question in the same session (prose answer).
func (s *Service) Ask(ctx context.Context, sessionID, question string) (string, error) {
	return s.run(ctx, sessionID, question, nil)
}

func (s *Service) run(ctx context.Context, sessionID, userMsg string, onStep StepFunc) (string, error) {
	return runRunner(ctx, s.runner, sessionID, userMsg, onStep)
}

// runRunner drives any ADK runner to completion, accumulating text and emitting a
// "tool" step for each function call (for SSE). Shared by both agents.
func runRunner(ctx context.Context, r *runner.Runner, sessionID, userMsg string, onStep StepFunc) (string, error) {
	msg := genai.NewContentFromText(userMsg, genai.RoleUser)
	var final strings.Builder
	for ev, err := range r.Run(ctx, "judge", sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			return final.String(), err
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				final.WriteString(p.Text)
			}
			if p.FunctionCall != nil && onStep != nil {
				onStep("tool", "running", toolCallMessage(p.FunctionCall.Name, p.FunctionCall.Args))
			}
			if p.FunctionResponse != nil && onStep != nil {
				if m := toolResultMessage(p.FunctionResponse.Name, p.FunctionResponse.Response); m != "" {
					onStep("tool", "ok", m)
				}
			}
		}
	}
	return final.String(), nil
}

// toolCallMessage builds a human-readable "running" step from a tool call's name and
// arguments, naming the concrete target (file + line window, search query, DQL, …) so the
// live timeline shows WHAT the agent is doing — not just which tool it called. Paths and
// queries are wrapped in backticks so the frontend can render them monospace.
func toolCallMessage(name string, args map[string]any) string {
	switch name {
	case "execute_dql":
		if q := firstLine(argStr(args, "dql", "query", "statement"), 70); q != "" {
			return "Querying Dynatrace · `" + q + "`…"
		}
		return "Querying Dynatrace spans…"
	case "read_source":
		path := argStr(args, "path")
		if path == "" {
			return "Reading a source file…"
		}
		if q := argStr(args, "query"); q != "" {
			return "Searching `" + path + "` for \"" + q + "\"…"
		}
		offset, limit := argInt(args, "offset"), argInt(args, "limit")
		if offset > 0 && limit > 0 {
			return fmt.Sprintf("Reading `%s` (lines %d–%d)…", path, offset, offset+limit-1)
		}
		return "Reading `" + path + "`…"
	case "propose_patch":
		if f := argStr(args, "file"); f != "" {
			return "Proposing a fix to `" + f + "`…"
		}
		return "Proposing a fix…"
	case "propose_instrumentation":
		if n := argLen(args, "candidates"); n > 0 {
			return fmt.Sprintf("Proposing instrumentation (%d %s)…", n, plural(n, "candidate", "candidates"))
		}
		return "Proposing instrumentation points…"
	case "write_instrumented_file":
		if f := argStr(args, "file"); f != "" {
			return "Writing instrumented `" + f + "`…"
		}
		return "Writing instrumented source…"
	case "write_artifact":
		if f := argStr(args, "file"); f != "" {
			return "Generating `" + f + "`…"
		}
		return "Generating test/build artifact…"
	default:
		return "Calling " + name + "…"
	}
}

// toolResultMessage turns a tool's result into an optional "ok" step. Only read_source
// yields one — reporting the RESOLVED line window (the reliable source of "which lines",
// even for whole-file reads). Returns "" to emit nothing, so other tools (or a runner that
// doesn't surface function responses) add no noise.
func toolResultMessage(name string, resp map[string]any) string {
	if name != "read_source" || resp == nil {
		return ""
	}
	total := argInt(resp, "total_lines")
	if total <= 0 {
		return ""
	}
	path := argStr(resp, "path")
	if path == "" {
		path = "source"
	}
	msg := "Read `" + path + "`"
	if start, end := argInt(resp, "start_line"), argInt(resp, "end_line"); start > 0 && end > 0 {
		msg += fmt.Sprintf(" lines %d–%d of %d", start, end, total)
	} else {
		msg += fmt.Sprintf(" (%d lines)", total)
	}
	if b, ok := resp["truncated"].(bool); ok && b {
		msg += ", truncated"
	}
	return msg
}

// argStr returns the first present, non-empty (trimmed) string among the given keys.
func argStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				return t
			}
		}
	}
	return ""
}

// argInt reads an integer-ish argument (JSON numbers arrive as float64).
func argInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

// argLen returns the length of an array-valued argument (e.g. instrumentation candidates).
func argLen(m map[string]any, key string) int {
	if a, ok := m[key].([]any); ok {
		return len(a)
	}
	return 0
}

// firstLine returns the first line of s, trimmed and capped to max runes (with an ellipsis).
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

const systemPrompt = `You are PatchPilot, an SRE/debugging assistant that investigates
production problems observed in Dynatrace and proposes human-gated code fixes.

Be efficient: make the MINIMUM number of tool calls. Do not call tools you don't need
(skip get_environment_info, list_problems, list_exceptions, verify_dql). Follow these
exact steps once each, in order:

1. ONE execute_dql call to gather the evidence. For an ERROR investigation, get the failing span
   + its exception:
   fetch spans, from:now()-30d | filter service.name == "<svc>" and span.status_code == "error"
   | fields span.name, span.status_message, span.events | sort timestamp desc | limit 1
   The span.events array contains exception.message and exception.stack_trace. For a PERFORMANCE
   investigation, the user request gives you the latency query to run instead (duration is in
   nanoseconds) — use that to find the slow operation and its magnitude.
2. read_source for the file named in the stack trace. The path is RELATIVE to the app source
   root: use the BASE NAME (".../demo_app/main.go:99" -> read_source path "main.go"). Read a
   WINDOW around the stack-trace line, not the whole file: offset = max(1, line-30), limit = 60.
   Check total_lines; if the bug spans more, widen the window or pass query to search for the
   responsible function/symbol. Do NOT read an entire large file.
3. propose_patch (file, unified_diff, patched_content, rationale). patched_content must be the
   FULL file. Change ONLY the function responsible for THIS problem and keep the rest of the file
   byte-identical — the file may contain other, unrelated issues you must NOT touch. If the file is
   larger than your window, do ONE more read_source with no offset/limit/query (or read enough to
   reconstruct it) before proposing, so patched_content is complete. This NEVER merges or deploys;
   a human approves.
4. Respond with ONLY a single JSON object (no prose, no markdown fences) of this exact shape:
{
  "rootCause": {"what": "...", "where": {"file": "main.go", "line": 99}, "why": "...", "impact": "...", "summary": "...", "details": "..."},
  "confidence": 0.0,
  "alternatives": ["..."],
  "proposedPatch": {"file": "main.go", "unifiedDiff": "--- a/main.go\n+++ b/main.go\n...", "rationale": "..."},
  "suggestedTest": "..."
}
confidence is your 0..1 confidence in the root cause. Keep strings concise EXCEPT details.
rootCause.summary is a single plain-language sentence a non-engineer can grasp (no jargon).
rootCause.details is a longer engineer-facing deep dive for further reading: the exact failing
expression, why the input reaches it, the relevant runtime/language semantics, and the edge
conditions — a few sentences is fine.

If instead the user asks a plain FOLLOW-UP QUESTION (not an investigation request), answer
concisely in 1–3 sentences of prose (you may use execute_dql to check the data); in that case do
NOT output the JSON object.`
