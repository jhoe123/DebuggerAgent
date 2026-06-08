// This file defines the instrumenter agent: a second ADK llmagent (built in
// agent.New, sharing the model + source sandbox) that scans Go source for
// Dynatrace/OpenTelemetry instrumentation gaps and applies human-selected edits.
//
//   - read_source(...)               — same reader the investigator uses.
//   - propose_instrumentation(...)   — records the candidate set (scan; review only).
//   - write_instrumented_file(...)   — records the FULL patched file (apply; local-only writes via democtl).
package agent

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/tools"
)

type instrumentCandidateArg struct {
	File        string `json:"file"`
	Symbol      string `json:"symbol,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	Kind        string `json:"kind"`
	Rationale   string `json:"rationale"`
	Snippet     string `json:"snippet,omitempty"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}
type proposeInstrumentationArgs struct {
	Summary    string                   `json:"summary"`
	Truncated  bool                     `json:"truncated,omitempty"`
	Candidates []instrumentCandidateArg `json:"candidates"`
}
type proposeInstrumentationResult struct {
	OK      bool   `json:"ok"`
	Count   int    `json:"count"`
	Message string `json:"message"`
}
type writeInstrumentedFileArgs struct {
	File           string `json:"file"`
	PatchedContent string `json:"patched_content"`
}
type writeInstrumentedFileResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// instrumentTools builds the tool set for the instrumenter agent.
func instrumentTools(sb *tools.Sandbox, store *tools.InstrumentStore) ([]tool.Tool, error) {
	readTool, err := newReadSourceTool(sb)
	if err != nil {
		return nil, fmt.Errorf("read_source tool: %w", err)
	}

	proposeTool, err := functiontool.New(functiontool.Config{
		Name: "propose_instrumentation",
		Description: "Record the instrumentation candidates you found (review only — nothing is written). " +
			"Provide a one-line summary and a list of candidates; each has file (project-relative), symbol, " +
			"start_line, kind (otel-bootstrap|tracer-init|span|record-error|attributes|metric), a one-line rationale, " +
			"a short snippet of the target location, and a small unified_diff hunk for display. Set truncated=true if you capped the list.",
	}, func(_ tool.Context, args proposeInstrumentationArgs) (proposeInstrumentationResult, error) {
		scan := api.InstrumentationScan{Summary: args.Summary, Truncated: args.Truncated}
		for _, c := range args.Candidates {
			scan.Candidates = append(scan.Candidates, api.InstrumentationCandidate{
				File: c.File, Symbol: c.Symbol, StartLine: c.StartLine, EndLine: c.EndLine,
				Kind: c.Kind, Rationale: c.Rationale, Snippet: c.Snippet, UnifiedDiff: c.UnifiedDiff,
			})
		}
		if err := store.SetScan(scan); err != nil {
			return proposeInstrumentationResult{OK: false, Message: err.Error()}, err
		}
		return proposeInstrumentationResult{OK: true, Count: len(scan.Candidates), Message: "instrumentation candidates recorded"}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("propose_instrumentation tool: %w", err)
	}

	writeTool, err := functiontool.New(functiontool.Config{
		Name: "write_instrumented_file",
		Description: "Record the FULL patched content of one file with the selected instrumentation applied. " +
			"Provide the project-relative file path and the COMPLETE new file content (valid, compilable Go, imports included). " +
			"Call once per affected file. This does NOT deploy — democtl writes/tests/builds it locally and rolls back on failure.",
	}, func(_ tool.Context, args writeInstrumentedFileArgs) (writeInstrumentedFileResult, error) {
		if err := store.SetPatchedFile(args.File, args.PatchedContent); err != nil {
			return writeInstrumentedFileResult{OK: false, Message: err.Error()}, err
		}
		return writeInstrumentedFileResult{OK: true, Message: "instrumented file recorded"}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("write_instrumented_file tool: %w", err)
	}

	return []tool.Tool{readTool, proposeTool, writeTool}, nil
}

// InstrumentSelected returns the scanned candidates matching ids (empty => all).
func (s *Service) InstrumentSelected(ids []string) []api.InstrumentationCandidate {
	return s.instStore.Selected(ids)
}

// ScanInstrumentation runs the instrumenter in SCAN mode and returns the recorded
// candidate set. Read-only (no writes), so it is safe to expose hosted.
func (s *Service) ScanInstrumentation(ctx context.Context, sessionID string, onStep StepFunc) (*api.InstrumentationScan, error) {
	if _, err := runRunner(ctx, s.instrument, sessionID, instrumentScanPrompt, onStep); err != nil {
		return nil, err
	}
	scan := s.instStore.Scan()
	if scan == nil {
		return &api.InstrumentationScan{
			Root:       s.instStore.Root(),
			Summary:    "No instrumentation gaps found.",
			Candidates: []api.InstrumentationCandidate{},
		}, nil
	}
	return scan, nil
}

// ApplyInstrumentation runs the instrumenter in APPLY mode for the selected
// candidates and returns the full patched files (file -> content). When repairErr
// is non-empty it is a repair turn: the agent fixes its edits given the build/test
// output (same session id keeps its prior context).
func (s *Service) ApplyInstrumentation(ctx context.Context, sessionID string, selected []api.InstrumentationCandidate, repairErr string, onStep StepFunc) (map[string]string, error) {
	s.instStore.ClearPatched()
	if _, err := runRunner(ctx, s.instrument, sessionID, applyInstrumentationPrompt(selected, repairErr), onStep); err != nil {
		return nil, err
	}
	files := s.instStore.PatchedFiles()
	if len(files) == 0 {
		return nil, fmt.Errorf("the agent did not produce any instrumented files")
	}
	return files, nil
}

const instrumentScanPrompt = "Scan the Go source under the project root for Dynatrace/OpenTelemetry " +
	"instrumentation gaps and call propose_instrumentation once with the candidates you find."

// applyInstrumentationPrompt builds the APPLY/repair user message from the selected
// candidates and (optionally) the failing build/test output.
func applyInstrumentationPrompt(selected []api.InstrumentationCandidate, repairErr string) string {
	var b strings.Builder
	if repairErr != "" {
		b.WriteString("Your previous instrumentation did not build/test cleanly. Treat the output below as authoritative, " +
			"fix the file(s) so they compile and pass, and call write_instrumented_file again with the corrected FULL content.\n\n")
		b.WriteString(repairErr)
		b.WriteString("\n\n")
	}
	b.WriteString("Apply EXACTLY these selected instrumentation changes (and only these). For each affected file, read it " +
		"in full, apply the changes, and call write_instrumented_file once with the COMPLETE new file content:\n")
	for _, c := range selected {
		fmt.Fprintf(&b, "- [%s] %s", c.Kind, c.File)
		if c.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", c.Symbol)
		}
		if c.StartLine > 0 {
			fmt.Fprintf(&b, " ~line %d", c.StartLine)
		}
		fmt.Fprintf(&b, ": %s\n", c.Rationale)
	}
	return b.String()
}

const instrumentPrompt = `You are an observability engineer who adds Dynatrace-grade OpenTelemetry
instrumentation to a Go service. You have three tools: read_source, propose_instrumentation,
and write_instrumented_file.

Follow the OpenTelemetry conventions already used in the codebase:
- Package tracer:    var tracer = otel.Tracer("<service>")
- Span per operation/handler:  ctx, span := tracer.Start(r.Context(), "GET /path"); defer span.End()
- Error/panic recording:  span.RecordError(err, trace.WithStackTrace(true)); span.SetStatus(codes.Error, err.Error())
- Useful attributes:  span.SetAttributes(attribute.Int(...), attribute.String(...))
- Bootstrap (only if missing): an OTLP/HTTP exporter wired from OTEL_* env vars.
Add any imports you introduce. Do NOT change code that is already instrumented.

Your behaviour depends on the request:

SCAN — read the Go sources under the project root and find spots that SHOULD emit telemetry
but don't: HTTP handlers without a span, operations without error recording, missing useful
span attributes, or a missing tracer/exporter bootstrap. Call propose_instrumentation ONCE with
up to 25 candidates (set truncated=true if you capped). For each candidate give file, symbol,
start_line, kind, a one-line rationale, a short snippet of the target location, and a SMALL
unified_diff hunk showing the proposed addition (display only — do NOT emit whole files here).
After the tool call, reply with the single word: done.

APPLY — you are given a specific list of selected changes. For EACH file they touch: read the
current file in full, apply EXACTLY those changes (and only those), and call
write_instrumented_file(file, patched_content) with the COMPLETE, compilable new file content.
Call it once per file. If the user reports a build/test failure, fix your edits and call
write_instrumented_file again with corrected content. After writing, reply: done.`
