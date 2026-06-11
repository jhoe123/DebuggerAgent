// Package settings holds the runtime-configurable pipeline settings (the test/build/
// deploy parameters + the health-check URL shown in the Settings UI). The backend is the
// source of truth — seeded from env at startup and updated via POST /api/pipeline/config
// — so the server-side reachability check can read the configured health URL. It is a
// leaf package (imports only internal/api) so democtl, pipeline, and the server can all
// share it without an import cycle.
package settings

import (
	"strings"
	"sync"

	"github.com/patchpilot/backend/internal/api"
)

// Store is a thread-safe holder for the current PipelineSettings.
type Store struct {
	mu sync.Mutex
	s  api.PipelineSettings
}

// New returns a Store seeded with the given settings (env-derived defaults).
func New(seed api.PipelineSettings) *Store {
	if seed.DeployParams == nil {
		seed.DeployParams = map[string]string{}
	}
	return &Store{s: clone(seed)}
}

// Get returns a copy of the current settings.
func (st *Store) Get() api.PipelineSettings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return clone(st.s)
}

// Set merges the non-empty fields of in into the stored settings and returns the result.
// Mode is read-only (env-controlled) and never updated here. Empty incoming fields leave
// the stored value unchanged, so a partial update can't blank out config.
func (st *Store) Set(in api.PipelineSettings) api.PipelineSettings {
	st.mu.Lock()
	defer st.mu.Unlock()
	if v := strings.TrimSpace(in.TestStrategy); v != "" {
		st.s.TestStrategy = v
	}
	if v := strings.TrimSpace(in.BuildStrategy); v != "" {
		st.s.BuildStrategy = v
	}
	if v := strings.TrimSpace(in.DeployTarget); v != "" {
		st.s.DeployTarget = v
	}
	if v := strings.TrimSpace(in.HealthURL); v != "" {
		st.s.HealthURL = v
	}
	if v := strings.TrimSpace(in.AppURL); v != "" {
		st.s.AppURL = v
	}
	for k, v := range in.DeployParams {
		if st.s.DeployParams == nil {
			st.s.DeployParams = map[string]string{}
		}
		st.s.DeployParams[k] = strings.TrimSpace(v)
	}
	return clone(st.s)
}

func clone(s api.PipelineSettings) api.PipelineSettings {
	cp := s
	cp.DeployParams = make(map[string]string, len(s.DeployParams))
	for k, v := range s.DeployParams {
		cp.DeployParams[k] = v
	}
	return cp
}
