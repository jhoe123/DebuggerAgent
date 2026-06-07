import { useEffect, useState } from "react";
import type { TestStatus } from "../types";
import { testReset, testStatus, testTrigger } from "../api";

// Clearly-labeled testing aid. Renders nothing unless the backend exposes the
// console (ENABLE_TEST_CONSOLE). Lets you trigger the bug, reset the demo source
// to its committed buggy state, and see status. NOT part of the product.
export function TestConsole({ onChange }: { onChange?: () => void }) {
  const [status, setStatus] = useState<TestStatus | null>(null);
  const [available, setAvailable] = useState(true);
  const [busy, setBusy] = useState("");
  const [open, setOpen] = useState(true);

  async function refresh() {
    try {
      setStatus(await testStatus());
    } catch {
      setAvailable(false);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  if (!available) return null;

  async function run(label: string, fn: () => Promise<TestStatus | unknown>) {
    setBusy(label);
    try {
      const s = await fn();
      if (s && typeof s === "object" && "sourceState" in (s as object)) {
        setStatus(s as TestStatus);
      } else {
        await refresh();
      }
      onChange?.();
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="test-console">
      <div className="tc-header" onClick={() => setOpen((o) => !o)}>
        <span className="tc-badge">TEST CONSOLE</span>
        <span className="tc-note">testing only — not part of the product</span>
        <span className="tc-toggle">{open ? "▾" : "▸"}</span>
      </div>
      {open && status && (
        <div className="tc-body">
          <div className="tc-status">
            <span className={`chip ${status.sourceState === "buggy" ? "chip-warn" : "chip-info"}`}>
              source: {status.sourceState}
            </span>
            <span className={`chip ${status.reachable ? "chip-ok" : "chip-warn"}`}>
              demo_app: {status.reachable ? "reachable" : "down"}
            </span>
            <span className="chip">{status.pendingPatch ? "patch pending" : "no patch"}</span>
          </div>
          <div className="tc-actions">
            <button disabled={!!busy} onClick={() => run("trigger", testTrigger)}>
              {busy === "trigger" ? "Triggering…" : "Trigger incident"}
            </button>
            <button disabled={!!busy} onClick={() => run("reset", testReset)}>
              {busy === "reset" ? "Resetting…" : "Reset demo to buggy"}
            </button>
            <button disabled={!!busy} onClick={() => run("refresh", testStatus)}>
              Refresh
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
