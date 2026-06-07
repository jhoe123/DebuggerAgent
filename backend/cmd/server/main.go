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
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/debuggeragent/backend/internal/agent"
	"github.com/debuggeragent/backend/internal/api"
	"github.com/debuggeragent/backend/internal/dynatrace"
)

type handlers struct {
	agent *agent.Service
	dt    *dynatrace.Client
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
	h := &handlers{agent: svc, dt: dt}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/problems", h.problems)
	mux.HandleFunc("POST /api/investigate", h.investigate)
	mux.HandleFunc("POST /api/approve-patch", h.approvePatch)

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

	prompt := "Investigate the production errors for the Dynatrace service \"" + req.ProblemID +
		"\". Query its recent error spans, read the offending source file, determine the root cause, " +
		"call propose_patch with a fix, then return the final JSON object."
	final, _, err := h.agent.Investigate(ctx, "sess-"+req.ProblemID, prompt, nil)
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
	writeJSON(w, http.StatusOK, inv)
}

func (h *handlers) approvePatch(w http.ResponseWriter, r *http.Request) {
	path, err := h.agent.Patches().ApplyApproved()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, api.ApproveResult{WrittenTo: path})
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
		if _, err := os.Stat(dir + r.URL.Path); err != nil && !strings.HasPrefix(r.URL.Path, "/assets") {
			http.ServeFile(w, r, dir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	})
}
