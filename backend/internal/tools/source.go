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
	"sync"
)

// Sandbox restricts all file access to a single root directory (the source tree
// being patched). It prevents path-traversal escapes (e.g. "../../etc/passwd").
// The root can be re-pointed at runtime via SetRoot (e.g. when a Git source is
// connected); a RWMutex keeps concurrent reads consistent with that swap.
type Sandbox struct {
	mu   sync.RWMutex
	root string // cleaned absolute path
}

// NewSandbox creates a sandbox rooted at the given directory.
func NewSandbox(root string) (*Sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root: %w", err)
	}
	return &Sandbox{root: filepath.Clean(abs)}, nil
}

// Root returns the current sandbox root (absolute, cleaned).
func (s *Sandbox) Root() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root
}

// SetRoot re-points the sandbox at a new root directory (resolved + cleaned), so
// read_source / propose_patch operate on a newly-connected Git source clone. Every
// agent and the patch store share one Sandbox, so a single SetRoot re-points them all.
func (s *Sandbox) SetRoot(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	s.mu.Lock()
	s.root = filepath.Clean(abs)
	s.mu.Unlock()
	return nil
}

// Resolve validates that rel stays within the sandbox root and returns the
// absolute on-disk path. It rejects any path that escapes the root.
func (s *Sandbox) Resolve(rel string) (string, error) {
	root := s.Root()
	abs := filepath.Clean(filepath.Join(root, rel))
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", rel, err)
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes source root", rel)
	}
	return abs, nil
}

// MaxUnrangedLines caps how many lines an unranged read returns. Beyond this,
// the agent must use Offset/Limit or Query so we don't waste context (and tokens)
// pulling a whole large file when the bug is in one region.
const MaxUnrangedLines = 400

const searchContext = 5 // lines of context around each search match

// ReadOptions controls how read_source slices a file. Zero value = read the whole
// file (subject to the MaxUnrangedLines guard), preserving the original behavior.
type ReadOptions struct {
	Offset int    // 1-based start line; 0 => from the beginning
	Limit  int    // max lines to return; 0 => to end (subject to the guard)
	Query  string // non-empty => search mode: return windows around matching lines
}

// Match is a single search hit with a context window.
type Match struct {
	Line      int    `json:"line"`       // 1-based line that matched Query
	StartLine int    `json:"start_line"` // context window start (1-based)
	EndLine   int    `json:"end_line"`   // context window end (inclusive)
	Snippet   string `json:"snippet"`    // the context window text
}

// ReadResult is the structured result of a ranged/search/unranged read.
type ReadResult struct {
	Path       string  `json:"path"`
	Content    string  `json:"content"`     // returned slice, or concatenated match windows
	StartLine  int     `json:"start_line"`  // 1-based start of Content (range/unranged modes)
	EndLine    int     `json:"end_line"`    // 1-based inclusive end of Content
	TotalLines int     `json:"total_lines"` // whole-file line count (for navigation)
	Truncated  bool    `json:"truncated"`   // guard limited the output
	Matches    []Match `json:"matches,omitempty"`
}

// ReadFull returns the entire file contents relative to the sandbox root. Used
// when constructing a patch (propose_patch needs the FULL patched file).
func (s *Sandbox) ReadFull(rel string) (string, error) {
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

// ReadSource reads a file relative to the sandbox root with optional ranging or
// search. This is the read_source tool exposed to the agent for stack-trace →
// code correlation; ranged/search reads keep large files from blowing the context.
func (s *Sandbox) ReadSource(rel string, opt ReadOptions) (ReadResult, error) {
	full, err := s.ReadFull(rel)
	if err != nil {
		return ReadResult{}, err
	}
	lines := strings.Split(full, "\n")
	// strings.Split on a trailing "\n" yields a final empty element; don't count it.
	total := len(lines)
	if total > 0 && lines[total-1] == "" {
		total--
		lines = lines[:total]
	}
	res := ReadResult{Path: rel, TotalLines: total}

	// Search mode: return ±searchContext windows around each matching line.
	if opt.Query != "" {
		var windows []string
		lastEnd := 0
		for i, ln := range lines {
			if !strings.Contains(ln, opt.Query) {
				continue
			}
			n := i + 1 // 1-based
			start := max(1, n-searchContext)
			end := min(total, n+searchContext)
			res.Matches = append(res.Matches, Match{
				Line: n, StartLine: start, EndLine: end,
				Snippet: strings.Join(lines[start-1:end], "\n"),
			})
			// Coalesce into the running Content, skipping overlaps.
			if start <= lastEnd {
				start = lastEnd + 1
			}
			if start <= end {
				windows = append(windows, strings.Join(lines[start-1:end], "\n"))
				lastEnd = end
			}
			if len(res.Matches) >= 20 {
				res.Truncated = true
				break
			}
		}
		res.Content = strings.Join(windows, "\n…\n")
		return res, nil
	}

	// Range mode: explicit Offset/Limit.
	if opt.Offset > 0 || opt.Limit > 0 {
		start := opt.Offset
		if start <= 0 {
			start = 1
		}
		if start > total {
			start = total + 1
		}
		end := total
		if opt.Limit > 0 && start+opt.Limit-1 < end {
			end = start + opt.Limit - 1
		}
		res.StartLine, res.EndLine = start, end
		if start <= end {
			res.Content = strings.Join(lines[start-1:end], "\n")
		}
		res.Truncated = end < total
		return res, nil
	}

	// Unranged: whole file if small; otherwise head + a steer toward ranged reads.
	if total <= MaxUnrangedLines {
		res.StartLine, res.EndLine, res.Content = 1, total, full
		return res, nil
	}
	res.StartLine, res.EndLine = 1, MaxUnrangedLines
	res.Truncated = true
	res.Content = strings.Join(lines[:MaxUnrangedLines], "\n") +
		fmt.Sprintf("\n… file has %d lines; use offset/limit or query to read more.", total)
	return res, nil
}
