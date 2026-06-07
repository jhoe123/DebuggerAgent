// Command server is the DebuggerAgent backend.
//
// It hosts the ADK Go agent (Gemini 3 + Dynatrace MCP) and exposes a REST/SSE
// API consumed by the React frontend. The agent wiring (Dynatrace MCP toolset,
// read_source, propose_patch) is added in tasks T4/T5 — see internal/agent.
//
// This skeleton compiles with the standard library only so the repo builds
// before any cloud credentials exist.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	mux := http.NewServeMux()

	// Liveness check.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// --- API surface (implemented in T6) ---
	// GET  /api/problems            -> list open Dynatrace problems (via MCP)
	// POST /api/investigate         -> run agent on a problem; stream reasoning (SSE)
	// POST /api/approve-patch       -> write the proposed diff to a branch/file (no merge/deploy)
	mux.HandleFunc("/api/", notImplemented)

	addr := ":" + envOr("PORT", "8080")
	log.Printf("DebuggerAgent backend listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented yet — see TASKS.md (T6)",
		"path":  r.URL.Path,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
