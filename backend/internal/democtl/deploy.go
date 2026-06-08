package democtl

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/patchpilot/backend/internal/api"
)

// deploy ships the freshly-built demo_app per the DeploymentSpec and returns the base
// URL to run the post-deploy HTTP smoke verify against. The local runner supports
// "local" (process restart), "docker" (image + container), and "script" (a detected
// deploy script). "cloud-run" is handled by the cloud-build runner (PIPELINE_MODE=cloudbuild).
func (c *Controller) deploy(ctx context.Context, spec DeploymentSpec, step func(api.Step)) (string, error) {
	switch spec.Target {
	case "", "local":
		step(api.Step{Stage: "deploy", Status: "running", Message: "Restarting the local demo_app with the fix"})
		if err := c.launch(); err != nil {
			return "", err
		}
		time.Sleep(1500 * time.Millisecond) // let it bind the port
		return c.demoURL, nil
	case "docker":
		return c.deployDocker(ctx, spec.Params, step)
	case "script":
		return c.deployScript(ctx, spec.Params, step)
	case "cloud-run":
		return "", fmt.Errorf("cloud-run target requires the cloud-build runner (set PIPELINE_MODE=cloudbuild)")
	default:
		return "", fmt.Errorf("unknown deploy target %q", spec.Target)
	}
}

// deployDocker builds an image (generating a Dockerfile if none exists) and runs it as a
// container. It maps to a host port other than 9090 by default so it doesn't collide
// with the local demo_app process the controller already owns.
func (c *Controller) deployDocker(ctx context.Context, params map[string]string, step func(api.Step)) (string, error) {
	image := paramOr(params, "image", "shopflow-demo")
	tag := paramOr(params, "tag", "local")
	name := paramOr(params, "containerName", "shopflow-demo")
	hostPort := paramOr(params, "hostPort", "9091")
	ref := image + ":" + tag

	if _, err := os.Stat(filepath.Join(c.demoDir, "Dockerfile")); err != nil {
		if c.buildGen == nil {
			return "", fmt.Errorf("no Dockerfile in demo_app and no generator available")
		}
		step(api.Step{Stage: "deploy", Status: "running", Message: "No Dockerfile — generating one…"})
		files, err := c.buildGen(ctx, "dockerfile", "")
		if err != nil {
			return "", fmt.Errorf("dockerfile generation failed: %w", err)
		}
		if err := c.writeFiles(files); err != nil {
			return "", err
		}
		step(api.Step{Stage: "deploy", Status: "info", Message: "Generated Dockerfile"})
	}

	step(api.Step{Stage: "deploy", Status: "running", Message: "docker build -t " + ref})
	if ok, out := c.run(ctx, c.demoDir, "docker", "build", "-t", ref, "."); !ok {
		return "", fmt.Errorf("docker build failed: %s", tail(out))
	}
	_, _ = c.run(ctx, c.demoDir, "docker", "rm", "-f", name) // best-effort cleanup of a prior container

	args := []string{"run", "-d", "--name", name, "-p", hostPort + ":9090"}
	for _, e := range c.otlpEnv {
		args = append(args, "-e", e)
	}
	args = append(args, ref)
	step(api.Step{Stage: "deploy", Status: "running", Message: fmt.Sprintf("docker run %s on :%s", ref, hostPort)})
	if ok, out := c.run(ctx, c.demoDir, "docker", args...); !ok {
		return "", fmt.Errorf("docker run failed: %s", tail(out))
	}
	time.Sleep(2 * time.Second) // let the container start + bind
	return "http://localhost:" + hostPort, nil
}

// deployScript runs a detected (or configured) deploy script. The base URL to verify is
// taken from params["url"], defaulting to the local demo URL.
func (c *Controller) deployScript(ctx context.Context, params map[string]string, step func(api.Step)) (string, error) {
	script := paramOr(params, "scriptPath", "")
	if script == "" {
		for _, n := range []string{"deploy.ps1", "deploy.sh"} {
			if _, err := os.Stat(filepath.Join(c.demoDir, n)); err == nil {
				script = n
				break
			}
		}
	}
	if script == "" {
		return "", fmt.Errorf("no deploy script found — add demo_app/deploy.ps1 (or .sh) or set deployment.params.scriptPath")
	}
	var cmd *exec.Cmd
	switch {
	case strings.HasSuffix(script, ".ps1"):
		cmd = exec.CommandContext(ctx, "pwsh", "-NoProfile", "-File", script)
	case strings.HasSuffix(script, ".sh"):
		cmd = exec.CommandContext(ctx, "bash", script)
	default:
		return "", fmt.Errorf("unrecognized deploy script %q (use .ps1 or .sh)", script)
	}
	cmd.Dir = c.demoDir
	cmd.Env = append(os.Environ(), c.otlpEnv...)
	step(api.Step{Stage: "deploy", Status: "running", Message: "Running deploy script: " + script})
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("deploy script failed: %s", tail(string(b)))
	}
	return paramOr(params, "url", c.demoURL), nil
}

// verifyDeploy runs the post-deploy HTTP smoke against baseURL: always /healthz, plus a
// scenario-specific assertion for the two seeded bugs (the Go test gate already proved
// the fix for generated-test scenarios). Returns (ok, verifyString) and emits steps.
func (c *Controller) verifyDeploy(ctx context.Context, baseURL, scenario string, step func(api.Step)) (bool, string) {
	step(api.Step{Stage: "verify", Status: "running", Message: "HTTP smoke: GET /healthz"})
	if code, _ := c.getURL(ctx, baseURL, "/healthz"); code != 200 {
		step(api.Step{Stage: "verify", Status: "fail", Message: fmt.Sprintf("Healthcheck failed (HTTP %d)", code)})
		return false, fmt.Sprintf("healthz %d", code)
	}
	switch scenario {
	case "performance":
		step(api.Step{Stage: "verify", Status: "running", Message: "Verifying /report?n=200 latency"})
		code, elapsed := c.timedGetURL(ctx, baseURL, "/report?n=200")
		verify := fmt.Sprintf("~657ms -> %dms", elapsed.Milliseconds())
		if code == 200 && elapsed < 150*time.Millisecond {
			step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Fixed — /report?n=200 now %dms (was ~657ms)", elapsed.Milliseconds())})
			return true, verify
		}
		step(api.Step{Stage: "verify", Status: "fail", Message: fmt.Sprintf("Still slow/failing: HTTP %d in %dms", code, elapsed.Milliseconds())})
		return false, verify
	case "error":
		step(api.Step{Stage: "verify", Status: "running", Message: "Verifying /checkout?index=99"})
		code, _ := c.getURL(ctx, baseURL, "/checkout?index=99")
		verify := fmt.Sprintf("500 -> %d", code)
		if code == 200 || code == 400 {
			step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Fixed — /checkout?index=99 now returns HTTP %d (was 500)", code)})
			return true, verify
		}
		step(api.Step{Stage: "verify", Status: "fail", Message: fmt.Sprintf("Unexpected status HTTP %d", code)})
		return false, verify
	default:
		step(api.Step{Stage: "verify", Status: "ok", Message: "Healthy — deployed service responding (HTTP 200 on /healthz)"})
		return true, "healthz 200"
	}
}

// run executes an external command in dir and returns its combined output.
func (c *Controller) run(ctx context.Context, dir, name string, args ...string) (bool, string) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return err == nil, string(b)
}

// getURL/timedGetURL are base-URL-parameterized variants of get/timedGet so the deploy
// verify can target a container's host port or a remote URL, not just the local demo URL.
func (c *Controller) getURL(ctx context.Context, base, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
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

func (c *Controller) timedGetURL(ctx context.Context, base, path string) (int, time.Duration) {
	start := time.Now()
	code, _ := c.getURL(ctx, base, path)
	return code, time.Since(start)
}

func targetName(spec DeploymentSpec) string {
	if spec.Target == "" {
		return "local"
	}
	return spec.Target
}

func paramOr(params map[string]string, key, def string) string {
	if params != nil {
		if v := strings.TrimSpace(params[key]); v != "" {
			return v
		}
	}
	return def
}

// tail returns the last ~1500 chars of command output, so a long build log doesn't
// overwhelm a step's Detail field.
func tail(s string) string {
	const max = 1500
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}
