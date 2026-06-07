package tools

import (
	"fmt"
	"os"
	"path/filepath"
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

// PatchStore holds the latest proposed patch pending human approval and applies
// it to an isolated output directory on approval. It NEVER writes to the source
// tree, never merges, and never deploys — the human-in-the-loop boundary.
type PatchStore struct {
	mu      sync.Mutex
	sandbox *Sandbox
	outDir  string
	latest  *PatchProposal
}

// NewPatchStore creates a store that validates paths against sandbox and writes
// approved patches under outDir.
func NewPatchStore(sandbox *Sandbox, outDir string) *PatchStore {
	return &PatchStore{sandbox: sandbox, outDir: outDir}
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

// ApplyApproved writes the approved patch (new file content + a .diff) under the
// output directory, mirroring the source-relative path. It returns the path
// written. It deliberately does not touch the source tree or deploy anything.
func (p *PatchStore) ApplyApproved() (string, error) {
	p.mu.Lock()
	prop := p.latest
	p.mu.Unlock()
	if prop == nil {
		return "", fmt.Errorf("no patch has been proposed")
	}
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
