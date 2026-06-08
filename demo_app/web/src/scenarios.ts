// The scenario registry is the single source of truth for the storefront: each entry
// renders an action (card/button) and declares which Dynatrace signal(s) it triggers.
// Trim or extend the demo by editing this array.

export type SignalKind =
  | "healthy"
  | "distributed"
  | "exception"
  | "slow"
  | "dependency"
  | "failrate"
  | "cpu"
  | "memory";

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
  dependency: "External dependency",
  failrate: "Failure rate",
  cpu: "CPU spike",
  memory: "Memory growth",
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
    id: "search",
    title: "Search catalog",
    framing: "Find “running shoes”.",
    method: "GET",
    endpoint: "/api/search?q=running%20shoes",
    signals: ["slow"],
    isIssue: true,
    howToTrigger:
      "Unindexed scan — latency grows with results and ~20% of calls hit a tail-latency spike. Shows as a slow op (high p95).",
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
    id: "pay",
    title: "Pay now",
    framing: "Charge the payment method.",
    method: "POST",
    endpoint: "/api/pay",
    signals: ["dependency"],
    isIssue: true,
    howToTrigger:
      "Calls an external payment gateway that fails ~30% (502) and sometimes times out (~2s). Shows a failing dependency in the service flow.",
  },
  {
    id: "giftcard",
    title: "Apply gift card",
    framing: "Redeem promo code “PROMO2026”.",
    method: "GET",
    endpoint: "/api/giftcard?code=PROMO2026",
    signals: ["exception"],
    isIssue: true,
    howToTrigger:
      "The new campaign’s code isn’t the expected GIFT-<amount> format → redemption throws → HTTP 400 with a recorded exception.",
  },
  {
    id: "flaky",
    title: "Add to cart",
    framing: "Add the item to the cart.",
    method: "GET",
    endpoint: "/api/flaky",
    signals: ["failrate"],
    isIssue: true,
    howToTrigger:
      "~30% of adds fail under contention (HTTP 500). Click a few times to build a failure-rate spike.",
  },
  {
    id: "cpu",
    title: "Run promo engine",
    framing: "Recompute storewide pricing.",
    method: "GET",
    endpoint: "/api/cpu?ms=1500",
    signals: ["cpu"],
    isIssue: true,
    howToTrigger:
      "Busy-loops a CPU core for ~1.5s recomputing prices instead of caching. Drives a CPU spike on the process.",
  },
  {
    id: "mem",
    title: "Build wishlist cache",
    framing: "Cache the shopper’s wishlist.",
    method: "GET",
    endpoint: "/api/mem?mb=16",
    resetEndpoint: "/api/mem?reset=true",
    signals: ["memory"],
    isIssue: true,
    howToTrigger:
      "Appends 16 MB to a cache that never frees — RSS climbs with each click. Use Reset to free it.",
  },
];
