// Package autopilot is the always-on auto-patch daemon. When enabled, it watches
// the Dynatrace problem feed and, for each NEWLY-detected problem, runs the agent
// to investigate + propose a fix and (in local mode) the democtl pipeline to
// apply/test/build/deploy/verify. Problems that surface together are BATCHED into
// a single deploy: each fix is investigated and applied to the source in turn —
// so every patch is generated on top of the previous one's changes (the seeded
// bugs share demo_app/main.go, so independent full-file patches would otherwise
// clobber each other) — then ALL fixes are verified in ONE consolidated pipeline
// run (single test/build/deploy covering every scenario), one app restart instead
// of one per fix. If the consolidated run fails, the batch falls back to fixing
// each problem on its own (reset → investigate → deploy) so one bad patch can't
// sink the others. Each problem's live status is tracked and can be halted,
// handing it back to manual control. With no democtl (Test Console off / hosted),
// it degrades to investigate + propose only.
package autopilot

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/gitsource"
	"github.com/patchpilot/backend/internal/history"
	"github.com/patchpilot/backend/internal/tools"
	"github.com/patchpilot/backend/internal/versions"
)

// ListFunc returns the current set of problems (e.g. dynatrace.Client.ListProblems).
type ListFunc func(context.Context) ([]api.Problem, error)

// investigator runs the agent to root-cause a problem and propose a fix. Satisfied
// by *agent.Service; an interface so the engine's batching is unit-testable.
type investigator interface {
	Investigate(ctx context.Context, sessionID, prompt string, onStep agent.StepFunc) (string, *tools.PatchProposal, error)
	InvestigatePrompt(problemID string) string
}

// remediator runs the apply→test→build→deploy→verify pipeline. Satisfied by BOTH
// *democtl.Controller (local, in-process) and *pipeline.CloudRunner (Cloud Build →
// Cloud Run), so the autopilot deploys via whichever runner the server selected by
// PIPELINE_MODE — the same runner the manual pipeline uses.
type remediator interface {
	Remediate(ctx context.Context, opts democtl.Options, emit func(api.Step)) api.PipelineResult
}

// sourceController manipulates the local SOURCE_ROOT the agent's read_source reads.
// The autopilot applies each fix to source BEFORE investigating the next so a later
// full-file patch builds on earlier ones (the seeded bugs share demo_app/main.go).
// Satisfied by *democtl.Controller; nil when no local controller exists (in which
// case fixes can't be made cumulative and the isolate fallback is unavailable).
type sourceController interface {
	// ApplyPatches writes a patch set to the source WITHOUT running the pipeline, so
	// the next investigation reads the cumulatively-patched source.
	ApplyPatches(list []tools.PatchProposal) error
	// ResetSource restores the committed (buggy) source — used before the isolate
	// fallback so each fix is retried from a clean base.
	ResetSource(ctx context.Context) error
}

// Batching knobs. The auto-patch cycle APPLIES each problem's fix first, waits for
// sibling problems to apply too (coalescing anything detected within batchFlushGrace
// of the last apply), then runs ONE pipeline for the whole batch — so problems are
// never built/deployed one-by-one.
const (
	batchFlushGrace = 35 * time.Second // idle wait for more problems before pipelining (≈ one poll)
	maxBatchSize    = 16               // safety cap on problems applied into one batch
	applyRetries    = 1                // retry a problem's patch once before skipping it
)

// Engine is the auto-patch daemon. Safe for concurrent use.
type Engine struct {
	agent     investigator
	runner    remediator       // active pipeline runner (Cloud Build or local); nil => propose-only
	source    sourceController // local source control for cumulative apply + isolate reset; may be nil
	hist      *history.Store
	arts      *artifact.Store
	git       *gitsource.Manager // optional: commit auto-fixes to a per-problem branch
	vers      *versions.Store    // optional: record every successful deploy as a revertable version
	localMode bool

	mu        sync.Mutex
	cfg       api.AutopilotConfig
	runs      map[string]*api.AutopilotRun
	order     []string       // insertion order of run ids (for newest-first snapshot)
	baseline  map[string]int // last-accounted occurrence count per problem id
	skip      map[string]bool
	activeIDs map[string]bool // problems in the in-flight batch (for targeted Cancel)
	cancel    context.CancelFunc
	stopping  bool // a StopAll teardown is unwinding — the dying batch must not re-mark its problems skip

	// baseCtx/list are captured by Run so SetConfig can kick an immediate poll the
	// moment autopatch is enabled (instead of waiting up to a full poll interval).
	baseCtx context.Context
	list    ListFunc

	flushGrace time.Duration // idle wait before the batch pipeline (overridable in tests)
	queue      chan string
}

// New builds the engine. runner is the active pipeline runner (Cloud Build or local;
// nil => propose-only). source is the local controller used to apply fixes to the
// SOURCE_ROOT between investigations and to reset for the isolate fallback (nil when
// no local controller exists). Config defaults per the struct literal below.
func New(ag *agent.Service, source *democtl.Controller, runner remediator, hist *history.Store, arts *artifact.Store) *Engine {
	e := &Engine{
		agent:     ag,
		hist:      hist,
		arts:      arts,
		localMode: runner != nil, // "apply/build/deploy available" (the frontend's localMode)
		cfg: api.AutopilotConfig{
			Enabled: true,
			Stages:  api.AutopilotStages{Apply: true, Test: true, Build: true, Deploy: true},
		},
		runs:       map[string]*api.AutopilotRun{},
		baseline:   map[string]int{},
		skip:       map[string]bool{},
		flushGrace: batchFlushGrace,
		queue:      make(chan string, 128),
	}
	// Assign only when present so the fields stay true nil interfaces (avoids the
	// typed-nil trap behind the `== nil` checks).
	if source != nil {
		e.source = source
	}
	if runner != nil {
		e.runner = runner
	}
	return e
}

// SetGitSource wires the managed Git source so successful auto-fixes are committed to a
// per-problem branch (push gated). The autopilot NEVER merges — confirmation does.
func (e *Engine) SetGitSource(gs *gitsource.Manager) { e.git = gs }

// SetVersions wires the deploy-version store so every successful autopilot deploy
// is recorded as a revertable version.
func (e *Engine) SetVersions(v *versions.Store) { e.vers = v }

// Run starts the serial worker and the problem poller until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, interval time.Duration, list ListFunc) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	e.mu.Lock()
	e.baseCtx = ctx // captured so SetConfig can kick an immediate poll on enable
	e.list = list
	e.mu.Unlock()
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

// tick polls the problem feed and enqueues anything the autopilot should act on:
// any currently-open problem it hasn't handled yet (so enabling autopatch picks up
// the problems already on the board), plus a fresh occurrence burst on a problem
// whose previous run already finished (a recurrence). Problems with an in-flight run
// are left alone, and finished ones aren't re-run unless their occurrences climb.
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
		prev := e.baseline[p.ID]
		e.baseline[p.ID] = p.Occurrences // always account for what we've observed
		if !e.cfg.Enabled || e.skip[p.ID] {
			continue
		}
		r, hasRun := e.runs[p.ID]
		if hasRun && !terminal(r.Phase) {
			continue // already queued or running
		}
		// Act on a never-handled open problem, or a fresh occurrence burst past a
		// finished run (a recurrence). A finished run with static occurrences is left
		// alone so a deployed/failed fix doesn't loop.
		if !hasRun || p.Occurrences > prev {
			e.runs[p.ID] = &api.AutopilotRun{
				ProblemID: p.ID, Title: p.Title, Kind: p.Kind, Phase: "queued", UpdatedAt: nowRFC(),
			}
			if !hasRun {
				e.order = append(e.order, p.ID)
			}
			toEnqueue = append(toEnqueue, p.ID)
		}
	}
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
			e.runBatch(ctx, id)
		}
	}
}

// investigated is a problem whose investigation produced a patch that was applied to
// the source, carried into the batch pipeline.
type investigated struct {
	id    string
	kind  string
	patch *tools.PatchProposal
}

// runBatch is one auto-patch cycle. Phase 1 APPLIES each problem's fix to the source
// (retry once, skip on persistent failure) and waits for sibling problems to apply
// too — coalescing anything detected within batchFlushGrace of the last apply — so
// nothing is built one-by-one. Phase 2 then runs ONE consolidated pipeline for every
// applied fix; on a consolidated failure it isolates each fix (reset → fix → deploy).
func (e *Engine) runBatch(parent context.Context, firstID string) {
	e.mu.Lock()
	if !e.cfg.Enabled {
		e.mu.Unlock()
		e.setPhase(firstID, "halted", "Autopatch off or halted before start.", nil)
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	e.activeIDs = map[string]bool{}
	e.cancel = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		e.activeIDs = nil
		e.cancel = nil
		e.stopping = false
		e.mu.Unlock()
	}()

	// Phase 1 — apply. Investigate + apply each problem's patch to source; applying
	// between problems makes later same-file patches cumulative. Coalesce stragglers
	// until the apply queue is idle for batchFlushGrace (or the batch is full).
	var batch []investigated
	applyOne := func(id string) {
		e.mu.Lock()
		e.activeIDs[id] = true
		e.mu.Unlock()
		if e.skipped(id) || runCtx.Err() != nil {
			e.markHalted(id)
			return
		}
		if in, ok := e.investigateAndApply(runCtx, id); ok {
			batch = append(batch, in)
		}
	}
	applyOne(firstID)
coalesce:
	for len(batch) < maxBatchSize {
		select {
		case <-runCtx.Done():
			break coalesce
		case id := <-e.queue:
			applyOne(id)
		case <-time.After(e.flushGrace):
			break coalesce
		}
	}

	if runCtx.Err() != nil {
		for _, in := range batch {
			e.markHalted(in.id)
		}
		return
	}
	// Propose-only mode, or nothing applied successfully.
	if e.runner == nil || len(batch) == 0 {
		return
	}

	// Phase 2 — pipeline. Verify ALL applied fixes in ONE run; the deduped-by-file set
	// carries each file's latest (cumulative) patch.
	ids := make([]string, 0, len(batch))
	for _, in := range batch {
		ids = append(ids, in.id)
		e.setPhase(in.id, "remediating", "All patches applied — running the batch pipeline…", nil)
	}
	res := e.remediate(runCtx, ids, dedupeByFile(batch), scenariosOf(batch))
	if runCtx.Err() != nil {
		for _, id := range ids {
			e.markHalted(id)
		}
		return
	}
	if res.Success {
		for _, in := range batch {
			e.commitFix(parent, in.id, in.patch)
			e.setPhase(in.id, "deployed", "Fixed & deployed"+verifySuffix(res), boolPtr(true))
		}
		return
	}

	// Consolidated pipeline failed → isolate. A single-fix batch has nothing to isolate.
	if len(batch) == 1 {
		e.setPhase(batch[0].id, "failed", "Pipeline failed"+verifySuffix(res)+failureSummary(res), boolPtr(false))
		return
	}
	e.isolate(parent, runCtx, batch)
}

// isolate is the fallback when a consolidated batch fails: it resets the source to
// a clean base and re-fixes each problem on its own (investigate → single-fix
// pipeline → deploy), so the fixes that work still ship and a single bad patch is
// the only one marked failed. Processing stays sequential, so each fix's fresh
// investigation still reads the source cumulatively patched by the prior fix.
func (e *Engine) isolate(parent, runCtx context.Context, patched []investigated) {
	if e.source == nil {
		for _, in := range patched {
			e.setPhase(in.id, "failed", "Batch failed; isolated retry needs local source control (unavailable).", boolPtr(false))
		}
		return
	}
	if err := e.source.ResetSource(runCtx); err != nil {
		for _, in := range patched {
			e.setPhase(in.id, "failed", "Batch failed; reset for isolated retry failed: "+err.Error(), boolPtr(false))
		}
		return
	}
	for _, in := range patched {
		if runCtx.Err() != nil || e.skipped(in.id) {
			e.markHalted(in.id)
			continue
		}
		e.setPhase(in.id, "investigating", "Batch failed — retrying this fix on its own…", nil)
		fresh, ok := e.investigateAndApply(runCtx, in.id)
		if !ok {
			continue
		}
		e.setPhase(fresh.id, "remediating", "Applying & verifying the fix…", nil)
		r := e.remediate(runCtx, []string{fresh.id}, []tools.PatchProposal{*fresh.patch}, []string{fresh.kind})
		if runCtx.Err() != nil {
			e.markHalted(fresh.id)
			continue
		}
		if r.Success {
			e.commitFix(parent, fresh.id, fresh.patch)
			e.setPhase(fresh.id, "deployed", "Fixed & deployed"+verifySuffix(r), boolPtr(true))
		} else {
			e.setPhase(fresh.id, "failed", "Pipeline failed"+verifySuffix(r)+failureSummary(r), boolPtr(false))
		}
	}
}

// investigateAndApply investigates a problem and applies its patch to the source,
// retrying once on failure (investigation error, no patch, or a failed apply) before
// skipping. ok=true means the fix is applied and ready to join the batch pipeline;
// ok=false means it was skipped (failed after retry, propose-only, or cancelled).
func (e *Engine) investigateAndApply(ctx context.Context, id string) (investigated, bool) {
	kind := e.kindOf(id)
	lastErr := "no patch was proposed"
	for attempt := 0; attempt <= applyRetries; attempt++ {
		if ctx.Err() != nil {
			e.markHalted(id)
			return investigated{}, false
		}
		if attempt == 0 {
			e.setPhase(id, "investigating", "Investigating root cause…", nil)
		} else {
			e.setPhase(id, "investigating", fmt.Sprintf("Patch failed — retrying (attempt %d/%d)…", attempt+1, applyRetries+1), nil)
		}
		patch, err := e.investigateRaw(ctx, id)
		if ctx.Err() != nil {
			e.markHalted(id)
			return investigated{}, false
		}

		// Propose-only mode (no pipeline runner): record + stop, never retried.
		if e.runner == nil {
			if err == nil && patch != nil {
				e.arts.RecordInvestigation(id, e.titleOf(id), kind, true, "")
				e.hist.RecordProposed(id, patch.File, patch.UnifiedDiff, patch.Rationale)
				e.setPhase(id, "proposed", "Patch proposed — approve to apply (no pipeline runner).", nil)
			} else {
				e.setPhase(id, "failed", "Investigation failed: "+errStr(err), boolPtr(false))
				e.arts.RecordInvestigation(id, e.titleOf(id), kind, false, errStr(err))
			}
			return investigated{}, false
		}

		if err == nil && patch != nil {
			// Apply to the local source so the NEXT investigation reads it cumulatively
			// (and a cloud build uploads source already carrying the prior fixes).
			if e.applyStage() && e.source != nil {
				if aerr := e.source.ApplyPatches([]tools.PatchProposal{*patch}); aerr != nil {
					lastErr = "apply: " + aerr.Error()
					continue // retry
				}
			}
			e.arts.RecordInvestigation(id, e.titleOf(id), kind, true, "")
			e.hist.RecordProposed(id, patch.File, patch.UnifiedDiff, patch.Rationale)
			return investigated{id: id, kind: kind, patch: patch}, true
		}
		if err != nil {
			lastErr = err.Error()
		}
	}
	// Exhausted the retry → skip this problem so it doesn't block the batch.
	e.setPhase(id, "failed", "Patch failed after retry — skipped: "+lastErr, boolPtr(false))
	e.arts.RecordInvestigation(id, e.titleOf(id), kind, false, lastErr)
	return investigated{}, false
}

// investigateRaw runs one agent investigation pass for a problem, streaming its tool
// steps, and returns the proposed patch (or nil) and any error.
func (e *Engine) investigateRaw(ctx context.Context, id string) (*tools.PatchProposal, error) {
	onStep := func(stage, status, message string) {
		e.appendStep(id, api.Step{Stage: stage, Status: status, Message: message})
	}
	_, patch, err := e.agent.Investigate(ctx, "auto-"+id, e.agent.InvestigatePrompt(id), onStep)
	return patch, err
}

// errStr renders an error as a string, or "" when nil.
func errStr(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

// remediate applies the given patch set in ONE pipeline run, fanning each step out
// to every problem in the batch and recording the shared result against all of them.
func (e *Engine) remediate(ctx context.Context, ids []string, patches []tools.PatchProposal, scenarios []string) api.PipelineResult {
	stages := e.stagesSnapshot()
	opts := democtl.Options{
		Apply:     stages.Apply,
		Test:      stages.Test,
		Build:     stages.Build,
		Deploy:    stages.Deploy,
		Patches:   patches,
		Scenarios: scenarios,
	}
	e.arts.RecordRunning(ids)
	res := e.runner.Remediate(ctx, opts, func(s api.Step) {
		for _, id := range ids {
			e.appendStep(id, s)
			e.setMessage(id, s.Stage+": "+s.Message)
		}
		e.arts.AppendStep(ids, s)
	})
	e.hist.RecordPipeline(strings.Join(ids, ", "), res)
	e.arts.RecordRun(ids, res)
	// Track the deploy as a revertable version (Success alone isn't enough — a
	// test/build-only run also reports Success without shipping anything).
	if res.Success && opts.Deploy && e.vers != nil && len(patches) > 0 {
		e.vers.RecordDeploy("autopilot", ids, scenarios, patches, res)
	}
	return res
}

// commitFix commits a successful auto-fix to its isolated per-problem branch (push
// gated). The autopilot NEVER merges — a human confirms to merge. Best-effort: a
// git failure doesn't undo a successful deploy.
func (e *Engine) commitFix(parent context.Context, id string, patch *tools.PatchProposal) {
	if e.git == nil || patch == nil || !e.git.IsConnected() || !e.git.Config().BranchPerFix {
		return
	}
	c, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	if _, _, err := e.git.CommitPatch(c, id, map[string]string{patch.File: patch.PatchedContent}, "PatchPilot autopilot fix for "+id); err != nil {
		log.Printf("autopilot: git commit for %s: %v", id, err)
	}
}

// dedupeByFile returns one proposal per target file, keeping the LATEST in apply
// order. Because patches are applied sequentially, the latest patch for a file
// carries every earlier fix to that file, so this is the correct consolidated set.
func dedupeByFile(in []investigated) []tools.PatchProposal {
	byFile := map[string]tools.PatchProposal{}
	var order []string
	for _, x := range in {
		if _, seen := byFile[x.patch.File]; !seen {
			order = append(order, x.patch.File)
		}
		byFile[x.patch.File] = *x.patch
	}
	out := make([]tools.PatchProposal, 0, len(order))
	for _, f := range order {
		out = append(out, byFile[f])
	}
	return out
}

// scenariosOf returns the distinct scenario kinds across the batch (for the test
// gate and post-deploy verify).
func scenariosOf(in []investigated) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range in {
		if x.kind != "" && !seen[x.kind] {
			seen[x.kind] = true
			out = append(out, x.kind)
		}
	}
	return out
}

// SetConfig updates the live config. Enabling kicks an immediate poll so open
// problems are picked up at once (instead of waiting up to a full poll interval).
// Disabling cancels the active run and drains any queued runs (marking them halted).
func (e *Engine) SetConfig(cfg api.AutopilotConfig) {
	e.mu.Lock()
	wasEnabled := e.cfg.Enabled
	e.cfg = cfg
	if wasEnabled && !cfg.Enabled {
		if e.cancel != nil {
			e.cancel()
			e.cancelRunnerBuild()
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
	enabling := !wasEnabled && cfg.Enabled
	ctx, list := e.baseCtx, e.list
	e.mu.Unlock()

	if enabling && ctx != nil && list != nil {
		go e.tick(ctx, list)
	}
}

// StopAll halts everything for a source-target swap: it cancels the active batch
// (context + remote Cloud Build), drains the queue, and marks every non-terminal
// run halted with the given reason. It returns how many runs it halted. Unlike
// Cancel, problems are NOT marked skip — and baseline is cleared — so the next
// poll re-picks the still-open problems against the NEW source. History and
// artifacts are untouched (they are the audit record).
func (e *Engine) StopAll(reason string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.stopping = true // the dying batch's markHalted must not undo the skip clear below
		e.cancel()
		e.cancelRunnerBuild()
	}
	for draining := true; draining; {
		select {
		case <-e.queue:
		default:
			draining = false
		}
	}
	halted := 0
	for _, r := range e.runs {
		if r != nil && !terminal(r.Phase) {
			r.Phase = "halted"
			r.Message = reason
			r.UpdatedAt = nowRFC()
			halted++
		}
	}
	e.skip = map[string]bool{}
	e.baseline = map[string]int{}
	return halted
}

// Cancel halts a problem's automation (and prevents it being re-picked), handing
// it to manual control. Cancelling the active run's context also kills in-flight
// go test/build (democtl uses exec.CommandContext).
func (e *Engine) Cancel(problemID string) {
	e.mu.Lock()
	e.skip[problemID] = true
	if e.activeIDs[problemID] && e.cancel != nil {
		e.cancel()
		e.cancelRunnerBuild()
	}
	if r := e.runs[problemID]; r != nil && !terminal(r.Phase) {
		r.Phase = "halted"
		r.Message = "Halted — handed to manual control."
		r.UpdatedAt = nowRFC()
	}
	e.mu.Unlock()
}

// cancelRunnerBuild asks the active runner to abort its in-flight remote build.
// Cancelling the run context only stops our side; a submitted Cloud Build keeps
// running server-side (and would still deploy) unless cancelled via the API. Local
// runners don't implement CancelActive — their processes die with the context.
func (e *Engine) cancelRunnerBuild() {
	c, ok := e.runner.(interface{ CancelActive(context.Context) error })
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.CancelActive(ctx); err != nil {
			log.Printf("autopilot: cancel remote build: %v", err)
		}
	}()
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
	var active []string
	for id := range e.activeIDs {
		active = append(active, id)
	}
	sort.Strings(active)
	return api.AutopilotSnapshot{Config: e.cfg, Runs: runs, LocalMode: e.localMode, ActiveIDs: active}
}

// stagesSnapshot returns the currently-configured pipeline stages under lock.
func (e *Engine) stagesSnapshot() api.AutopilotStages {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Stages
}

// skipped reports whether a problem should no longer be auto-patched — either it
// was cancelled (handed to manual control) or autopatch was turned off.
func (e *Engine) skipped(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.skip[id] || !e.cfg.Enabled
}

// applyStage reports whether the configured pipeline includes the apply stage.
func (e *Engine) applyStage() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Stages.Apply
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
	if !e.stopping {
		e.skip[id] = true // a StopAll teardown wants the problem re-picked against the new source
	}
	r := e.runs[id]
	alreadyHalted := r != nil && r.Phase == "halted"
	e.mu.Unlock()
	if alreadyHalted {
		return // keep the original halt reason (e.g. StopAll's source-swap message)
	}
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

// failureSummary says WHY a pipeline failed from its last failing step, so the run's
// terminal message carries the cause (stage, message, a trimmed log excerpt) instead
// of a bare "Pipeline failed".
func failureSummary(res api.PipelineResult) string {
	for i := len(res.Steps) - 1; i >= 0; i-- {
		s := res.Steps[i]
		if s.Status != "fail" {
			continue
		}
		out := " — " + s.Stage + ": " + s.Message
		if d := strings.TrimSpace(s.Detail); d != "" {
			if len(d) > 200 {
				d = d[:200] + "…"
			}
			out += "\n" + d
		}
		return out
	}
	return ""
}

func nowRFC() string       { return time.Now().UTC().Format(time.RFC3339) }
func boolPtr(b bool) *bool { return &b }
