import { useEffect, useState } from "react";
import { resetDemo } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useToast } from "../context/ToastContext";
import { saveDismissed, saveRuns } from "../lib/localStore";

// Judge-facing debug menu: a topbar button opening a panel with what to expect while
// testing (deploy duration, language support, autopatch behavior, persistence) and a
// "reset testing" action that restores the demo app's original seeded bugs by
// re-cloning the original repository. A clearly-labeled testing aid — not part of the
// product flow.
export function DebugMenu() {
  const { demoAppName, demoAppUrl } = useAppData();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  // Esc closes the panel (not while a reset is running).
  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape" && !busy) {
        setOpen(false);
        setConfirming(false);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, busy]);

  function close() {
    if (busy) return;
    setOpen(false);
    setConfirming(false);
  }

  async function runReset() {
    setBusy(true);
    try {
      const res = await resetDemo();
      if (res.error) {
        toast.error(`Reset incomplete: ${res.error}`);
        setBusy(false);
        return;
      }
      if (res.mode === "none") {
        toast.error("Runs were stopped, but no demo source is connected to restore — connect a Git source in Settings.");
        setBusy(false);
        return;
      }
      // Clear the browser-local testing state (dismissals + run history), then reload
      // so every context re-reads a clean slate.
      saveDismissed({});
      saveRuns([]);
      window.location.reload();
    } catch (e) {
      toast.error(`Reset failed: ${String(e)}`);
      setBusy(false);
    }
  }

  return (
    <>
      <button
        className="ghost-btn"
        onClick={() => setOpen(true)}
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Testing guide & reset (for judges)"
      >
        🐞 Debug
      </button>
      {open && (
        <div className="debug-overlay" onClick={close}>
          <div
            className="debug-panel"
            role="dialog"
            aria-modal="true"
            aria-label="Testing guide for judges"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="debug-head">
              <h3>Testing guide for judges</h3>
              <button className="ghost-btn" onClick={close} aria-label="Close" disabled={busy}>
                ✕
              </button>
            </div>

            <ul className="debug-notes">
              <li>
                <span className="debug-ico" aria-hidden>
                  ⏱
                </span>
                <span>
                  <strong>Deploys take 10+ minutes.</strong> A full pipeline (test → build → deploy →
                  verify) runs on Google Cloud Build, with a hard cap of 20 minutes. Watch live progress
                  on the Problems page and the status banner — no need to refresh or retry.
                </span>
              </li>
              <li>
                <span className="debug-ico" aria-hidden>
                  🧪
                </span>
                <span>
                  <strong>Limited language support.</strong> Auto-fix has been tested on a limited set of{" "}
                  <strong>Go and Python</strong> projects only; the project language is auto-detected from
                  the repository.
                </span>
              </li>
              <li>
                <span className="debug-ico" aria-hidden>
                  🤖
                </span>
                <span>
                  <strong>Autopatch.</strong> While ON (topbar pill), open problems are picked up and
                  fixed automatically within ~30 seconds. A reset pauses it so the restored bugs stay in
                  place — resume it from the topbar to watch PatchPilot fix them.
                </span>
              </li>
              <li>
                <span className="debug-ico" aria-hidden>
                  💾
                </span>
                <span>
                  <strong>Persistence.</strong> Dismissed problems and run history are stored in this
                  browser.{" "}
                  {demoAppUrl ? (
                    <>
                      The live demo app (
                      <a href={demoAppUrl} target="_blank" rel="noreferrer">
                        {demoAppName ?? "open it"}
                      </a>
                      ) is what triggers the issues — it serves whatever build was deployed last.
                    </>
                  ) : (
                    "The deployed app is what triggers the issues — it serves whatever build was deployed last."
                  )}
                </span>
              </li>
            </ul>

            <hr className="git-divider" />

            <h4 className="debug-reset-title">Reset testing</h4>
            <p className="debug-reset-hint">
              Restores the original demo source — with its seeded bugs — by re-cloning the original
              repository, then <strong>redeploys the original demo app in the background (~10 min)</strong>{" "}
              so the live app trips its bugs again. Also stops all in-flight runs, <strong>pauses
              Autopatch</strong> (resume it from the topbar when you want PatchPilot to fix the bugs),
              discards pending patches and lifecycle records, and clears this browser&apos;s dismissals
              &amp; history.
            </p>
            {confirming ? (
              <div className="git-swap-warning" role="alert">
                <strong>Reset everything?</strong>
                <p>
                  In-flight automation is halted, Autopatch is paused, and the workspace is replaced with
                  a fresh clone — this can take a minute; the page reloads when done. The original demo
                  app then redeploys in the background (~10 min).
                </p>
                <div className="pipe-actions" style={{ marginTop: 0 }}>
                  <button className="danger-btn" disabled={busy} onClick={runReset}>
                    {busy ? "Resetting…" : "Yes, reset everything"}
                  </button>
                  <button className="seg-btn" disabled={busy} onClick={() => setConfirming(false)}>
                    Cancel
                  </button>
                </div>
              </div>
            ) : (
              <button className="danger-btn" onClick={() => setConfirming(true)}>
                Reset testing (restore bugs)
              </button>
            )}
          </div>
        </div>
      )}
    </>
  );
}
