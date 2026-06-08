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
