import { useCallback, useEffect, useMemo, useState } from "react";
import { scenarios, type Scenario } from "./scenarios";
import { fetchCatalog, runReset, runScenario, type Product, type RunResult } from "./api";
import { ScenarioCard } from "./components/ScenarioCard";
import { ConsolePanel, type ConsoleEntry } from "./components/ConsolePanel";
import { ProductGrid } from "./components/ProductGrid";
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

  const loadCatalog = useCallback(async () => {
    setLoading(true);
    setProducts(await fetchCatalog());
    setLoading(false);
  }, []);

  useEffect(() => {
    void loadCatalog();
  }, [loadCatalog]);

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
    },
    [record, loadCatalog],
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

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">🛒 ShopFlow</div>
        <div className="tagline">
          a demo storefront that emits real Dynatrace signals · {issueCount} one-click issues
        </div>
      </header>

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
