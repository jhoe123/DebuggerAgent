import type { Scenario } from "./scenarios";

export interface RunResult {
  completed: boolean; // request finished (vs network error)
  status: number; // HTTP status (0 if the request never completed)
  latencyMs: number; // client-measured round trip
  traceId: string; // from X-Trace-Id header, falling back to the JSON body
  body: string; // raw response text
}

export interface Product {
  id: string;
  name: string;
  price: number;
  category: string;
}

async function request(method: string, endpoint: string): Promise<RunResult> {
  const start = performance.now();
  try {
    const res = await fetch(endpoint, { method });
    const text = await res.text();
    const latencyMs = Math.round(performance.now() - start);
    let traceId = res.headers.get("X-Trace-Id") ?? "";
    if (!traceId) {
      try {
        traceId = (JSON.parse(text) as { traceId?: string }).traceId ?? "";
      } catch {
        /* response wasn't JSON (e.g. the plain-text /checkout body) */
      }
    }
    return { completed: true, status: res.status, latencyMs, traceId, body: text };
  } catch (e) {
    return {
      completed: false,
      status: 0,
      latencyMs: Math.round(performance.now() - start),
      traceId: "",
      body: e instanceof Error ? e.message : String(e),
    };
  }
}

// --- Build self-check (GET /api/status) ---
// Reports whether the running binary still has the seeded bugs or has been patched, so the
// storefront can show a buggy/patched banner. See demo_app/status.go.

export type BuildVerdictKind = "buggy" | "partially-patched" | "patched";

export interface BugStatus {
  id: string; // matches a scenario id (checkout / report); the human label is resolved client-side
  fixed: boolean;
  detail: string;
}

export interface BuildVerdict {
  app: string; // demo-app display name (DEMO_APP_NAME) — drives the storefront brand
  verdict: BuildVerdictKind;
  bugs: BugStatus[];
}

// fetchBuildStatus returns null on any failure (network error, non-200, or unparseable
// body) so the banner degrades silently — e.g. against an older web/dist served by a
// binary that predates the /api/status endpoint.
export async function fetchBuildStatus(): Promise<BuildVerdict | null> {
  try {
    const res = await fetch("/api/status");
    if (!res.ok) return null;
    return (await res.json()) as BuildVerdict;
  } catch {
    return null;
  }
}

export function runScenario(s: Scenario): Promise<RunResult> {
  return request(s.method, s.endpoint);
}

export function runReset(endpoint: string): Promise<RunResult> {
  return request("GET", endpoint);
}

export async function fetchCatalog(): Promise<Product[]> {
  const res = await request("GET", "/api/catalog");
  if (!res.completed || res.status !== 200) return [];
  try {
    return (JSON.parse(res.body) as { products?: Product[] }).products ?? [];
  } catch {
    return [];
  }
}
