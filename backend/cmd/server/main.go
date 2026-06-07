// Command server is the DebuggerAgent backend: it hosts the ADK Go agent
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

	"github.com/debuggeragent/backend/internal/agent"
	"github.com/debuggeragent/backend/internal/api"
	"github.com/debuggeragent/backend/internal/democtl"
	"github.com/debuggeragent/backend/internal/dynatrace"
	"github.com/debuggeragent/backend/internal/history"
	"github.com/debuggeragent/backend/internal/slack"
)

type handlers struct {
	agent *agent.Service
	dt    *dynatrace.Client
	demo  *democtl.Controller // nil unless ENABLE_TEST_CONSOLE=true (local only)
	hist  *history.Store
}

func main() {
	cfg := agent.LoadConfig()
	ctx := context.Background()

	svc, err := agent.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	dt, err := dynatrace.Open(ctx, cfg.MCPNodeBin, cfg.DTEnvironment, cfg.DTPlatformTok)
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
	h := &handlers{agent: svc, dt: dt, demo: demo, hist: history.New(200, cfg.PatchOutputDir)}

	// Slack: background poller posting a consolidated digest of active bugs.
	if n := slack.New(cfg.SlackWebhookURL); n != nil && dt != nil {
		go n.Run(ctx, cfg.SlackPollInterval, dt.ListProblems)
	} else if cfg.SlackWebhookURL != "" && dt == nil {
		log.Printf("WARNING: SLACK_WEBHOOK_URL set but Dynatrace unavailable — Slack disabled")
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
	mux.HandleFunc("GET /api/history", h.history) // audit log (hosted-safe)

	if cfg.EnableTestConsole {
		mux.HandleFunc("GET /api/test/status", h.testStatus)
		mux.HandleFunc("POST /api/test/trigger", h.testTrigger)
		mux.HandleFunc("POST /api/test/reset", h.testReset)
		mux.HandleFunc("POST /api/remediate", h.remediate)
	}

	// Optional: serve the built React app (Cloud Run). Dev uses the Vite server.
	if webDir := os.Getenv("WEB_DIR"); webDir != "" {
		mux.Handle("/", spaFileServer(webDir))
	}

	addr := ":" + cfg.Port
	log.Printf("DebuggerAgent backend listening on %s (model=%s, dt=%s)", addr, cfg.GeminiModel, cfg.DTEnvironment)
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

	final, _, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, investigatePrompt(req.ProblemID), nil)
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

// splitProblemID parses a composite "<kind>:<service>" problem ID. IDs without a
// recognized prefix default to the error scenario (backward compatible).
func splitProblemID(id string) (kind, svc string) {
	if k, s, ok := strings.Cut(id, ":"); ok && (k == "error" || k == "performance") {
		return k, s
	}
	return "error", id
}

func investigatePrompt(problemID string) string {
	kind, svc := splitProblemID(problemID)
	if kind == "performance" {
		return "Investigate the PERFORMANCE problem for the Dynatrace service \"" + svc +
			"\". Find the slowest operation: ONE execute_dql like `fetch spans, from:now()-30d | " +
			"filter service.name == \"" + svc + "\" and span.status_code != \"error\" | summarize " +
			"p95 = percentile(duration, 95), c = count(), by:{span.name} | sort p95 desc | limit 1` " +
			"(duration is in nanoseconds). Read the source for that operation, find the code that makes " +
			"it slow, call propose_patch with an optimization (change ONLY that function; keep the rest of " +
			"the file byte-identical), then return the final JSON object. rootCause.what should name the " +
			"slow operation and the cause; suggestedTest should assert the latency is now under budget."
	}
	return "Investigate the production errors for the Dynatrace service \"" + svc +
		"\". Query its recent error spans, read the offending source file, determine the root cause, " +
		"call propose_patch with a fix, then return the final JSON object."
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
	onStep := func(stage, status, message string) { sse.step(api.Step{Stage: stage, Status: status, Message: message}) }
	final, _, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, investigatePrompt(req.ProblemID), onStep)
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
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	result := h.demo.Remediate(ctx, opts, func(s api.Step) { sse.step(s) })
	h.hist.RecordPipeline(req.ProblemID, result)
	sse.event("result", result)
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
