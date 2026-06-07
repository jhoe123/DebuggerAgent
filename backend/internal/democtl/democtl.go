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
	"strings"
	"sync"
	"time"

	"github.com/debuggeragent/backend/internal/api"
	"github.com/debuggeragent/backend/internal/tools"
)

// Options selects which pipeline stages run and which scenario to remediate.
type Options struct {
	Apply    bool   `json:"apply"`
	Test     bool   `json:"test"`
	Build    bool   `json:"build"`
	Deploy   bool   `json:"deploy"`
	Scenario string `json:"scenario"` // "error" (default) | "performance"
}

// Controller owns the local demo_app process and source.
type Controller struct {
	repoRoot string
	demoDir  string // <repo>/demo_app
	demoURL  string // http://localhost:9090
	binPath  string
	otlpEnv  []string
	patches  *tools.PatchStore

	mu   sync.Mutex
	proc *exec.Cmd
	http *http.Client
}

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
	step := func(s api.Step) { steps = append(steps, s); if emit != nil { emit(s) } }
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
		step(api.Step{Stage: "test", Status: "running", Message: "Running go test (regression gate)"})
		ok, out := c.test(ctx, opts.Scenario)
		if !ok {
			return fail("test", "Tests failed — deploy blocked", out)
		}
		step(api.Step{Stage: "test", Status: "ok", Message: "Tests passed", Detail: out})
	}
	if opts.Build {
		step(api.Step{Stage: "build", Status: "running", Message: "Building demo_app"})
		ok, out := c.build(ctx)
		if !ok {
			return fail("build", "Build failed", out)
		}
		step(api.Step{Stage: "build", Status: "ok", Message: "Build succeeded"})
	}
	if opts.Deploy {
		step(api.Step{Stage: "deploy", Status: "running", Message: "Restarting demo_app with the fix"})
		if err := c.launch(); err != nil {
			return fail("deploy", "Deploy (restart) failed", err.Error())
		}
		time.Sleep(1500 * time.Millisecond) // let it bind the port
		step(api.Step{Stage: "deploy", Status: "ok", Message: "demo_app restarted"})

		if opts.Scenario == "performance" {
			step(api.Step{Stage: "verify", Status: "running", Message: "Verifying /report?n=200 latency"})
			code, elapsed := c.timedGet(ctx, "/report?n=200")
			verify = fmt.Sprintf("~657ms -> %dms", elapsed.Milliseconds())
			if code == 200 && elapsed < 150*time.Millisecond {
				step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Fixed — /report?n=200 now %dms (was ~657ms)", elapsed.Milliseconds())})
			} else {
				step(api.Step{Stage: "verify", Status: "fail", Message: fmt.Sprintf("Still slow/failing: HTTP %d in %dms", code, elapsed.Milliseconds())})
				return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: verify}
			}
		} else {
			step(api.Step{Stage: "verify", Status: "running", Message: "Verifying /checkout?index=99"})
			code, _ := c.get(ctx, "/checkout?index=99")
			verify = fmt.Sprintf("500 -> %d", code)
			if code == 200 || code == 400 {
				step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Fixed — /checkout?index=99 now returns HTTP %d (was 500)", code)})
			} else {
				step(api.Step{Stage: "verify", Status: "fail", Message: fmt.Sprintf("Unexpected status HTTP %d", code)})
				return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: verify}
			}
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

// test runs the regression gate scoped to the scenario being remediated, so fixing
// one bug isn't blocked by the other deliberately-seeded bug's failing test.
func (c *Controller) test(ctx context.Context, scenario string) (bool, string) {
	runFilter := "TestCheckout"
	if scenario == "performance" {
		runFilter = "TestReport"
	}
	out, err := c.goCmd(ctx, "test", "-run", runFilter, "./...")
	return err == nil, out
}

func (c *Controller) build(ctx context.Context) (bool, string) {
	out, err := c.goCmd(ctx, "build", "-o", c.binPath, ".")
	return err == nil, out
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
