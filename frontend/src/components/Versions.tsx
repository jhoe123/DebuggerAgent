import { useEffect, useState } from "react";
import type { DeployVersion, Step } from "../types";
import { getVersion, listVersions, revertVersion } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import { AgentSteps } from "./AgentSteps";
import { DiffViewer } from "./DiffViewer";

const SOURCE_CHIP: Record<DeployVersion["source"], { label: string; cls: string }> = {
  manual: { label: "manual", cls: "chip-info" },
  autopilot: { label: "autopilot", cls: "chip-ok" },
  revert: { label: "revert", cls: "chip-warn" },
};

function when(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return new Date(t).toLocaleString();
}

// Deploy versions: every successful deploy is recorded automatically (retention-
// pruned). Each card shows what shipped; "Revert & redeploy" restores that exact
// source state and ships it again, recording the revert as a new version.
export function Versions({ reloadKey }: { reloadKey: number }) {
  const { streaming, setStreaming, refreshArtifacts, reloadHistory } = useAppData();
  const { activeIds, config, refresh: refreshAutopilot } = useAutopilot();
  const toast = useToast();

  const [versions, setVersions] = useState<DeployVersion[]>([]);
  const [loading, setLoading] = useState(false);
  const [details, setDetails] = useState<Record<string, DeployVersion>>({}); // lazily-loaded diffs per id
  const [revertingId, setRevertingId] = useState<string | null>(null);
  const [steps, setSteps] = useState<Step[]>([]); // live steps of the in-flight revert

  async function refresh() {
    setLoading(true);
    try {
      setVersions(await listVersions());
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    refresh();
  }, [reloadKey]);

  // Lazily fetch a version's per-file diffs the first time its details are opened.
  async function loadDetail(id: string) {
    if (details[id]) return;
    try {
      const d = await getVersion(id);
      setDetails((prev) => ({ ...prev, [id]: d }));
    } catch (e) {
      toast.error(`Couldn't load version detail: ${String(e)}`);
    }
  }

  // seqOf renders "v3" for a revertOf id, falling back to the raw id when the
  // target has been pruned from the list.
  function seqOf(id: string): string {
    const hit = versions.find((v) => v.id === id);
    if (hit) return `v${hit.seq}`;
    const m = /^v(\d+)-/.exec(id);
    return m ? `v${m[1]}` : id;
  }

  async function runRevert(v: DeployVersion) {
    setRevertingId(v.id);
    setStreaming(true);
    setSteps([]);
    try {
      const res = await revertVersion(v.id, (s) => setSteps((prev) => [...prev, s]));
      if (res.success) toast.success(`Restored v${v.seq} and redeployed`);
      else toast.error(`Revert to v${v.seq} failed — a gate failed`);
    } catch (e) {
      toast.error(`Revert failed: ${String(e)}`);
    } finally {
      setRevertingId(null);
      setStreaming(false);
      await refresh();
      await refreshArtifacts();
      await refreshAutopilot(); // revert pauses autopatch — reflect it immediately
      reloadHistory();
    }
  }

  const autoBusy = activeIds.length > 0;
  const busy = streaming || revertingId !== null;
  const disabledReason = autoBusy
    ? "An autopilot batch is running — halt it (or pause autopatch) first"
    : revertingId
      ? "A revert is already in progress"
      : streaming
        ? "Disabled while a live run is streaming"
        : undefined;

  return (
    <section className="history">
      <div className="history-header">
        <p className="muted">
          Every successful deploy, tracked automatically. Revert restores that exact source state and
          redeploys it — recorded as a new version, so nothing is lost.
        </p>
        <span className="mini-chip">{versions.length}</span>
        <button className="ghost-btn" onClick={refresh} disabled={loading}>
          {loading ? "…" : "Refresh"}
        </button>
      </div>

      {config.enabled && versions.length > 0 && (
        <p className="muted" style={{ fontSize: "0.82rem" }}>
          Note: reverting pauses autopatch automatically (resume it from the topbar) so the restored
          state isn't immediately re-fixed.
        </p>
      )}

      <div className="history-body">
        {versions.length === 0 && (
          <p className="muted">No deploys recorded yet — versions appear after the first successful deploy.</p>
        )}
        {versions.map((v, i) => {
          const src = SOURCE_CHIP[v.source] ?? SOURCE_CHIP.manual;
          const detail = details[v.id];
          const isReverting = revertingId === v.id;
          return (
            <div key={v.id} className="history-entry">
              <div className="history-top">
                <span className="chip chip-info">v{v.seq}</span>
                <span className={`chip ${src.cls}`}>{src.label}</span>
                <span className="history-summary">
                  {v.summary}
                  {v.revertOf ? ` (restores ${seqOf(v.revertOf)})` : ""}
                </span>
                <span className="history-when">{when(v.createdAt)}</span>
                <button
                  className="ghost-btn"
                  onClick={() => runRevert(v)}
                  disabled={busy || autoBusy}
                  title={
                    disabledReason ??
                    (i === 0
                      ? "Redeploy this version (the current state) — pauses autopatch"
                      : "Restore the source exactly as of this version and redeploy — pauses autopatch")
                  }
                >
                  {isReverting ? "Reverting…" : i === 0 ? "Redeploy" : "⤺ Revert & redeploy"}
                </button>
              </div>
              <div className="history-files">
                {v.problemIds.map((p) => (
                  <span key={p} className="mini-chip">
                    {p}
                  </span>
                ))}
                {v.files.map((f) => (
                  <code key={f} className="mini-chip">
                    {f}
                  </code>
                ))}
                {v.verify && <span className="mini-chip">{v.verify}</span>}
              </div>
              <details className="alternatives" onToggle={(e) => e.currentTarget.open && loadDetail(v.id)}>
                <summary>Patches in this version</summary>
                {!detail && <p className="muted">Loading…</p>}
                {detail &&
                  (detail.patches?.length ? (
                    detail.patches.map((p) => (
                      <div key={p.file} style={{ marginBottom: "0.8rem" }}>
                        <code className="mini-chip">{p.file}</code>
                        {p.rationale && <p className="muted" style={{ margin: "0.3rem 0" }}>{p.rationale}</p>}
                        {p.unifiedDiff && <DiffViewer diff={p.unifiedDiff} />}
                      </div>
                    ))
                  ) : (
                    <p className="muted">No patch details recorded.</p>
                  ))}
              </details>
              {isReverting && steps.length > 0 && <AgentSteps steps={steps} />}
            </div>
          );
        })}
      </div>
    </section>
  );
}
