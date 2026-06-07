package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Config holds runtime configuration read from the environment (.env locally,
// service env vars on Cloud Run).
type Config struct {
	GeminiModel    string // GEMINI_MODEL
	GCPProject     string // GOOGLE_CLOUD_PROJECT
	GCPLocation    string // GOOGLE_CLOUD_LOCATION (Gemini 3.x → "global")
	DTEnvironment  string // DT_ENVIRONMENT
	DTPlatformTok  string // DT_PLATFORM_TOKEN
	MCPNodeBin     string // MCP_NODE_BIN — node.exe used to run the Dynatrace MCP server
	SourceRoot     string // SOURCE_ROOT (read_source sandbox)
	PatchOutputDir string // PATCH_OUTPUT_DIR (approved patches)
	Port           string // PORT
}

// LoadConfig loads .env (best-effort) then reads configuration from the environment.
func LoadConfig() Config {
	loadDotEnv()
	return Config{
		GeminiModel:    env("GEMINI_MODEL", "gemini-3.1-pro-preview"),
		GCPProject:     os.Getenv("GOOGLE_CLOUD_PROJECT"),
		GCPLocation:    env("GOOGLE_CLOUD_LOCATION", "global"),
		DTEnvironment:  os.Getenv("DT_ENVIRONMENT"),
		DTPlatformTok:  os.Getenv("DT_PLATFORM_TOKEN"),
		MCPNodeBin:     os.Getenv("MCP_NODE_BIN"),
		SourceRoot:     env("SOURCE_ROOT", "./demo_app"),
		PatchOutputDir: env("PATCH_OUTPUT_DIR", "./.patches"),
		Port:           env("PORT", "8080"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv reads .env from the working dir or its parent (so the server can be
// run from either the repo root or backend/). Existing env vars are not overridden.
func loadDotEnv() {
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
		return
	}
}
