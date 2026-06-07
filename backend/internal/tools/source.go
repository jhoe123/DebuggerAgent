// Package tools implements the agent's non-MCP tools: read_source and propose_patch.
//
// These are plain Go (no cloud/MCP deps) so they are unit-testable without
// credentials. Task T4 wraps them as ADK Go FunctionTools.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sandbox restricts all file access to a single root directory (the demo app
// source). It prevents path-traversal escapes (e.g. "../../etc/passwd").
type Sandbox struct {
	Root string // cleaned absolute path
}

// NewSandbox creates a sandbox rooted at the given directory.
func NewSandbox(root string) (*Sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root: %w", err)
	}
	return &Sandbox{Root: filepath.Clean(abs)}, nil
}

// Resolve validates that rel stays within the sandbox root and returns the
// absolute on-disk path. It rejects any path that escapes the root.
func (s *Sandbox) Resolve(rel string) (string, error) {
	abs := filepath.Clean(filepath.Join(s.Root, rel))
	r, err := filepath.Rel(s.Root, abs)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", rel, err)
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes source root", rel)
	}
	return abs, nil
}

// ReadSource returns the contents of a file relative to the sandbox root.
// This is the read_source tool exposed to the agent for stack-trace → code
// correlation.
func (s *Sandbox) ReadSource(rel string) (string, error) {
	abs, err := s.Resolve(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	return string(b), nil
}
