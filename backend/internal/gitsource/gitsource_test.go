package gitsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
)

func TestStoreSetPreservesTokenAndMergesFlags(t *testing.T) {
	st := New(Config{})
	st.Set(api.GitSourceConfig{
		RepoURL: "https://example.com/repo.git", AuthToken: "secret-pat",
		WorkingBranch: "main", BranchPerFix: true, AutoMergeOnConfirm: true, PushEnabled: true,
	})
	// An empty token must NOT wipe the stored secret; flags are taken as-is.
	c := st.Set(api.GitSourceConfig{AuthToken: "", PushEnabled: false, BranchPerFix: true, AutoMergeOnConfirm: false})
	if c.AuthToken != "secret-pat" {
		t.Fatalf("empty token wiped the secret: %q", c.AuthToken)
	}
	if c.PushEnabled || c.AutoMergeOnConfirm || !c.BranchPerFix {
		t.Fatalf("flags not merged as-is: %+v", c)
	}
	if c.RepoURL != "https://example.com/repo.git" {
		t.Fatalf("repo URL lost on partial update: %q", c.RepoURL)
	}
}

func TestSlugifyAndMask(t *testing.T) {
	if got := slugify("error:checkout 404/x"); got != "error-checkout-404-x" {
		t.Fatalf("slugify = %q", got)
	}
	if got := maskRepoURL("https://user:tok@github.com/o/r.git"); got != "https://github.com/o/r.git" {
		t.Fatalf("maskRepoURL leaked creds: %q", got)
	}
}

// TestBranchPerFixLifecycle exercises connect → create branch → commit → confirm
// (merge + delete) against a real local repo. Skipped when git is unavailable.
func TestBranchPerFixLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()

	// An "origin" bare repo seeded with one commit on main.
	origin := filepath.Join(t.TempDir(), "origin.git")
	work := t.TempDir()
	runGitT(t, "", "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, "", "clone", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\nfunc main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "seed")
	runGitT(t, seed, "push", "origin", "main")

	clone := filepath.Join(work, "clone")
	arts := artifact.New("", false)
	var rooted string
	st := New(Config{
		RepoURL: origin, WorkingBranch: "main", BranchPrefix: "patchpilot/fix-",
		BranchPerFix: true, AutoMergeOnConfirm: true, PushEnabled: true,
		CommitAuthorName: "PatchPilot", CommitAuthorEmail: "pp@test", CloneDir: clone,
	})
	m := NewManager(st, arts, true, func(root string) error { rooted = root; return nil })

	if _, err := m.Connect(ctx, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if rooted != clone {
		t.Fatalf("onRoot not called with clone dir: %q", rooted)
	}

	branch, err := m.CreateFixBranch(ctx, "error:checkout")
	if err != nil {
		t.Fatalf("create fix branch: %v", err)
	}
	files := map[string]string{"main.go": "package main\nfunc main(){/*fixed*/}\n"}
	if _, _, err := m.CommitPatch(ctx, "error:checkout", files, "fix checkout"); err != nil {
		t.Fatalf("commit patch: %v", err)
	}

	res, err := m.ConfirmFix(ctx, "error:checkout", nil)
	if err != nil {
		t.Fatalf("confirm fix: %v", err)
	}
	if !res.Merged {
		t.Fatalf("expected merged, got %+v", res)
	}
	// The fix branch must be gone after an auto-merge confirm.
	if out, _ := m.runGit(ctx, clone, false, "branch", "--list", branch); out != "" {
		t.Fatalf("fix branch still present after confirm: %q", out)
	}
	// The working branch must contain the fix.
	got, _ := os.ReadFile(filepath.Join(clone, "main.go"))
	if !contains(string(got), "fixed") {
		t.Fatalf("working tree missing the merged fix: %q", got)
	}
	// The artifact records the confirmation.
	for _, a := range arts.List() {
		if a.ProblemID == "error:checkout" && (!a.Confirmed || a.Overall != "confirmed") {
			t.Fatalf("artifact not marked confirmed: %+v", a)
		}
	}
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
