// API client for the Go backend, with graceful fallback to mock data while the
// backend endpoints (T6) are still stubs. Once the backend returns real JSON,
// the same calls "just work" with no UI changes.
import type {
  ApproveResult,
  AskResult,
  Investigation,
  PipelineOptions,
  PipelineResult,
  Problem,
  Step,
  TestStatus,
  TriggerResult,
} from "./types";
import { mockInvestigation, mockProblems } from "./mock";

const BASE = import.meta.env.VITE_API_BASE_URL ?? "";

let usedMock = false;
/** True if any call has fallen back to mock data this session. */
export const wasMock = () => usedMock;

async function real<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!res.ok) throw new Error(`${path} -> ${res.status}`);
  return (await res.json()) as T;
}

export async function listProblems(): Promise<Problem[]> {
  try {
    return await real<Problem[]>("/api/problems");
  } catch {
    usedMock = true;
    return mockProblems;
  }
}

export async function investigate(problemId: string): Promise<Investigation> {
  try {
    return await real<Investigation>("/api/investigate", {
      method: "POST",
      body: JSON.stringify({ problemId }),
    });
  } catch {
    usedMock = true;
    return { ...mockInvestigation, problemId };
  }
}

export async function approvePatch(problemId: string): Promise<ApproveResult> {
  try {
    return await real<ApproveResult>("/api/approve-patch", {
      method: "POST",
      body: JSON.stringify({ problemId }),
    });
  } catch {
    usedMock = true;
    return { writtenTo: `.patches/${mockInvestigation.proposedPatch.file}` };
  }
}

export async function ask(problemId: string, question: string): Promise<AskResult> {
  return real<AskResult>("/api/ask", {
    method: "POST",
    body: JSON.stringify({ problemId, question }),
  });
}

// --- Test Console (only responds when the backend has ENABLE_TEST_CONSOLE) ---

export async function testStatus(): Promise<TestStatus> {
  return real<TestStatus>("/api/test/status");
}
export async function testTrigger(): Promise<TriggerResult> {
  return real<TriggerResult>("/api/test/trigger", { method: "POST" });
}
export async function testReset(): Promise<TestStatus> {
  return real<TestStatus>("/api/test/reset", { method: "POST" });
}

// --- SSE helpers (live reasoning stream + pipeline) ---

// Reads an SSE response, invoking onStep for each `step` event; resolves with the
// final `result` event payload (parsed as T).
async function consumeSSE<T>(
  path: string,
  body: unknown,
  onStep: (s: Step) => void,
): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok || !res.body) throw new Error(`${path} -> ${res.status}`);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let result: T | undefined;
  let errored: string | undefined;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const chunks = buf.split("\n\n");
    buf = chunks.pop() ?? "";
    for (const chunk of chunks) {
      const ev = /^event:\s*(.+)$/m.exec(chunk)?.[1]?.trim();
      const dataLine = /^data:\s*([\s\S]*)$/m.exec(chunk)?.[1];
      if (!ev || !dataLine) continue;
      const data = JSON.parse(dataLine);
      if (ev === "step") onStep(data as Step);
      else if (ev === "result") result = data as T;
      else if (ev === "error") errored = data.error ?? "stream error";
    }
  }
  if (errored) throw new Error(errored);
  if (result === undefined) throw new Error("stream ended without a result");
  return result;
}

// investigateStream: streams agent steps, resolves with the Investigation.
// Falls back to the non-streaming endpoint (and mock) on failure.
export async function investigateStream(
  problemId: string,
  onStep: (s: Step) => void,
): Promise<Investigation> {
  try {
    return await consumeSSE<Investigation>("/api/investigate/stream", { problemId }, onStep);
  } catch {
    return investigate(problemId);
  }
}

// remediate: streams pipeline stages, resolves with the PipelineResult.
export async function remediate(
  options: PipelineOptions,
  onStep: (s: Step) => void,
): Promise<PipelineResult> {
  return consumeSSE<PipelineResult>("/api/remediate", { options }, onStep);
}
