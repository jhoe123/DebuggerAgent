// Command vertexcheck is a throwaway connectivity probe: it uses ADC + the same
// genai Vertex client the backend uses to confirm Gemini is reachable before the
// e2e matrix spends real time/tokens. Not committed.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/genai"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = "emogent-demo-2026"
	}
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = "global"
	}
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-3.5-flash"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
	if err != nil {
		fmt.Println("CLIENT_ERR:", err)
		os.Exit(1)
	}
	resp, err := client.Models.GenerateContent(ctx, model, genai.Text("Reply with the single word: pong"), nil)
	if err != nil {
		fmt.Println("GEN_ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("VERTEX_OK model=%s reply=%q\n", model, resp.Text())
}
