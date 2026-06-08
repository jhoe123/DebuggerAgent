import type { BuildVerdict } from "../api";
import { scenarios } from "../scenarios";

// Bug ids map to storefront scenarios — resolve the human label from that registry (the
// single source of truth) so labels aren't hardcoded/duplicated. Falls back to the id.
const SCENARIO_LABELS: Record<string, string> = Object.fromEntries(
  scenarios.map((s) => [s.id, s.title]),
);

// BuildStatus is the buggy-vs-patched banner. It reflects the running binary's actual
// behavior (via GET /api/status), so a tester can tell at a glance whether a patch is
// live — the storefront otherwise looks identical before and after a fix.
//
// Presentational only: App owns the fetch (single source of truth) so the per-scenario
// "fixed" badges and this banner can't disagree. Renders nothing when the verdict is
// unavailable (e.g. an older web/dist served by a binary without /api/status).

interface Props {
  verdict: BuildVerdict | null;
  loading: boolean;
}

export function BuildStatus({ verdict, loading }: Props) {
  if (!verdict) {
    if (loading) {
      return <div className="build-status loading">Checking build…</div>;
    }
    return null;
  }

  const total = verdict.bugs.length;
  const fixed = verdict.bugs.filter((b) => b.fixed).length;

  let cls = "partial";
  let headline = `Patched build — ${fixed} of ${total} known issues resolved`;
  let icon = "🛠";
  if (verdict.verdict === "buggy") {
    cls = "buggy";
    icon = "⚠";
    headline = `Buggy build — ${total} known issue${total === 1 ? "" : "s"} live`;
  } else if (verdict.verdict === "patched") {
    cls = "patched";
    icon = "✓";
    headline = "Patched build — all known issues resolved";
  }

  return (
    <div className={`build-status ${cls}`}>
      <span className="build-headline">
        <span className="build-icon">{icon}</span> {headline}
      </span>
      <span className="build-bugs">
        {verdict.bugs.map((b) => (
          <span key={b.id} className={`build-bug ${b.fixed ? "fixed" : "broken"}`} title={b.detail}>
            {SCENARIO_LABELS[b.id] ?? b.id} {b.fixed ? "✓" : "✗"}
          </span>
        ))}
      </span>
    </div>
  );
}
