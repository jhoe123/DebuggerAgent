package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds runtime configuration read from the environment (.env locally,
// service env vars on Cloud Run).
type Config struct {
	GeminiModel    string // GEMINI_MODEL
	GCPProject     string // GOOGLE_CLOUD_PROJECT
	GCPLocation    string // GOOGLE_CLOUD_LOCATION (Gemini 3.x → "global")
	DTEnvironment  string // DT_ENVIRONMENT
	DTPlatformTok  string // DT_PLATFORM_TOKEN
	DTApiToken     string // DT_API_TOKEN (OTLP ingest, used when the backend owns demo_app)
	MCPNodeBin     string // MCP_NODE_BIN — node.exe used to run the Dynatrace MCP server
	SourceRoot     string // SOURCE_ROOT (read_source sandbox)
	PatchOutputDir string // PATCH_OUTPUT_DIR (approved patches)
	Port           string // PORT

	// ClearIssuesOnStart, when true (default), makes ListProblems only surface
	// problems that occur after server startup — so a freshly started instance
	// shows a clean, empty problem list and accumulates only new incidents.
	// Set CLEAR_ISSUES_ON_START=false to show the full 30-day problem window.
	ClearIssuesOnStart bool // CLEAR_ISSUES_ON_START

	// Local-only demo controls (Test Console + auto-remediation pipeline).
	EnableTestConsole bool   // ENABLE_TEST_CONSOLE — gates demo controls; OFF in the hosted product
	DemoAppURL        string // DEMO_APP_URL (default http://localhost:9090)

	// Slack notifications (optional). When SlackWebhookURL is set, a background
	// poller posts a consolidated digest of active bugs.
	SlackWebhookURL   string        // SLACK_WEBHOOK_URL (secret; never committed)
	SlackPollInterval time.Duration // SLACK_POLL_INTERVAL (default 60s)
}

// LoadConfig loads .env (best-effort) then reads configuration from the environment.
// Relative SOURCE_ROOT / PATCH_OUTPUT_DIR are resolved against the .env directory
// (repo root) so the server works regardless of its working directory.
func LoadConfig() Config {
	baseDir := loadDotEnv()
	return Config{
		GeminiModel:    env("GEMINI_MODEL", "gemini-3.5-flash"),
		GCPProject:     os.Getenv("GOOGLE_CLOUD_PROJECT"),
		GCPLocation:    env("GOOGLE_CLOUD_LOCATION", "global"),
		DTEnvironment:  os.Getenv("DT_ENVIRONMENT"),
		DTPlatformTok:  os.Getenv("DT_PLATFORM_TOKEN"),
		DTApiToken:     os.Getenv("DT_API_TOKEN"),
		MCPNodeBin:     os.Getenv("MCP_NODE_BIN"),
		SourceRoot:     resolveRel(baseDir, env("SOURCE_ROOT", "./demo_app")),
		PatchOutputDir: resolveRel(baseDir, env("PATCH_OUTPUT_DIR", "./.patches")),
		Port:           env("PORT", "8080"),

		// Default ON: a fresh server starts with a clean problem list (only
		// incidents detected after startup). Opt out with "false".
		ClearIssuesOnStart: os.Getenv("CLEAR_ISSUES_ON_START") != "false",

		// Default ON so the full app works locally; the hosted Cloud Run image pins
		// ENABLE_TEST_CONSOLE=false (see Dockerfile) to stay human-gated. Opt out with "false".
		EnableTestConsole: os.Getenv("ENABLE_TEST_CONSOLE") != "false",
		DemoAppURL:        env("DEMO_APP_URL", "http://localhost:9090"),

		SlackWebhookURL:   os.Getenv("SLACK_WEBHOOK_URL"),
		SlackPollInterval: parseDuration(env("SLACK_POLL_INTERVAL", "60s"), 60*time.Second),
	}
}

func parseDuration(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func resolveRel(baseDir, p string) string {
	if baseDir == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

// loadDotEnv reads .env from the working dir or its parent (so the server can be
// run from the repo root or backend/). It returns the directory of the loaded
// .env (or "" if none). Existing env vars are not overridden.
func loadDotEnv() string {
	for _, p := range []string{".env", filepath.Join("..", ".env")} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if _, exists := os.LookupEnv(k); !exists {
				_ = os.Setenv(k, v)
			}
		}
		f.Close()
		return filepath.Dir(p)
	}
	return ""
}
