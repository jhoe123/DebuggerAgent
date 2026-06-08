package gitsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseLsRemote(t *testing.T) {
	out := "ref: refs/heads/main\tHEAD\n" +
		"deadbeef\tHEAD\n" +
		"deadbeef\trefs/heads/main\n" +
		"cafe1234\trefs/heads/dev\n" +
		"aaaa1111\trefs/heads/feature/login\n" +
		"bbbb2222\trefs/tags/v1.0\n" +
		"cccc3333\trefs/tags/v1.0^{}\n"
	branches, def := parseLsRemote(out)
	if def != "main" {
		t.Fatalf("default branch = %q, want main", def)
	}
	want := []string{"dev", "feature/login", "main"} // sorted, tags excluded
	if len(branches) != len(want) {
		t.Fatalf("branches = %v, want %v", branches, want)
	}
	for i := range want {
		if branches[i] != want[i] {
			t.Fatalf("branches = %v, want %v", branches, want)
		}
	}
}

// TestValidateRemoteAndBaseBranch exercises ValidateRemote against a real local repo and
// confirms a new working branch is created off a chosen base at connect. Skipped when git
// is unavailable.
func TestValidateRemoteAndBaseBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()

	// An "origin" bare repo with two branches: main (seed) and dev (extra file).
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, "", "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, "", "clone", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "seed")
	runGitT(t, seed, "push", "origin", "main")
	runGitT(t, seed, "checkout", "-b", "dev")
	if err := os.WriteFile(filepath.Join(seed, "dev.txt"), []byte("on dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "dev work")
	runGitT(t, seed, "push", "origin", "dev")

	// Seed a NEW working branch ("feature-x") to be created off base "dev" at connect.
	clone := filepath.Join(t.TempDir(), "clone")
	st := New(Config{
		RepoURL: origin, WorkingBranch: "feature-x", BaseBranch: "dev",
		BranchPrefix: "patchpilot/fix-", CommitAuthorName: "PatchPilot", CommitAuthorEmail: "pp@test",
		CloneDir: clone,
	})
	m := NewManager(st, nil, true, nil)

	// Validate lists both branches and identifies the default.
	res := m.ValidateRemote(ctx, origin, "")
	if !res.Valid {
		t.Fatalf("ValidateRemote not valid: %+v", res)
	}
	if res.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", res.DefaultBranch)
	}
	if !hasString(res.Branches, "main") || !hasString(res.Branches, "dev") {
		t.Fatalf("branches missing main/dev: %v", res.Branches)
	}

	if _, err := m.Connect(ctx, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if out, _ := m.runGit(ctx, clone, false, "rev-parse", "--abbrev-ref", "HEAD"); trimNL(out) != "feature-x" {
		t.Fatalf("current branch = %q, want feature-x", trimNL(out))
	}
	// feature-x branched off dev, so dev.txt must be present.
	if _, err := os.Stat(filepath.Join(clone, "dev.txt")); err != nil {
		t.Fatalf("new working branch not based on dev (dev.txt missing): %v", err)
	}
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
