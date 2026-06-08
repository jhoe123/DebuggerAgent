// Package democtl manages the LOCAL demo_app for the Test Console and the
// auto-remediation pipeline. It is enabled only when ENABLE_TEST_CONSOLE=true and
// is never active in the hosted product. It can build/run/restart demo_app, apply
// the agent's approved patch to its source, run tests as a deploy gate, and reset
// the source to its committed (buggy) state via git.
package democtl

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/tools"
)

// Options selects which pipeline stages run and which scenario to remediate.
type Options struct {
	Apply    bool   `json:"apply"`
	Test     bool   `json:"test"`
	Build    bool   `json:"build"`
	Deploy   bool   `json:"deploy"`
	Scenario string `json:"scenario"` // "error" (default) | "performance"

	// TestStrategy controls the deploy gate's test resolution:
	//   "auto" (default) — reuse a committed/existing test if one covers the fix,
	//                       otherwise the builder agent generates one ("lazy").
	//   "reuse"          — require an existing test; fail if none is found.
	//   "generate"       — always generate a fresh test (skip the seeded-scenario fast path).
	//   "skip"           — bypass the test gate.
	TestStrategy string `json:"testStrategy,omitempty"`

	// BuildStrategy controls how the build stage runs:
	//   "auto" (default) — run a detected build script (build.ps1/build.sh/Makefile) if
	//                       present, otherwise `go build`.
	//   "script"         — require/generate a build script (frontend + Go) and run it.
	//   "default"        — always `go build` (backend binary only).
	BuildStrategy string `json:"buildStrategy,omitempty"`

	// Deployment selects where/how the deploy stage ships the build (see DeploymentSpec).
	Deployment DeploymentSpec `json:"deployment,omitempty"`
}

// DeploymentSpec selects the deploy target and its parameters. Target is one of:
//   "" / "local" — restart the local demo_app process (default).
//   "docker"     — build an image and run it as a container. Params: image, tag, containerName, hostPort.
//   "script"     — run a (detected or generated) deploy script. Params: scriptPath.
//   "cloud-run"  — build via Cloud Build and deploy to Cloud Run (handled by the cloud runner).
type DeploymentSpec struct {
	Target string            `json:"target,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// TestGenFunc resolves the regression test guarding the latest patch: it returns the
// `go test -run` name and any NEW test files to write (empty => an existing test is
// reused). errOut (when non-empty) is a repair turn after the generated test failed to
// compile. Supplied by the server so democtl stays decoupled from the agent package.
type TestGenFunc func(ctx context.Context, file, rationale, errOut string) (runName string, files map[string]string, err error)

// BuildGenFunc generates a build artifact on demand (a build script or Dockerfile),
// returning file -> content. errOut (when non-empty) is a repair turn. Supplied by the
// server so democtl stays decoupled from the agent package.
type BuildGenFunc func(ctx context.Context, kind, errOut string) (files map[string]string, err error)

// Controller owns the local demo_app process and source.
type Controller struct {
	repoRoot string
	demoDir  string // <repo>/demo_app
	demoURL  string // http://localhost:9090
	binPath  string
	otlpEnv  []string
	patches  *tools.PatchStore
	testGen  TestGenFunc  // optional: detect-or-generate the regression test (set by server)
	buildGen BuildGenFunc // optional: generate a build artifact when missing (set by server)

	mu   sync.Mutex
	proc *exec.Cmd
	http *http.Client
}

// SetTestGenerator wires the builder agent's detect-or-generate test resolver. When
// unset, the pipeline falls back to the committed seeded-scenario tests.
func (c *Controller) SetTestGenerator(fn TestGenFunc) { c.testGen = fn }

// SetBuildGenerator wires the builder agent's build-artifact generator. When unset, the
// build stage falls back to a detected build script or `go build`.
func (c *Controller) SetBuildGenerator(fn BuildGenFunc) { c.buildGen = fn }

// New builds a controller. sourceRoot is the demo_app dir (SOURCE_ROOT).
func New(sourceRoot, demoURL, dtEnvironment, dtAPIToken string, patches *tools.PatchStore) *Controller {
	demoDir, _ := filepath.Abs(sourceRoot)
	c := &Controller{
		repoRoot: filepath.Dir(demoDir),
		demoDir:  demoDir,
		demoURL:  strings.TrimRight(demoURL, "/"),
		binPath:  filepath.Join(demoDir, "demo_app_run"+exeSuffix()),
		patches:  patches,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
	// OTLP env so the running demo_app exports exceptions to Dynatrace.
	if dtAPIToken != "" && dtEnvironment != "" {
		endpoint := strings.Replace(dtEnvironment, ".apps.", ".live.", 1) + "/api/v2/otlp"
		c.otlpEnv = []string{
			"OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint,
			"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Api-Token " + dtAPIToken,
			"OTEL_SERVICE_NAME=checkout-demo",
		}
	}
	return c
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// Start builds and launches demo_app (best-effort; logs through the returned error).
func (c *Controller) Start(ctx context.Context) error {
	if ok, _ := c.build(ctx); !ok {
		return fmt.Errorf("demo_app initial build failed")
	}
	return c.launch()
}

func (c *Controller) launch() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
		_, _ = c.proc.Process.Wait()
		c.proc = nil
	}
	cmd := exec.Command(c.binPath)
	cmd.Dir = c.demoDir
	cmd.Env = append(os.Environ(), c.otlpEnv...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch demo_app: %w", err)
	}
	c.proc = cmd
	return nil
}

// Stop terminates the demo_app process.
func (c *Controller) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
		_, _ = c.proc.Process.Wait()
		c.proc = nil
	}
}

// Status reports the demo source/process state for the Test Console.
func (c *Controller) Status(ctx context.Context) api.TestStatus {
	state := "buggy"
	out, _ := c.git(ctx, "status", "--porcelain", "demo_app/main.go")
	if strings.TrimSpace(out) != "" {
		state = "modified"
	}
	return api.TestStatus{
		SourceState:  state,
		Reachable:    c.reachable(ctx),
		PendingPatch: c.patches.Latest() != nil,
		DemoAppURL:   c.demoURL,
	}
}

// Trigger seeds telemetry for BOTH scenarios: the buggy /checkout (exceptions) and
// the slow /report (high latency), so an error problem and a performance problem
// both surface in Dynatrace.
func (c *Controller) Trigger(ctx context.Context, n int) api.TriggerResult {
	if n <= 0 {
		n = 5
	}
	res := api.TriggerResult{}
	_, _ = c.get(ctx, "/checkout?index=1") // a valid call for contrast
	for i := 0; i < n; i++ {
		code, _ := c.get(ctx, "/checkout?index=99")
		res.Sent++
		res.Codes = append(res.Codes, code)
		rc, _ := c.get(ctx, "/report?n=200") // seed slow-operation telemetry
		res.Sent++
		res.Codes = append(res.Codes, rc)
	}
	return res
}

// ResetSource restores the committed (buggy) demo_app/main.go and restarts.
func (c *Controller) ResetSource(ctx context.Context) error {
	if _, err := c.git(ctx, "checkout", "--", "demo_app/main.go"); err != nil {
		return err
	}
	c.patches.Clear()
	if ok, out := c.build(ctx); !ok {
		return fmt.Errorf("rebuild after reset failed: %s", out)
	}
	return c.launch()
}

// Remediate runs the configured pipeline stages, emitting each as a Step. Deploy is
// gated on tests passing. Returns the terminal result.
func (c *Controller) Remediate(ctx context.Context, opts Options, emit func(api.Step)) api.PipelineResult {
	var steps []api.Step
	var files []string
	verify := ""
	step := func(s api.Step) {
		steps = append(steps, s)
		if emit != nil {
			emit(s)
		}
	}
	fail := func(stage, msg, detail string) api.PipelineResult {
		step(api.Step{Stage: stage, Status: "fail", Message: msg, Detail: detail})
		return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: verify}
	}

	if prop := c.patches.Latest(); prop != nil {
		files = []string{prop.File}
	}
	if opts.Apply {
		step(api.Step{Stage: "apply", Status: "running", Message: "Applying the proposed patch to demo_app source"})
		if err := c.ApplyPatch(); err != nil {
			return fail("apply", "Apply failed", err.Error())
		}
		step(api.Step{Stage: "apply", Status: "ok", Message: "Patch applied to demo_app/main.go"})
	}
	if opts.Test {
		ok, out := c.resolveTest(ctx, opts, step)
		if !ok {
			return fail("test", "Tests failed — deploy blocked", out)
		}
		step(api.Step{Stage: "test", Status: "ok", Message: "Tests passed", Detail: out})
	}
	if opts.Build {
		ok, out := c.resolveBuild(ctx, opts, step)
		if !ok {
			return fail("build", "Build failed", out)
		}
		step(api.Step{Stage: "build", Status: "ok", Message: "Build succeeded"})
	}
	if opts.Deploy {
		baseURL, err := c.deploy(ctx, opts.Deployment, step)
		if err != nil {
			return fail("deploy", "Deploy failed", err.Error())
		}
		step(api.Step{Stage: "deploy", Status: "ok", Message: "Deployed (" + targetName(opts.Deployment) + ")"})

		ok, v := c.verifyDeploy(ctx, baseURL, opts.Scenario, step)
		verify = v
		if !ok {
			return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: verify}
		}
	}
	return api.PipelineResult{Steps: steps, Success: true, Files: files, Verify: verify}
}

// ApplyPatch writes the pending patch's full content to the demo_app source.
func (c *Controller) ApplyPatch() error {
	prop := c.patches.Latest()
	if prop == nil {
		return fmt.Errorf("no patch has been proposed")
	}
	dest := filepath.Join(c.demoDir, filepath.Clean(prop.File))
	return os.WriteFile(dest, []byte(prop.PatchedContent), 0o644)
}

// RepairFunc regenerates the instrumented files after a failed test/build. It
// receives the combined error output and returns file -> full content (same shape
// as the initial apply). Supplied by the server so democtl stays decoupled from
// the agent package.
type RepairFunc func(ctx context.Context, errOutput string) (map[string]string, error)

// maxRepairAttempts caps the auto-debug loop before democtl rolls back.
const maxRepairAttempts = 2

// ApplyInstrumentation writes the supplied instrumented files to demo_app source,
// then runs test → build → deploy → verify(/healthz). On a test or build failure it
// asks repair (the agent) to fix the files and retries, up to maxRepairAttempts; if
// it still can't go green it restores the source via git and reports failure.
func (c *Controller) ApplyInstrumentation(ctx context.Context, files map[string]string, opts Options, repair RepairFunc, emit func(api.Step)) api.PipelineResult {
	var steps []api.Step
	verify := ""
	step := func(s api.Step) {
		steps = append(steps, s)
		if emit != nil {
			emit(s)
		}
	}
	touched := sortedFileKeys(files)
	done := func(success bool) api.PipelineResult {
		return api.PipelineResult{Steps: steps, Success: success, Files: touched, Verify: verify}
	}
	rollback := func(stage, msg, detail string) api.PipelineResult {
		step(api.Step{Stage: stage, Status: "fail", Message: msg, Detail: detail})
		step(api.Step{Stage: "rollback", Status: "running", Message: "Restoring demo_app source to last committed state"})
		if err := c.restore(ctx); err != nil {
			step(api.Step{Stage: "rollback", Status: "fail", Message: "Rollback failed", Detail: err.Error()})
		} else {
			step(api.Step{Stage: "rollback", Status: "ok", Message: "Source restored — no changes kept"})
		}
		return done(false)
	}

	if opts.Apply {
		step(api.Step{Stage: "apply", Status: "running", Message: fmt.Sprintf("Applying instrumentation to %d file(s)", len(files))})
		if err := c.writeFiles(files); err != nil {
			return rollback("apply", "Apply failed", err.Error())
		}
		step(api.Step{Stage: "apply", Status: "ok", Message: "Instrumentation written: " + strings.Join(touched, ", ")})
	}

	// Test → build, with an auto-debug repair loop shared across both gates.
	attempt := 0
	repairOrRollback := func(stage, out string) (api.PipelineResult, bool) {
		if attempt >= maxRepairAttempts || repair == nil {
			return rollback(stage, fmt.Sprintf("%s failed after %d repair attempt(s)", capitalize(stage), attempt), out), false
		}
		attempt++
		step(api.Step{Stage: "debug", Status: "running", Message: fmt.Sprintf("%s failed — agent is repairing the instrumentation (attempt %d/%d)", stage, attempt, maxRepairAttempts)})
		fixed, err := repair(ctx, out)
		if err != nil {
			return rollback("debug", "Auto-debug failed", err.Error()), false
		}
		if err := c.writeFiles(fixed); err != nil {
			return rollback("debug", "Re-apply after repair failed", err.Error()), false
		}
		files = fixed
		touched = sortedFileKeys(files)
		step(api.Step{Stage: "debug", Status: "ok", Message: "Agent repaired the instrumentation; retrying"})
		return api.PipelineResult{}, true
	}
	for {
		if opts.Test {
			step(api.Step{Stage: "test", Status: "running", Message: "Vetting & compiling instrumented code (gate)"})
			ok, out := c.vet(ctx)
			if !ok {
				if rb, retry := repairOrRollback("test", out); !retry {
					return rb
				}
				continue
			}
			step(api.Step{Stage: "test", Status: "ok", Message: "Vet & compile passed", Detail: out})
		}
		if opts.Build {
			step(api.Step{Stage: "build", Status: "running", Message: "Building demo_app"})
			ok, out := c.build(ctx)
			if !ok {
				if rb, retry := repairOrRollback("build", out); !retry {
					return rb
				}
				continue
			}
			step(api.Step{Stage: "build", Status: "ok", Message: "Build succeeded"})
		}
		break
	}

	if opts.Deploy {
		step(api.Step{Stage: "deploy", Status: "running", Message: "Restarting demo_app with the instrumentation"})
		if err := c.launch(); err != nil {
			return rollback("deploy", "Deploy (restart) failed", err.Error())
		}
		time.Sleep(1500 * time.Millisecond) // let it bind the port
		step(api.Step{Stage: "deploy", Status: "ok", Message: "demo_app restarted"})

		step(api.Step{Stage: "verify", Status: "running", Message: "Verifying /healthz"})
		code, _ := c.get(ctx, "/healthz")
		verify = fmt.Sprintf("healthz %d", code)
		if code == 200 {
			_, _ = c.get(ctx, "/checkout?index=1") // emit a span so new telemetry flows to Dynatrace
			step(api.Step{Stage: "verify", Status: "ok", Message: "Healthy — instrumented demo_app is running (HTTP 200 on /healthz)"})
		} else {
			return rollback("verify", fmt.Sprintf("Healthcheck failed (HTTP %d)", code), "")
		}
	}
	return done(true)
}

// writeFiles writes each file (path relative to the source root) into demo_app.
func (c *Controller) writeFiles(files map[string]string) error {
	for rel, content := range files {
		dest := filepath.Join(c.demoDir, filepath.Clean(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restore reverts demo_app to its last committed state and rebuilds/relaunches it.
func (c *Controller) restore(ctx context.Context) error {
	if _, err := c.git(ctx, "checkout", "--", "demo_app"); err != nil {
		return err
	}
	if ok, out := c.build(ctx); !ok {
		return fmt.Errorf("rebuild after rollback failed: %s", out)
	}
	return c.launch()
}

// vet compiles every package (including test files) and runs go vet's static
// checks. It is the instrumentation gate: it catches broken AI-generated edits
// (bad imports, type errors, suspicious constructs) without being blocked by
// demo_app's deliberately-failing seeded-bug regression tests, which assert FIXED
// behavior and are out of scope for adding telemetry.
func (c *Controller) vet(ctx context.Context) (bool, string) {
	out, err := c.goCmd(ctx, "vet", "./...")
	return err == nil, out
}

func sortedFileKeys(files map[string]string) []string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// test runs the regression gate scoped to the scenario being remediated, so fixing
// one bug isn't blocked by the other deliberately-seeded bug's failing test.
func (c *Controller) test(ctx context.Context, scenario string) (bool, string) {
	filter := scenarioFilter(scenario)
	if filter == "" {
		filter = "TestCheckout" // legacy default
	}
	return c.runTest(ctx, filter)
}

// scenarioFilter maps the two seeded scenarios to their committed regression tests.
// Empty => no committed test (the lazy detect-or-generate path handles it).
func scenarioFilter(scenario string) string {
	switch scenario {
	case "performance":
		return "TestReport"
	case "error":
		return "TestCheckout"
	default:
		return ""
	}
}

// runTest runs a single named test (scoped with -run so other deliberately-seeded
// bugs' failing tests don't block this gate).
func (c *Controller) runTest(ctx context.Context, runFilter string) (bool, string) {
	out, err := c.goCmd(ctx, "test", "-run", runFilter, "./...")
	return err == nil, out
}

// looksLikeBuildError distinguishes a generated test that doesn't COMPILE (worth one
// agent repair turn) from one that compiles but FAILS its assertion (a real gate fail).
func looksLikeBuildError(out string) bool {
	for _, s := range []string{"build failed", "[build failed]", "cannot use", "undefined:", "syntax error", "expected ", "imported and not used", "redeclared", "does not implement"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// resolveTest runs the deploy gate's test. For the two seeded scenarios it reuses the
// committed tests (fast, no AI). Otherwise it asks the builder agent to detect-or-
// generate a regression test (the "lazy" gate), writes any generated file, runs it, and
// repairs a non-compiling generated test up to maxRepairAttempts. It emits progress
// steps; the caller emits the terminal ok/fail step. Returns (passed, output).
func (c *Controller) resolveTest(ctx context.Context, opts Options, step func(api.Step)) (bool, string) {
	strategy := opts.TestStrategy
	if strategy == "" {
		strategy = "auto"
	}
	if strategy == "skip" {
		step(api.Step{Stage: "test", Status: "info", Message: "Test gate skipped (testStrategy=skip)"})
		return true, "test gate skipped"
	}

	// Fast path: the seeded scenarios ship committed regression tests.
	if strategy != "generate" {
		if filter := scenarioFilter(opts.Scenario); filter != "" {
			step(api.Step{Stage: "test", Status: "running", Message: "Running committed regression test: " + filter})
			return c.runTest(ctx, filter)
		}
	}

	// No generator wired (agent unavailable): fall back to the package gate.
	if c.testGen == nil {
		step(api.Step{Stage: "test", Status: "running", Message: "Running go test (regression gate)"})
		return c.test(ctx, opts.Scenario)
	}

	var file, rationale string
	if prop := c.patches.Latest(); prop != nil {
		file, rationale = prop.File, prop.Rationale
	}

	errOut, attempt := "", 0
	for {
		step(api.Step{Stage: "test", Status: "running", Message: "Resolving regression test (reuse existing or generate)…"})
		runName, files, err := c.testGen(ctx, file, rationale, errOut)
		if err != nil {
			return false, "test generation failed: " + err.Error()
		}
		if strategy == "reuse" && len(files) > 0 {
			return false, "no existing test covers the fix and testStrategy=reuse"
		}
		if len(files) > 0 {
			if err := c.writeFiles(files); err != nil {
				return false, "writing the generated test failed: " + err.Error()
			}
			step(api.Step{Stage: "test", Status: "info", Message: "Generated regression test: " + strings.Join(sortedFileKeys(files), ", ")})
		} else {
			step(api.Step{Stage: "test", Status: "info", Message: "Reusing existing test " + runName})
		}
		step(api.Step{Stage: "test", Status: "running", Message: "Running go test -run " + runName})
		ok, out := c.runTest(ctx, runName)
		if ok {
			return true, out
		}
		if len(files) > 0 && attempt < maxRepairAttempts && looksLikeBuildError(out) {
			attempt++
			step(api.Step{Stage: "debug", Status: "running", Message: fmt.Sprintf("Generated test didn't compile — agent repairing (attempt %d/%d)", attempt, maxRepairAttempts)})
			errOut = out
			continue
		}
		return false, out
	}
}

func (c *Controller) build(ctx context.Context) (bool, string) {
	out, err := c.goCmd(ctx, "build", "-o", c.binPath, ".")
	return err == nil, out
}

// resolveBuild runs the build stage: a detected build script (build.ps1/build.sh/
// Makefile) when present, otherwise `go build`. With BuildStrategy "script" it asks the
// builder agent to generate a build script (frontend + Go) when none exists. After a
// script build it ensures the local-deploy binary exists. Emits progress; returns (ok, out).
func (c *Controller) resolveBuild(ctx context.Context, opts Options, step func(api.Step)) (bool, string) {
	strategy := opts.BuildStrategy
	if strategy == "" {
		strategy = "auto"
	}
	if strategy != "default" {
		script := c.detectBuildScript()
		if script == "" && strategy == "script" && c.buildGen != nil {
			step(api.Step{Stage: "build", Status: "running", Message: "No build script found — generating one…"})
			files, err := c.buildGen(ctx, "build-script", "")
			if err != nil {
				return false, "build-script generation failed: " + err.Error()
			}
			if err := c.writeFiles(files); err != nil {
				return false, "writing the generated build script failed: " + err.Error()
			}
			step(api.Step{Stage: "build", Status: "info", Message: "Generated build script: " + strings.Join(sortedFileKeys(files), ", ")})
			script = c.detectBuildScript()
		}
		if script != "" {
			step(api.Step{Stage: "build", Status: "running", Message: "Running build script: " + script})
			ok, out := c.runBuildScript(ctx, script)
			if !ok {
				return false, out
			}
			// The script may target a frontend bundle / image only; make sure the
			// local-deploy binary exists so the deploy (restart) step can launch it.
			if _, err := os.Stat(c.binPath); err != nil {
				if bok, bout := c.build(ctx); !bok {
					return false, out + "\n" + bout
				}
			}
			return true, out
		}
	}
	step(api.Step{Stage: "build", Status: "running", Message: "Building demo_app (go build)"})
	return c.build(ctx)
}

// detectBuildScript returns the name of a recognized build script under demoDir, or "".
func (c *Controller) detectBuildScript() string {
	for _, name := range []string{"build.ps1", "build.sh", "Makefile", "makefile"} {
		if _, err := os.Stat(filepath.Join(c.demoDir, name)); err == nil {
			return name
		}
	}
	return ""
}

// runBuildScript executes a detected build script in demoDir, returning its output.
func (c *Controller) runBuildScript(ctx context.Context, script string) (bool, string) {
	var cmd *exec.Cmd
	switch {
	case script == "Makefile" || script == "makefile":
		cmd = exec.CommandContext(ctx, "make", "build")
	case strings.HasSuffix(script, ".ps1"):
		cmd = exec.CommandContext(ctx, "pwsh", "-NoProfile", "-File", script)
	case strings.HasSuffix(script, ".sh"):
		cmd = exec.CommandContext(ctx, "bash", script)
	default:
		return false, "unrecognized build script: " + script
	}
	cmd.Dir = c.demoDir
	b, err := cmd.CombinedOutput()
	return err == nil, string(b)
}

func (c *Controller) goCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = c.demoDir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func (c *Controller) git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-C", c.repoRoot}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func (c *Controller) reachable(ctx context.Context) bool {
	code, err := c.get(ctx, "/healthz")
	return err == nil && code == 200
}

func (c *Controller) get(ctx context.Context, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.demoURL+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// timedGet returns the status code and wall-clock latency of a GET (for perf verify).
func (c *Controller) timedGet(ctx context.Context, path string) (int, time.Duration) {
	start := time.Now()
	code, _ := c.get(ctx, path)
	return code, time.Since(start)
}
