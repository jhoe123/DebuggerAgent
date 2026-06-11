package autopilot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/history"
	"github.com/patchpilot/backend/internal/tools"
	"github.com/patchpilot/backend/internal/versions"
)

// fakeAgent resolves each problem's investigation from a fixed table keyed by the
// problem id parsed from the session ("auto-<id>").
type fakeAgent struct {
	patches map[string]*tools.PatchProposal
	errs    map[string]error
}

func (f *fakeAgent) InvestigatePrompt(id string) string { return "prompt:" + id }

func (f *fakeAgent) Investigate(ctx context.Context, sessionID, prompt string, onStep agent.StepFunc) (string, *tools.PatchProposal, error) {
	id := strings.TrimPrefix(sessionID, "auto-")
	if onStep != nil {
		onStep("investigate", "ok", "looked at "+id)
	}
	if err := f.errs[id]; err != nil {
		return "", nil, err
	}
	return "final", f.patches[id], nil
}

// fakeDemo records every Remediate/ApplyPatches/ResetSource call and decides
// pipeline success via fail(opts).
type fakeDemo struct {
	mu      sync.Mutex
	calls   []democtl.Options
	applied [][]tools.PatchProposal
	resets  int
	fail    func(opts democtl.Options) bool
}

func (d *fakeDemo) Remediate(ctx context.Context, opts democtl.Options, emit func(api.Step)) api.PipelineResult {
	d.mu.Lock()
	d.calls = append(d.calls, opts)
	d.mu.Unlock()
	if emit != nil {
		emit(api.Step{Stage: "apply", Status: "ok", Message: "applied"})
	}
	success := true
	if d.fail != nil {
		success = !d.fail(opts)
	}
	return api.PipelineResult{Success: success, Verify: "healthz 200"}
}

func (d *fakeDemo) ApplyPatches(list []tools.PatchProposal) error {
	d.mu.Lock()
	d.applied = append(d.applied, list)
	d.mu.Unlock()
	return nil
}

func (d *fakeDemo) ResetSource(ctx context.Context) error {
	d.mu.Lock()
	d.resets++
	d.mu.Unlock()
	return nil
}

func (d *fakeDemo) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *fakeDemo) resetCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resets
}

// testDeps is satisfied by anything usable as both the pipeline runner and the local
// source controller (fakeDemo and the real *democtl.Controller).
type testDeps interface {
	remediator
	sourceController
}

func newTestEngine(ag investigator, demo testDeps) *Engine {
	e := &Engine{
		agent: ag,
		hist:  history.New(0, ""),
		arts:  artifact.New("", false),
		cfg: api.AutopilotConfig{
			Enabled: true,
			Stages:  api.AutopilotStages{Apply: true, Test: true, Build: true, Deploy: true},
		},
		runs:       map[string]*api.AutopilotRun{},
		baseline:   map[string]int{},
		skip:       map[string]bool{},
		flushGrace: 10 * time.Millisecond, // keep the coalesce window short in tests
		queue:      make(chan string, 128),
	}
	if demo != nil {
		e.runner = demo
		e.source = demo
		e.localMode = true
	}
	return e
}

// runBatchWith enqueues any extra problem ids, then runs one batch cycle seeded with
// firstID (mirroring how the worker drives runBatch off the poll queue).
func runBatchWith(e *Engine, firstID string, rest ...string) {
	for _, id := range rest {
		e.queue <- id
	}
	e.runBatch(context.Background(), firstID)
}

func drainQueue(e *Engine) []string {
	var out []string
	for {
		select {
		case id := <-e.queue:
			out = append(out, id)
		default:
			return out
		}
	}
}

// Problems seen while autopatch was OFF are baselined but never handled; enabling
// must then pick them up (the "autopatch on does nothing" bug). Static occurrences
// must NOT cause an already-handled or in-flight problem to be re-enqueued.
func TestTick_EnablingPicksUpKnownOpenProblems(t *testing.T) {
	probs := []api.Problem{
		{ID: "error:checkout-demo", Occurrences: 5},
		{ID: "performance:report-demo", Occurrences: 3},
	}
	list := func(context.Context) ([]api.Problem, error) { return probs, nil }
	e := newTestEngine(&fakeAgent{}, &fakeDemo{})

	// Autopatch OFF: poll only records the baseline, enqueues nothing.
	e.cfg.Enabled = false
	e.tick(context.Background(), list)
	if got := drainQueue(e); len(got) != 0 {
		t.Fatalf("disabled poll should enqueue nothing, got %v", got)
	}

	// Turn autopatch ON: the next poll must enqueue both already-known open problems.
	e.cfg.Enabled = true
	e.tick(context.Background(), list)
	if got := drainQueue(e); len(got) != 2 {
		t.Fatalf("enabling should enqueue both known open problems, got %v", got)
	}

	// A follow-up poll with the same static occurrences must not re-enqueue (runs are
	// now in-flight at phase "queued").
	e.tick(context.Background(), list)
	if got := drainQueue(e); len(got) != 0 {
		t.Fatalf("static re-poll must not re-enqueue in-flight problems, got %v", got)
	}
}

func phaseOf(e *Engine, id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.runs[id]; r != nil {
		return r.Phase
	}
	return ""
}

func scenarioSet(opts democtl.Options) map[string]bool {
	s := map[string]bool{}
	for _, sc := range opts.Scenarios {
		s[sc] = true
	}
	return s
}

// Happy path: two problems surfacing together are investigated then deployed in a
// SINGLE consolidated pipeline run carrying both patches and both scenarios.
func TestProcessBatch_ConsolidatesTwoFixes(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout":     {File: "checkout.go", PatchedContent: "x"},
		"performance:report": {File: "report.go", PatchedContent: "y"},
	}}
	demo := &fakeDemo{}
	e := newTestEngine(ag, demo)

	runBatchWith(e, "error:checkout", "performance:report")

	if demo.callCount() != 1 {
		t.Fatalf("expected exactly 1 consolidated pipeline run, got %d", demo.callCount())
	}
	got := demo.calls[0]
	if len(got.Patches) != 2 {
		t.Fatalf("expected 2 patches in the batch, got %d", len(got.Patches))
	}
	sc := scenarioSet(got)
	if len(sc) != 2 || !sc["error"] || !sc["performance"] {
		t.Fatalf("expected scenarios {error, performance}, got %v", got.Scenarios)
	}
	// Each fix is applied to source before the next is investigated (cumulative).
	if len(demo.applied) != 2 {
		t.Fatalf("expected 2 sequential source applies, got %d", len(demo.applied))
	}
	if p := phaseOf(e, "error:checkout"); p != "deployed" {
		t.Fatalf("error:checkout phase = %q, want deployed", p)
	}
	if p := phaseOf(e, "performance:report"); p != "deployed" {
		t.Fatalf("performance:report phase = %q, want deployed", p)
	}
}

// One investigation fails to produce a patch: that problem is marked failed and
// excluded, while the other still deploys in a single (1-patch) run.
func TestProcessBatch_ExcludesFailedInvestigation(t *testing.T) {
	ag := &fakeAgent{
		patches: map[string]*tools.PatchProposal{
			"error:checkout": {File: "checkout.go", PatchedContent: "x"},
		},
		errs: map[string]error{
			"performance:report": context.DeadlineExceeded,
		},
	}
	demo := &fakeDemo{}
	e := newTestEngine(ag, demo)

	runBatchWith(e, "error:checkout", "performance:report")

	if demo.callCount() != 1 {
		t.Fatalf("expected 1 pipeline run, got %d", demo.callCount())
	}
	if len(demo.calls[0].Patches) != 1 {
		t.Fatalf("expected 1 patch (failed one excluded), got %d", len(demo.calls[0].Patches))
	}
	if p := phaseOf(e, "error:checkout"); p != "deployed" {
		t.Fatalf("error:checkout phase = %q, want deployed", p)
	}
	if p := phaseOf(e, "performance:report"); p != "failed" {
		t.Fatalf("performance:report phase = %q, want failed", p)
	}
}

// Consolidated run fails → isolate: retry each fix alone, deploying only the ones
// that pass on their own.
func TestProcessBatch_IsolatesOnBatchFailure(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout":     {File: "checkout.go", PatchedContent: "x"},
		"performance:report": {File: "report.go", PatchedContent: "y"},
	}}
	// Fail the consolidated run (>1 patch) and fail report.go even on its own; let
	// checkout.go pass alone.
	demo := &fakeDemo{fail: func(o democtl.Options) bool {
		if len(o.Patches) > 1 {
			return true
		}
		return len(o.Patches) == 1 && o.Patches[0].File == "report.go"
	}}
	e := newTestEngine(ag, demo)

	runBatchWith(e, "error:checkout", "performance:report")

	// 1 consolidated + 2 isolated retries, with a source reset before isolating.
	if demo.callCount() != 3 {
		t.Fatalf("expected 3 pipeline runs (1 batch + 2 isolated), got %d", demo.callCount())
	}
	if demo.resetCount() != 1 {
		t.Fatalf("expected 1 source reset before isolating, got %d", demo.resetCount())
	}
	if p := phaseOf(e, "error:checkout"); p != "deployed" {
		t.Fatalf("error:checkout phase = %q, want deployed (passes alone)", p)
	}
	if p := phaseOf(e, "performance:report"); p != "failed" {
		t.Fatalf("performance:report phase = %q, want failed (fails alone)", p)
	}
}

// cancelableDemo is a runner whose Remediate blocks until its context is cancelled,
// and which records CancelActive calls — mirroring the cloud runner's halt path
// (context cancel stops polling; CancelActive aborts the remote build).
type cancelableDemo struct {
	fakeDemo
	started   chan struct{} // closed when Remediate begins
	cancelled chan struct{} // closed on CancelActive
	startOnce sync.Once
	stopOnce  sync.Once
}

func newCancelableDemo() *cancelableDemo {
	return &cancelableDemo{started: make(chan struct{}), cancelled: make(chan struct{})}
}

func (d *cancelableDemo) Remediate(ctx context.Context, opts democtl.Options, emit func(api.Step)) api.PipelineResult {
	d.startOnce.Do(func() { close(d.started) })
	<-ctx.Done()
	return api.PipelineResult{Success: false}
}

func (d *cancelableDemo) CancelActive(ctx context.Context) error {
	d.stopOnce.Do(func() { close(d.cancelled) })
	return nil
}

// Halting a problem mid-pipeline must cancel the run context AND ask the runner to
// abort its remote build; the snapshot must expose the in-flight batch while it
// runs and clear it after.
func TestCancel_AbortsRunnerBuildAndClearsActiveIDs(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout": {File: "checkout.go", PatchedContent: "x"},
	}}
	demo := newCancelableDemo()
	e := newTestEngine(ag, demo)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runBatchWith(e, "error:checkout")
	}()

	select {
	case <-demo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline never started")
	}
	snap := e.Snapshot()
	if len(snap.ActiveIDs) != 1 || snap.ActiveIDs[0] != "error:checkout" {
		t.Fatalf("mid-batch snapshot should expose the active problem, got %v", snap.ActiveIDs)
	}

	e.Cancel("error:checkout")
	select {
	case <-demo.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("halt never reached the runner's CancelActive")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch never finished after cancel")
	}
	if p := phaseOf(e, "error:checkout"); p != "halted" {
		t.Fatalf("phase = %q, want halted", p)
	}
	if got := e.Snapshot().ActiveIDs; len(got) != 0 {
		t.Fatalf("post-batch snapshot should have no active ids, got %v", got)
	}
}

// Disabling autopatch mid-run must also abort the runner's remote build and drain
// queued problems to halted.
func TestSetConfig_DisableAbortsRunnerBuild(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout": {File: "checkout.go", PatchedContent: "x"},
	}}
	demo := newCancelableDemo()
	e := newTestEngine(ag, demo)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runBatchWith(e, "error:checkout")
	}()
	select {
	case <-demo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline never started")
	}

	cfg := e.Snapshot().Config
	cfg.Enabled = false
	e.SetConfig(cfg)
	select {
	case <-demo.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("disable never reached the runner's CancelActive")
	}
	<-done
}

// A successful autopilot deploy records exactly one revertable version; a failed
// batch records none; deploy-disabled stages record none.
func TestRemediate_RecordsDeployVersion(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout":     {File: "checkout.go", PatchedContent: "x"},
		"performance:report": {File: "report.go", PatchedContent: "y"},
	}}
	demo := &fakeDemo{}
	e := newTestEngine(ag, demo)
	e.vers = versions.New("", 0, false)

	runBatchWith(e, "error:checkout", "performance:report")

	list := e.vers.List()
	if len(list) != 1 {
		t.Fatalf("one consolidated deploy should record one version, got %d", len(list))
	}
	v := list[0]
	if v.Source != "autopilot" || len(v.ProblemIDs) != 2 || len(v.Files) != 2 {
		t.Fatalf("unexpected version: %+v", v)
	}

	// Failed pipeline (single fix, no isolate) records nothing new.
	ag.errs = nil
	demo.fail = func(democtl.Options) bool { return true }
	runBatchWith(e, "error:other")
	if got := len(e.vers.List()); got != 1 {
		t.Fatalf("failed run must not record a version, got %d", got)
	}

	// Deploy stage off: success without a ship records nothing.
	demo.fail = nil
	e.cfg.Stages.Deploy = false
	e.skip = map[string]bool{}
	delete(e.runs, "error:checkout")
	runBatchWith(e, "error:checkout")
	if got := len(e.vers.List()); got != 1 {
		t.Fatalf("deploy-disabled run must not record a version, got %d", got)
	}
}

// failureSummary should carry the last failing step's stage/message and a trimmed
// detail excerpt, so terminal "failed" messages explain themselves.
func TestFailureSummary(t *testing.T) {
	res := api.PipelineResult{Steps: []api.Step{
		{Stage: "test", Status: "fail", Message: "go test failed", Detail: "FAIL: TestCheckout\n" + strings.Repeat("x", 300)},
		{Stage: "build", Status: "ok", Message: "built"},
	}}
	got := failureSummary(res)
	if !strings.Contains(got, "test: go test failed") {
		t.Errorf("summary should name the failing stage+message: %q", got)
	}
	if !strings.Contains(got, "FAIL: TestCheckout") || !strings.Contains(got, "…") {
		t.Errorf("summary should carry a trimmed detail excerpt: %q", got)
	}
	if failureSummary(api.PipelineResult{}) != "" {
		t.Error("no failing step should yield an empty summary")
	}
}

// Propose-only mode (no demo controller): patches are proposed, never remediated.
func TestProcessBatch_ProposeOnlyWhenNoDemo(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout": {File: "checkout.go", PatchedContent: "x"},
	}}
	e := newTestEngine(ag, nil)

	runBatchWith(e, "error:checkout")

	if p := phaseOf(e, "error:checkout"); p != "proposed" {
		t.Fatalf("error:checkout phase = %q, want proposed", p)
	}
}

// StopAll (a Git source target swap) must cancel the active batch + remote build,
// drain queued problems, halt every non-terminal run WITH the swap reason (the
// dying batch's markHalted must not overwrite it), and clear skip/baseline so the
// next poll re-picks the still-open problems against the new source.
func TestStopAll_HaltsActiveCancelsBuildAndDrainsQueue(t *testing.T) {
	ag := &fakeAgent{patches: map[string]*tools.PatchProposal{
		"error:checkout": {File: "checkout.go", PatchedContent: "x"},
	}}
	demo := newCancelableDemo()
	e := newTestEngine(ag, demo)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.runBatch(context.Background(), "error:checkout")
	}()
	select {
	case <-demo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline never started")
	}

	// The batch is blocked in the pipeline (past its coalesce window): seed a second,
	// still-queued problem and some baseline state for StopAll to clear.
	e.mu.Lock()
	e.runs["performance:report"] = &api.AutopilotRun{ProblemID: "performance:report", Phase: "queued"}
	e.order = append(e.order, "performance:report")
	e.baseline["error:checkout"] = 3
	e.mu.Unlock()
	e.queue <- "performance:report"

	const reason = "Halted - Git source target changed."
	if halted := e.StopAll(reason); halted < 1 {
		t.Fatalf("StopAll halted %d runs, want >= 1", halted)
	}
	select {
	case <-demo.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll never reached the runner's CancelActive")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch never finished after StopAll")
	}

	if got := drainQueue(e); len(got) != 0 {
		t.Fatalf("queue should be drained, got %v", got)
	}
	for _, id := range []string{"error:checkout", "performance:report"} {
		if p := phaseOf(e, id); p != "halted" {
			t.Fatalf("%s phase = %q, want halted", id, p)
		}
	}
	e.mu.Lock()
	msg := e.runs["error:checkout"].Message
	skips, base := len(e.skip), len(e.baseline)
	stopping := e.stopping
	e.mu.Unlock()
	if msg != reason {
		t.Fatalf("halted message = %q, want the StopAll reason", msg)
	}
	if skips != 0 || base != 0 {
		t.Fatalf("skip/baseline must stay cleared for the new source, got %d skips / %d baselined", skips, base)
	}
	if stopping {
		t.Fatal("stopping flag must reset when the batch unwinds")
	}
}

// StopAll with nothing in flight is a safe no-op that reports zero halted runs.
func TestStopAll_IdleReturnsZero(t *testing.T) {
	demo := newCancelableDemo()
	e := newTestEngine(&fakeAgent{}, demo)

	if halted := e.StopAll("swap"); halted != 0 {
		t.Fatalf("idle StopAll halted %d runs, want 0", halted)
	}
	select {
	case <-demo.cancelled:
		t.Fatal("idle StopAll must not cancel the runner")
	default:
	}
}
