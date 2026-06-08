package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PatchProposal is a human-reviewable fix produced by the agent. The agent fills
// UnifiedDiff (for display) and PatchedContent (the full new file). Nothing is
// written to disk until a human approves (see PatchStore.ApplyApproved).
type PatchProposal struct {
	File           string    `json:"file"`            // path relative to the sandbox root
	UnifiedDiff    string    `json:"unified_diff"`    // for human display in the diff viewer
	PatchedContent string    `json:"patched_content"` // full new file content; written on approval
	Rationale      string    `json:"rationale"`       // why this fixes the root cause
	CreatedAt      time.Time `json:"created_at"`
}

// StagedPatch is a proposed patch the user has added to the consolidation batch,
// tagged with the problem it fixes and when it was staged.
type StagedPatch struct {
	ProblemID     string    `json:"problemId"`
	StagedAt      time.Time `json:"stagedAt"`
	PatchProposal           // embedded: File / UnifiedDiff / PatchedContent / Rationale / CreatedAt
}

// PatchStore holds the latest proposed patch pending human approval, the most
// recent proposal per problem, and a consolidation batch the user can test/build/
// deploy together. It applies approved patches to an isolated output directory; it
// NEVER writes to the source tree, never merges, and never deploys — the
// human-in-the-loop boundary. (democtl applies staged patches to source at deploy.)
type PatchStore struct {
	mu       sync.Mutex
	sandbox  *Sandbox
	outDir   string
	latest   *PatchProposal           // most recent proposal (any problem) — autopilot/approve path
	proposed map[string]PatchProposal // most recent proposal per problemId (staging source)
	staged   map[string]StagedPatch   // the consolidation batch, keyed by problemId
}

// NewPatchStore creates a store that validates paths against sandbox and writes
// approved patches under outDir.
func NewPatchStore(sandbox *Sandbox, outDir string) *PatchStore {
	return &PatchStore{
		sandbox:  sandbox,
		outDir:   outDir,
		proposed: map[string]PatchProposal{},
		staged:   map[string]StagedPatch{},
	}
}

// Propose records a patch proposal after validating its target path is inside the
// sandbox. This is the propose_patch tool exposed to the agent.
func (p *PatchStore) Propose(prop PatchProposal) error {
	if prop.File == "" {
		return fmt.Errorf("propose_patch: file is required")
	}
	if prop.PatchedContent == "" {
		return fmt.Errorf("propose_patch: patched_content is required")
	}
	if _, err := p.sandbox.Resolve(prop.File); err != nil {
		return err
	}
	prop.CreatedAt = time.Now()
	p.mu.Lock()
	p.latest = &prop
	p.mu.Unlock()
	return nil
}

// Latest returns the pending proposal, or nil if none.
func (p *PatchStore) Latest() *PatchProposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest
}

// Clear discards any pending proposal (used by the Test Console reset). The
// staged batch is left intact so a reset doesn't silently drop queued work.
func (p *PatchStore) Clear() {
	p.mu.Lock()
	p.latest = nil
	p.mu.Unlock()
}

// SetProposed records the proposal produced when investigating a specific problem,
// so it can be staged later regardless of which problem was investigated most
// recently (the volatile `latest` is overwritten by every investigation).
func (p *PatchStore) SetProposed(problemID string, prop *PatchProposal) {
	if prop == nil || problemID == "" {
		return
	}
	p.mu.Lock()
	p.proposed[problemID] = *prop
	p.mu.Unlock()
}

// Stage adds the problem's most recent proposal to the consolidation batch. It
// errors if the problem hasn't been investigated this session (no proposal known).
func (p *PatchStore) Stage(problemID string) (StagedPatch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prop, ok := p.proposed[problemID]
	if !ok {
		return StagedPatch{}, fmt.Errorf("no proposed patch for %q — investigate it first", problemID)
	}
	sp := StagedPatch{ProblemID: problemID, StagedAt: time.Now(), PatchProposal: prop}
	p.staged[problemID] = sp
	return sp, nil
}

// Unstage removes a problem's patch from the batch.
func (p *PatchStore) Unstage(problemID string) {
	p.mu.Lock()
	delete(p.staged, problemID)
	p.mu.Unlock()
}

// ClearStaged empties the consolidation batch (after a successful deploy).
func (p *PatchStore) ClearStaged() {
	p.mu.Lock()
	p.staged = map[string]StagedPatch{}
	p.mu.Unlock()
}

// Staged returns the batch, oldest-staged first (stable order for the UI).
func (p *PatchStore) Staged() []StagedPatch {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]StagedPatch, 0, len(p.staged))
	for _, sp := range p.staged {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StagedAt.Before(out[j].StagedAt) })
	return out
}

// StagedForApply returns the proposals to write to source, deduped by file so two
// staged patches touching the same file don't double-write — the most recently
// staged one wins.
func (p *PatchStore) StagedForApply() []PatchProposal {
	staged := p.Staged() // oldest-first; later entries overwrite earlier per file
	byFile := map[string]PatchProposal{}
	for _, sp := range staged {
		byFile[sp.File] = sp.PatchProposal
	}
	// Preserve a stable order (by file) for deterministic output.
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	out := make([]PatchProposal, 0, len(files))
	for _, f := range files {
		out = append(out, byFile[f])
	}
	return out
}

// ApplyApproved writes the latest approved patch under the output directory.
func (p *PatchStore) ApplyApproved() (string, error) {
	p.mu.Lock()
	prop := p.latest
	p.mu.Unlock()
	if prop == nil {
		return "", fmt.Errorf("no patch has been proposed")
	}
	return p.WriteApproved(*prop)
}

// WriteApproved writes an approved patch (new file content + a .diff) under the
// output directory, mirroring the source-relative path. It returns the path
// written. It deliberately does not touch the source tree or deploy anything.
func (p *PatchStore) WriteApproved(prop PatchProposal) (string, error) {
	dest := filepath.Join(p.outDir, filepath.Clean(prop.File))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("create patch dir: %w", err)
	}
	if err := os.WriteFile(dest, []byte(prop.PatchedContent), 0o644); err != nil {
		return "", fmt.Errorf("write patched file: %w", err)
	}
	if prop.UnifiedDiff != "" {
		_ = os.WriteFile(dest+".diff", []byte(prop.UnifiedDiff), 0o644)
	}
	return dest, nil
}
