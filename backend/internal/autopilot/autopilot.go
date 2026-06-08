// Package autopilot is the always-on auto-patch daemon. When enabled, it watches
// the Dynatrace problem feed and, for each NEWLY-detected problem, runs the agent
// to investigate + propose a fix and (in local mode) the democtl pipeline to
// apply/test/build/deploy/verify — serially (one worker), since the backend has a
// single patch store and a single demo_app. Each problem's live status is tracked
// and can be halted, handing it back to manual control. With no democtl (Test
// Console off / hosted), it degrades to investigate + propose only.
package autopilot

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/gitsource"
	"github.com/patchpilot/backend/internal/history"
)

// ListFunc returns the current set of problems (e.g. dynatrace.Client.ListProblems).
type ListFunc func(context.Context) ([]api.Problem, error)

// Engine is the auto-patch daemon. Safe for concurrent use.
type Engine struct {
	agent     *agent.Service
	demo      *democtl.Controller // nil => propose-only (no apply/build/deploy)
	hist      *history.Store
	arts      *artifact.Store
	git       *gitsource.Manager // optional: commit auto-fixes to a per-problem branch
	localMode bool

	mu       sync.Mutex
	cfg      api.AutopilotConfig
	runs     map[string]*api.AutopilotRun
	order    []string       // insertion order of run ids (for newest-first snapshot)
	baseline map[string]int // last-accounted occurrence count per problem id
	skip     map[string]bool
	primed   bool
	activeID string
	cancel   context.CancelFunc

	queue chan string
}

// New builds the engine. demo may be nil (propose-only mode). Config defaults to
// disabled with all stages on (opt-in via SetConfig).
func New(ag *agent.Service, demo *democtl.Controller, hist *history.Store, arts *artifact.Store) *Engine {
	return &Engine{
		agent:     ag,
		demo:      demo,
		hist:      hist,
		arts:      arts,
		localMode: demo != nil,
		cfg: api.AutopilotConfig{
			Enabled: false,
			Stages:  api.AutopilotStages{Apply: true, Test: true, Build: true, Deploy: true},
		},
		runs:     map[string]*api.AutopilotRun{},
		baseline: map[string]int{},
		skip:     map[string]bool{},
		queue:    make(chan string, 128),
	}
}

// SetGitSource wires the managed Git source so successful auto-fixes are committed to a
// per-problem branch (push gated). The autopilot NEVER merges — confirmation does.
func (e *Engine) SetGitSource(gs *gitsource.Manager) { e.git = gs }

// Run starts the serial worker and the problem poller until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, interval time.Duration, list ListFunc) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go e.worker(ctx)
	log.Printf("Autopilot poller started (every %s, localMode=%v)", interval, e.localMode)
	t := time.NewTicker(interval)
	defer t.Stop()
	e.tick(ctx, list)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx, list)
		}
	}
}

func (e *Engine) tick(ctx context.Context, list ListFunc) {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probs, err := list(c)
	if err != nil {
		log.Printf("autopilot: list problems failed: %v", err)
		return
	}

	var toEnqueue []string
	e.mu.Lock()
	for _, p := range probs {
		prev, known := e.baseline[p.ID]
		e.baseline[p.ID] = p.Occurrences // always account for what we've observed
		// First poll only records the backlog so enabling never patches what's already open.
		if !e.primed || !e.cfg.Enabled || e.skip[p.ID] {
			continue
		}
		if r := e.runs[p.ID]; r != nil && !terminal(r.Phase) {
			continue // already queued or running
		}
		// Act on a brand-new problem (since startup) or a fresh occurrence burst.
		if !known || p.Occurrences > prev {
			_, had := e.runs[p.ID]
			e.runs[p.ID] = &api.AutopilotRun{
				ProblemID: p.ID, Title: p.Title, Kind: p.Kind, Phase: "queued", UpdatedAt: nowRFC(),
			}
			if !had {
				e.order = append(e.order, p.ID)
			}
			toEnqueue = append(toEnqueue, p.ID)
		}
	}
	e.primed = true
	e.mu.Unlock()

	for _, id := range toEnqueue {
		select {
		case e.queue <- id:
		default:
			log.Printf("autopilot: queue full, dropping %s", id)
		}
	}
}

func (e *Engine) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-e.queue:
			e.process(ctx, id)
		}
	}
}

func (e *Engine) process(parent context.Context, id string) {
	e.mu.Lock()
	if e.skip[id] || !e.cfg.Enabled {
		e.mu.Unlock()
		e.setPhase(id, "halted", "Autopatch off or halted before start.", nil)
		return
	}
	stages := e.cfg.Stages
	runCtx, cancel := context.WithCancel(parent)
	e.activeID = id
	e.cancel = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		if e.activeID == id {
			e.activeID = ""
			e.cancel = nil
		}
		e.mu.Unlock()
	}()

	// 1. Investigate (root cause + proposed patch).
	e.setPhase(id, "investigating", "Investigating root cause…", nil)
	onStep := func(stage, status, message string) {
		e.appendStep(id, api.Step{Stage: stage, Status: status, Message: message})
	}
	_, patch, err := e.agent.Investigate(runCtx, "auto-"+id, e.agent.InvestigatePrompt(id), onStep)
	if runCtx.Err() != nil {
		e.markHalted(id)
		return
	}
	if err != nil {
		e.setPhase(id, "failed", "Investigation failed: "+err.Error(), boolPtr(false))
		e.arts.RecordInvestigation(id, e.titleOf(id), e.kindOf(id), false, err.Error())
		return
	}
	kind, _ := agent.SplitProblemID(id)
	e.arts.RecordInvestigation(id, e.titleOf(id), kind, true, "")
	if patch != nil {
		e.hist.RecordProposed(id, patch.File, patch.UnifiedDiff, patch.Rationale)
	}

	// 2. Remediate — local mode only; otherwise terminal at "proposed".
	if e.demo == nil {
		e.setPhase(id, "proposed", "Patch proposed — approve to apply (local mode off).", nil)
		return
	}
	if patch == nil {
		e.setPhase(id, "failed", "No patch was proposed — cannot remediate.", boolPtr(false))
		return
	}
	e.setPhase(id, "remediating", "Applying & verifying the fix…", nil)
	opts := democtl.Options{Apply: stages.Apply, Test: stages.Test, Build: stages.Build, Deploy: stages.Deploy, Scenario: kind}
	res := e.demo.Remediate(runCtx, opts, func(s api.Step) {
		e.appendStep(id, s)
		e.setMessage(id, s.Stage+": "+s.Message)
	})
	if runCtx.Err() != nil {
		e.markHalted(id)
		return
	}
	e.hist.RecordPipeline(id, res)
	e.arts.RecordRun([]string{id}, res)
	if res.Success {
		// Commit the auto-fix to its isolated branch (push gated). Never merges — a human
		// confirms to merge. Best-effort: a git failure doesn't undo a successful deploy.
		if e.git != nil && patch != nil && e.git.IsConnected() && e.git.Config().BranchPerFix {
			c, cancel := context.WithTimeout(parent, 60*time.Second)
			if _, _, gerr := e.git.CommitPatch(c, id, map[string]string{patch.File: patch.PatchedContent}, "PatchPilot autopilot fix for "+id); gerr != nil {
				log.Printf("autopilot: git commit for %s: %v", id, gerr)
			}
			cancel()
		}
		e.setPhase(id, "deployed", "Fixed & deployed"+verifySuffix(res), boolPtr(true))
	} else {
		e.setPhase(id, "failed", "Pipeline failed"+verifySuffix(res), boolPtr(false))
	}
}

// SetConfig updates the live config. Disabling cancels the active run and drains
// any queued runs (marking them halted).
func (e *Engine) SetConfig(cfg api.AutopilotConfig) {
	e.mu.Lock()
	wasEnabled := e.cfg.Enabled
	e.cfg = cfg
	if wasEnabled && !cfg.Enabled {
		if e.cancel != nil {
			e.cancel()
		}
		for draining := true; draining; {
			select {
			case id := <-e.queue:
				if r := e.runs[id]; r != nil && r.Phase == "queued" {
					r.Phase = "halted"
					r.Message = "Autopatch turned off"
					r.UpdatedAt = nowRFC()
				}
			default:
				draining = false
			}
		}
	}
	e.mu.Unlock()
}

// Cancel halts a problem's automation (and prevents it being re-picked), handing
// it to manual control. Cancelling the active run's context also kills in-flight
// go test/build (democtl uses exec.CommandContext).
func (e *Engine) Cancel(problemID string) {
	e.mu.Lock()
	e.skip[problemID] = true
	if e.activeID == problemID && e.cancel != nil {
		e.cancel()
	}
	if r := e.runs[problemID]; r != nil && !terminal(r.Phase) {
		r.Phase = "halted"
		r.Message = "Halted — handed to manual control."
		r.UpdatedAt = nowRFC()
	}
	e.mu.Unlock()
}

// Snapshot returns the current config + runs (newest-first) + local-mode flag.
func (e *Engine) Snapshot() api.AutopilotSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs := make([]api.AutopilotRun, 0, len(e.order))
	for i := len(e.order) - 1; i >= 0; i-- {
		if r := e.runs[e.order[i]]; r != nil {
			runs = append(runs, *r)
		}
	}
	return api.AutopilotSnapshot{Config: e.cfg, Runs: runs, LocalMode: e.localMode}
}

// --- run-state mutators (each stamps UpdatedAt) ---

func (e *Engine) withRun(id string, fn func(r *api.AutopilotRun)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[id]
	if r == nil {
		r = &api.AutopilotRun{ProblemID: id, Phase: "queued"}
		e.runs[id] = r
		e.order = append(e.order, id)
	}
	fn(r)
	r.UpdatedAt = nowRFC()
}

func (e *Engine) setPhase(id, phase, msg string, success *bool) {
	e.withRun(id, func(r *api.AutopilotRun) {
		r.Phase = phase
		r.Message = msg
		if success != nil {
			r.Success = success
		}
	})
}

func (e *Engine) setMessage(id, msg string) {
	e.withRun(id, func(r *api.AutopilotRun) { r.Message = msg })
}

func (e *Engine) appendStep(id string, s api.Step) {
	e.withRun(id, func(r *api.AutopilotRun) { r.Steps = append(r.Steps, s) })
}

// titleOf returns the tracked problem title (for artifact display), or "".
func (e *Engine) titleOf(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.runs[id]; r != nil {
		return r.Title
	}
	return ""
}

// kindOf returns the problem kind ("error"/"performance") parsed from the id.
func (e *Engine) kindOf(id string) string {
	kind, _ := agent.SplitProblemID(id)
	return kind
}

func (e *Engine) markHalted(id string) {
	e.mu.Lock()
	e.skip[id] = true
	e.mu.Unlock()
	e.setPhase(id, "halted", "Halted — handed to manual control.", nil)
}

func terminal(phase string) bool {
	switch phase {
	case "deployed", "failed", "halted", "proposed":
		return true
	}
	return false
}

func verifySuffix(res api.PipelineResult) string {
	if res.Verify != "" {
		return " (" + res.Verify + ")"
	}
	return ""
}

func nowRFC() string       { return time.Now().UTC().Format(time.RFC3339) }
func boolPtr(b bool) *bool { return &b }
