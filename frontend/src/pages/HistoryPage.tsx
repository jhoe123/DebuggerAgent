import { useState } from "react";
import { useAppData } from "../context/AppDataContext";
import { History } from "../components/History";
import { Versions } from "../components/Versions";

type Tab = "changes" | "versions";

export function HistoryPage() {
  const { historyKey } = useAppData();
  const [tab, setTab] = useState<Tab>("changes");
  return (
    <>
      <h2 className="page-title">Patch &amp; change history</h2>
      <div className="tabs">
        <button className={`tab${tab === "changes" ? " active" : ""}`} onClick={() => setTab("changes")}>
          Changes
        </button>
        <button className={`tab${tab === "versions" ? " active" : ""}`} onClick={() => setTab("versions")}>
          Deploy versions
        </button>
      </div>
      {tab === "changes" ? <History reloadKey={historyKey} /> : <Versions reloadKey={historyKey} />}
    </>
  );
}
