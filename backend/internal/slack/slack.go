// Package slack posts a consolidated "rolling digest" of active bugs to a Slack
// Incoming Webhook. A background poller (Run) lists problems on an interval and
// calls Sync, which posts a single digest message — but only when the set of bugs
// actually changes (deduped by a content hash), so recurring occurrences don't spam
// the channel. Stdlib only; failures are logged, never fatal.
package slack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/patchpilot/backend/internal/api"
)

// ListFunc returns the current set of problems (e.g. dynatrace.Client.ListProblems).
type ListFunc func(context.Context) ([]api.Problem, error)

// Notifier posts digests to a Slack Incoming Webhook. It is configurable at
// runtime (webhook + enabled) via SetConfig — the background poller (Run) keeps
// polling and posts only while enabled with a webhook set.
type Notifier struct {
	client *http.Client

	mu         sync.Mutex
	enabled    bool
	webhookURL string
	lastHash   string
}

// New returns a Notifier seeded from the env webhook (enabled when non-empty).
// It is never nil, so Slack can be enabled later at runtime from Settings.
func New(webhookURL string) *Notifier {
	return &Notifier{
		client:     &http.Client{Timeout: 10 * time.Second},
		enabled:    webhookURL != "",
		webhookURL: webhookURL,
	}
}

// SetConfig updates the notifier at runtime. A non-empty webhookURL replaces the
// current webhook; an empty webhookURL leaves it unchanged, so the enable toggle
// never wipes a configured secret. A config change re-arms the digest so the next
// poll re-posts to the (possibly new) channel.
func (n *Notifier) SetConfig(enabled bool, webhookURL string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.enabled = enabled
	if webhookURL != "" {
		n.webhookURL = webhookURL
	}
	n.lastHash = ""
	n.mu.Unlock()
}

// Status returns the current configuration for display. It never returns the raw
// webhook — only a masked preview.
func (n *Notifier) Status() api.SlackStatus {
	if n == nil {
		return api.SlackStatus{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return api.SlackStatus{
		Enabled:    n.enabled,
		Configured: n.webhookURL != "",
		Preview:    maskWebhook(n.webhookURL),
	}
}

// Test posts a one-off message to the current webhook to validate it.
func (n *Notifier) Test() error {
	if n == nil {
		return fmt.Errorf("slack notifier unavailable")
	}
	n.mu.Lock()
	url := n.webhookURL
	n.mu.Unlock()
	if url == "" {
		return fmt.Errorf("no Slack webhook configured")
	}
	return n.post(url, ":satellite: *PatchPilot* — test message. Slack notifications are wired up correctly.")
}

// maskWebhook returns a display-safe form of a webhook URL (host + last 4 chars),
// never the full secret path.
func maskWebhook(u string) string {
	if u == "" {
		return ""
	}
	host := u
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			host = rest[:j]
		} else {
			host = rest
		}
	}
	tail := u
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return host + "/…" + tail
}

// Run polls list every interval and syncs the digest until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context, interval time.Duration, list ListFunc) {
	if n == nil {
		return
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	log.Printf("Slack poller started (digest poll every %s; posts only while enabled)", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	n.tick(ctx, list) // fire once at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.tick(ctx, list)
		}
	}
}

func (n *Notifier) tick(ctx context.Context, list ListFunc) {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probs, err := list(c)
	if err != nil {
		log.Printf("slack: list problems failed: %v", err)
		return
	}
	if err := n.Sync(probs); err != nil {
		log.Printf("slack: post failed: %v", err)
	}
}

// Sync posts the consolidated digest if Slack is enabled+configured and the bug
// set changed since the last post.
func (n *Notifier) Sync(problems []api.Problem) error {
	if n == nil {
		return nil
	}
	hash := fingerprint(problems)
	n.mu.Lock()
	enabled, url, unchanged := n.enabled, n.webhookURL, hash == n.lastHash
	n.mu.Unlock()
	if !enabled || url == "" {
		return nil // Slack disabled or no webhook configured
	}
	if unchanged {
		return nil // rolling digest: nothing new to report
	}
	if err := n.post(url, digestText(problems)); err != nil {
		return err
	}
	n.mu.Lock()
	n.lastHash = hash
	n.mu.Unlock()
	return nil
}

// fingerprint hashes the set of problems by ID + occurrence bucket so the digest is
// re-posted on a new/cleared bug or a material change in volume, not every poll.
func fingerprint(problems []api.Problem) string {
	keys := make([]string, 0, len(problems))
	for _, p := range problems {
		keys = append(keys, fmt.Sprintf("%s|%d", p.ID, bucket(p.Occurrences)))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

// bucket coarsens occurrence counts so minor increments don't re-trigger a post.
func bucket(n int) int {
	switch {
	case n <= 0:
		return 0
	case n < 10:
		return 1
	case n < 100:
		return 2
	case n < 1000:
		return 3
	default:
		return 4
	}
}

func digestText(problems []api.Problem) string {
	if len(problems) == 0 {
		return ":white_check_mark: *PatchPilot* — no active issues."
	}
	var errs, perf int
	for _, p := range problems {
		if p.Kind == "performance" {
			perf++
		} else {
			errs++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, ":lady_beetle: *PatchPilot — %d active issue(s)* (%d error, %d performance)\n",
		len(problems), errs, perf)
	max := len(problems)
	if max > 10 {
		max = 10
	}
	for _, p := range problems[:max] {
		kind := strings.ToUpper(p.Kind)
		if kind == "" {
			kind = "ERROR"
		}
		line := fmt.Sprintf("• [%s] %s — %s · %d occurrences", kind, p.Title, p.Entity, p.Occurrences)
		if p.Metric != "" {
			line += " · " + p.Metric
		}
		if p.DynatraceURL != "" {
			line += fmt.Sprintf(" · <%s|Dynatrace>", p.DynatraceURL)
		}
		b.WriteString(line + "\n")
	}
	if len(problems) > max {
		fmt.Fprintf(&b, "_…and %d more._\n", len(problems)-max)
	}
	fmt.Fprintf(&b, "_updated %s_", time.Now().Format(time.RFC1123))
	return b.String()
}

func (n *Notifier) post(url, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}
