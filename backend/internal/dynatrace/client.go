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
	env     string // tenant base URL (for deep links)
}

// Open launches the Dynatrace MCP server and initializes an MCP session.
func Open(ctx context.Context, nodeBin, dtEnvironment, dtToken string) (*Client, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "debuggeragent", Version: "0.1.0"}, nil)
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: MCPCommand(nodeBin, dtEnvironment, dtToken)}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect dynatrace mcp: %w", err)
	}
	return &Client{session: sess, env: dtEnvironment}, nil
}

func (c *Client) Close() error { return c.session.Close() }

// ExecuteDQL runs a DQL statement and returns the result records (from _meta.records).
func (c *Client) ExecuteDQL(ctx context.Context, dql string) ([]map[string]any, error) {
	records, _, err := c.executeDQLMeta(ctx, dql)
	return records, err
}

// executeDQLMeta also returns the Grail bytes scanned (for cost awareness).
func (c *Client) executeDQLMeta(ctx context.Context, dql string) ([]map[string]any, int64, error) {
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "execute_dql",
		Arguments: map[string]any{"dqlStatement": dql},
	})
	if err != nil {
		return nil, 0, err
	}
	var meta struct {
		Records      []map[string]any `json:"records"`
		ScannedBytes int64            `json:"scannedBytes"`
	}
	if b, mErr := json.Marshal(res.Meta); mErr == nil {
		_ = json.Unmarshal(b, &meta)
	}
	return meta.Records, meta.ScannedBytes, nil
}

// perfThresholdMs is the p95 latency (milliseconds) above which an operation is
// flagged as a performance problem. The threshold is applied in Go: Grail span
// `duration` is a duration TYPE, and the post-summarize `p95` won't compare against
// a bare nanosecond literal in DQL, so we filter on the parsed value here instead.
const perfThresholdMs = 500

// ListProblems summarizes recent error spans AND slow operations into one problem
// list for the UI. Each problem is tagged Kind ("error"|"performance"); its ID is a
// composite "<kind>:<service>" so error and perf problems on the same service don't
// collide (the server's prompt builder splits it back apart).
func (c *Client) ListProblems(ctx context.Context) ([]api.Problem, error) {
	const errorDQL = `fetch spans, from:now()-30d
| filter span.status_code == "error"
| summarize count = count(), latest = max(start_time), by:{service.name, span.name, span.status_message}
| sort latest desc
| limit 20`
	// Non-error spans: status_code is null for unset spans, and `!= "error"` drops
	// nulls (null != x is null), so include nulls explicitly. The p95 threshold is
	// applied in Go below (duration-typed p95 won't compare in DQL).
	const perfDQL = `fetch spans, from:now()-30d
| filter isNull(span.status_code) or span.status_code != "error"
| summarize count = count(), p95 = percentile(duration, 95), latest = max(start_time), by:{service.name, span.name}
| sort p95 desc
| limit 20`

	var problems []api.Problem
	var scanned int64

	errRecs, errScan, err := c.executeDQLMeta(ctx, errorDQL)
	if err != nil {
		return nil, err
	}
	scanned += errScan
	for _, r := range errRecs {
		svc := str(r["service.name"])
		count := atoi(str(r["count"]))
		problems = append(problems, api.Problem{
			ID:            "error:" + svc,
			Kind:          "error",
			Title:         str(r["span.status_message"]),
			Severity:      "ERROR",
			Status:        "OPEN",
			AffectedUsers: count,
			Occurrences:   count,
			DynatraceURL:  c.env,
			StartedAt:     str(r["latest"]),
			Entity:        strings.TrimSpace(svc + " · " + str(r["span.name"])),
		})
	}

	// Performance problems are best-effort: don't fail the whole list if the perf
	// query errors (e.g. older tenant lacking percentile()).
	if perfRecs, perfScan, perfErr := c.executeDQLMeta(ctx, perfDQL); perfErr == nil {
		scanned += perfScan
		for _, r := range perfRecs {
			p95ms := int64(atof(str(r["p95"])) / 1e6)
			if p95ms < perfThresholdMs {
				continue // not slow enough to flag
			}
			svc := str(r["service.name"])
			count := atoi(str(r["count"]))
			span := str(r["span.name"])
			problems = append(problems, api.Problem{
				ID:            "performance:" + svc,
				Kind:          "performance",
				Title:         "Slow operation: " + span,
				Severity:      "RESOURCE",
				Status:        "OPEN",
				AffectedUsers: count,
				Occurrences:   count,
				Metric:        fmt.Sprintf("p95 %d ms", p95ms),
				DynatraceURL:  c.env,
				StartedAt:     str(r["latest"]),
				Entity:        strings.TrimSpace(svc + " · " + span),
			})
		}
	}

	// Stamp the shared Grail scan total on every problem (cost awareness).
	for i := range problems {
		problems[i].GrailScannedBytes = scanned
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

// atof parses a numeric value that Grail may return as a number or numeric string.
func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }
