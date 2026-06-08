import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import type { Problem, TestStatus } from "../types";
import { listProblems, testStatus, wasMock } from "../api";
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

  // History reload signal.
  historyKey: number;
  reloadHistory: () => void;

  // True if any API call fell back to mock data this session.
  mock: boolean;

  // Set true while an SSE stream is active so polling pauses (avoids churn).
  streaming: boolean;
  setStreaming: (b: boolean) => void;
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
    historyKey,
    reloadHistory,
    mock,
    streaming,
    setStreaming,
  };

  return <AppDataContext.Provider value={value}>{children}</AppDataContext.Provider>;
}

export function useAppData(): AppDataValue {
  const v = useContext(AppDataContext);
  if (!v) throw new Error("useAppData must be used within AppDataProvider");
  return v;
}
