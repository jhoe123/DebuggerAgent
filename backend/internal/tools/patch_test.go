package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (*PatchStore, string, string) {
	t.Helper()
	srcRoot := t.TempDir()
	outDir := t.TempDir()
	sb, err := NewSandbox(srcRoot)
	if err != nil {
		t.Fatal(err)
	}
	return NewPatchStore(sb, outDir), srcRoot, outDir
}

func TestProposeValidatesInput(t *testing.T) {
	store, _, _ := newTestStore(t)
	if err := store.Propose(PatchProposal{File: "", PatchedContent: "x"}); err == nil {
		t.Error("expected error for empty file")
	}
	if err := store.Propose(PatchProposal{File: "main.go", PatchedContent: ""}); err == nil {
		t.Error("expected error for empty patched_content")
	}
	if err := store.Propose(PatchProposal{File: "../escape.go", PatchedContent: "x"}); err == nil {
		t.Error("expected error for path escaping sandbox")
	}
}

func TestApplyApprovedWritesToOutDirOnly(t *testing.T) {
	store, srcRoot, outDir := newTestStore(t)
	prop := PatchProposal{
		File:           "checkout.go",
		PatchedContent: "package main // fixed\n",
		UnifiedDiff:    "--- a/checkout.go\n+++ b/checkout.go\n",
		Rationale:      "bounds check added",
	}
	if err := store.Propose(prop); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	dest, err := store.ApplyApproved()
	if err != nil {
		t.Fatalf("ApplyApproved: %v", err)
	}

	// Patched file written under outDir, not the source tree.
	if got, _ := os.ReadFile(dest); string(got) != prop.PatchedContent {
		t.Errorf("patched content mismatch: %q", got)
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "checkout.go")); !os.IsNotExist(err) {
		t.Error("source tree must NOT be modified")
	}
	if _, err := os.Stat(dest + ".diff"); err != nil {
		t.Errorf("expected .diff sidecar: %v", err)
	}
	if filepath.Dir(dest) != filepath.Clean(outDir) {
		t.Errorf("patch written outside outDir: %s", dest)
	}
}

func TestApplyApprovedNoProposal(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.ApplyApproved(); err == nil {
		t.Error("expected error when no patch proposed")
	}
}

func TestStageRequiresProposal(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.Stage("error:svc"); err == nil {
		t.Error("expected error staging a problem with no proposal")
	}
	store.SetProposed("error:svc", &PatchProposal{File: "main.go", PatchedContent: "x"})
	if _, err := store.Stage("error:svc"); err != nil {
		t.Fatalf("Stage after SetProposed: %v", err)
	}
	if got := store.Staged(); len(got) != 1 || got[0].ProblemID != "error:svc" {
		t.Errorf("expected 1 staged patch for error:svc, got %+v", got)
	}
}

// TestProposedSurvivesRestart guards the "Add to batch -> 400 no proposed patch"
// regression: a proposal recorded by one server instance must be stage-able after a
// restart, since the investigation that produced it is persisted client-side and in
// the artifact store but the in-memory map is not.
func TestProposedSurvivesRestart(t *testing.T) {
	srcRoot := t.TempDir()
	outDir := t.TempDir()
	sb, err := NewSandbox(srcRoot)
	if err != nil {
		t.Fatal(err)
	}

	first := NewPatchStore(sb, outDir)
	prop := &PatchProposal{File: "main.go", PatchedContent: "package main // fixed\n", Rationale: "bounds check"}
	first.SetProposed("error:checkout-demo", prop)

	// New store over the same outDir == a server restart.
	restarted := NewPatchStore(sb, outDir)
	sp, err := restarted.Stage("error:checkout-demo")
	if err != nil {
		t.Fatalf("Stage after restart: %v", err)
	}
	if sp.PatchedContent != prop.PatchedContent {
		t.Errorf("reloaded content mismatch: got %q", sp.PatchedContent)
	}
}

func TestStagedForApplyDedupesByFile(t *testing.T) {
	store, _, _ := newTestStore(t)
	store.SetProposed("error:a", &PatchProposal{File: "main.go", PatchedContent: "first"})
	store.SetProposed("perf:b", &PatchProposal{File: "report.go", PatchedContent: "report"})
	store.SetProposed("error:c", &PatchProposal{File: "main.go", PatchedContent: "second"})
	if _, err := store.Stage("error:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage("perf:b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage("error:c"); err != nil { // same file as error:a, staged later → wins
		t.Fatal(err)
	}
	apply := store.StagedForApply()
	if len(apply) != 2 {
		t.Fatalf("expected 2 files after dedupe, got %d: %+v", len(apply), apply)
	}
	for _, p := range apply {
		if p.File == "main.go" && p.PatchedContent != "second" {
			t.Errorf("expected latest-staged content for main.go, got %q", p.PatchedContent)
		}
	}
}

func TestUnstageAndClearStaged(t *testing.T) {
	store, _, _ := newTestStore(t)
	store.SetProposed("error:a", &PatchProposal{File: "a.go", PatchedContent: "x"})
	store.SetProposed("error:b", &PatchProposal{File: "b.go", PatchedContent: "y"})
	_, _ = store.Stage("error:a")
	_, _ = store.Stage("error:b")
	store.Unstage("error:a")
	if got := store.Staged(); len(got) != 1 || got[0].ProblemID != "error:b" {
		t.Errorf("expected only error:b after Unstage, got %+v", got)
	}
	store.ClearStaged()
	if got := store.Staged(); len(got) != 0 {
		t.Errorf("expected empty batch after ClearStaged, got %+v", got)
	}
}
