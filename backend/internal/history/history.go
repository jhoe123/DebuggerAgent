// Package history is a small, dependency-free audit log of changes the agent
// makes: proposed patches, approvals, and auto-remediation pipeline runs. It lets
// the UI show a history of patches/changes (and which files were affected).
//
// Storage is an in-memory ring buffer (works everywhere, including hosted Cloud
// Run) with optional best-effort append-only persistence to
// <PATCH_OUTPUT_DIR>/history.jsonl. Note: Cloud Run /tmp is ephemeral, so the
// ring buffer — not the file — is the source of truth.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/debuggeragent/backend/internal/api"
)

// Store is a thread-safe ring buffer of history entries (newest last internally).
type Store struct {
	mu   sync.Mutex
	buf  []api.HistoryEntry
	max  int
	seq  int64
	file string // "" => memory only
}

// New returns a store keeping up to max entries. If persistDir is non-empty it
// best-effort appends to persistDir/history.jsonl and tail-loads it on startup.
func New(max int, persistDir string) *Store {
	if max <= 0 {
		max = 200
	}
	s := &Store{max: max}
	if persistDir != "" {
		s.file = filepath.Join(persistDir, "history.jsonl")
		s.load()
	}
	return s
}

// Record stamps ID/CreatedAt (if unset), appends, trims to max, and persists.
func (s *Store) Record(e api.HistoryEntry) api.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if e.ID == "" {
		e.ID = time.Now().UTC().Format("20060102T150405") + "-" + itoa(s.seq)
	}
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if e.Files == nil {
		e.Files = []string{}
	}
	s.buf = append(s.buf, e)
	if len(s.buf) > s.max {
		s.buf = s.buf[len(s.buf)-s.max:]
	}
	s.persist(e)
	return e
}

// List returns a snapshot of entries, newest first.
func (s *Store) List() []api.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.HistoryEntry, len(s.buf))
	for i, e := range s.buf {
		out[len(s.buf)-1-i] = e
	}
	return out
}

// --- convenience recorders (primitives only — no coupling to tools.PatchProposal) ---

// RecordProposed logs an agent-proposed patch awaiting human approval.
func (s *Store) RecordProposed(problemID, file, diff, rationale string) {
	s.Record(api.HistoryEntry{
		Kind: "proposed", ProblemID: problemID, Files: filesOf(file),
		Summary: trim("Proposed fix: "+rationale, 160), Status: "proposed", Diff: diff,
	})
}

// RecordApproved logs a human approval that wrote the patch to disk.
func (s *Store) RecordApproved(problemID, file, diff, writtenTo string) {
	s.Record(api.HistoryEntry{
		Kind: "approved", ProblemID: problemID, Files: filesOf(file),
		Summary: "Patch approved → " + writtenTo, Status: "written", Diff: diff, WrittenTo: writtenTo,
	})
}

// RecordPipeline logs an auto-remediation pipeline run.
func (s *Store) RecordPipeline(problemID string, res api.PipelineResult) {
	status, verb := "failed", "failed"
	if res.Success {
		status, verb = "success", "succeeded"
	}
	summary := "Auto-remediation " + verb
	if res.Verify != "" {
		summary += " (" + res.Verify + ")"
	}
	s.Record(api.HistoryEntry{
		Kind: "pipeline", ProblemID: problemID, Files: res.Files,
		Summary: summary, Status: status, Steps: res.Steps, Verify: res.Verify,
	})
}

// --- persistence (best-effort; never fatal) ---

func (s *Store) persist(e api.HistoryEntry) {
	if s.file == "" {
		return
	}
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if b, err := json.Marshal(e); err == nil {
		_, _ = f.Write(append(b, '\n'))
	}
}

func (s *Store) load() {
	f, err := os.Open(s.file)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate large diffs
	for sc.Scan() {
		var e api.HistoryEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
			s.buf = append(s.buf, e)
		}
	}
	if len(s.buf) > s.max {
		s.buf = s.buf[len(s.buf)-s.max:]
	}
}

func filesOf(file string) []string {
	if file == "" {
		return []string{}
	}
	return []string{file}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
