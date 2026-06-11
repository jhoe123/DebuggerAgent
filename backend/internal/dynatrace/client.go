package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/patchpilot/backend/internal/api"
)

// Client is a thin direct MCP client to the Dynatrace MCP server.
type Client struct {
	session      *mcp.ClientSession
	env          string    // tenant base URL (for deep links)
	baseline     time.Time // server start time — the lookback floor when clearOnStart is set
	clearOnStart bool      // only surface problems detected after startup (CLEAR_ISSUES_ON_START)

	// exec runs a DQL statement; indirection lets tests stub Grail without an MCP session.
	exec func(ctx context.Context, dql string) ([]map[string]any, int64, error)

	// Snapshot cache: /api/problems (7s poll), the autopilot tick (30s) and the Slack
	// digest all call ListProblems; serving a short-TTL snapshot gives every consumer
	// the same view (no flicker from re-running live queries) and cuts Grail cost.
	// cacheMu also serializes the underlying queries (singleflight).
	cacheMu  sync.Mutex
	cache    []api.Problem
	cachedAt time.Time
	lastPerf []api.Problem // last successful perf sub-result (perf query is best-effort)
}

// Open launches the Dynatrace MCP server and initializes an MCP session.
// When clearOnStart is true, ListProblems only looks back to this moment, so a
// freshly started server presents a clean (empty) problem list.
func Open(ctx context.Context, nodeBin, dtEnvironment, dtToken string, clearOnStart bool) (*Client, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "patchpilot", Version: "0.1.0"}, nil)
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: MCPCommand(nodeBin, dtEnvironment, dtToken)}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect dynatrace mcp: %w", err)
	}
	cl := &Client{session: sess, env: dtEnvironment, baseline: time.Now(), clearOnStart: clearOnStart}
	cl.exec = cl.executeDQLMeta
	return cl, nil
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

// maxLookbackSecs caps how far back we ever scan (the original 30-day window) so a
// long-running instance doesn't grow its Grail scan unbounded.
const maxLookbackSecs = 30 * 24 * 3600

// fromWindow returns the DQL `from:` expression for the problem queries. With
// clear-on-start enabled it looks back only to server boot (a window that grows
// with uptime, capped at 30 days) so pre-existing problems stay hidden; otherwise
// it uses the full 30-day window.
func (c *Client) fromWindow() string {
	if !c.clearOnStart {
		return "now()-30d"
	}
	secs := int64(time.Since(c.baseline).Seconds())
	if secs < 1 {
		secs = 1
	}
	if secs > maxLookbackSecs {
		secs = maxLookbackSecs
	}
	return fmt.Sprintf("now()-%ds", secs)
}

// cacheTTL is how long a ListProblems snapshot is served before re-querying Grail.
// Shorter than the autopilot tick (30s) and a small multiple of the UI poll (7s),
// so every consumer sees the same list within a window.
const cacheTTL = 10 * time.Second

// maxStale caps how long a stale snapshot is served when the error query keeps
// failing; past this the error propagates so callers notice the outage.
const maxStale = 5 * time.Minute

// ListProblems summarizes recent error spans AND slow operations into one problem
// list for the UI. Each problem is tagged Kind ("error"|"performance"); its ID is a
// composite "<kind>:<service>" so error and perf problems on the same service don't
// collide (the server's prompt builder splits it back apart).
//
// Results are cached for cacheTTL and rows are aggregated per service, so the list
// is stable across polls: one entry per <kind>:<service>, deterministic order, and
// a transient query failure serves the last good snapshot instead of blanking.
func (c *Client) ListProblems(ctx context.Context) ([]api.Problem, error) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cache != nil && time.Since(c.cachedAt) < cacheTTL {
		return append([]api.Problem(nil), c.cache...), nil
	}

	problems, err := c.listProblemsFresh(ctx)
	if err != nil {
		// Serve the last good snapshot on transient failure (capped) so one bad
		// poll doesn't blank or churn every consumer's list.
		if c.cache != nil && time.Since(c.cachedAt) < maxStale {
			return append([]api.Problem(nil), c.cache...), nil
		}
		return nil, err
	}
	c.cache = problems
	c.cachedAt = time.Now()
	return append([]api.Problem(nil), problems...), nil
}

// listProblemsFresh runs the live Grail queries. Caller holds cacheMu.
func (c *Client) listProblemsFresh(ctx context.Context) ([]api.Problem, error) {
	from := c.fromWindow()
	// limit 50 (not 20): rows are per {service, span, message} before aggregation,
	// so a generous limit keeps busy services from churning at the cutoff edge.
	errorDQL := `fetch spans, from:` + from + `
| filter span.status_code == "error"
| summarize count = count(), latest = max(start_time), by:{service.name, span.name, span.status_message}
| sort latest desc
| limit 50`
	// Non-error spans: status_code is null for unset spans, and `!= "error"` drops
	// nulls (null != x is null), so include nulls explicitly. The p95 threshold is
	// applied in Go below (duration-typed p95 won't compare in DQL).
	perfDQL := `fetch spans, from:` + from + `
| filter isNull(span.status_code) or span.status_code != "error"
| summarize count = count(), p95 = percentile(duration, 95), latest = max(start_time), by:{service.name, span.name}
| sort p95 desc
| limit 50`

	var scanned int64

	errRecs, errScan, err := c.exec(ctx, errorDQL)
	if err != nil {
		return nil, err
	}
	scanned += errScan
	problems := aggregateErrorRows(errRecs, c.env)

	// Performance problems are best-effort: don't fail the whole list if the perf
	// query errors (e.g. older tenant lacking percentile()); reuse the last good
	// perf set instead so those entries don't blink out for a few polls.
	if perfRecs, perfScan, perfErr := c.exec(ctx, perfDQL); perfErr == nil {
		scanned += perfScan
		c.lastPerf = aggregatePerfRows(perfRecs, c.env)
	}
	problems = append(problems, c.lastPerf...)

	// Stamp the shared Grail scan total on every problem (cost awareness).
	for i := range problems {
		problems[i].GrailScannedBytes = scanned
	}
	// Deterministic wire order: aggregation maps + query sorts vary poll to poll,
	// so sort by kind then id to keep every consumer's view stable.
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Kind != problems[j].Kind {
			return problems[i].Kind < problems[j].Kind
		}
		return problems[i].ID < problems[j].ID
	})
	return problems, nil
}

// aggregateErrorRows folds error rows (one per {service, span, message}) into one
// problem per service. IDs are "error:<service>" — multiple rows per service MUST
// collapse here or consumers see duplicate IDs whose fields flip between polls.
// Rows arrive sorted latest-desc, so the first row per service is the newest and
// supplies the title/entity/timestamp; counts sum across all rows.
func aggregateErrorRows(recs []map[string]any, env string) []api.Problem {
	var problems []api.Problem
	idx := map[string]int{} // service -> index in problems
	for _, r := range recs {
		svc := str(r["service.name"])
		count := atoi(str(r["count"]))
		if i, ok := idx[svc]; ok {
			problems[i].AffectedUsers += count
			problems[i].Occurrences += count
			continue
		}
		idx[svc] = len(problems)
		problems = append(problems, api.Problem{
			ID:            "error:" + svc,
			Kind:          "error",
			Title:         str(r["span.status_message"]),
			Severity:      "ERROR",
			Status:        "OPEN",
			AffectedUsers: count,
			Occurrences:   count,
			DynatraceURL:  env,
			StartedAt:     str(r["latest"]),
			Entity:        strings.TrimSpace(svc + " · " + str(r["span.name"])),
		})
	}
	return problems
}

// aggregatePerfRows folds slow-operation rows (one per {service, span}) into one
// problem per service, applying the p95 threshold per row first. Rows arrive
// sorted p95-desc, so the first qualifying row per service is the slowest and
// supplies the title/metric/entity; counts sum across qualifying rows.
func aggregatePerfRows(recs []map[string]any, env string) []api.Problem {
	var problems []api.Problem
	idx := map[string]int{} // service -> index in problems
	for _, r := range recs {
		p95ms := int64(atof(str(r["p95"])) / 1e6)
		if p95ms < perfThresholdMs {
			continue // not slow enough to flag
		}
		svc := str(r["service.name"])
		count := atoi(str(r["count"]))
		span := str(r["span.name"])
		if i, ok := idx[svc]; ok {
			problems[i].AffectedUsers += count
			problems[i].Occurrences += count
			continue
		}
		idx[svc] = len(problems)
		problems = append(problems, api.Problem{
			ID:            "performance:" + svc,
			Kind:          "performance",
			Title:         "Slow operation: " + span,
			Severity:      "RESOURCE",
			Status:        "OPEN",
			AffectedUsers: count,
			Occurrences:   count,
			Metric:        fmt.Sprintf("p95 %d ms", p95ms),
			DynatraceURL:  env,
			StartedAt:     str(r["latest"]),
			Entity:        strings.TrimSpace(svc + " · " + span),
		})
	}
	return problems
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
