import type { ReactNode } from "react";

// Shimmer placeholders shown while data loads for the first time.
export function Skeleton({ count = 3, className = "skeleton-card" }: { count?: number; className?: string }) {
  return (
    <>
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className={`skeleton ${className}`} />
      ))}
    </>
  );
}

// Empty placeholder with an optional call to action.
export function EmptyState({
  title,
  message,
  action,
}: {
  title: string;
  message?: string;
  action?: ReactNode;
}) {
  return (
    <div className="state">
      <h3>{title}</h3>
      {message && <p>{message}</p>}
      {action}
    </div>
  );
}

// Error placeholder with a Retry button.
export function ErrorState({
  title = "Something went wrong",
  message,
  onRetry,
}: {
  title?: string;
  message?: string;
  onRetry?: () => void;
}) {
  return (
    <div className="state">
      <h3>{title}</h3>
      {message && <p>{message}</p>}
      {onRetry && (
        <button className="ghost-btn" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}
