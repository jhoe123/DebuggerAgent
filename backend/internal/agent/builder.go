// This file defines the builder agent: a third ADK llmagent (built in agent.New,
// sharing the model + source sandbox) that GENERATES pipeline artifacts on demand —
// regression tests today, build/deploy scripts next — and reuses existing ones when
// they already cover the need ("lazy" detect-or-generate).
//
//   - read_source(...)     — same reader the other agents use.
//   - write_artifact(...)  — records a FULL generated file (test/script/Dockerfile).
package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/patchpilot/backend/internal/lang"
	"github.com/patchpilot/backend/internal/tools"
)

type writeArtifactArgs struct {
	File    string `json:"file"`
	Content string `json:"content"`
}
type writeArtifactResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// builderTools builds the tool set for the builder agent.
func builderTools(sb *tools.Sandbox, store *tools.ArtifactStore) ([]tool.Tool, error) {
	readTool, err := newReadSourceTool(sb)
	if err != nil {
		return nil, fmt.Errorf("read_source tool: %w", err)
	}
	writeTool, err := functiontool.New(functiontool.Config{
		Name: "write_artifact",
		Description: "Record the FULL content of one generated file (a regression test, a build script, a Dockerfile, or a " +
			"deploy script). Provide the project-relative path and the COMPLETE file content. Call once per file. This does " +
			"NOT run or deploy anything — the pipeline writes it locally (or folds it into the cloud build source) and rolls back on failure.",
	}, func(_ tool.Context, args writeArtifactArgs) (writeArtifactResult, error) {
		if err := store.Set(args.File, args.Content); err != nil {
			return writeArtifactResult{OK: false, Message: err.Error()}, err
		}
		return writeArtifactResult{OK: true, Message: "artifact recorded"}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("write_artifact tool: %w", err)
	}
	return []tool.Tool{readTool, writeTool}, nil
}

var runNameRE = regexp.MustCompile(`RUN=([A-Za-z0-9_]+)`)

// TestResolution is the outcome of GenerateTest: the test-selector name to run as the
// deploy gate (go test -run <RunName> / pytest -k <RunName>), plus any newly generated
// test files (empty when an existing test is reused).
type TestResolution struct {
	RunName string            // test to run as the gate (go test -run <RunName> / pytest -k <RunName>)
	Files   map[string]string // generated file(s) to write; empty => an existing test is reused
}

// GenerateTest asks the builder agent to ensure a regression test guards the file that
// was just fixed. The agent reads the fixed file and the package's existing tests, then
// EITHER reuses an existing test (replies RUN=<name>, writes nothing) OR writes a new
// <x>_test.go via write_artifact (replies RUN=<name>). A non-empty repairErr is a repair
// turn: the previous test didn't compile — fix it. This is the "lazy" detect-or-generate
// test gate, with the detection delegated to the agent.
func (s *Service) GenerateTest(ctx context.Context, sessionID, file, rationale, repairErr string, onStep StepFunc) (TestResolution, error) {
	s.artifacts.Clear()
	prof := s.language()
	final, err := runRunner(ctx, s.builder, sessionID, testGenPrompt(prof, file, rationale, repairErr), onStep)
	if err != nil {
		return TestResolution{}, err
	}
	files := s.artifacts.Files()
	run := ""
	if m := runNameRE.FindStringSubmatch(final); m != nil {
		run = m[1]
	}
	if run == "" { // fall back to the test func name in a generated file
		re := prof.TestFuncRE()
		for _, content := range files {
			if m := re.FindStringSubmatch(content); m != nil {
				run = m[1]
				break
			}
		}
	}
	if run == "" {
		return TestResolution{}, fmt.Errorf("the builder did not identify a test to run (expected a RUN=<TestName> line)")
	}
	return TestResolution{RunName: run, Files: files}, nil
}

// GenerateBuildArtifact asks the builder agent to produce a build artifact and returns
// file -> content. kind is "build-script" (a script that builds the React storefront +
// the Go binary) or "dockerfile" (a multi-stage image that does the same). errOut (when
// non-empty) is a repair turn.
func (s *Service) GenerateBuildArtifact(ctx context.Context, sessionID, kind, errOut string, onStep StepFunc) (map[string]string, error) {
	s.artifacts.Clear()
	if _, err := runRunner(ctx, s.builder, sessionID, buildArtifactPrompt(s.language(), kind, errOut), onStep); err != nil {
		return nil, err
	}
	files := s.artifacts.Files()
	if len(files) == 0 {
		return nil, fmt.Errorf("the builder did not produce a %s", kind)
	}
	return files, nil
}

func buildArtifactPrompt(prof lang.Profile, kind, errOut string) string {
	var b strings.Builder
	if errOut != "" {
		b.WriteString("Your previous artifact failed. Treat the output below as authoritative, fix it, and call " +
			"write_artifact again with the corrected content.\n\n")
		b.WriteString(errOut)
		b.WriteString("\n\n")
	}
	if kind == "dockerfile" {
		b.WriteString(prof.DockerfileGuidance())
	} else { // build-script
		b.WriteString(prof.BuildScriptGuidance())
	}
	return b.String()
}

func testGenPrompt(prof lang.Profile, file, rationale, repairErr string) string {
	var b strings.Builder
	if repairErr != "" {
		b.WriteString("Your previous test did not compile/run cleanly. Treat the output below as authoritative, fix the " +
			"test file, and call write_artifact again with the corrected FULL content. End with RUN=<TestName>.\n\n")
		b.WriteString(repairErr)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "A fix was just applied to %q.\nRationale: %s\n\n", file, rationale)
	fmt.Fprintf(&b, "Ensure a %s regression test guards this fix; it will be run as the deploy gate. "+
		"If no existing test already covers it, %s. End with RUN=<TestName>.", prof.BuilderRole(), prof.TestFileGuidance())
	return b.String()
}

const builderPrompt = `You are a test engineer. A bug was just fixed in a file; ensure a regression test guards
the FIXED behavior — one that FAILS on the buggy code and PASSES once fixed, written in the project's
existing test framework and language.

Tools: read_source (read any file, project-relative) and write_artifact (record a full file).

Steps:
1. read_source the fixed file to see the responsible function(s).
2. read_source the module/package's existing test files to check whether a test already covers this behavior.
3a. If an existing test already asserts the FIXED behavior of the responsible function, REUSE it: write
    nothing and reply with exactly: RUN=<ExistingTestName>
3b. Otherwise WRITE a new test as described in the task (the file name, framework and assertion style are
    specified there). Keep it deterministic and fast and mirror the style of the existing tests. Then reply
    with exactly: RUN=<NewTestName>

Finish with a single final line: RUN=<TestName>  — and nothing after it.`
