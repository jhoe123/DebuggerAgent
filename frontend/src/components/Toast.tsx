import { useToast, type ToastKind } from "../context/ToastContext";

const icon = (k: ToastKind) => (k === "success" ? "✓" : k === "error" ? "✗" : "•");

// Fixed-position stack of dismissible toasts. Mounted once in the AppShell.
export function ToastViewport() {
  const { toasts, dismiss } = useToast();
  if (toasts.length === 0) return null;
  return (
    <div className="toast-viewport" aria-live="polite">
      {toasts.map((t) => (
        <div
          key={t.id}
          className={`toast toast-${t.kind}`}
          role="status"
          onClick={() => dismiss(t.id)}
          title="Dismiss"
        >
          <span className="toast-icon">{icon(t.kind)}</span>
          <span className="toast-msg">{t.message}</span>
        </div>
      ))}
    </div>
  );
}
