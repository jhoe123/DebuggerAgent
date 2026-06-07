// API client for the Go backend, with graceful fallback to mock data while the
// backend endpoints (T6) are still stubs. Once the backend returns real JSON,
// the same calls "just work" with no UI changes.
import type { ApproveResult, Investigation, Problem } from "./types";
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
