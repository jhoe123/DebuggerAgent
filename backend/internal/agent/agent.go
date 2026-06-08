// Package agent defines the PatchPilot root-cause investigator: an ADK Go
// llmagent backed by Gemini 3.1 (Vertex AI) with three tools —
//
//   - the Dynatrace MCP toolset (problems, logs, spans, DQL) over stdio,
//   - read_source(path)          — reads files under SOURCE_ROOT for code correlation,
//   - propose_patch(...)         — records a human-reviewable diff; never merges/deploys.
package agent

import (
	"context"
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

// Service wraps the ADK runners and the agent's stores. It hosts two agents that
// share the same model and source sandbox: the investigator (runner/patches) and
// the instrumenter (instrument/instStore — see instrument.go).
type Service struct {
	runner  *runner.Runner
	patches *tools.PatchStore

	instrument *runner.Runner
	instStore  *tools.InstrumentStore
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

	return &Service{runner: r, patches: patches, instrument: ir, instStore: instStore}, nil
}

// Patches exposes the patch store (used by the approve-patch endpoint).
func (s *Service) Patches() *tools.PatchStore { return s.patches }

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
				onStep("tool", "running", toolMessage(p.FunctionCall.Name))
			}
		}
	}
	return final.String(), nil
}

func toolMessage(name string) string {
	switch name {
	case "execute_dql":
		return "Querying Dynatrace spans…"
	case "read_source":
		return "Reading the source file…"
	case "propose_patch":
		return "Proposing a fix…"
	case "propose_instrumentation":
		return "Proposing instrumentation points…"
	case "write_instrumented_file":
		return "Writing instrumented source…"
	default:
		return "Calling " + name + "…"
	}
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
