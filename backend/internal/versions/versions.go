// Package versions tracks every successful deploy as an immutable, numbered
// version so the user can revert to any point in deploy history. Each version
// snapshots the CUMULATIVE patch overlay — every file modified since the pristine
// base up to that deploy — so restoring a version is deterministic: pristine
// source + the version's file set. Reverts are append-only (git-revert style): a
// successful revert records a NEW version pointing at the one it restored.
//
// Storage mirrors artifact.Store: an in-memory map is the source of truth, with
// best-effort one-file-per-version JSON under <dir>/versions/. Retention is
// managed automatically (oldest versions pruned past the cap).
package versions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/tools"
)

// defaultRetention is how many versions are kept when VERSION_RETENTION is unset.
const defaultRetention = 20

// Version is the internal record. Patches is the cumulative overlay (full
// PatchedContent per file), denormalized so every version is self-contained.
type Version struct {
	ID         string                `json:"id"`
	Seq        int                   `json:"seq"`
	CreatedAt  string                `json:"createdAt"` // RFC3339
	Source     string                `json:"source"`    // "manual" | "autopilot" | "revert"
	Summary    string                `json:"summary"`
	ProblemIDs []string              `json:"problemIds"`
	Scenarios  []string              `json:"scenarios"`
	Files      []string              `json:"files"`
	Verify     string                `json:"verify,omitempty"`
	RevertOf   string                `json:"revertOf,omitempty"`
	Patches    []tools.PatchProposal `json:"patches"`
}

// Store keeps deploy versions in memory and mirrors them to disk.
type Store struct {
	mu        sync.Mutex
	dir       string // <PATCH_OUTPUT_DIR>/versions; "" => memory only
	retention int
	seq       int
	order     []string           // version ids, oldest first
	m         map[string]Version // id -> version
	// cumulative is the current overlay state (file -> latest proposal): what the
	// deployed source carries relative to the pristine base. Updated on every
	// recorded deploy; reset (not merged) on revert.
	cumulative map[string]tools.PatchProposal
}

// New returns a store persisting under baseDir/versions (memory-only when baseDir
// is empty). retention <= 0 uses the default. clearOnStart wipes prior versions
// (demo fresh-start semantics, mirroring the artifact store).
func New(baseDir string, retention int, clearOnStart bool) *Store {
	if retention <= 0 {
		retention = defaultRetention
	}
	s := &Store{
		retention:  retention,
		m:          map[string]Version{},
		cumulative: map[string]tools.PatchProposal{},
	}
	if baseDir != "" {
		s.dir = filepath.Join(baseDir, "versions")
		if clearOnStart {
			_ = os.RemoveAll(s.dir)
		}
		s.load()
	}
	return s
}

// RecordDeploy records a successful deploy as the next version: the run's patches
// merge into the cumulative overlay and the merged set is snapshotted. source is
// "manual" or "autopilot".
func (s *Store) RecordDeploy(source string, problemIDs, scenarios []string, run []tools.PatchProposal, res api.PipelineResult) api.DeployVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range run {
		s.cumulative[p.File] = p
	}
	summary := fmt.Sprintf("Deployed %d patch(es) for %d problem(s)", len(run), len(problemIDs))
	v := s.appendLocked(Version{
		Source:     source,
		Summary:    summary,
		ProblemIDs: append([]string(nil), problemIDs...),
		Scenarios:  append([]string(nil), scenarios...),
		Verify:     res.Verify,
		Patches:    s.snapshotCumulativeLocked(),
	})
	return lean(v)
}

// RecordRevert records a successful revert deploy: the cumulative overlay is RESET
// to the target version's patch set (files modified after the target are pristine
// again) and a new version is appended pointing back at the target.
func (s *Store) RecordRevert(target Version, res api.PipelineResult) api.DeployVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cumulative = map[string]tools.PatchProposal{}
	for _, p := range target.Patches {
		s.cumulative[p.File] = p
	}
	v := s.appendLocked(Version{
		Source:     "revert",
		Summary:    fmt.Sprintf("Reverted to v%d", target.Seq),
		ProblemIDs: append([]string(nil), target.ProblemIDs...),
		Scenarios:  append([]string(nil), target.Scenarios...),
		Verify:     res.Verify,
		RevertOf:   target.ID,
		Patches:    s.snapshotCumulativeLocked(),
	})
	return lean(v)
}

// appendLocked assigns seq/id/timestamps/files, stores, persists and prunes.
// Caller holds mu and has already set Patches to the snapshot to record.
func (s *Store) appendLocked(v Version) Version {
	s.seq++
	v.Seq = s.seq
	v.ID = fmt.Sprintf("v%d-%s", v.Seq, time.Now().UTC().Format("20060102T150405"))
	v.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	files := make([]string, 0, len(v.Patches))
	for _, p := range v.Patches {
		files = append(files, p.File)
	}
	v.Files = files
	s.m[v.ID] = v
	s.order = append(s.order, v.ID)
	s.persist(v)
	s.pruneLocked()
	return v
}

// snapshotCumulativeLocked returns the cumulative overlay sorted by file (a
// deterministic, self-contained copy). Caller holds mu.
func (s *Store) snapshotCumulativeLocked() []tools.PatchProposal {
	out := make([]tools.PatchProposal, 0, len(s.cumulative))
	for _, p := range s.cumulative {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// List returns all versions newest-first, without patch payloads (lean for the UI).
func (s *Store) List() []api.DeployVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.DeployVersion, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		if v, ok := s.m[s.order[i]]; ok {
			out = append(out, lean(v))
		}
	}
	return out
}

// Get returns the full internal record (revert input), including patch contents.
func (s *Store) Get(id string) (Version, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	return v, ok
}

// Detail returns the API shape with display patches (diff + rationale, never the
// full patched content).
func (s *Store) Detail(id string) (api.DeployVersion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return api.DeployVersion{}, false
	}
	d := lean(v)
	d.Patches = make([]api.VersionPatch, 0, len(v.Patches))
	for _, p := range v.Patches {
		d.Patches = append(d.Patches, api.VersionPatch{File: p.File, UnifiedDiff: p.UnifiedDiff, Rationale: p.Rationale})
	}
	return d, true
}

// lean maps a Version to its API shape without patch payloads.
func lean(v Version) api.DeployVersion {
	return api.DeployVersion{
		ID:         v.ID,
		Seq:        v.Seq,
		CreatedAt:  v.CreatedAt,
		Source:     v.Source,
		Summary:    v.Summary,
		ProblemIDs: v.ProblemIDs,
		Files:      v.Files,
		Verify:     v.Verify,
		RevertOf:   v.RevertOf,
	}
}

// pruneLocked drops the oldest versions past the retention cap (and their files).
// Caller holds mu.
func (s *Store) pruneLocked() {
	for len(s.order) > s.retention {
		id := s.order[0]
		s.order = s.order[1:]
		delete(s.m, id)
		if s.dir != "" {
			_ = os.Remove(filepath.Join(s.dir, id+".json"))
		}
	}
}

// persist writes one version file. Best-effort: failures are ignored (the
// in-memory map is the source of truth; hosted /tmp is ephemeral anyway).
func (s *Store) persist(v Version) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.dir, v.ID+".json"), b, 0o644)
}

// load rehydrates versions from disk: rebuilds the map and seq-ordered list,
// resumes the sequence counter, and restores the cumulative overlay from the
// newest version (each version is self-contained).
func (s *Store) load() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	var all []Version
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var v Version
		if json.Unmarshal(b, &v) != nil || v.ID == "" {
			continue
		}
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	for _, v := range all {
		s.m[v.ID] = v
		s.order = append(s.order, v.ID)
		if v.Seq > s.seq {
			s.seq = v.Seq
		}
	}
	if len(all) > 0 {
		for _, p := range all[len(all)-1].Patches {
			s.cumulative[p.File] = p
		}
	}
}
