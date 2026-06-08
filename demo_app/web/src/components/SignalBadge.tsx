import { SIGNAL_LABELS, type SignalKind } from "../scenarios";

export function SignalBadge({ kind }: { kind: SignalKind }) {
  return <span className={`badge badge-${kind}`}>{SIGNAL_LABELS[kind]}</span>;
}
