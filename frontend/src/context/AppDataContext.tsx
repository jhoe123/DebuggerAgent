import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { GitSourceStatus, PipelineSettings, Problem, ProblemArtifact, StagedPatch, TestStatus } from "../types";
import { getGitSource, getPipelineConfig, listArtifacts, listPatches, listProblems, repoDisplayName, resolveDemoAppUrl, testStatus, wasMock, clearPatches } from "../api";
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

  const problemsPoll = usePolling<Problem[]>(
    async () => {
      const p = await listProblems();
      setMock(wasMock());
      return p;
    },
    20000,
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
