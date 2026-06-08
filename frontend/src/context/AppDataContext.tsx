import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import type { GitSourceStatus, PipelineSettings, Problem, ProblemArtifact, StagedPatch, TestStatus } from "../types";
import { getGitSource, getPipelineConfig, listArtifacts, listPatches, fetchProblems, repoDisplayName, resolveDemoAppUrl, testStatus, wasMock, clearPatches } from "../api";
import { mockProblems } from "../mock";
import { usePolling } from "../hooks/usePolling";

interface AppDataValue {
  // Problems (polled ~20s; paused while a stream runs).
  problems: Problem[];
  problemsLoading: boolean; // true only on the first load (no data yet)
  problemsError: unknown;
  problemsUpdatedAt: number | null;
  refreshProblems: () => Promise<void>;

  // Test Console availability + status (polled ~10s).
  consoleAvailable: boolean;
  testStatus: TestStatus | undefined;
  refreshTestStatus: () => Promise<void>;
  testUpdatedAt: number | null;

  // Demo app (running source project) origin to link testers to after a deploy, resolved
  // from the Test Console URL (local) or the pipeline health URL (hosted). undefined when unknown.
  demoAppUrl: string | undefined;
  // Demo app display name, derived from the configured Git source repo. undefined when no
  // source repo is configured (callers fall back to generic copy).
  demoAppName: string | undefined;

  // Consolidation batch (staged patches) + durable per-problem status artifacts.
  staged: StagedPatch[];
  refreshPatches: () => Promise<void>;
  clearPatches: () => Promise<void>;
  artifacts: ProblemArtifact[];
  artifactMap: Record<string, ProblemArtifact>;
  refreshArtifacts: () => Promise<void>;

  // History reload signal.
  historyKey: number;
  reloadHistory: () => void;

  // True if any API call fell back to mock data this session.
  mock: boolean;

  // Set true while an SSE stream is active so polling pauses (avoids churn).
  streaming: boolean;
  setStreaming: (b: boolean) => void;

  // Git source settings (polled ~15s).
  gitSource: GitSourceStatus | undefined;
  refreshGitSource: () => Promise<void>;
}

const AppDataContext = createContext<AppDataValue | null>(null);

export function AppDataProvider({ children }: { children: ReactNode }) {
  const [historyKey, setHistoryKey] = useState(0);
  const reloadHistory = useCallback(() => setHistoryKey((k) => k + 1), []);
  const [streaming, setStreaming] = useState(false);
  const [mock, setMock] = useState(false);

  // Stable problems list. /api/problems is a LIVE Dynatrace query (fetch spans → summarize),
  // so each poll returns slightly different counts/latency/ordering, and problems near the
  // p95 threshold or the result limit flicker in and out. To keep the UI consistent we
  // stabilize by problem id: preserve a stable order (new problems surface at the top),
  // update fields in place, and only drop a problem after it's been absent for a few polls.
  const seenReal = useRef(false);
  const servingMock = useRef(false);
  const orderRef = useRef<string[]>([]); // stable display order (newest-appeared first)
  const knownRef = useRef<Map<string, Problem>>(new Map()); // last-known fields per id
  const missRef = useRef<Map<string, number>>(new Map()); // consecutive polls a known id was absent
  const GRACE_POLLS = 3; // keep a vanished problem this many polls before dropping (debounces flicker)

  function resetStabilizer() {
    orderRef.current = [];
    knownRef.current.clear();
    missRef.current.clear();
  }

  function stabilizeProblems(incoming: Problem[]): Problem[] {
    const incomingIds = new Set(incoming.map((p) => p.id));
    // Newly-seen problems surface at the top; known ones keep their position + fields refresh.
    for (let i = incoming.length - 1; i >= 0; i--) {
      const p = incoming[i];
      knownRef.current.set(p.id, p);
      missRef.current.set(p.id, 0);
      if (!orderRef.current.includes(p.id)) orderRef.current.unshift(p.id);
    }
    // Age out problems that have been absent beyond the grace window.
    orderRef.current = orderRef.current.filter((id) => {
      if (incomingIds.has(id)) return true;
      const miss = (missRef.current.get(id) ?? 0) + 1;
      missRef.current.set(id, miss);
      if (miss > GRACE_POLLS) {
        knownRef.current.delete(id);
        missRef.current.delete(id);
        return false;
      }
      return true;
    });
    return orderRef.current.map((id) => knownRef.current.get(id)).filter((p): p is Problem => !!p);
  }

  const problemsPoll = usePolling<Problem[]>(
    async () => {
      try {
        const p = await fetchProblems(); // throws on transport/HTTP error
        if (servingMock.current) {
          resetStabilizer(); // drop mock entries when the real backend comes online
          servingMock.current = false;
        }
        seenReal.current = true;
        setMock(wasMock());
        return stabilizeProblems(p);
      } catch (e) {
        if (!seenReal.current) {
          if (!servingMock.current) {
            resetStabilizer();
            servingMock.current = true;
          }
          setMock(true);
          return stabilizeProblems(mockProblems); // first-load demo fallback only
        }
        throw e; // already have data — usePolling keeps the last stabilized list (no blank)
      }
    },
    7000,
    { paused: streaming },
  );

  const testPoll = usePolling<TestStatus>(() => testStatus(), 10000, { paused: streaming });
  // Pipeline config rarely changes; poll slowly just to source the demo-app URL fallback.
  const pipelinePoll = usePolling<PipelineSettings>(() => getPipelineConfig(), 60000, { paused: streaming });
  const patchesPoll = usePolling<StagedPatch[]>(() => listPatches(), 15000, { paused: streaming });
  const artifactsPoll = usePolling<ProblemArtifact[]>(() => listArtifacts(), 15000, { paused: streaming });
  const gitSourcePoll = usePolling<GitSourceStatus>(() => getGitSource(), 15000, { paused: streaming });

  const artifacts = artifactsPoll.data ?? [];
  const artifactMap = useMemo(() => {
    const m: Record<string, ProblemArtifact> = {};
    for (const a of artifacts) m[a.problemId] = a;
    return m;
  }, [artifacts]);

  const value: AppDataValue = {
    problems: problemsPoll.data ?? [],
    problemsLoading: problemsPoll.loading && problemsPoll.data === undefined,
    problemsError: problemsPoll.error,
    problemsUpdatedAt: problemsPoll.updatedAt,
    refreshProblems: problemsPoll.refresh,
    // Console is available once /api/test/status has ever returned a payload.
    consoleAvailable: testPoll.data !== undefined,
    testStatus: testPoll.data,
    refreshTestStatus: testPoll.refresh,
    testUpdatedAt: testPoll.updatedAt,
    demoAppUrl: resolveDemoAppUrl(testPoll.data?.demoAppUrl, pipelinePoll.data?.healthUrl) ?? undefined,
    demoAppName: repoDisplayName(gitSourcePoll.data?.repoUrlPreview) ?? undefined,
    staged: patchesPoll.data ?? [],
    refreshPatches: patchesPoll.refresh,
    clearPatches: async () => {
      await clearPatches();
      await patchesPoll.refresh();
    },
    artifacts,
    artifactMap,
    refreshArtifacts: artifactsPoll.refresh,
    historyKey,
    reloadHistory,
    mock,
    streaming,
    setStreaming,
    gitSource: gitSourcePoll.data,
    refreshGitSource: gitSourcePoll.refresh,
  };

  return <AppDataContext.Provider value={value}>{children}</AppDataContext.Provider>;
}

export function useAppData(): AppDataValue {
  const v = useContext(AppDataContext);
  if (!v) throw new Error("useAppData must be used within AppDataProvider");
  return v;
}
