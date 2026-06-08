package tools

import (
	"fmt"
	"sync"
)

// ArtifactStore holds files the builder agent generates on demand — regression
// tests, build scripts, Dockerfiles, deploy scripts — keyed by source-relative path.
// Like PatchStore/InstrumentStore it validates every path against the sandbox and
// never writes to disk itself; democtl writes the files locally (or the cloud runner
// folds them into the build source) and rolls back on failure.
type ArtifactStore struct {
	mu      sync.Mutex
	sandbox *Sandbox
	files   map[string]string // file -> full content
}

// NewArtifactStore creates a store that validates paths against sandbox.
func NewArtifactStore(sandbox *Sandbox) *ArtifactStore {
	return &ArtifactStore{sandbox: sandbox}
}

// Set records the full content for one generated file. The path must resolve inside
// the sandbox. This backs the write_artifact tool.
func (s *ArtifactStore) Set(file, content string) error {
	if file == "" {
		return fmt.Errorf("write_artifact: file is required")
	}
	if content == "" {
		return fmt.Errorf("write_artifact: content is required")
	}
	if _, err := s.sandbox.Resolve(file); err != nil {
		return err
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[file] = content
	s.mu.Unlock()
	return nil
}

// Files returns a copy of the recorded files (path -> content).
func (s *ArtifactStore) Files() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.files))
	for k, v := range s.files {
		out[k] = v
	}
	return out
}

// Clear drops any recorded files (between generation/repair attempts).
func (s *ArtifactStore) Clear() {
	s.mu.Lock()
	s.files = nil
	s.mu.Unlock()
}
