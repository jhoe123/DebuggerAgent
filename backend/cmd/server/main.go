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
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/autopilot"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/dynatrace"
	"github.com/patchpilot/backend/internal/history"
	"github.com/patchpilot/backend/internal/pipeline"
	"github.com/patchpilot/backend/internal/slack"
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
	ap       *autopilot.Engine
	notifier *slack.Notifier // runtime-configurable Slack digest notifier
}

func main() {
	cfg := agent.LoadConfig()
	ctx := context.Background()

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
			log.Printf("Test Console + auto-remediation ENABLED (backend owns demo_app at %s)", cfg.DemoAppURL)
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
			log.Printf("Cloud Build remediation ENABLED (deploy %q to Cloud Run in %s) — protect this endpoint (it deploys on approval)", cfg.DemoRunService, cfg.CloudRunRegion)
		}
	}

	hist := history.New(200, cfg.PatchOutputDir)
	ap := autopilot.New(svc, demo, hist)
	// Slack notifier: seeded from SLACK_WEBHOOK_URL but reconfigurable at runtime
	// from Settings (POST /api/slack/config). Never nil.
	notifier := slack.New(cfg.SlackWebhookURL)
	h := &handlers{agent: svc, dt: dt, demo: demo, runner: runner, hist: hist, ap: ap, notifier: notifier}

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
	mux.HandleFunc("GET /api/history", h.history)                 // audit log (hosted-safe)
	mux.HandleFunc("POST /api/instrument/scan", h.instrumentScan) // read-only review (hosted-safe)
	// Autopilot: hosted-safe (propose-only without democtl; real deploy still needs local mode).
	mux.HandleFunc("GET /api/autopilot", h.autopilotSnapshot)
	mux.HandleFunc("POST /api/autopilot/config", h.autopilotConfig)
	mux.HandleFunc("POST /api/autopilot/cancel", h.autopilotCancel)
	// Slack notifications: configurable from Settings (status never returns the raw webhook).
	mux.HandleFunc("GET /api/slack", h.slackStatus)
	mux.HandleFunc("POST /api/slack/config", h.slackConfig)
	mux.HandleFunc("POST /api/slack/test", h.slackTest)

	if cfg.EnableTestConsole {
		mux.HandleFunc("GET /api/test/status", h.testStatus)
		mux.HandleFunc("POST /api/test/trigger", h.testTrigger)
		mux.HandleFunc("POST /api/test/reset", h.testReset)
		mux.HandleFunc("POST /api/instrument/apply", h.instrumentApply) // writes/builds/runs — local only
	}
	// Remediation runs on the local democtl runner or the cloud Cloud Build runner.
	if h.runner != nil {
		mux.HandleFunc("POST /api/remediate", h.remediate)
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

	final, _, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, agent.InvestigatePrompt(req.ProblemID), nil)
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
	h.recordProposed(req.ProblemID, inv)
	writeJSON(w, http.StatusOK, inv)
}

// recordProposed logs the agent's proposed patch to the audit history.
func (h *handlers) recordProposed(problemID string, inv api.Investigation) {
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

	sse.step(api.Step{Stage: "investigate", Status: "running", Message: "Starting investigation…"})
	onStep := func(stage, status, message string) {
		sse.step(api.Step{Stage: stage, Status: status, Message: message})
	}
	final, _, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, agent.InvestigatePrompt(req.ProblemID), onStep)
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
	h.recordProposed(req.ProblemID, inv)
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

// remediate runs the auto-remediation pipeline, streaming each stage (SSE) then a result event.
func (h *handlers) remediate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID string           `json:"problemId"`
		Options   *democtl.Options `json:"options"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	opts := democtl.Options{Apply: true, Test: true, Build: true, Deploy: true}
	if req.Options != nil {
		opts = *req.Options
	}
	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	// Cloud Build (build + deploy to Cloud Run) can take many minutes; allow for it.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Minute)
	defer cancel()
	result := h.runner.Remediate(ctx, opts, func(s api.Step) { sse.step(s) })
	h.hist.RecordPipeline(req.ProblemID, result)
	sse.event("result", result)
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
