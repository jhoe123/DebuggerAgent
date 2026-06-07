// Package dynatrace launches the Dynatrace MCP server and exposes a small
// direct client (used for the deterministic problem list; the agent uses its own
// MCP toolset for investigations).
package dynatrace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const mcpPackage = "@dynatrace-oss/dynatrace-mcp-server@latest"

// MCPCommand builds the command that launches the Dynatrace MCP server. It runs
// the server via nodeBin's npx-cli (nodeBin must be Node >= 20.17) and prepends
// that Node to PATH so the server npx spawns also uses it. Falls back to npx on PATH.
func MCPCommand(nodeBin, dtEnvironment, dtToken string) *exec.Cmd {
	env := os.Environ()
	var cmd *exec.Cmd
	if nodeBin != "" {
		nodeDir := filepath.Dir(nodeBin)
		npxCli := filepath.Join(nodeDir, "node_modules", "npm", "bin", "npx-cli.js")
		if _, err := os.Stat(npxCli); err == nil {
			cmd = exec.Command(nodeBin, npxCli, "-y", mcpPackage)
			env = prependPath(env, nodeDir)
		}
	}
	if cmd == nil {
		cmd = exec.Command("npx", "-y", mcpPackage)
	}
	cmd.Env = append(env,
		"DT_ENVIRONMENT="+dtEnvironment,
		"DT_PLATFORM_TOKEN="+dtToken,
		"DT_MCP_DISABLE_TELEMETRY=true",
	)
	return cmd
}

func prependPath(env []string, dir string) []string {
	sep := string(os.PathListSeparator)
	for i, e := range env {
		if len(e) >= 5 && strings.EqualFold(e[:5], "PATH=") {
			env[i] = "PATH=" + dir + sep + e[5:]
			return env
		}
	}
	return append(env, "PATH="+dir)
}
