import { useCallback, useEffect, useRef, useState } from "react";

export interface PollState<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  updatedAt: number | null;
  refresh: () => Promise<void>;
}

// usePolling runs fn immediately, then on an interval, exposing data/error/loading
// plus a manual refresh and the last-updated timestamp. Polling pauses when `paused`
// is true (e.g. while an SSE stream is active) and stops when `enabled` is false.
// Concurrent refreshes are coalesced.
export function usePolling<T>(
  fn: () => Promise<T>,
  intervalMs: number,
  opts?: { enabled?: boolean; paused?: boolean },
): PollState<T> {
  const enabled = opts?.enabled ?? true;
  const paused = opts?.paused ?? false;

  const [data, setData] = useState<T>();
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const fnRef = useRef(fn);
  fnRef.current = fn;
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    setLoading(true);
    try {
      const r = await fnRef.current();
      setData(r);
      setError(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
      inflight.current = false;
    }
  }, []);

  useEffect(() => {
    if (enabled) refresh();
  }, [enabled, refresh]);

  useEffect(() => {
    if (!enabled || paused || intervalMs <= 0) return;
    const id = setInterval(refresh, intervalMs);
    return () => clearInterval(id);
  }, [enabled, paused, intervalMs, refresh]);

  return { data, error, loading, updatedAt, refresh };
}

// agoLabel renders a compact "updated Ns ago" string from an epoch-ms timestamp.
export function agoLabel(ts: number | null): string {
  if (ts == null) return "";
  const s = Math.max(0, Math.round((Date.now() - ts) / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  return `${Math.round(s / 3600)}h ago`;
}
