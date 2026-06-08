import { useAppData } from "../context/AppDataContext";
import { InstrumentationPanel } from "../components/Instrumentation";

// Auto-Instrument page. The panel carries its own header/description.
export function InstrumentPage() {
  const { consoleAvailable, reloadHistory } = useAppData();
  return <InstrumentationPanel available={consoleAvailable} onComplete={reloadHistory} />;
}
