// Package agent defines the DebuggerAgent root-cause investigator.
//
// Built in tasks T4/T5. The agent is an ADK Go agent backed by Gemini 3 with
// three tools:
//
//   - Dynatrace MCP toolset — connected to `npx @dynatrace-oss/dynatrace-mcp-server`
//     via a static platform token (DT_ENVIRONMENT, DT_PLATFORM_TOKEN). Provides
//     get-problem, logs, spans, and DQL.
//   - read_source(path)          — reads files under SOURCE_ROOT for stack-trace → code
//     correlation (path is validated to stay within SOURCE_ROOT).
//   - propose_patch(file, diff)  — returns a unified diff for human review. Applying it is
//     a separate, explicitly approved action; the agent never merges or deploys.
//
// Fallback (see PROJECT.md): if ADK Go's MCP support is too immature, call Vertex AI
// Gemini directly and use a Go MCP client for Dynatrace — still Gemini-powered.
package agent

// Config holds the runtime configuration read from the environment.
type Config struct {
	GeminiModel    string // GEMINI_MODEL
	GCPProject     string // GOOGLE_CLOUD_PROJECT
	GCPLocation    string // GOOGLE_CLOUD_LOCATION
	DTEnvironment  string // DT_ENVIRONMENT
	DTPlatformTok  string // DT_PLATFORM_TOKEN
	SourceRoot     string // SOURCE_ROOT (read_source sandbox)
	PatchOutputDir string // PATCH_OUTPUT_DIR (approved patches)
}

// TODO(T4): construct the ADK Go agent (Gemini 3) and attach the Dynatrace MCP toolset.
// TODO(T5): implement read_source and propose_patch tools + the investigation prompt.
