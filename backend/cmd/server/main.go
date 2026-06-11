// Command server is the PatchPilot backend: it hosts the ADK Go agent
// (Gemini 3.1 + Dynatrace MCP) and exposes the REST API consumed by the React UI.
//
//	GET  /api/problems       -> recent error spans summarized as problems (direct MCP/DQL)
//	POST /api/investigate     -> run the agent on a problem; returns an Investigation
//	POST /api/approve-patch   -> write the proposed patch to a branch/file (no merge/deploy)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/artifact"
	"github.com/patchpilot/backend/internal/autopilot"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/dynatrace"
	"github.com/patchpilot/backend/internal/gitsource"
	"github.com/patchpilot/backend/internal/history"
	"github.com/patchpilot/backend/internal/pipeline"
	"github.com/patchpilot/backend/internal/settings"
	"github.com/patchpilot/backend/internal/slack"
	"github.com/patchpilot/backend/internal/tools"
	"github.com/patchpilot/backend/internal/versions"
)

// remediator runs the apply→test→build→deploy pipeline. Implemented by the local
// democtl.Controller and the cloud pipeline.CloudRunner; selected by PIPELINE_MODE.
type remediator interface {
	Remediate(ctx context.Context, opts democtl.Options, emit func(api.Step)) api.PipelineResult
}

type handlers struct {
	agent    *agent.Service
	dt       *dynatrace.Client
	demo     *democtl.Controller // nil unless ENABLE_TEST_CONSOLE=true (local only)
	runner   remediator          // local (democtl) or cloud (pipeline.CloudRunner)
	hist     *history.Store
	arts     *artifact.Store // durable per-problem lifecycle status
	ap       *autopilot.Engine
	notifier *slack.Notifier    // runtime-configurable Slack digest notifier
	pipe     *settings.Store    // runtime-configurable test/build/deploy settings + health URL
	gs       *gitsource.Manager // managed Git source (branch-per-fix + confirm-to-merge)
	vers     *versions.Store    // deploy versions (every successful deploy; revert targets)

	revertBusy atomic.Bool // one revert at a time (it resets + redeploys the source)
}

func main() {
	cfg := agent.LoadConfig()
	ctx := context.Background()

	if cfg.ClearIssuesOnStart && cfg.PatchOutputDir != "" {
		log.Printf("Clearing patch output directory (cache) on start: %s", cfg.PatchOutputDir)
		if err := os.RemoveAll(cfg.PatchOutputDir); err != nil {
			log.Printf("WARNING: clear patch output directory: %v", err)
		}
		if err := os.MkdirAll(cfg.PatchOutputDir, 0o755); err != nil {
			log.Printf("WARNING: recreate patch output directory: %v", err)
		}
	}

	svc, err := agent.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	dt, err := dynatrace.Open(ctx, cfg.MCPNodeBin, cfg.DTEnvironment, cfg.DTPlatformTok, cfg.ClearIssuesOnStart)
	if err != nil {
		// Non-fatal: keep serving so the agent and health check still work.
		log.Printf("WARNING: dynatrace client unavailable: %v", err)
	} else {
		defer dt.Close()
	}
	// Local-only demo controls (Test Console + auto-remediation pipeline).
	var demo *democtl.Controller
	if cfg.EnableTestConsole {
		demo = democtl.New(cfg.SourceRoot, cfg.DemoAppURL, cfg.DTEnvironment, cfg.DTApiToken, svc.Patches())
		if err := demo.Start(ctx); err != nil {
			log.Printf("WARNING: demo controller start: %v", err)
		} else {
			defer demo.Stop()
			log.Printf("Test Console + auto-remediation ENABLED (backend owns demo_app at %s, detected language: %s)", cfg.DemoAppURL, demo.Language())
		}
	}
	// The builder agent's lazy test/build artifact resolvers, shared by both runners:
	// reuse an existing test/script if it covers the need, otherwise generate one.
	genTest := func(ctx context.Context, file, rationale, errOut string) (string, map[string]string, error) {
		res, err := svc.GenerateTest(ctx, fmt.Sprintf("testgen-%d", time.Now().UnixNano()), file, rationale, errOut, nil)
		if err != nil {
			return "", nil, err
		}
		return res.RunName, res.Files, nil
	}
	genBuild := func(ctx context.Context, kind, errOut string) (map[string]string, error) {
		return svc.GenerateBuildArtifact(ctx, fmt.Sprintf("buildgen-%d", time.Now().UnixNano()), kind, errOut, nil)
	}

	var runner remediator
	var cloudRunner *pipeline.CloudRunner // non-nil in cloudbuild mode; re-pointed on Git connect
	if demo != nil {
		demo.SetTestGenerator(genTest)
		demo.SetBuildGenerator(genBuild)
		runner = demo
	}
	// Cloud-native runner: deploy demo_app to Cloud Run via Cloud Build (hosted path).
	if cfg.PipelineMode == "cloudbuild" {
		cloud, err := pipeline.New(ctx, pipeline.Config{
			Project:    cfg.GCPProject,
			Region:     cfg.CloudRunRegion,
			Bucket:     cfg.CloudBuildBucket,
			ARRepo:     cfg.ArtifactRegistryRepo,
			Service:    cfg.DemoRunService,
			SourceRoot: cfg.SourceRoot,
			OTLPEnv:    otlpEnvMap(cfg),
		}, svc.Patches(), genTest, genBuild)
		if err != nil {
			log.Printf("WARNING: cloud build runner unavailable: %v", err)
		} else {
			defer cloud.Close()
			runner = cloud
			cloudRunner = cloud
			log.Printf("Cloud Build remediation ENABLED (deploy %q to Cloud Run in %s, detected language: %s) — protect this endpoint (it deploys on approval)", cfg.DemoRunService, cfg.CloudRunRegion, cloud.Language())
		}
	}

	// Runtime-configurable pipeline settings (test/build/deploy params + health URL),
	// seeded from env so the defaults reflect the current deployment. democtl reads the
	// health URL for its reachability check; the remediate handler uses these as the
	// base Options.
	pipe := settings.New(api.PipelineSettings{
		Mode:          cfg.PipelineMode,
		TestStrategy:  cfg.TestStrategy,
		BuildStrategy: cfg.BuildStrategy,
		DeployTarget:  defaultDeployTarget(cfg),
		HealthURL:     cfg.DemoAppURL,
		AppURL:        cfg.AppURL,
		DeployParams: map[string]string{
			"project":      cfg.GCPProject,
			"region":       cfg.CloudRunRegion,
			"service":      cfg.DemoRunService,
			"sourceBucket": cfg.CloudBuildBucket,
			"artifactRepo": cfg.ArtifactRegistryRepo,
		},
	})
	if demo != nil {
		demo.SetSettings(pipe)
	}

	hist := history.New(200, cfg.PatchOutputDir)
	arts := artifact.New(cfg.PatchOutputDir, cfg.ClearIssuesOnStart)
	vers := versions.New(cfg.PatchOutputDir, cfg.VersionRetention, cfg.ClearIssuesOnStart)
	ap := autopilot.New(svc, demo, runner, hist, arts)
	ap.SetVersions(vers)

	// Managed Git source: clone a repo, branch per fix, and merge on confirm. Re-points
	// the read_source sandbox and (local) pipeline at the clone when connected. Mutating
	// ops are gated by ENABLE_GIT_SOURCE; status/config stay available either way.
	gitStore := gitsource.New(gitsource.Config{
		RepoURL:            cfg.GitSourceRepoURL,
		AuthToken:          cfg.GitSourceAuthToken,
		WorkingBranch:      cfg.GitSourceWorkingBranch,
		BranchPrefix:       cfg.GitSourceBranchPrefix,
		BranchPerFix:       cfg.GitSourceBranchPerFix,
		AutoMergeOnConfirm: cfg.GitSourceAutoMerge,
		PushEnabled:        cfg.GitSourcePushEnabled,
		CommitAuthorName:   cfg.GitSourceCommitName,
		CommitAuthorEmail:  cfg.GitSourceCommitEmail,
		CloneDir:           cfg.GitSourceCloneDir,
	})
	gitRoot := func(dir string) error {
		if err := svc.SetSourceRoot(dir); err != nil {
			return err
		}
		if demo != nil {
			demo.SetSourceRoot(dir)
		}
		// Cloud runner packages source from its own SourceRoot; re-point it at the clone
		// so cloud builds upload the Git-tracked source (with accumulated fixes), not the
		// original SOURCE_ROOT.
		if cloudRunner != nil {
			cloudRunner.SetSourceRoot(dir)
		}
		return nil
	}
	gs := gitsource.NewManager(gitStore, arts, cfg.EnableGitSource, gitRoot)
	ap.SetGitSource(gs)
	if cfg.EnableGitSource && gs.Available() && cfg.GitSourceRepoURL != "" {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if _, err := gs.Connect(cctx, nil); err != nil {
				log.Printf("WARNING: git source auto-connect failed: %v", err)
			} else {
				log.Printf("Git source connected: %s (working branch %s)", gs.Status(cctx).RepoURLPreview, cfg.GitSourceWorkingBranch)
			}
		}()
	}

	// Slack notifier: seeded from SLACK_WEBHOOK_URL but reconfigurable at runtime
	// from Settings (POST /api/slack/config). Never nil.
	notifier := slack.New(cfg.SlackWebhookURL)
	h := &handlers{agent: svc, dt: dt, demo: demo, runner: runner, hist: hist, arts: arts, ap: ap, notifier: notifier, pipe: pipe, gs: gs, vers: vers}

	// Auto-patch daemon: reacts to newly-detected problems (opt-in via Settings).
	if dt != nil {
		go ap.Run(ctx, 30*time.Second, dt.ListProblems)
	} else {
		log.Printf("WARNING: Dynatrace unavailable — autopilot poller disabled")
	}

	// Slack: background poller posting a consolidated digest of active bugs. Always
	// started (it posts only while enabled+configured); needs a problem source.
	if dt != nil {
		go notifier.Run(ctx, cfg.SlackPollInterval, dt.ListProblems)
	} else {
		log.Printf("WARNING: Dynatrace unavailable — Slack digest poller disabled (test message still works)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/problems", h.problems)
	mux.HandleFunc("POST /api/investigate", h.investigate)
	mux.HandleFunc("POST /api/investigate/stream", h.investigateStream)
	mux.HandleFunc("POST /api/approve-patch", h.approvePatch)
	mux.HandleFunc("POST /api/ask", h.ask)
	// Patch consolidation batch + durable per-problem status (hosted-safe; staging
	// just records — the deploy still needs a runner, gated below).
	mux.HandleFunc("GET /api/patches", h.listPatches)
	mux.HandleFunc("POST /api/patches/stage", h.stagePatch)
	mux.HandleFunc("POST /api/patches/unstage", h.unstagePatch)
	mux.HandleFunc("POST /api/patches/clear", h.clearPatches)
	mux.HandleFunc("GET /api/artifacts", h.artifacts)
	mux.HandleFunc("GET /api/history", h.history) // audit log (hosted-safe)
	// Deploy versions: every successful deploy is recorded; reads are hosted-safe.
	mux.HandleFunc("GET /api/versions", h.versionsList)
	mux.HandleFunc("GET /api/versions/{id}", h.versionDetail)
	mux.HandleFunc("POST /api/instrument/scan", h.instrumentScan) // read-only review (hosted-safe)
	// Autopilot: hosted-safe (propose-only without democtl; real deploy still needs local mode).
	mux.HandleFunc("GET /api/autopilot", h.autopilotSnapshot)
	mux.HandleFunc("POST /api/autopilot/config", h.autopilotConfig)
	mux.HandleFunc("POST /api/autopilot/cancel", h.autopilotCancel)
	// Slack notifications: configurable from Settings (status never returns the raw webhook).
	mux.HandleFunc("GET /api/slack", h.slackStatus)
	mux.HandleFunc("POST /api/slack/config", h.slackConfig)
	mux.HandleFunc("POST /api/slack/test", h.slackTest)
	// Pipeline & deploy settings (test/build/deploy params + health URL), configured from Settings.
	mux.HandleFunc("GET /api/pipeline/config", h.pipelineConfig)
	mux.HandleFunc("POST /api/pipeline/config", h.pipelineConfigSet)
	// Git source: status/config are side-effect-free and always available; connect/branch/
	// confirm/cleanup mutate the working tree and are gated by ENABLE_GIT_SOURCE (checked in
	// the manager). confirm-fix is the ONLY path that merges a fix into the working branch.
	mux.HandleFunc("GET /api/git-source", h.gitSourceStatus)
	mux.HandleFunc("POST /api/git-source/config", h.gitSourceConfigSet)
	mux.HandleFunc("POST /api/git-source/validate", h.gitSourceValidate)
	mux.HandleFunc("POST /api/git-source/connect", h.gitSourceConnect)
	mux.HandleFunc("POST /api/git-source/connect/stream", h.gitSourceConnectStream)
	mux.HandleFunc("POST /api/git-source/branch", h.gitSourceBranch)
	mux.HandleFunc("POST /api/git-source/cleanup", h.gitSourceCleanup)
	mux.HandleFunc("POST /api/confirm-fix", h.confirmFix)
	// Judge-facing testing reset: stops automation, clears patches + lifecycle
	// artifacts, and restores the original (buggy) demo source.
	mux.HandleFunc("POST /api/demo/reset", h.demoReset)

	if cfg.EnableTestConsole {
		mux.HandleFunc("GET /api/test/status", h.testStatus)
		mux.HandleFunc("POST /api/test/trigger", h.testTrigger)
		mux.HandleFunc("POST /api/test/reset", h.testReset)
		mux.HandleFunc("POST /api/instrument/apply", h.instrumentApply) // writes/builds/runs — local only
	}
	// Remediation runs on the local democtl runner or the cloud Cloud Build runner.
	if h.runner != nil {
		mux.HandleFunc("POST /api/remediate", h.remediate)
		mux.HandleFunc("POST /api/pipeline/run", h.pipelineRun)    // deploy the consolidated batch
		mux.HandleFunc("POST /api/versions/revert", h.versionRevert) // restore a deploy version + redeploy
	}

	// Optional: serve the built React app (Cloud Run). Dev uses the Vite server.
	if webDir := os.Getenv("WEB_DIR"); webDir != "" {
		mux.Handle("/", spaFileServer(webDir))
	}

	addr := ":" + cfg.Port
	log.Printf("PatchPilot backend listening on %s (model=%s, dt=%s)", addr, cfg.GeminiModel, cfg.DTEnvironment)
	log.Fatal(http.ListenAndServe(addr, withCORS(mux)))
}

func (h *handlers) problems(w http.ResponseWriter, r *http.Request) {
	if h.dt == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "dynatrace client unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	probs, err := h.dt.ListProblems(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, probs)
}

func (h *handlers) investigate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()

	final, patch, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, h.agent.InvestigatePrompt(req.ProblemID), nil)
	if err != nil {
		log.Printf("investigate %q error: %v", req.ProblemID, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	inv, err := parseInvestigation(final, req.ProblemID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "raw": final})
		return
	}
	h.recordProposed(req.ProblemID, inv, patch)
	writeJSON(w, http.StatusOK, inv)
}

// recordProposed logs the agent's proposed patch to the audit history, remembers it
// per-problem so it can be staged later, and records the investigation on the
// problem's durable artifact.
func (h *handlers) recordProposed(problemID string, inv api.Investigation, patch *tools.PatchProposal) {
	kind, _ := agent.SplitProblemID(problemID)
	h.arts.RecordInvestigation(problemID, "", kind, true, inv.RootCause.Summary)
	if patch != nil {
		h.agent.Patches().SetProposed(problemID, patch)
	}
	if inv.ProposedPatch.File == "" {
		return
	}
	h.hist.RecordProposed(problemID, inv.ProposedPatch.File, inv.ProposedPatch.UnifiedDiff, inv.ProposedPatch.Rationale)
}

// history returns the audit log (newest first). Read-only and hosted-safe.
func (h *handlers) history(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.HistoryResponse{Entries: h.hist.List()})
}

func (h *handlers) approvePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	path, err := h.agent.Patches().ApplyApproved()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if prop := h.agent.Patches().Latest(); prop != nil {
		h.hist.RecordApproved(req.ProblemID, prop.File, prop.UnifiedDiff, path)
	}
	writeJSON(w, http.StatusOK, api.ApproveResult{WrittenTo: path})
}

// investigateStream runs the agent and streams step milestones (SSE), then a final result event.
func (h *handlers) investigateStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()

	sse.step(api.Step{Stage: "investigate", Status: "running", Message: "Starting investigation…"})
	onStep := func(stage, status, message string) {
		sse.step(api.Step{Stage: stage, Status: status, Message: message})
	}
	final, patch, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, h.agent.InvestigatePrompt(req.ProblemID), onStep)
	if err != nil {
		log.Printf("investigate(stream) %q error: %v", req.ProblemID, err)
		sse.event("error", map[string]string{"error": err.Error()})
		return
	}
	inv, err := parseInvestigation(final, req.ProblemID)
	if err != nil {
		sse.event("error", map[string]string{"error": err.Error(), "raw": final})
		return
	}
	h.recordProposed(req.ProblemID, inv, patch)
	sse.step(api.Step{Stage: "investigate", Status: "ok", Message: "Root cause identified"})
	sse.event("result", inv)
}

func (h *handlers) ask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
		Question  string `json:"question"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Question == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	answer, err := h.agent.Ask(ctx, "sess-"+req.ProblemID, req.Question)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, api.AskResult{Answer: strings.TrimSpace(answer)})
}

// --- Test Console + pipeline (registered only when ENABLE_TEST_CONSOLE=true) ---

func (h *handlers) testStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, h.demo.Status(ctx))
}

func (h *handlers) testTrigger(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, h.demo.Trigger(ctx, 5))
}

func (h *handlers) testReset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := h.demo.ResetSource(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.demo.Status(ctx))
}

// demoReset is the judge-facing "reset testing" action: stop all in-flight automation,
// pause autopatch, discard pending patch proposals and per-problem lifecycle artifacts,
// then restore the demo app's original (buggy) source — a fresh clone of the configured
// repository when the Git source is in use, else the local Test Console reset. In git
// mode it also redeploys the restored source to the demo service in the background
// (~10 min) so the LIVE app trips its seeded bugs again. Autopatch must pause here:
// otherwise the next poll would immediately re-fix the restored bugs and its pipeline
// would race the background redeploy over the same GCS base and Cloud Run service.
// Always responds 200; partial failures land in the error field for a precise toast.
func (h *handlers) demoReset(w http.ResponseWriter, r *http.Request) {
	log.Printf("demo reset: stopping automation and restoring the original source")
	res := api.DemoResetResult{Mode: "none"}
	res.HaltedRuns = h.ap.StopAll("Halted — testing was reset; the demo source was restored to its original state.")
	if cfg := h.ap.Snapshot().Config; cfg.Enabled {
		cfg.Enabled = false
		h.ap.SetConfig(cfg)
		res.AutopatchPaused = true
	}
	// Manual pipeline runs aren't engine-tracked; abort an in-flight remote build too
	// (no-op when idle; local runners don't implement CancelActive).
	if c, ok := h.runner.(interface{ CancelActive(context.Context) error }); ok {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := c.CancelActive(cctx); err != nil {
			log.Printf("demo reset: cancel remote build: %v", err)
		}
		ccancel()
	}
	h.agent.Patches().Reset()
	h.arts.Reset() // cleared before reconnect so no stale fix-branch records rehydrate

	switch {
	case h.gs.Available() && strings.TrimSpace(h.gs.Config().RepoURL) != "":
		res.Mode = "git"
		if err := h.gs.ResetWorkspace(); err != nil {
			res.Error = "reset workspace: " + err.Error()
			break
		}
		res.WorkspaceReset = true
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		if _, err := h.gs.Connect(ctx, nil); err != nil {
			res.Error = "re-clone: " + err.Error()
			break
		}
		res.Reconnected = true
		if h.runner != nil {
			res.RedeployStarted = true
			go h.redeployOriginal()
		}
	case h.demo != nil:
		res.Mode = "local"
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := h.demo.ResetSource(ctx); err != nil {
			res.Error = err.Error()
		} else {
			res.SourceReset = true // ResetSource rebuilds + relaunches, so the local app is already buggy again
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// redeployOriginal rebuilds and redeploys the demo app from the freshly restored
// (original) source so the live service trips its seeded bugs again. Patch-free:
// Apply=false ships the source as-is, and tests are skipped — the original code is
// intentionally buggy, so a regression gate would block the deploy. ForceSync uploads
// the full pristine tree so no patched overlay survives in the build base. Runs
// detached (a Cloud Build takes ~10 min); the result lands in the history log.
func (h *handlers) redeployOriginal() {
	ps := h.pipe.Get()
	opts := democtl.Options{
		Apply: false, Test: false, Build: true, Deploy: true,
		BuildStrategy: ps.BuildStrategy,
		Deployment:    democtl.DeploymentSpec{Target: ps.DeployTarget, Params: ps.DeployParams},
		ForceSync:     true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	log.Printf("demo reset: redeploying the original demo app in the background")
	result := h.runner.Remediate(ctx, opts, func(s api.Step) {
		log.Printf("demo reset redeploy: [%s] %s %s", s.Stage, s.Status, s.Message)
	})
	h.hist.RecordPipeline("demo reset — redeploy original app", result)
	if result.Success {
		log.Printf("demo reset: original demo app redeployed (%s)", result.Verify)
	} else {
		log.Printf("demo reset: redeploy of the original app FAILED (%s)", result.Verify)
	}
}

// remediate runs the auto-remediation pipeline, streaming each stage (SSE) then a result event.
func (h *handlers) remediate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string           `json:"problemId"`
		Options   *democtl.Options `json:"options"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Base the run on the configured pipeline settings (test/build strategy, deploy
	// target + params), then overlay any per-request overrides from the Pipeline panel.
	ps := h.pipe.Get()
	opts := democtl.Options{
		Apply: true, Test: true, Build: true, Deploy: true,
		TestStrategy:  ps.TestStrategy,
		BuildStrategy: ps.BuildStrategy,
		Deployment:    democtl.DeploymentSpec{Target: ps.DeployTarget, Params: ps.DeployParams},
	}
	if o := req.Options; o != nil {
		opts.Apply, opts.Test, opts.Build, opts.Deploy = o.Apply, o.Test, o.Build, o.Deploy
		opts.Scenario = o.Scenario
		if o.TestStrategy != "" {
			opts.TestStrategy = o.TestStrategy
		}
		if o.BuildStrategy != "" {
			opts.BuildStrategy = o.BuildStrategy
		}
		if o.Deployment.Target != "" {
			opts.Deployment.Target = o.Deployment.Target
		}
		if len(o.Deployment.Params) > 0 {
			opts.Deployment.Params = o.Deployment.Params
		}
		opts.ForceSync = o.ForceSync
	}
	// Pre-resolve the patch set the runners would fall back to anyway, so the
	// deploy-version record below knows exactly what shipped.
	if len(opts.Patches) == 0 {
		if p := h.agent.Patches().Latest(); p != nil {
			opts.Patches = []tools.PatchProposal{*p}
		}
	}
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	// Cloud Build (build + deploy to Cloud Run) can take many minutes; allow for it.
	// Detach from r.Context() so a browser refresh / SSE disconnect can't cancel the
	// in-flight pipeline (incl. the Gemini call) — the run finishes and records its
	// result to the artifact store, which the UI reconciles on reload.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()
	h.arts.RecordRunning([]string{req.ProblemID})
	result := h.runner.Remediate(ctx, opts, func(s api.Step) {
		sse.step(s)
		h.arts.AppendStep([]string{req.ProblemID}, s)
	})
	h.hist.RecordPipeline(req.ProblemID, result)
	h.arts.RecordRun([]string{req.ProblemID}, result)
	if result.Success && opts.Deploy && len(opts.Patches) > 0 {
		scen := opts.Scenario
		if scen == "" {
			scen = "error"
		}
		h.vers.RecordDeploy("manual", []string{req.ProblemID}, []string{scen}, opts.Patches, result)
	}
	sse.event("result", result)
}

// --- Patch consolidation batch + durable per-problem artifacts ---

// stagedResponse builds the GET /api/patches payload (display fields only).
func (h *handlers) stagedResponse() api.PatchesResponse {
	staged := h.agent.Patches().Staged()
	out := make([]api.StagedPatch, 0, len(staged))
	for _, sp := range staged {
		out = append(out, api.StagedPatch{
			ProblemID:   sp.ProblemID,
			File:        sp.File,
			UnifiedDiff: sp.UnifiedDiff,
			Rationale:   sp.Rationale,
			StagedAt:    sp.StagedAt.Format(time.RFC3339),
		})
	}
	return api.PatchesResponse{Patches: out}
}

func (h *handlers) listPatches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.stagedResponse())
}

// stagePatch adds a problem's proposed fix to the consolidation batch. Staging also
// writes the approved patch file (the tangible artifact), audits the approval, and
// marks the problem's status as staged.
func (h *handlers) stagePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	sp, err := h.agent.Patches().Stage(req.ProblemID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if path, werr := h.agent.Patches().WriteApproved(sp.PatchProposal); werr == nil {
		h.hist.RecordApproved(req.ProblemID, sp.File, sp.UnifiedDiff, path)
	}
	kind, _ := agent.SplitProblemID(req.ProblemID)
	h.arts.RecordStaged(req.ProblemID, "", kind)
	// When a Git source is connected with branch-per-fix, create the isolated branch now
	// so the UI can surface it; the fix is committed to it after a successful pipeline run.
	h.maybeCreateFixBranch(r.Context(), req.ProblemID)
	writeJSON(w, http.StatusOK, h.stagedResponse())
}

func (h *handlers) unstagePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	h.agent.Patches().Unstage(req.ProblemID)
	writeJSON(w, http.StatusOK, h.stagedResponse())
}

func (h *handlers) clearPatches(w http.ResponseWriter, r *http.Request) {
	h.agent.Patches().ClearStaged()
	writeJSON(w, http.StatusOK, h.stagedResponse())
}

func (h *handlers) artifacts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.ArtifactsResponse{Artifacts: h.arts.List()})
}

// pipelineRun applies the consolidated batch, then runs test→build→deploy→verify
// once (SSE), recording the result on every involved problem's artifact.
func (h *handlers) pipelineRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options *democtl.Options `json:"options"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	staged := h.agent.Patches().Staged()
	if len(staged) == 0 {
		sse.event("error", map[string]string{"error": "no patches staged — add a fix to the batch first"})
		return
	}
	// Base the batch deploy on the configured pipeline settings (test/build strategy,
	// deploy target + params), then overlay any per-request overrides from BatchPanel.
	ps := h.pipe.Get()
	opts := democtl.Options{
		Apply: true, Test: true, Build: true, Deploy: true,
		TestStrategy:  ps.TestStrategy,
		BuildStrategy: ps.BuildStrategy,
		Deployment:    democtl.DeploymentSpec{Target: ps.DeployTarget, Params: ps.DeployParams},
	}
	if o := req.Options; o != nil {
		opts.Apply, opts.Test, opts.Build, opts.Deploy = o.Apply, o.Test, o.Build, o.Deploy
		if o.TestStrategy != "" {
			opts.TestStrategy = o.TestStrategy
		}
		if o.BuildStrategy != "" {
			opts.BuildStrategy = o.BuildStrategy
		}
		if o.Deployment.Target != "" {
			opts.Deployment.Target = o.Deployment.Target
		}
		if len(o.Deployment.Params) > 0 {
			opts.Deployment.Params = o.Deployment.Params
		}
		opts.ForceSync = o.ForceSync
	}
	opts.Patches = h.agent.Patches().StagedForApply()
	scen := map[string]bool{}
	var problemIDs []string
	for _, sp := range staged {
		problemIDs = append(problemIDs, sp.ProblemID)
		if kind, _ := agent.SplitProblemID(sp.ProblemID); !scen[kind] {
			scen[kind] = true
			opts.Scenarios = append(opts.Scenarios, kind)
		}
	}
	// Cloud Build (build + deploy to Cloud Run) can take many minutes; allow for it.
	// Detach from r.Context() so a browser refresh / SSE disconnect can't cancel the
	// in-flight pipeline (incl. the Gemini call) — the run finishes and records its
	// result to the artifact store, which the UI reconciles on reload.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()
	h.arts.RecordRunning(problemIDs)
	result := h.runner.Remediate(ctx, opts, func(s api.Step) {
		sse.step(s)
		h.arts.AppendStep(problemIDs, s)
	})
	h.hist.RecordPipeline(strings.Join(problemIDs, ", "), result)
	h.arts.RecordRun(problemIDs, result)


	if result.Success {
		if opts.Deploy && len(opts.Patches) > 0 {
			h.vers.RecordDeploy("manual", problemIDs, opts.Scenarios, opts.Patches, result)
		}
		// Commit each fix onto its isolated branch (push gated). Done before clearing the
		// batch so the patched content is still available. Never merges — confirm does.
		// Detached from r.Context() too: a refresh during the deploy mustn't skip the
		// commit (commitFixes bounds each commit to its own 60s timeout).
		h.commitFixes(context.WithoutCancel(r.Context()), staged)
	}
	sse.event("result", result)
}

// --- Deploy versions (every successful deploy; revert + redeploy) ---

func (h *handlers) versionsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.VersionsResponse{Versions: h.vers.List()})
}

func (h *handlers) versionDetail(w http.ResponseWriter, r *http.Request) {
	d, ok := h.vers.Detail(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown version"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// versionRevert restores a recorded deploy version and redeploys it (SSE).
// The cumulative snapshot makes this deterministic: reset the local source to the
// pristine base (local mode), then run the pipeline with the version's full file
// set (ForceSync repackages pristine source + overlay in cloud mode). Autopatch is
// paused first — otherwise it would re-detect the restored problems and promptly
// undo the revert. A successful revert is recorded as a NEW version (append-only).
func (h *handlers) versionRevert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VersionID string `json:"versionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	v, ok := h.vers.Get(req.VersionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown version"})
		return
	}
	if snap := h.ap.Snapshot(); len(snap.ActiveIDs) > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an autopilot batch is running — halt it (or pause autopatch) first"})
		return
	}
	if !h.revertBusy.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a revert is already in progress"})
		return
	}
	defer h.revertBusy.Store(false)

	sse, okSSE := newSSE(w)
	if !okSSE {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()

	// Pause autopatch so the restored (likely buggy) state isn't immediately re-fixed.
	if cfg := h.ap.Snapshot().Config; cfg.Enabled {
		cfg.Enabled = false
		h.ap.SetConfig(cfg)
		sse.step(api.Step{Stage: "apply", Status: "info", Message: "Autopatch paused — resume it from the topbar when you're done."})
	}

	// Local mode: restore the WHOLE demo tree to the pristine base first, so files
	// patched after the target version are reverted too. Cloud mode doesn't mutate
	// its local source — ForceSync below repackages pristine source + overlay.
	if h.demo != nil {
		sse.step(api.Step{Stage: "apply", Status: "running", Message: "Resetting source to the pristine base…"})
		if err := h.demo.ResetTree(ctx); err != nil {
			res := api.PipelineResult{Success: false, Steps: []api.Step{{Stage: "apply", Status: "fail", Message: "Source reset failed", Detail: err.Error()}}}
			sse.step(res.Steps[0])
			h.hist.RecordRevert(v.Seq, res)
			sse.event("result", res)
			return
		}
		sse.step(api.Step{Stage: "apply", Status: "ok", Message: "Source reset to pristine base"})
	}

	ps := h.pipe.Get()
	opts := democtl.Options{
		Apply: true, Test: false, Build: true, Deploy: true,
		TestStrategy:  "skip", // restoring a known-good deploy; no agent test-gen
		BuildStrategy: ps.BuildStrategy,
		Deployment:    democtl.DeploymentSpec{Target: ps.DeployTarget, Params: ps.DeployParams},
		ForceSync:     true,
		Patches:       v.Patches,
		Scenarios:     v.Scenarios,
	}
	sse.step(api.Step{Stage: "apply", Status: "info", Message: fmt.Sprintf("Restoring v%d (%d file(s)) and redeploying…", v.Seq, len(v.Patches))})
	result := h.runner.Remediate(ctx, opts, func(s api.Step) { sse.step(s) })
	if result.Success {
		nv := h.vers.RecordRevert(v, result)
		sse.step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Recorded as v%d (restores v%d)", nv.Seq, v.Seq)})
	}
	h.hist.RecordRevert(v.Seq, result)
	sse.event("result", result)
}

// --- Pipeline & deploy settings (configured from Settings; hosted-safe) ---

func (h *handlers) pipelineConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.pipe.Get()
	// Response-only: lets the UI enable Deploy whenever a runner exists (local or cloud),
	// rather than gating it on the Test Console (/api/test/status) being mounted.
	cfg.RunnerAvailable = h.runner != nil
	writeJSON(w, http.StatusOK, cfg)
}

func (h *handlers) pipelineConfigSet(w http.ResponseWriter, r *http.Request) {
	var in api.PipelineSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pipeline settings"})
		return
	}
	writeJSON(w, http.StatusOK, h.pipe.Set(in))
}

// defaultDeployTarget seeds the deploy target from env: explicit DEPLOY_TARGET wins,
// else "cloud-run" in cloudbuild mode, otherwise "local".
func defaultDeployTarget(cfg agent.Config) string {
	if cfg.DeployTarget != "" {
		return cfg.DeployTarget
	}
	if cfg.PipelineMode == "cloudbuild" {
		return "cloud-run"
	}
	return "local"
}

// --- Auto-instrumentation (scan is hosted-safe; apply is local-only) ---

// instrumentScan runs the read-only instrumentation review and streams steps (SSE),
// then a final InstrumentationScan result event.
func (h *handlers) instrumentScan(w http.ResponseWriter, r *http.Request) {
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()

	sse.step(api.Step{Stage: "scan", Status: "running", Message: "Scanning source for instrumentation gaps…"})
	onStep := func(stage, status, message string) {
		sse.step(api.Step{Stage: stage, Status: status, Message: message})
	}
	sessionID := fmt.Sprintf("instrument-scan-%d", time.Now().UnixNano())
	scan, err := h.agent.ScanInstrumentation(ctx, sessionID, onStep)
	if err != nil {
		log.Printf("instrument scan error: %v", err)
		sse.event("error", map[string]string{"error": err.Error()})
		return
	}
	h.hist.RecordScan(scan.Root, scan.Summary, filesOfCandidates(scan.Candidates))
	sse.step(api.Step{Stage: "scan", Status: "ok", Message: fmt.Sprintf("Found %d instrumentation candidate(s)", len(scan.Candidates))})
	sse.event("result", scan)
}

// instrumentApply generates the instrumented files for the selected candidates,
// then runs the local apply→test→debug→build→deploy→verify pipeline (SSE).
func (h *handlers) instrumentApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs     []string         `json:"ids"`
		Options *democtl.Options `json:"options"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()

	selected := h.agent.InstrumentSelected(req.IDs)
	if len(selected) == 0 {
		sse.event("error", map[string]string{"error": "no candidates selected — run a scan first"})
		return
	}
	opts := democtl.Options{Apply: true, Test: true, Build: true, Deploy: true}
	if req.Options != nil {
		opts = *req.Options
	}
	onStep := func(stage, status, message string) {
		sse.step(api.Step{Stage: stage, Status: status, Message: message})
	}
	sessionID := fmt.Sprintf("instrument-apply-%d", time.Now().UnixNano())

	sse.step(api.Step{Stage: "generate", Status: "running", Message: fmt.Sprintf("Generating instrumented source for %d change(s)…", len(selected))})
	files, err := h.agent.ApplyInstrumentation(ctx, sessionID, selected, "", onStep)
	if err != nil {
		sse.event("error", map[string]string{"error": err.Error()})
		return
	}
	sse.step(api.Step{Stage: "generate", Status: "ok", Message: "Instrumented source generated"})

	// repair re-invokes the agent (same session) to fix the files given build/test output.
	repair := func(ctx context.Context, errOut string) (map[string]string, error) {
		return h.agent.ApplyInstrumentation(ctx, sessionID, selected, errOut, onStep)
	}
	result := h.demo.ApplyInstrumentation(ctx, files, opts, repair, func(s api.Step) { sse.step(s) })
	h.hist.RecordInstrumentation(result)
	sse.event("result", result)
}

// --- Autopilot (auto-patch daemon) ---

func (h *handlers) autopilotSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.ap.Snapshot())
}

func (h *handlers) autopilotConfig(w http.ResponseWriter, r *http.Request) {
	var cfg api.AutopilotConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid config"})
		return
	}
	h.ap.SetConfig(cfg)
	writeJSON(w, http.StatusOK, h.ap.Snapshot())
}

func (h *handlers) autopilotCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	h.ap.Cancel(req.ProblemID)
	writeJSON(w, http.StatusOK, h.ap.Snapshot())
}

// slackStatus returns the current Slack config (never the raw webhook).
func (h *handlers) slackStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.notifier.Status())
}

// slackConfig updates the Slack notifier (enable/disable + optional webhook).
func (h *handlers) slackConfig(w http.ResponseWriter, r *http.Request) {
	var cfg api.SlackConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid config"})
		return
	}
	h.notifier.SetConfig(cfg.Enabled, cfg.WebhookURL)
	writeJSON(w, http.StatusOK, h.notifier.Status())
}

// slackTest posts a one-off message to validate the configured webhook.
func (h *handlers) slackTest(w http.ResponseWriter, r *http.Request) {
	if err := h.notifier.Test(); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- Git source (branch-per-fix + confirm-to-merge) ---

// gitSourceStatus returns the display-safe Git source status (token never returned).
func (h *handlers) gitSourceStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, h.gs.Status(ctx))
}

// gitSourceConfigSet merges a config update (empty token preserves the stored secret).
// When the update re-targets the managed source (different repo URL or working branch),
// it first stops everything aimed at the old project — the active autopilot batch, any
// in-flight remote build, and pending patch proposals — and on a repo change deletes
// the workspace clone so the follow-up connect starts fresh. The response reports what
// was stopped/reset so the UI can warn the user. (A manual LOCAL pipeline run has no
// cancel handle today; it dies with its own timeout and lands against old artifacts.)
func (h *handlers) gitSourceConfigSet(w http.ResponseWriter, r *http.Request) {
	var in api.GitSourceConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid git source config"})
		return
	}
	repoChanged, branchChanged := h.gs.TargetChange(in) // BEFORE SetConfig merges the update
	res := api.GitSourceApplyResult{TargetChanged: repoChanged || branchChanged}
	if res.TargetChanged {
		log.Printf("git source: target changed (repo=%v branch=%v) — stopping work on the old project", repoChanged, branchChanged)
		res.HaltedRuns = h.ap.StopAll("Halted — Git source target changed; work against the old project was stopped.")
		// Manual pipeline runs aren't engine-tracked; abort an in-flight remote build too
		// (no-op when idle; local runners don't implement CancelActive).
		if c, ok := h.runner.(interface{ CancelActive(context.Context) error }); ok {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := c.CancelActive(cctx); err != nil {
				log.Printf("git source: cancel remote build: %v", err)
			}
			ccancel()
		}
		h.agent.Patches().Reset()
		res.PatchesCleared = true
		if repoChanged { // branch-only change keeps the clone; fetch+checkout switches branches
			if err := h.gs.ResetWorkspace(); err != nil {
				log.Printf("git source: reset workspace: %v", err)
			} else {
				res.WorkspaceReset = true
			}
		}
	}
	h.gs.SetConfig(in)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res.GitSourceStatus = h.gs.Status(ctx)
	writeJSON(w, http.StatusOK, res)
}

// gitSourceValidate checks a repo URL (+ optional token) and lists its remote branches
// without cloning. It always responds 200 — an unreachable/auth-failed repo comes back as
// {valid:false, error:…} so the UI can render the message inline.
func (h *handlers) gitSourceValidate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RepoURL   string `json:"repoUrl"`
		AuthToken string `json:"authToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, h.gs.ValidateRemote(ctx, in.RepoURL, in.AuthToken))
}

// gitSourceConnect clones-or-fetches the repo and re-points the source root at it.
func (h *handlers) gitSourceConnect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if _, err := h.gs.Connect(ctx, nil); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.gs.Status(ctx))
}

// gitSourceConnectStream is the streaming variant of connect: it emits clone/checkout
// progress steps over SSE and ends with the resolved status, so the UI can show a live
// timeline during the (possibly slow) clone.
func (h *handlers) gitSourceConnectStream(w http.ResponseWriter, r *http.Request) {
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	stopHeartbeat := startHeartbeat(ctx, sse)
	defer stopHeartbeat()

	if _, err := h.gs.Connect(ctx, func(s api.Step) { sse.step(s) }); err != nil {
		sse.event("error", map[string]string{"error": err.Error()})
		return
	}
	sse.event("result", h.gs.Status(ctx))
}

// gitSourceBranch creates a problem's fix branch on demand (when branch-per-fix is on).
func (h *handlers) gitSourceBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	branch, err := h.gs.CreateFixBranch(ctx, req.ProblemID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"branch": branch})
}

// confirmFix is the human merge gate: it merges the problem's fix branch into the
// working branch (pushing + deleting per config). It is the ONLY merge trigger.
func (h *handlers) confirmFix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ProblemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "problemId required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := h.gs.ConfirmFix(ctx, req.ProblemID, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	h.hist.RecordApproved(req.ProblemID, res.MergedBranch, "", "merged into "+res.IntoBranch)
	// Clean up once every open fix branch has been confirmed.
	if len(h.gs.Status(ctx).Branches) == 0 {
		_, _ = h.gs.CleanupConfirmed(ctx)
	}
	writeJSON(w, http.StatusOK, res)
}

// gitSourceCleanup deletes the fix branches of all confirmed problems.
func (h *handlers) gitSourceCleanup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	removed, err := h.gs.CleanupConfirmed(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// maybeCreateFixBranch creates the per-problem fix branch when a Git source is connected
// with branch-per-fix on. Best-effort: failures don't block staging.
func (h *handlers) maybeCreateFixBranch(ctx context.Context, problemID string) {
	if h.gs == nil || !h.gs.IsConnected() || !h.gs.Config().BranchPerFix {
		return
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := h.gs.CreateFixBranch(c, problemID); err != nil {
		log.Printf("git source: create fix branch for %s: %v", problemID, err)
	}
}

// commitFixes commits each problem's patched files onto its own fix branch (push gated
// by config). It only commits to isolated branches — it never merges; confirmation is
// the merge gate. Best-effort: failures are logged, not fatal.
func (h *handlers) commitFixes(ctx context.Context, staged []tools.StagedPatch) {
	if h.gs == nil || !h.gs.IsConnected() || !h.gs.Config().BranchPerFix {
		return
	}
	byProblem := map[string]map[string]string{}
	for _, sp := range staged {
		if byProblem[sp.ProblemID] == nil {
			byProblem[sp.ProblemID] = map[string]string{}
		}
		byProblem[sp.ProblemID][sp.File] = sp.PatchedContent
	}
	for id, files := range byProblem {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		if _, _, err := h.gs.CommitPatch(c, id, files, "PatchPilot fix for "+id); err != nil {
			log.Printf("git source: commit fix for %s: %v", id, err)
		}
		cancel()
	}
}

// otlpEnvMap derives the OTEL_* env to set on the Cloud Run-deployed demo_app so it
// reports to Dynatrace (mirrors democtl's local OTLP wiring). Returns nil with no creds.
func otlpEnvMap(cfg agent.Config) map[string]string {
	if cfg.DTApiToken == "" || cfg.DTEnvironment == "" {
		return nil
	}
	endpoint := strings.Replace(cfg.DTEnvironment, ".apps.", ".live.", 1) + "/api/v2/otlp"
	svc := cfg.DemoRunService
	if svc == "" {
		svc = "checkout-demo"
	}
	return map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_EXPORTER_OTLP_HEADERS":  "Authorization=Api-Token " + cfg.DTApiToken,
		"OTEL_SERVICE_NAME":           svc,
	}
}

// filesOfCandidates returns the unique set of files referenced by candidates.
func filesOfCandidates(cands []api.InstrumentationCandidate) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		if c.File != "" && !seen[c.File] {
			seen[c.File] = true
			out = append(out, c.File)
		}
	}
	return out
}

// parseInvestigation extracts the JSON object from the agent's final text.
func parseInvestigation(text, problemID string) (api.Investigation, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return api.Investigation{}, errNoJSON
	}
	var inv api.Investigation
	if err := json.Unmarshal([]byte(text[start:end+1]), &inv); err != nil {
		return api.Investigation{}, err
	}
	inv.ProblemID = problemID
	if inv.Alternatives == nil {
		inv.Alternatives = []string{}
	}
	return inv, nil
}

var errNoJSON = jsonError("agent did not return a JSON object")

type jsonError string

func (e jsonError) Error() string { return string(e) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// sseStream writes Server-Sent Events.
type sseStream struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseStream, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseStream{w: w, f: f}, true
}

func (s *sseStream) event(name string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	s.f.Flush()
}

func (s *sseStream) step(st api.Step) { s.event("step", st) }

func (s *sseStream) ping() {
	fmt.Fprint(s.w, ": ping\n\n")
	s.f.Flush()
}

func startHeartbeat(ctx context.Context, sse *sseStream) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sse.ping()
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(stop) }
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaFileServer serves static files and falls back to index.html for client routes.
func spaFileServer(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unmatched API routes must 404 as JSON-ish, not fall back to index.html
		// (e.g. /api/test/* when the Test Console is disabled).
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if _, err := os.Stat(dir + r.URL.Path); err != nil && !strings.HasPrefix(r.URL.Path, "/assets") {
			http.ServeFile(w, r, dir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	})
}
