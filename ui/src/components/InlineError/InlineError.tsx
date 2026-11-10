import { useState } from "react";
import { Button } from "../Button/Button";
import "./InlineError.css";

export type InlineErrorProps =
  | { variant?: "bar"; message: string; queryId?: string; onRetry: () => unknown; retrying?: boolean }
  /** Next to the control that failed (the control itself is the retry), §6A.2 failure. */
  | { variant: "beside"; message: string };

/** A one-line error on top of stale content (handoff §6A.6), or beside a failed trigger. Never a toast. */
export function InlineError(props: InlineErrorProps) {
  if (props.variant === "beside") {
    return (
      <span role="alert" className="inline-error-beside text-ink">
        {`✕ ${props.message}`}
      </span>
    );
  }
  return <InlineErrorBar {...props} />;
}

function InlineErrorBar({ message, queryId, onRetry, retrying }: Extract<InlineErrorProps, { onRetry: unknown }>) {
  const [busy, setBusy] = useState(false);
  const loading = retrying ?? busy;
  return (
    <div role="alert" className="inline-error bg-surface-raised text-ink">
      <span className="type-data">{`✕ ${message}${queryId ? ` · ${queryId}` : ""}`}</span>
      <span className="flex-1" />
      <Button
        className="px-2 py-0.5"
        loading={loading}
        loadingLabel="Retrying…"
        onClick={async () => {
          setBusy(true);
          try {
            await onRetry();
          } finally {
            setBusy(false);
          }
        }}
      >
        Retry
      </Button>
    </div>
  );
}
