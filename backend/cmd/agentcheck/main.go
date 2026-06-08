// Command agentcheck is a dev smoke test: it builds the agent and runs one
// investigation prompt, exercising Vertex (Gemini), the Dynatrace MCP launch,
// and tool calls. Run from backend/: `go run ./cmd/agentcheck`.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/patchpilot/backend/internal/agent"
)

func main() {
	cfg := agent.LoadConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	svc, err := agent.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	fmt.Println(">> agent built; launching MCP + calling Gemini...")

	prompt := `Use your Dynatrace tools to list the current open problems in the environment.
Do NOT call propose_patch. Reply with a one-line summary: how many problems, and the title of the most recent one (or "NONE" if there are no open problems).`

	final, _, err := svc.Investigate(ctx, "smoke", prompt, nil)
	if err != nil {
		log.Fatalf("\ninvestigate: %v", err)
	}
	fmt.Println("\n>> FINAL:", final)
}
