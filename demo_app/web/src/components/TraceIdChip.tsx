import { useState } from "react";

// TraceIdChip shows the span's trace id and copies the full value, so an operator can
// paste it into Dynatrace to find the exact distributed trace this click produced.
export function TraceIdChip({ traceId }: { traceId: string }) {
  const [copied, setCopied] = useState(false);
  if (!traceId) return <span className="trace-none">no trace id</span>;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(traceId);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      /* clipboard unavailable (e.g. non-secure context) */
    }
  };

  return (
    <button className="trace-chip" onClick={copy} title={`Copy trace id ${traceId}`}>
      🔗 {copied ? "copied!" : `${traceId.slice(0, 8)}…`}
    </button>
  );
}
