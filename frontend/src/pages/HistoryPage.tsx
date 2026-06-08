import { useAppData } from "../context/AppDataContext";
import { History } from "../components/History";

export function HistoryPage() {
  const { historyKey } = useAppData();
  return (
    <>
      <h2 className="page-title">Patch &amp; change history</h2>
      <History reloadKey={historyKey} />
    </>
  );
}
