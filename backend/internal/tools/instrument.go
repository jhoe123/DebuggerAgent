package tools

import (
	"fmt"
	"sync"

	"github.com/patchpilot/backend/internal/api"
)

// InstrumentStore holds the latest instrumentation scan (the candidate set the
// agent proposed) and, during apply, the full patched file contents the agent
// produced for the selected candidates. Like PatchStore it validates every file
// path against the sandbox and NEVER writes to the source tree itself — democtl
// does that, local-only.
type InstrumentStore struct {
	mu      sync.Mutex
	sandbox *Sandbox
	scan    *api.InstrumentationScan
	patched map[string]string // file -> full patched content (apply phase)
}

// NewInstrumentStore creates a store that validates paths against sandbox.
func NewInstrumentStore(sandbox *Sandbox) *InstrumentStore {
	return &InstrumentStore{sandbox: sandbox}
}

// Root returns the sandbox root the scan covers.
func (s *InstrumentStore) Root() string { return s.sandbox.Root() }

// SetScan validates and records the candidate set, assigning a stable ID to each
// candidate (so the client only ever round-trips IDs, never full file bodies).
func (s *InstrumentStore) SetScan(scan api.InstrumentationScan) error {
	for i := range scan.Candidates {
		c := &scan.Candidates[i]
		if c.File == "" {
			return fmt.Errorf("propose_instrumentation: candidate %d is missing a file", i)
		}
		if _, err := s.sandbox.Resolve(c.File); err != nil {
			return err
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("%s::%s::%s::%d::%d", c.File, c.Symbol, c.Kind, c.StartLine, i)
		}
	}
	scan.Root = s.sandbox.Root()
	if scan.Candidates == nil {
		scan.Candidates = []api.InstrumentationCandidate{}
	}
	s.mu.Lock()
	s.scan = &scan
	s.patched = nil
	s.mu.Unlock()
	return nil
}

// Scan returns the latest recorded scan, or nil if none.
func (s *InstrumentStore) Scan() *api.InstrumentationScan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scan
}

// Selected returns the candidates whose IDs are in ids (preserving scan order).
// An empty ids slice means "all" (apply-all).
func (s *InstrumentStore) Selected(ids []string) []api.InstrumentationCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scan == nil {
		return nil
	}
	all := len(ids) == 0
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []api.InstrumentationCandidate
	for _, c := range s.scan.Candidates {
		if all || want[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// SetPatchedFile records the full patched content for a file (apply phase). The
// path must resolve inside the sandbox.
func (s *InstrumentStore) SetPatchedFile(file, content string) error {
	if file == "" {
		return fmt.Errorf("write_instrumented_file: file is required")
	}
	if content == "" {
		return fmt.Errorf("write_instrumented_file: patched_content is required")
	}
	if _, err := s.sandbox.Resolve(file); err != nil {
		return err
	}
	s.mu.Lock()
	if s.patched == nil {
		s.patched = map[string]string{}
	}
	s.patched[file] = content
	s.mu.Unlock()
	return nil
}

// PatchedFiles returns a copy of the recorded patched files (file -> content).
func (s *InstrumentStore) PatchedFiles() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.patched))
	for k, v := range s.patched {
		out[k] = v
	}
	return out
}

// ClearPatched drops any recorded patched files (between apply/repair attempts).
func (s *InstrumentStore) ClearPatched() {
	s.mu.Lock()
	s.patched = nil
	s.mu.Unlock()
}
