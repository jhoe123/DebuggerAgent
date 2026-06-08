// The scenario registry is the single source of truth for the storefront: each entry
// renders an action (card/button) and declares which Dynatrace signal(s) it triggers.
// Trim or extend the demo by editing this array.

// The demo is intentionally scoped to exactly two patchable bugs — one exception and one
// slow operation — plus healthy baseline traffic, so patching both yields a fully stable
// (all-green) storefront. Keep the signal kinds limited to what those scenarios use.
export type SignalKind = "healthy" | "distributed" | "exception" | "slow";

export interface Scenario {
  id: string;
  title: string; // storefront action label
  framing: string; // what the shopper is "doing"
  method: "GET" | "POST";
  endpoint: string; // same-origin path (defaults baked in so it's one click)
  resetEndpoint?: string; // optional secondary action (e.g. clear the memory cache)
  signals: SignalKind[];
  isIssue: boolean; // shows the bug cue + amber ring
  howToTrigger: string; // inline note: what happens when you click
}

export const SIGNAL_LABELS: Record<SignalKind, string> = {
  healthy: "Healthy trace",
  distributed: "Distributed trace",
  exception: "Exception",
  slow: "Slow · high p95",
};

export const scenarios: Scenario[] = [
  {
    id: "catalog",
    title: "Browse products",
    framing: "Load the storefront catalog.",
    method: "GET",
    endpoint: "/api/catalog",
    signals: ["healthy"],
    isIssue: false,
    howToTrigger: "Healthy baseline traffic — a fast span with no errors.",
  },
  {
    id: "recommend",
    title: "Recommended for you",
    framing: "Personalized picks for this shopper.",
    method: "GET",
    endpoint: "/api/recommend?user=demo",
    signals: ["distributed"],
    isIssue: false,
    howToTrigger:
      "Builds a multi-span trace waterfall: catalog.lookup → inventory.check → pricing.calc.",
  },
  {
    id: "checkout",
    title: "Place order",
    framing: "Check out the cart.",
    method: "GET",
    endpoint: "/checkout?index=99",
    signals: ["exception"],
    isIssue: true,
    howToTrigger:
      "Checks out an out-of-range cart line → index-out-of-range panic → HTTP 500 with a stack trace on the span.",
  },
  {
    id: "report",
    title: "Sales report",
    framing: "Generate the daily sales summary.",
    method: "GET",
    endpoint: "/report?n=200",
    signals: ["slow"],
    isIssue: true,
    howToTrigger:
      "Builds a 200-row report with a per-item blocking call (~600ms). The fix removes the per-item I/O so it returns in <100ms.",
  },
];
