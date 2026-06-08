// Browser-local persistence for problem dismissals and investigation/pipeline runs.
//
// Why localStorage (not the backend): problems are fetched live from Dynatrace and
// the Cloud Run filesystem (/tmp) is ephemeral, so the only durable, refresh-proof
// home for "which problems I hid" and "what the agent found last time" is the user's
// own browser. This module is zero-dependency and mirrors the da.* / try-catch
// pattern used by SettingsContext.
import type { Investigation, PipelineResult, Step } from "../types";

const DISMISSED_KEY = "da.dismissed";
const RUNS_KEY = "da.runs";

/** Keep only the most recent N runs so localStorage never approaches its ~5MB cap. */
export const MAX_RUNS = 25;

export interface DismissedEntry {
  dismissedAt: string; // ISO
}
export type DismissedMap = Record<string, DismissedEntry>;

export type RunType = "investigation" | "pipeline" | "confirm";

export interface LocalRun {
  id: string; // `${Date.now()}-${problemId}`
  problemId: string;
  title?: string;
  kind?: "error" | "performance";
  type: RunType;
  investigation?: Investigation; // type === "investigation"
  steps?: Step[];
  pipeline?: PipelineResult; // type === "pipeline"
  status: "ok" | "failed";
  createdAt: string; // ISO
}

export interface ProblemStatus {
  investigated: boolean;
  patched: boolean; // a pipeline run succeeded
  failed: boolean; // a pipeline run failed
  confirmed: boolean; // a fix was confirmed (merged to the working branch)
}

function loadJSON<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key);
    if (v != null) return JSON.parse(v) as T;
  } catch {
    /* ignore unavailable / malformed */
  }
  return fallback;
}

function saveJSON(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* ignore quota / unavailable — caller handles trimming for runs */
  }
}

// --- dismissals ---

export function loadDismissed(): DismissedMap {
  return loadJSON<DismissedMap>(DISMISSED_KEY, {});
}

export function saveDismissed(map: DismissedMap): void {
  saveJSON(DISMISSED_KEY, map);
}

// --- runs (capped ring, newest first) ---

export function loadRuns(): LocalRun[] {
  const runs = loadJSON<LocalRun[]>(RUNS_KEY, []);
  return Array.isArray(runs) ? runs : [];
}

// Persist runs, trimming to MAX_RUNS. On a quota error, keep dropping the oldest
// run and retry so a single large diff can't wedge persistence permanently.
export function saveRuns(runs: LocalRun[]): LocalRun[] {
  let trimmed = runs.slice(0, MAX_RUNS);
  for (;;) {
    try {
      localStorage.setItem(RUNS_KEY, JSON.stringify(trimmed));
      return trimmed;
    } catch {
      if (trimmed.length <= 1) return trimmed; // can't shrink further; give up silently
      trimmed = trimmed.slice(0, trimmed.length - 1);
    }
  }
}

/** Prepend a run (newest first), trim, persist; returns the new list. */
export function addRun(runs: LocalRun[], run: LocalRun): LocalRun[] {
  return saveRuns([run, ...runs]);
}

/** Most recent run of a given type for a problem, if any. */
export function latestRun(runs: LocalRun[], problemId: string, type: RunType): LocalRun | undefined {
  return runs.find((r) => r.problemId === problemId && r.type === type);
}

/** Per-problem derived status used for at-a-glance badges on the list. */
export function problemStatusMap(runs: LocalRun[]): Record<string, ProblemStatus> {
  const map: Record<string, ProblemStatus> = {};
  for (const r of runs) {
    const s = (map[r.problemId] ??= { investigated: false, patched: false, failed: false, confirmed: false });
    if (r.type === "investigation") s.investigated = true;
    if (r.type === "pipeline") {
      if (r.status === "ok") s.patched = true;
      else s.failed = true;
    }
    if (r.type === "confirm" && r.status === "ok") s.confirmed = true;
  }
  return map;
}
