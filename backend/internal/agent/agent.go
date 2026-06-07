// Package agent defines the DebuggerAgent root-cause investigator: an ADK Go
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

	"github.com/debuggeragent/backend/internal/dynatrace"
	"github.com/debuggeragent/backend/internal/tools"
)

type readSourceArgs struct {
	Path string `json:"path"`
}
type readSourceResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
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

// Service wraps the ADK runner and the patch store.
type Service struct {
	runner  *runner.Runner
	patches *tools.PatchStore
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

	readTool, err := functiontool.New(functiontool.Config{
		Name:        "read_source",
		Description: "Read a source file from the application repository (path relative to the project root) to correlate a stack trace or error to the responsible code.",
	}, func(_ tool.Context, args readSourceArgs) (readSourceResult, error) {
		content, err := sb.ReadSource(args.Path)
		if err != nil {
			return readSourceResult{}, err
		}
		return readSourceResult{Path: args.Path, Content: content}, nil
	})
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
		Name:        "debugger_agent",
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
		AppName:           "debuggeragent",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}
	return &Service{runner: r, patches: patches}, nil
}

// Patches exposes the patch store (used by the approve-patch endpoint).
func (s *Service) Patches() *tools.PatchStore { return s.patches }

// Investigate runs the agent. It streams text chunks to onText (for SSE) and
// returns the final aggregated text plus the proposed patch (if any).
func (s *Service) Investigate(ctx context.Context, sessionID, prompt string, onText func(string)) (string, *tools.PatchProposal, error) {
	msg := genai.NewContentFromText(prompt, genai.RoleUser)
	var final strings.Builder
	for ev, err := range s.runner.Run(ctx, "judge", sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			return final.String(), s.patches.Latest(), err
		}
		if txt := eventText(ev); txt != "" {
			final.WriteString(txt)
			if onText != nil {
				onText(txt)
			}
		}
	}
	return final.String(), s.patches.Latest(), nil
}

func eventText(ev *session.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range ev.Content.Parts {
		if p != nil && p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

const systemPrompt = `You are DebuggerAgent, an SRE/debugging assistant that investigates
production problems observed in Dynatrace and proposes human-gated code fixes.

You have these tools:
- Dynatrace MCP tools. IMPORTANT: backend service exceptions (from OpenTelemetry) live in the
  "spans" table, NOT in list_problems (Davis problems) or list_exceptions (RUM). Use execute_dql,
  e.g.  fetch spans, from:now()-24h | filter service.name == "<svc>" and span.status_code == "error"
  | fields timestamp, span.name, span.status_message, span.events | sort timestamp desc | limit 5
  The span.events array contains exception.message and exception.stack_trace.
- read_source(path): read a file from the application repo. The path is RELATIVE to the app
  source root (e.g. "main.go"); derive it from the BASE NAME of the file in the stack trace
  (e.g. ".../demo_app/main.go:99" -> read_source("main.go")).
- propose_patch(file, unified_diff, patched_content, rationale): record a reviewable fix.
  This NEVER merges or deploys; a human approves separately. Always pass the FULL patched
  file content in patched_content.

Process:
1. Identify the problem (use the service id/name from the user if given).
2. Gather evidence via execute_dql: get the exception message and stack trace from the spans.
3. Locate the offending code with read_source and reason about the true root cause (cite file:line).
4. Call propose_patch with a minimal, correct fix (mention a regression test in the rationale).
5. Finally, respond with ONLY a single JSON object (no prose, no markdown fences) of this exact shape:
{
  "rootCause": {"what": "...", "where": {"file": "main.go", "line": 99}, "why": "...", "impact": "..."},
  "confidence": 0.0,
  "alternatives": ["..."],
  "proposedPatch": {"file": "main.go", "unifiedDiff": "--- a/main.go\n+++ b/main.go\n...", "rationale": "..."},
  "suggestedTest": "..."
}
confidence is your 0..1 confidence in the root cause. Keep strings concise.`
