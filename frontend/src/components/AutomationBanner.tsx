import { useState } from "react";
import { Link } from "react-router-dom";
import { useAppData } from "../context/AppDataContext";
import { isActivePhase, useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";

// Persistent app-level strip under the topbar showing what the automation is doing
// right now, with the one control that always matters: pause/resume the autopilot.
// Hidden when there is nothing to report (autopilot idle + enabled, no manual run).
export function AutomationBanner() {
  const { available, config, runs, activeCount, setConfig } = useAutopilot();
  const { streaming } = useAppData();
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  if (!available) return null;

  async function toggle() {
    setBusy(true);
    try {
      await setConfig({ ...config, enabled: !config.enabled });
      toast.success(config.enabled ? "Autopatch paused" : "Autopatch resumed");
    } catch (e) {
      toast.error(`Couldn't update autopatch: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  }

  const active = Object.values(runs)
    .filter((r) => isActivePhase(r.phase))
    .sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));

  if (active.length > 0) {
    const latest = active[0];
    return (
      <div className="automation-banner banner-working" role="status">
        <span className="banner-spin" aria-hidden>
          ⟳
        </span>
        <span className="banner-text">
          Autopilot is working on {activeCount} problem{activeCount === 1 ? "" : "s"}
          {latest?.message ? ` — ${latest.message}` : ""}
        </span>
        <span className="banner-actions">
          <Link className="ghost-btn" to="/problems">
            View
          </Link>
          <button
            className="ghost-btn"
            onClick={toggle}
            disabled={busy}
            title="Pause the autopilot — halts the active batch and queued problems, handing them to manual control"
          >
            {busy ? "Pausing…" : "⏸ Pause autopilot"}
          </button>
        </span>
      </div>
    );
  }

  if (!config.enabled) {
    return (
      <div className="automation-banner banner-paused" role="status">
        <span className="banner-text">Autopatch is paused — new problems won’t be fixed automatically.</span>
        <span className="banner-actions">
          <button className="ghost-btn" onClick={toggle} disabled={busy} title="Re-enable the autopilot — open problems are picked up immediately">
            {busy ? "Resuming…" : "▶ Resume autopatch"}
          </button>
        </span>
      </div>
    );
  }

  if (streaming) {
    return (
      <div className="automation-banner banner-working" role="status">
        <span className="banner-spin" aria-hidden>
          ⟳
        </span>
        <span className="banner-text">Manual run in progress — live steps are streaming below.</span>
      </div>
    );
  }

  return null;
}
