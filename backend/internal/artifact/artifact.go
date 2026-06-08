// Package artifact is a durable, per-problem lifecycle record: how a fix moved
// through investigation → patch (staged) → test → build → deploy → verify. Unlike
// the append-only history log, it's keyed by problemId and answers "what is the
// current status of this problem?".
//
// Storage is an in-memory map (source of truth, works everywhere including hosted
// Cloud Run) with best-effort one-file-per-problem JSON under <dir>/artifacts/.
// The pipeline that produces these stages only runs in local / cloud-build mode,
// where that directory is on persistent disk.
package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/api"
)

// pipeline stage keys tracked on an artifact, in lifecycle order.
const (
	stageInvestigation = "investigation"
	stagePatch         = "patch"
	stageTest          = "test"
	stageBuild         = "build"
	stageDeploy        = "deploy"
	stageVerify        = "verify"
)

// Store keeps per-problem artifacts in memory and mirrors them to disk.
type Store struct {
	mu  sync.Mutex
	dir string // <PATCH_OUTPUT_DIR>/artifacts; "" => memory only
	m   map[string]*api.ProblemArtifact
}

// New returns a store. If baseDir is non-empty it persists to baseDir/artifacts/
// and loads any existing artifacts on startup.
func New(baseDir string) *Store {
	s := &Store{m: map[string]*api.ProblemArtifact{}}
	if baseDir != "" {
		s.dir = filepath.Join(baseDir, "artifacts")
		s.load()
	}
	return s
}

// get returns the artifact for id, creating a blank one if absent. Caller holds mu.
func (s *Store) get(problemID, title, kind string) *api.ProblemArtifact {
	a := s.m[problemID]
	if a == nil {
		a = &api.ProblemArtifact{ProblemID: problemID, Stages: map[string]api.ArtifactStage{}}
		s.m[problemID] = a
	}
	if title != "" {
		a.Title = title
	}
	if kind != "" {
		a.Kind = kind
	}
	return a
}

// RecordInvestigation marks the investigation stage and sets overall=investigated
// (or failed). detail is a short root-cause summary.
func (s *Store) RecordInvestigation(problemID, title, kind string, ok bool, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.get(problemID, title, kind)
	a.Stages[stageInvestigation] = stage(ok, detail)
	if ok {
		a.Overall = "investigated"
	} else {
		a.Overall = "failed"
	}
	s.stamp(a)
}

// RecordStaged marks the patch stage and sets overall=staged.
func (s *Store) RecordStaged(problemID, title, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.get(problemID, title, kind)
	a.Stages[stagePatch] = stage(true, "patch added to batch")
	a.Overall = "staged"
	s.stamp(a)
}

// RecordRun attributes a (possibly batched) pipeline result to every problem whose
// patch was in the run, splitting the result's stages across each artifact.
func (s *Store) RecordRun(problemIDs []string, res api.PipelineResult) {
	stages := stageStatuses(res)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range problemIDs {
		a := s.get(id, "", "")
		for _, key := range []string{stageTest, stageBuild, stageDeploy, stageVerify} {
			if st, ok := stages[key]; ok {
				a.Stages[key] = api.ArtifactStage{Status: st, At: nowRFC()}
			}
		}
		a.Verify = res.Verify
		if res.Success {
			a.Overall = "deployed"
		} else {
			a.Overall = "failed"
		}
		s.stamp(a)
	}
}

// List returns all artifacts, most-recently-updated first.
func (s *Store) List() []api.ProblemArtifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.ProblemArtifact, 0, len(s.m))
	for _, a := range s.m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// --- internals ---

// stamp updates the timestamp and best-effort persists the artifact. Caller holds mu.
func (s *Store) stamp(a *api.ProblemArtifact) {
	a.UpdatedAt = nowRFC()
	s.persist(a)
}

func (s *Store) persist(a *api.ProblemArtifact) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	if b, err := json.MarshalIndent(a, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(s.dir, safeID(a.ProblemID)+".json"), b, 0o644)
	}
}

func (s *Store) load() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var a api.ProblemArtifact
		if json.Unmarshal(b, &a) == nil && a.ProblemID != "" {
			if a.Stages == nil {
				a.Stages = map[string]api.ArtifactStage{}
			}
			s.m[a.ProblemID] = &a
		}
	}
}

func stage(ok bool, detail string) api.ArtifactStage {
	status := "ok"
	if !ok {
		status = "failed"
	}
	return api.ArtifactStage{Status: status, At: nowRFC(), Detail: detail}
}

// stageStatuses reduces a pipeline result's steps to a final status per stage
// (last ok/fail wins), mapping the stream's "fail" to the artifact's "failed".
func stageStatuses(res api.PipelineResult) map[string]string {
	out := map[string]string{}
	for _, st := range res.Steps {
		switch st.Stage {
		case stageTest, stageBuild, stageDeploy, stageVerify, "apply":
			switch st.Status {
			case "ok":
				out[st.Stage] = "ok"
			case "fail":
				out[st.Stage] = "failed"
			}
		}
	}
	return out
}

// safeID makes a problemId (e.g. "error:checkout") safe as a filename.
func safeID(id string) string {
	return strings.NewReplacer(":", "_", "/", "__", "\\", "__", " ", "_").Replace(id)
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }
