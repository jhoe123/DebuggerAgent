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
