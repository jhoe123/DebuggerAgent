package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/tools"
)

// scriptedAgent stands in for the real Gemini agent: for each problem it reads the
// CURRENT demo_app/main.go from disk and returns a full-file patch applying that
// scenario's real fix. Because the engine applies each patch before investigating
// the next, the second scenario reads a source that already has the first fix — so
// the final main.go ends up with BOTH fixes (the two seeded bugs share that file).
type scriptedAgent struct{ demoDir string }

func (s *scriptedAgent) InvestigatePrompt(id string) string { return id }

func (s *scriptedAgent) Investigate(ctx context.Context, sessionID, prompt string, onStep agent.StepFunc) (string, *tools.PatchProposal, error) {
	if onStep != nil {
		onStep("investigate", "ok", "read source")
	}
	id := strings.TrimPrefix(sessionID, "auto-")
	kind, _ := agent.SplitProblemID(id)
	b, err := os.ReadFile(filepath.Join(s.demoDir, "main.go"))
	if err != nil {
		return "", nil, err
	}
	src := string(b)
	switch kind {
	case "error":
		// Bounds-check /checkout so an out-of-range index returns 400 instead of panicking.
		src = strings.Replace(src, "selected := items[idx]",
			"if idx < 0 || idx >= len(items) {\n\t\thttp.Error(w, \"index out of range\", http.StatusBadRequest)\n\t\treturn\n\t}\n\tselected := items[idx]", 1)
	case "performance":
		// Drop the per-item blocking sleep (and its now-unused time import) so /report is fast.
		src = removeLines(src, "time.Sleep(", `"time"`)
	}
	return "", &tools.PatchProposal{File: "main.go", PatchedContent: src, Rationale: kind + " fix"}, nil
}

func removeLines(src string, needles ...string) string {
	lines := strings.Split(src, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		drop := false
		for _, n := range needles {
			if strings.Contains(ln, n) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, ln)
		}
	}
	return strings.Join(kept, "\n")
}

// TestProcessBatch_EndToEnd_RealPipeline drives the autopilot batch through the REAL
// democtl pipeline (real `go test` + `go build` gates) against a copy of the actual
// demo_app, proving that with autopatch on, two problems sharing one file are fixed
// and verified in a single consolidated pipeline run.
func TestProcessBatch_EndToEnd_RealPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-pipeline e2e in -short mode")
	}
	srcDemo := repoDemoApp(t)
	demoDir := t.TempDir()
	copyGoModule(t, srcDemo, demoDir)

	sandbox, err := tools.NewSandbox(demoDir)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	patches := tools.NewPatchStore(sandbox, filepath.Join(t.TempDir(), "out"))
	ctrl := democtl.New(demoDir, "http://localhost:9090", "", "", patches)

	e := newTestEngine(&scriptedAgent{demoDir: demoDir}, ctrl)
	// Run apply + the real test/build gates; skip deploy so the test doesn't launch a
	// process or need git (the gates are what prove both fixes compile and pass).
	e.cfg.Stages.Deploy = false

	runBatchWith(e, "error:checkout-demo", "performance:report-demo")

	for _, id := range []string{"error:checkout-demo", "performance:report-demo"} {
		if p := phaseOf(e, id); p != "deployed" {
			t.Fatalf("%s phase = %q, want deployed (real go test/build gate should pass with both fixes)", id, p)
		}
	}

	// The single consolidated source now carries BOTH fixes.
	final, err := os.ReadFile(filepath.Join(demoDir, "main.go"))
	if err != nil {
		t.Fatalf("read patched main.go: %v", err)
	}
	got := string(final)
	if !strings.Contains(got, "http.StatusBadRequest") {
		t.Error("checkout bounds-check fix missing from final source")
	}
	if strings.Contains(got, "time.Sleep(") {
		t.Error("report perf fix not applied — per-item sleep still present")
	}
}

// repoDemoApp locates the repo's demo_app dir relative to this test file, skipping
// the test if the layout isn't available (e.g. an isolated CI checkout).
func repoDemoApp(t *testing.T) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate test file")
	}
	// .../backend/internal/autopilot/batch_e2e_test.go → repo root is three dirs up from backend.
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "demo_app")
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Skipf("demo_app not found at %s: %v", dir, err)
	}
	abs, _ := filepath.Abs(dir)
	return abs
}

// copyGoModule copies just the Go sources + module files needed to test/build the
// demo package (skips web/ and any built binaries).
func copyGoModule(t *testing.T, src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read demo_app: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !(strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
