import { useCallback, useEffect, useMemo, useState } from "react";
import { scenarios, type Scenario } from "./scenarios";
import { fetchBuildStatus, fetchCatalog, runReset, runScenario, type BuildVerdict, type Product, type RunResult } from "./api";
import { ScenarioCard } from "./components/ScenarioCard";
import { ConsolePanel, type ConsoleEntry } from "./components/ConsolePanel";
import { ProductGrid } from "./components/ProductGrid";
import { BuildStatus } from "./components/BuildStatus";
import { Legend } from "./components/Legend";

let seq = 0;

function classOf(res: RunResult): string {
  if (!res.completed || res.status >= 500) return "err";
  if (res.status >= 400) return "warn";
  return "ok";
}

export default function App() {
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const [entries, setEntries] = useState<ConsoleEntry[]>([]);
  const [last, setLast] = useState<Record<string, RunResult>>({});
  const [build, setBuild] = useState<BuildVerdict | null>(null);
  const [buildLoading, setBuildLoading] = useState(true);

  const loadCatalog = useCallback(async () => {
    setLoading(true);
    setProducts(await fetchCatalog());
    setLoading(false);
  }, []);

  // Refresh the buggy/patched verdict. Called on mount and after each action (never on a
  // timer) so the checkout probe — the only one that emits a span — stays quiet.
  const refreshBuild = useCallback(async () => {
    setBuildLoading(true);
    setBuild(await fetchBuildStatus());
    setBuildLoading(false);
  }, []);

  useEffect(() => {
    void loadCatalog();
    void refreshBuild();
  }, [loadCatalog, refreshBuild]);

  const record = useCallback((title: string, signals: Scenario["signals"], scenarioId: string, res: RunResult) => {
    setLast((m) => ({ ...m, [scenarioId]: res }));
    setEntries((es) =>
      [
        {
          id: ++seq,
          title,
          signals,
          completed: res.completed,
          status: res.status,
          latencyMs: res.latencyMs,
          traceId: res.traceId,
          time: new Date().toLocaleTimeString(),
          statusClass: classOf(res),
        },
        ...es,
      ].slice(0, 50),
    );
  }, []);

  const onRun = useCallback(
    async (s: Scenario) => {
      const res = await runScenario(s);
      record(s.title, s.signals, s.id, res);
      if (s.id === "catalog") void loadCatalog();
      void refreshBuild();
    },
    [record, loadCatalog, refreshBuild],
  );

  const onReset = useCallback(
    async (s: Scenario) => {
      if (!s.resetEndpoint) return;
      const res = await runReset(s.resetEndpoint);
      record(`${s.title} (reset)`, s.signals, s.id, res);
    },
    [record],
  );

  const issueCount = useMemo(() => scenarios.filter((s) => s.isIssue).length, []);
  // Per-scenario fixed state, keyed by the bug ids the self-check reports (checkout/report).
  const fixedById = useMemo(() => {
    const m: Record<string, boolean> = {};
    for (const b of build?.bugs ?? []) m[b.id] = b.fixed;
    return m;
  }, [build]);

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">🛒 {build?.app ?? "ShopFlow"}</div>
        <div className="tagline">
          a demo storefront that emits real Dynatrace signals · {issueCount} one-click issues
        </div>
      </header>

      <BuildStatus verdict={build} loading={buildLoading} />

      <Legend />

      <div className="layout">
        <main>
          <section className="panel">
            <h2>Catalog</h2>
            <ProductGrid products={products} loading={loading} />
          </section>
          <section className="panel">
            <h2>Storefront actions</h2>
            <div className="cards">
              {scenarios.map((s) => (
                <ScenarioCard
                  key={s.id}
                  scenario={s}
                  onRun={onRun}
                  onReset={s.resetEndpoint ? onReset : undefined}
                  last={last[s.id]}
                  fixed={fixedById[s.id]}
                />
              ))}
            </div>
          </section>
        </main>
        <ConsolePanel entries={entries} onClear={() => setEntries([])} />
      </div>
    </div>
  );
}
