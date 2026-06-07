package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/debuggeragent/backend/internal/api"
)

// Client is a thin direct MCP client to the Dynatrace MCP server.
type Client struct {
	session *mcp.ClientSession
}

// Open launches the Dynatrace MCP server and initializes an MCP session.
func Open(ctx context.Context, nodeBin, dtEnvironment, dtToken string) (*Client, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "debuggeragent", Version: "0.1.0"}, nil)
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: MCPCommand(nodeBin, dtEnvironment, dtToken)}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect dynatrace mcp: %w", err)
	}
	return &Client{session: sess}, nil
}

func (c *Client) Close() error { return c.session.Close() }

// ExecuteDQL runs a DQL statement and returns the result records (from _meta.records).
func (c *Client) ExecuteDQL(ctx context.Context, dql string) ([]map[string]any, error) {
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "execute_dql",
		Arguments: map[string]any{"dqlStatement": dql},
	})
	if err != nil {
		return nil, err
	}
	var meta struct {
		Records []map[string]any `json:"records"`
	}
	if b, mErr := json.Marshal(res.Meta); mErr == nil {
		_ = json.Unmarshal(b, &meta)
	}
	return meta.Records, nil
}

// ListProblems summarizes recent error spans into a problem list for the UI.
func (c *Client) ListProblems(ctx context.Context) ([]api.Problem, error) {
	const dql = `fetch spans, from:now()-24h
| filter span.status_code == "error"
| summarize count = count(), latest = max(start_time), by:{service.name, span.name, span.status_message}
| sort latest desc
| limit 20`
	records, err := c.ExecuteDQL(ctx, dql)
	if err != nil {
		return nil, err
	}
	problems := make([]api.Problem, 0, len(records))
	for _, r := range records {
		svc := str(r["service.name"])
		problems = append(problems, api.Problem{
			ID:            svc,
			Title:         str(r["span.status_message"]),
			Severity:      "ERROR",
			Status:        "OPEN",
			AffectedUsers: atoi(str(r["count"])),
			StartedAt:     str(r["latest"]),
			Entity:        strings.TrimSpace(svc + " · " + str(r["span.name"])),
		})
	}
	return problems, nil
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
