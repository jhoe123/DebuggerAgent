import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react";
import type { AutopilotConfig, AutopilotRun, AutopilotSnapshot } from "../types";
import { getAutopilot, setAutopilotConfig, cancelAutopilot } from "../api";
import { usePolling } from "../hooks/usePolling";

interface AutopilotValue {
  available: boolean; // autopilot endpoint reachable
  localMode: boolean; // apply/build/deploy available (else propose-only)
  config: AutopilotConfig;
  runs: Record<string, AutopilotRun>; // keyed by problemId
  activeCount: number;
  setConfig: (cfg: AutopilotConfig) => Promise<void>;
  cancel: (problemId: string) => Promise<void>;
  refresh: () => Promise<void>;
}

const DEFAULT_CONFIG: AutopilotConfig = {
  enabled: true,
  stages: { apply: true, test: true, build: true, deploy: true },
};

const ACTIVE_PHASES = new Set(["queued", "investigating", "remediating"]);

const AutopilotContext = createContext<AutopilotValue | null>(null);

export function AutopilotProvider({ children }: { children: ReactNode }) {
  // Poll a touch faster than problems so automation status feels live.
  const poll = usePolling<AutopilotSnapshot>(() => getAutopilot(), 5000);

  const snap = poll.data;
  const runs = useMemo(() => {
    const m: Record<string, AutopilotRun> = {};
    for (const r of snap?.runs ?? []) m[r.problemId] = r;
    return m;
  }, [snap]);

  const setConfig = useCallback(
    async (cfg: AutopilotConfig) => {
      await setAutopilotConfig(cfg);
      await poll.refresh();
    },
    [poll],
  );
  const cancel = useCallback(
    async (problemId: string) => {
      await cancelAutopilot(problemId);
      await poll.refresh();
    },
    [poll],
  );

  const value: AutopilotValue = {
    available: snap !== undefined,
    localMode: snap?.localMode ?? false,
    config: snap?.config ?? DEFAULT_CONFIG,
    runs,
    activeCount: Object.values(runs).filter((r) => ACTIVE_PHASES.has(r.phase)).length,
    setConfig,
    cancel,
    refresh: poll.refresh,
  };

  return <AutopilotContext.Provider value={value}>{children}</AutopilotContext.Provider>;
}

export function useAutopilot(): AutopilotValue {
  const v = useContext(AutopilotContext);
  if (!v) throw new Error("useAutopilot must be used within AutopilotProvider");
  return v;
}

export function isActivePhase(phase: string): boolean {
  return ACTIVE_PHASES.has(phase);
}
