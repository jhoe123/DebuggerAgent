package gitsource

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
)

// Manager owns the cloned working tree and performs all git operations against it.
// A single mutex serializes operations because the working tree is shared (the same
// single-worker assumption as the autopilot).
type Manager struct {
	store   *Store
	arts    *artifact.Store
	onRoot  func(root string) error // swap the active source root (agent sandbox + democtl)
	gitOK   bool                    // git is on PATH
	enabled bool                    // ENABLE_GIT_SOURCE — mutating ops allowed

	mu        sync.Mutex
	connected bool
	branches  map[string]string // problemID -> fix branch
	lastErr   string
}

// NewManager builds the manager. onRoot (may be nil) re-points the read_source
// sandbox and the local pipeline at the clone after a successful connect. It probes
// for the git binary and rehydrates the branch map from durable artifacts.
func NewManager(store *Store, arts *artifact.Store, enabled bool, onRoot func(root string) error) *Manager {
	m := &Manager{
		store:    store,
		arts:     arts,
		onRoot:   onRoot,
		enabled:  enabled,
		gitOK:    gitAvailable(),
		branches: map[string]string{},
	}
	if arts != nil {
		for _, a := range arts.List() {
			if a.FixBranch != "" && !a.Confirmed {
				m.branches[a.ProblemID] = a.FixBranch
			}
		}
	}
	return m
}

// Available reports whether the git binary is present and the feature is enabled.
func (m *Manager) Available() bool { return m.gitOK && m.enabled }

// IsConnected reports whether a clone is active (cheap; no git call).
func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// Config exposes the current config (the server reads BranchPerFix for its hooks).
func (m *Manager) Config() Config { return m.store.Get() }

// SetConfig merges a config update (an empty token preserves the stored secret).
func (m *Manager) SetConfig(in api.GitSourceConfig) { m.store.Set(in) }

// Connect clones the repo (or fetches if already cloned), checks out the working
// branch, and re-points the active source root at the clone. emit (optional) receives
// progress steps. Returns the resolved clone dir.
func (m *Manager) Connect(ctx context.Context, emit func(api.Step)) (string, error) {
	step := func(s api.Step) {
		if emit != nil {
			emit(s)
		}
	}
	if !m.gitOK {
		return "", fmt.Errorf("git is not installed on this host")
	}
	if !m.enabled {
		return "", fmt.Errorf("git source is disabled (set ENABLE_GIT_SOURCE=true)")
	}
	cfg := m.store.Get()
	if strings.TrimSpace(cfg.RepoURL) == "" {
		return "", fmt.Errorf("no repository URL configured")
	}
	dir := strings.TrimSpace(cfg.CloneDir)
	if dir == "" {
		return "", fmt.Errorf("no clone directory configured")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if isGitRepo(dir) {
		step(api.Step{Stage: "fetch", Status: "running", Message: "Fetching latest from origin…"})
		if out, err := m.runGit(ctx, dir, true, "fetch", "origin", "--prune"); err != nil {
			return m.failConnect(step, "fetch", out, err)
		}
	} else {
		step(api.Step{Stage: "clone", Status: "running", Message: "Cloning " + maskRepoURL(cfg.RepoURL) + "…"})
		_ = os.MkdirAll(filepath.Dir(dir), 0o755)
		_ = os.RemoveAll(dir) // a partial/empty dir would break a fresh clone
		if out, err := m.runGit(ctx, "", true, "clone", cfg.RepoURL, dir); err != nil {
			return m.failConnect(step, "clone", out, err)
		}
		step(api.Step{Stage: "clone", Status: "ok", Message: "Cloned into working directory"})
	}

	if err := m.ensureWorkingBranch(ctx, dir, cfg.WorkingBranch); err != nil {
		m.connected = false
		m.lastErr = err.Error()
		step(api.Step{Stage: "checkout", Status: "fail", Message: err.Error()})
		return dir, err
	}
	// Commit identity for merges/commits.
	_, _ = m.runGit(ctx, dir, false, "config", "user.name", cfg.CommitAuthorName)
	_, _ = m.runGit(ctx, dir, false, "config", "user.email", cfg.CommitAuthorEmail)

	m.connected = true
	m.lastErr = ""
	m.rehydrateBranches(ctx, dir, cfg.BranchPrefix)

	if m.onRoot != nil {
		if err := m.onRoot(dir); err != nil {
			m.lastErr = "source root: " + err.Error()
			step(api.Step{Stage: "checkout", Status: "fail", Message: m.lastErr})
			return dir, err
		}
	}
	step(api.Step{Stage: "checkout", Status: "ok", Message: "On " + cfg.WorkingBranch + " — source root now points at the clone"})
	return dir, nil
}

// CreateFixBranch ensures a per-problem fix branch exists (off the working branch)
// and is checked out. Returns the branch name. Idempotent if it already exists.
func (m *Manager) CreateFixBranch(ctx context.Context, problemID string) (string, error) {
	if err := m.requireReady(); err != nil {
		return "", err
	}
	cfg := m.store.Get()
	dir := cfg.CloneDir
	branch := m.branchName(problemID)

	m.mu.Lock()
	defer m.mu.Unlock()
	if out, err := m.runGit(ctx, dir, false, "checkout", cfg.WorkingBranch); err != nil {
		return "", fmt.Errorf("checkout %s: %s", cfg.WorkingBranch, strings.TrimSpace(out))
	}
	if _, err := m.runGit(ctx, dir, false, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		_, _ = m.runGit(ctx, dir, false, "checkout", branch)
	} else if out, err := m.runGit(ctx, dir, false, "checkout", "-b", branch); err != nil {
		return "", fmt.Errorf("create branch %s: %s", branch, strings.TrimSpace(out))
	}
	m.branches[problemID] = branch
	if m.arts != nil {
		m.arts.RecordFixBranch(problemID, branch, false)
	}
	return branch, nil
}

// CommitPatch writes the given files (relpath -> full content) onto the problem's fix
// branch — recreated deterministically from the working branch so the branch holds
// exactly this fix regardless of prior working-tree state — commits them, and pushes
// when PushEnabled. Returns whether it pushed and the branch. A no-op when the content
// already matches the working branch.
func (m *Manager) CommitPatch(ctx context.Context, problemID string, files map[string]string, message string) (pushed bool, branch string, err error) {
	if rerr := m.requireReady(); rerr != nil {
		return false, "", rerr
	}
	cfg := m.store.Get()
	dir := cfg.CloneDir
	branch = m.branchName(problemID)

	m.mu.Lock()
	defer m.mu.Unlock()
	// Clean the working tree to the working branch, then (re)create the fix branch at
	// its tip — so the branch contains only this fix, even after a pipeline applied
	// patches to the shared clone.
	if out, e := m.runGit(ctx, dir, false, "checkout", "-f", cfg.WorkingBranch); e != nil {
		return false, branch, fmt.Errorf("checkout %s: %s", cfg.WorkingBranch, strings.TrimSpace(out))
	}
	if out, e := m.runGit(ctx, dir, false, "checkout", "-B", branch, cfg.WorkingBranch); e != nil {
		return false, branch, fmt.Errorf("create branch %s: %s", branch, strings.TrimSpace(out))
	}
	rels := make([]string, 0, len(files))
	for rel, content := range files {
		if strings.TrimSpace(rel) == "" {
			continue
		}
		dest := filepath.Join(dir, filepath.Clean(rel))
		if e := os.MkdirAll(filepath.Dir(dest), 0o755); e != nil {
			return false, branch, fmt.Errorf("write %s: %w", rel, e)
		}
		if e := os.WriteFile(dest, []byte(content), 0o644); e != nil {
			return false, branch, fmt.Errorf("write %s: %w", rel, e)
		}
		rels = append(rels, filepath.ToSlash(filepath.Clean(rel)))
	}
	if len(rels) == 0 {
		m.branches[problemID] = branch
		return false, branch, nil
	}
	if out, e := m.runGit(ctx, dir, false, append([]string{"add", "--"}, rels...)...); e != nil {
		return false, branch, fmt.Errorf("git add: %s", strings.TrimSpace(out))
	}
	// Nothing differs from the working branch => nothing to commit.
	if _, e := m.runGit(ctx, dir, false, "diff", "--cached", "--quiet"); e == nil {
		m.branches[problemID] = branch
		if m.arts != nil {
			m.arts.RecordFixBranch(problemID, branch, false)
		}
		return false, branch, nil
	}
	if message == "" {
		message = "PatchPilot fix for " + problemID
	}
	if out, e := m.runGit(ctx, dir, false,
		"-c", "user.name="+cfg.CommitAuthorName,
		"-c", "user.email="+cfg.CommitAuthorEmail,
		"commit", "-m", message); e != nil {
		return false, branch, fmt.Errorf("git commit: %s", strings.TrimSpace(out))
	}
	m.branches[problemID] = branch
	if cfg.PushEnabled {
		if out, e := m.runGit(ctx, dir, true, "push", "-u", "origin", branch); e != nil {
			return false, branch, fmt.Errorf("git push: %s", strings.TrimSpace(out))
		}
		pushed = true
	}
	if m.arts != nil {
		m.arts.RecordFixBranch(problemID, branch, pushed)
	}
	return pushed, branch, nil
}

// ConfirmFix merges a problem's fix branch into the working branch (a human action),
// pushes the working branch when PushEnabled, and — when AutoMergeOnConfirm — deletes
// the now-merged fix branch (local + remote). It is the ONLY path that merges, and it
// records the confirmation on the durable artifact.
func (m *Manager) ConfirmFix(ctx context.Context, problemID string, emit func(api.Step)) (api.ConfirmFixResult, error) {
	step := func(s api.Step) {
		if emit != nil {
			emit(s)
		}
	}
	res := api.ConfirmFixResult{ProblemID: problemID}
	if err := m.requireReady(); err != nil {
		return res, err
	}
	cfg := m.store.Get()
	dir := cfg.CloneDir
	branch := m.branchName(problemID)
	res.IntoBranch = cfg.WorkingBranch
	res.MergedBranch = branch

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.runGit(ctx, dir, false, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return res, fmt.Errorf("no fix branch %q to merge — has a fix been committed for this problem?", branch)
	}
	step(api.Step{Stage: "merge", Status: "running", Message: "Merging " + branch + " into " + cfg.WorkingBranch})
	if out, err := m.runGit(ctx, dir, false, "checkout", cfg.WorkingBranch); err != nil {
		return res, fmt.Errorf("checkout %s: %s", cfg.WorkingBranch, strings.TrimSpace(out))
	}
	if out, err := m.runGit(ctx, dir, false, "merge", "--no-ff", "-m", "Merge fix "+branch+" (confirmed)", branch); err != nil {
		_, _ = m.runGit(ctx, dir, false, "merge", "--abort") // keep the tree clean
		return res, fmt.Errorf("merge conflict — aborted; resolve manually: %s", strings.TrimSpace(out))
	}
	res.Merged = true
	step(api.Step{Stage: "merge", Status: "ok", Message: "Merged into " + cfg.WorkingBranch})

	if cfg.PushEnabled {
		if out, err := m.runGit(ctx, dir, true, "push", "origin", cfg.WorkingBranch); err != nil {
			return res, fmt.Errorf("push %s: %s", cfg.WorkingBranch, strings.TrimSpace(out))
		}
		res.Pushed = true
		step(api.Step{Stage: "push", Status: "ok", Message: "Pushed " + cfg.WorkingBranch + " to origin"})
	}
	if cfg.AutoMergeOnConfirm {
		m.deleteBranch(ctx, dir, branch, cfg.PushEnabled, step)
	}
	delete(m.branches, problemID)
	if m.arts != nil {
		m.arts.RecordConfirmed(problemID, res.Pushed)
	}
	res.Detail = "fix confirmed and merged into " + cfg.WorkingBranch
	return res, nil
}

// CleanupConfirmed deletes the fix branches of all confirmed problems (local +
// remote when push is enabled). Returns the branches removed.
func (m *Manager) CleanupConfirmed(ctx context.Context) ([]string, error) {
	if err := m.requireReady(); err != nil {
		return nil, err
	}
	cfg := m.store.Get()
	dir := cfg.CloneDir

	m.mu.Lock()
	defer m.mu.Unlock()
	_, _ = m.runGit(ctx, dir, false, "checkout", cfg.WorkingBranch) // don't sit on a branch we delete
	var removed []string
	if m.arts == nil {
		return removed, nil
	}
	for _, a := range m.arts.List() {
		if !a.Confirmed || a.FixBranch == "" {
			continue
		}
		if _, err := m.runGit(ctx, dir, false, "rev-parse", "--verify", "refs/heads/"+a.FixBranch); err == nil {
			m.deleteBranch(ctx, dir, a.FixBranch, cfg.PushEnabled, func(api.Step) {})
			removed = append(removed, a.FixBranch)
		}
		delete(m.branches, a.ProblemID)
	}
	return removed, nil
}

// Status returns the display-safe status (the token + repo creds are never included).
func (m *Manager) Status(ctx context.Context) api.GitSourceStatus {
	cfg := m.store.Get()
	st := api.GitSourceStatus{
		Enabled:            m.gitOK && m.enabled,
		Configured:         strings.TrimSpace(cfg.RepoURL) != "",
		TokenConfigured:    cfg.AuthToken != "",
		RepoURLPreview:     maskRepoURL(cfg.RepoURL),
		WorkingBranch:      cfg.WorkingBranch,
		BranchPrefix:       cfg.BranchPrefix,
		BranchPerFix:       cfg.BranchPerFix,
		AutoMergeOnConfirm: cfg.AutoMergeOnConfirm,
		PushEnabled:        cfg.PushEnabled,
		CommitAuthorName:   cfg.CommitAuthorName,
		CommitAuthorEmail:  cfg.CommitAuthorEmail,
		Branches:           []api.GitFixBranch{},
	}
	dir := cfg.CloneDir

	m.mu.Lock()
	st.LastError = m.lastErr
	connected := m.connected
	for pid, b := range m.branches {
		st.Branches = append(st.Branches, api.GitFixBranch{Name: b, ProblemID: pid})
	}
	m.mu.Unlock()

	if connected && isGitRepo(dir) {
		st.Connected = true
		if out, err := m.runGit(ctx, dir, false, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			st.CurrentBranch = strings.TrimSpace(out)
		}
		if out, err := m.runGit(ctx, dir, false, "status", "--porcelain"); err == nil {
			st.Dirty = strings.TrimSpace(out) != ""
		}
	}
	sort.Slice(st.Branches, func(i, j int) bool { return st.Branches[i].Name < st.Branches[j].Name })
	return st
}

// --- internals ---

// requireReady guards mutating ops: git present, feature enabled, and connected.
// It must be called BEFORE taking m.mu (it locks internally to read `connected`).
func (m *Manager) requireReady() error {
	if !m.gitOK {
		return fmt.Errorf("git is not installed on this host")
	}
	if !m.enabled {
		return fmt.Errorf("git source is disabled (set ENABLE_GIT_SOURCE=true)")
	}
	m.mu.Lock()
	connected := m.connected
	m.mu.Unlock()
	if !connected {
		return fmt.Errorf("git source is not connected — connect a repository first")
	}
	return nil
}

// failConnect records the failure and returns it (caller holds m.mu).
func (m *Manager) failConnect(step func(api.Step), stage, out string, err error) (string, error) {
	m.connected = false
	m.lastErr = err.Error()
	step(api.Step{Stage: stage, Status: "fail", Message: "Git " + stage + " failed", Detail: strings.TrimSpace(out)})
	return "", err
}

// ensureWorkingBranch checks out the working branch, creating/tracking it as needed,
// then best-effort fast-forwards to origin. Caller holds m.mu.
func (m *Manager) ensureWorkingBranch(ctx context.Context, dir, branch string) error {
	switch {
	case verifyRef(ctx, m, dir, "refs/heads/"+branch):
		if out, err := m.runGit(ctx, dir, false, "checkout", branch); err != nil {
			return fmt.Errorf("checkout %s: %s", branch, strings.TrimSpace(out))
		}
	case verifyRef(ctx, m, dir, "refs/remotes/origin/"+branch):
		if out, err := m.runGit(ctx, dir, false, "checkout", "-b", branch, "origin/"+branch); err != nil {
			return fmt.Errorf("checkout %s: %s", branch, strings.TrimSpace(out))
		}
	default:
		if out, err := m.runGit(ctx, dir, false, "checkout", "-b", branch); err != nil {
			return fmt.Errorf("create %s: %s", branch, strings.TrimSpace(out))
		}
	}
	_, _ = m.runGit(ctx, dir, true, "pull", "--ff-only", "origin", branch)
	return nil
}

// rehydrateBranches repopulates the in-memory problemID->branch map after a (re)connect
// by matching local branches against not-yet-confirmed artifacts. Caller holds m.mu.
func (m *Manager) rehydrateBranches(ctx context.Context, dir, prefix string) {
	if m.arts == nil {
		return
	}
	live := map[string]bool{}
	if out, err := m.runGit(ctx, dir, false, "branch", "--list", prefix+"*"); err == nil {
		for _, ln := range strings.Split(out, "\n") {
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "* "))
			if name != "" {
				live[name] = true
			}
		}
	}
	for _, a := range m.arts.List() {
		if a.FixBranch != "" && !a.Confirmed && live[a.FixBranch] {
			m.branches[a.ProblemID] = a.FixBranch
		}
	}
}

// deleteBranch deletes a merged fix branch locally (and remotely when push is on).
// Caller holds m.mu.
func (m *Manager) deleteBranch(ctx context.Context, dir, branch string, remote bool, step func(api.Step)) {
	if out, err := m.runGit(ctx, dir, false, "branch", "-d", branch); err != nil {
		step(api.Step{Stage: "cleanup", Status: "info", Message: "Could not delete local branch " + branch, Detail: strings.TrimSpace(out)})
	} else {
		step(api.Step{Stage: "cleanup", Status: "ok", Message: "Deleted fix branch " + branch})
	}
	if remote {
		_, _ = m.runGit(ctx, dir, true, "push", "origin", "--delete", branch)
	}
}

func (m *Manager) branchName(problemID string) string {
	cfg := m.store.Get()
	prefix := cfg.BranchPrefix
	if prefix == "" {
		prefix = "patchpilot/fix-"
	}
	return prefix + slugify(problemID)
}

// runGit runs `git -C dir args...`. When withToken is true and a PAT is configured, it
// injects an HTTPS Authorization header for that single invocation (clone/fetch/push)
// so the token is used without being written to .git/config or the URL. The token is
// never included in returned output or error messages.
func (m *Manager) runGit(ctx context.Context, dir string, withToken bool, args ...string) (string, error) {
	cfg := m.store.Get()
	var full []string
	if withToken && cfg.AuthToken != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + cfg.AuthToken))
		full = append(full, "-c", "http.extraHeader=Authorization: Basic "+auth)
	}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		return out, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// verifyRef reports whether a ref exists in the clone.
func verifyRef(ctx context.Context, m *Manager, dir, ref string) bool {
	_, err := m.runGit(ctx, dir, false, "rev-parse", "--verify", ref)
	return err == nil
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func isGitRepo(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// maskRepoURL returns a display-safe form of a repo URL: host + path with any embedded
// userinfo (e.g. https://user:token@host/..) stripped. Never reveals a token.
func maskRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		scheme, rest := u[:i+3], u[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		return scheme + rest
	}
	return u
}

// slugify makes a problemId safe as a git branch component: only [A-Za-z0-9_-], with
// runs of other characters collapsed to a single dash and trimmed.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "fix"
	}
	return out
}
