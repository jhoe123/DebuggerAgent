package gitsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestCanonRepoURL(t *testing.T) {
	same := [][2]string{
		{"https://github.com/Org/Repo.git", "https://github.com/org/repo"},
		{"https://user:tok@github.com/o/r.git", "https://github.com/o/r"},
		{"https://github.com/o/r/", "https://github.com/o/r"},
		{`C:\work\repo.git`, "c:/work/repo"},
	}
	for _, c := range same {
		if !sameRepoURL(c[0], c[1]) {
			t.Errorf("sameRepoURL(%q, %q) = false, want true", c[0], c[1])
		}
	}
	if sameRepoURL("https://github.com/o/r", "https://github.com/o/r2") {
		t.Error("different repos must not compare equal")
	}
}

// TargetChange must mirror Store.Set's "empty keeps existing" semantics, ignore the
// masked-preview round trip, and report nothing when no clone exists on disk.
func TestTargetChange(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	st := New(Config{RepoURL: "https://u:tok@github.com/o/r.git", WorkingBranch: "main", CloneDir: clone})
	m := NewManager(st, nil, true, nil)

	if r, b := m.TargetChange(api.GitSourceConfig{RepoURL: "https://github.com/other/x"}); r || b {
		t.Fatalf("no clone on disk must report no change, got repo=%v branch=%v", r, b)
	}
	// Fabricate a clone — isGitRepo only checks for a .git entry.
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, b := m.TargetChange(api.GitSourceConfig{RepoURL: "https://github.com/o/r", WorkingBranch: "main"}); r || b {
		t.Fatalf("masked-preview round trip must not read as a change, got repo=%v branch=%v", r, b)
	}
	if r, b := m.TargetChange(api.GitSourceConfig{}); r || b {
		t.Fatalf("empty fields keep existing values, got repo=%v branch=%v", r, b)
	}
	if r, _ := m.TargetChange(api.GitSourceConfig{RepoURL: "https://github.com/other/x"}); !r {
		t.Fatal("new repo URL must report repoChanged")
	}
	if _, b := m.TargetChange(api.GitSourceConfig{WorkingBranch: "develop"}); !b {
		t.Fatal("new working branch must report branchChanged")
	}
}

// Re-targeting the config and reconnecting must wipe a clone whose origin points at
// the OLD repository (never fetch into it) and emit a visible "clean" step.
func TestConnect_WipesOnOriginMismatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	originA := seedBareOrigin(t, "a.txt", "A")
	originB := seedBareOrigin(t, "b.txt", "B")
	clone := filepath.Join(t.TempDir(), "clone")
	st := New(Config{RepoURL: originA, WorkingBranch: "main", CloneDir: clone})
	m := NewManager(st, nil, true, nil)
	if _, err := m.Connect(ctx, nil); err != nil {
		t.Fatalf("connect A: %v", err)
	}
	marker := filepath.Join(clone, "marker.txt")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	st.Set(api.GitSourceConfig{RepoURL: originB})
	var stages []string
	if _, err := m.Connect(ctx, func(s api.Step) { stages = append(stages, s.Stage) }); err != nil {
		t.Fatalf("connect B: %v", err)
	}
	cleaned := false
	for _, s := range stages {
		if s == "clean" {
			cleaned = true
		}
	}
	if !cleaned {
		t.Fatalf("expected a clean step in the timeline, got %v", stages)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("old workspace file survived the wipe")
	}
	out, err := m.runGit(ctx, clone, false, "remote", "get-url", "origin")
	if err != nil || !sameRepoURL(strings.TrimSpace(out), originB) {
		t.Fatalf("origin = %q, want %q", strings.TrimSpace(out), originB)
	}
	if _, err := os.Stat(filepath.Join(clone, "b.txt")); err != nil {
		t.Fatal("new repo content missing after re-clone")
	}
	if !m.IsConnected() {
		t.Fatal("not connected after re-target")
	}
}

// ResetWorkspace must delete the clone and disconnect; a fresh Connect then re-clones.
func TestResetWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	origin := seedBareOrigin(t, "a.txt", "A")
	clone := filepath.Join(t.TempDir(), "clone")
	st := New(Config{RepoURL: origin, WorkingBranch: "main", CloneDir: clone})
	m := NewManager(st, nil, true, nil)
	if _, err := m.Connect(ctx, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := m.ResetWorkspace(); err != nil {
		t.Fatalf("reset workspace: %v", err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("clone dir still on disk after reset")
	}
	if m.IsConnected() {
		t.Fatal("still connected after reset")
	}
	if len(m.Status(ctx).Branches) != 0 {
		t.Fatal("branch map must be empty after reset")
	}

	if _, err := m.Connect(ctx, nil); err != nil {
		t.Fatalf("fresh connect after reset: %v", err)
	}
	if !m.IsConnected() {
		t.Fatal("fresh connect did not reconnect")
	}
}

// seedBareOrigin creates a bare origin repo seeded with one commit (file=content)
// on main and returns its path.
func seedBareOrigin(t *testing.T, file, content string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, "", "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, "", "clone", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	runGitT(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "seed")
	runGitT(t, seed, "push", "origin", "main")
	return origin
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
