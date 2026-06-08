import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import {
  addRun,
  latestRun,
  loadDismissed,
  loadRuns,
  problemStatusMap,
  saveDismissed,
  type DismissedMap,
  type LocalRun,
  type ProblemStatus,
} from "../lib/localStore";

// Minimal payload callers pass to saveRun; id/createdAt are stamped here.
type NewRun = Omit<LocalRun, "id" | "createdAt">;

interface LocalStoreValue {
  // Dismissals (browser-local; problems re-appear from Dynatrace, so we hide them client-side).
  // A dismissal is occurrence-scoped: pass the problem's latest startedAt so a NEW
  // occurrence (re-triggered incident) resurfaces past the dismissal.
  dismissed: DismissedMap;
  isDismissed: (id: string, startedAt?: string) => boolean;
  dismiss: (ids: string[]) => void;
  restore: (ids: string[]) => void;
  clearDismissed: () => void;

  // Investigation/pipeline runs, persisted so results survive a refresh.
  runs: LocalRun[];
  saveRun: (run: NewRun) => void;
  latestInvestigation: (problemId: string) => LocalRun | undefined;
  latestPipeline: (problemId: string) => LocalRun | undefined;
  statusMap: Record<string, ProblemStatus>;
}

const LocalStoreContext = createContext<LocalStoreValue | null>(null);

export function LocalStoreProvider({ children }: { children: ReactNode }) {
  const [dismissed, setDismissed] = useState<DismissedMap>(() => loadDismissed());
  const [runs, setRuns] = useState<LocalRun[]>(() => loadRuns());

  const isDismissed = useCallback(
    (id: string, startedAt?: string) => {
      const d = dismissed[id];
      if (!d) return false;
      // Problem IDs are stable composites (e.g. "error:checkout-demo"), so a re-triggered
      // incident reuses the same ID. Treat the dismissal as stale once the problem's latest
      // occurrence is newer than when it was dismissed, so fresh incidents reappear.
      if (startedAt && new Date(startedAt).getTime() > new Date(d.dismissedAt).getTime()) {
        return false;
      }
      return true;
    },
    [dismissed],
  );

  const dismiss = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    setDismissed((prev) => {
      const next = { ...prev };
      const at = new Date().toISOString();
      for (const id of ids) next[id] = { dismissedAt: at };
      saveDismissed(next);
      return next;
    });
  }, []);

  const restore = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    setDismissed((prev) => {
      const next = { ...prev };
      for (const id of ids) delete next[id];
      saveDismissed(next);
      return next;
    });
  }, []);

  const clearDismissed = useCallback(() => {
    setDismissed(() => {
      saveDismissed({});
      return {};
    });
  }, []);

  const saveRun = useCallback((run: NewRun) => {
    const full: LocalRun = {
      ...run,
      id: `${Date.now()}-${run.problemId}`,
      createdAt: new Date().toISOString(),
    };
    setRuns((prev) => addRun(prev, full));
  }, []);

  const latestInvestigation = useCallback(
    (problemId: string) => latestRun(runs, problemId, "investigation"),
    [runs],
  );
  const latestPipeline = useCallback(
    (problemId: string) => latestRun(runs, problemId, "pipeline"),
    [runs],
  );
  const statusMap = useMemo(() => problemStatusMap(runs), [runs]);

  const value: LocalStoreValue = {
    dismissed,
    isDismissed,
    dismiss,
    restore,
    clearDismissed,
    runs,
    saveRun,
    latestInvestigation,
    latestPipeline,
    statusMap,
  };

  return <LocalStoreContext.Provider value={value}>{children}</LocalStoreContext.Provider>;
}

export function useLocalStore(): LocalStoreValue {
  const v = useContext(LocalStoreContext);
  if (!v) throw new Error("useLocalStore must be used within LocalStoreProvider");
  return v;
}
