// Package gitsource manages an external Git source repository PatchPilot patches.
// It clones the repo, points the read_source sandbox and the local pipeline at the
// clone, creates an isolated branch per fix, commits patches to it, and — only when a
// human confirms a fix — merges that branch into the working branch and deletes it.
//
// Push to the remote is permission-gated (PushEnabled); with it off, all branch /
// commit / merge work stays local. The auth token (HTTPS PAT) is a secret: it is
// injected per git invocation (never written to .git/config or the URL, never logged)
// and never returned by any status call. It is a leaf package (imports only
// internal/api and internal/artifact) so the agent, democtl, and the server can share
// it without an import cycle.
package gitsource

import (
	"strings"
	"sync"

	"github.com/patchpilot/backend/internal/api"
)

// Config is the runtime Git source configuration (the secret token lives only here).
type Config struct {
	RepoURL            string
	AuthToken          string // secret HTTPS PAT
	WorkingBranch      string
	BranchPrefix       string
	BranchPerFix       bool
	AutoMergeOnConfirm bool
	PushEnabled        bool
	CommitAuthorName   string
	CommitAuthorEmail  string
	CloneDir           string
}

// Store is a thread-safe holder for the Git source Config.
type Store struct {
	mu sync.Mutex
	c  Config
}

// New returns a Store seeded from env-derived defaults, filling sensible blanks.
func New(seed Config) *Store {
	if seed.WorkingBranch == "" {
		seed.WorkingBranch = "main"
	}
	if seed.BranchPrefix == "" {
		seed.BranchPrefix = "patchpilot/fix-"
	}
	if seed.CommitAuthorName == "" {
		seed.CommitAuthorName = "PatchPilot"
	}
	if seed.CommitAuthorEmail == "" {
		seed.CommitAuthorEmail = "patchpilot@local"
	}
	return &Store{c: seed}
}

// Get returns a copy of the current config.
func (st *Store) Get() Config {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.c
}

// Set merges an incoming API config. Non-empty string fields replace the stored
// value; an empty AuthToken leaves the stored secret unchanged (mirrors slack.SetConfig)
// so toggling a flag never wipes the PAT. The three bool flags are taken as-is (the UI
// always submits the full form). Returns the merged config.
func (st *Store) Set(in api.GitSourceConfig) Config {
	st.mu.Lock()
	defer st.mu.Unlock()
	if v := strings.TrimSpace(in.RepoURL); v != "" {
		st.c.RepoURL = v
	}
	if v := strings.TrimSpace(in.AuthToken); v != "" {
		st.c.AuthToken = v
	}
	if v := strings.TrimSpace(in.WorkingBranch); v != "" {
		st.c.WorkingBranch = v
	}
	if v := strings.TrimSpace(in.BranchPrefix); v != "" {
		st.c.BranchPrefix = v
	}
	if v := strings.TrimSpace(in.CommitAuthorName); v != "" {
		st.c.CommitAuthorName = v
	}
	if v := strings.TrimSpace(in.CommitAuthorEmail); v != "" {
		st.c.CommitAuthorEmail = v
	}
	if v := strings.TrimSpace(in.CloneDir); v != "" {
		st.c.CloneDir = v
	}
	st.c.BranchPerFix = in.BranchPerFix
	st.c.AutoMergeOnConfirm = in.AutoMergeOnConfirm
	st.c.PushEnabled = in.PushEnabled
	return st.c
}
