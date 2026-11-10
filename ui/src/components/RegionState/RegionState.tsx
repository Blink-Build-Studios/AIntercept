import type { ReactNode } from "react";
import { useDelayedFlag } from "../../lib/interaction";
import { ErrorState } from "../ErrorState/ErrorState";
import { InlineError } from "../InlineError/InlineError";
import { ProgressBar } from "../ProgressBar/ProgressBar";
import { SlowNotice } from "../SlowNotice/SlowNotice";
import "./RegionState.css";

export type RegionStatus = "loading" | "refetching" | "error" | "empty" | "ready";

export type RegionStateProps = {
  status: RegionStatus;
  /** Whether there is (possibly stale) content to keep showing. */
  hasData: boolean;
  skeleton: ReactNode;
  empty?: ReactNode;
  /** With no data, ErrorState shows `title` (default "Couldn’t load <label>") over `message`. */
  error?: { message: string; title?: string; queryId?: string; onRetry: () => void };
  /** Set once the request has run 8 s (useSlowFlag); shows "Still loading…" with Cancel. */
  slow?: { reason: string; size?: string; onCancel: () => void } | null;
  label: string;
  /** Draw the region's own panel (surface fill, hairline frame). */
  framed?: boolean;
  /** The region's footprint, held in every state (§6A.8). */
  minHeight?: number;
  /** Pins the busy indicators on (skipping the 150 ms delay), for stories. */
  "data-force"?: "busy";
  children?: ReactNode;
};

/**
 * Every query-backed region renders through this (handoff §6A.3; ESLint
 * local/query-through-regionstate enforces it):
 * - first load: the skeleton, at the content's final size;
 * - refetch: previous content dimmed with a progress bar, after 150 ms, for ≥ 400 ms;
 * - error with data: an inline error above the stale content; without data: ErrorState;
 * - empty: only after a successful query.
 */
export function RegionState({ status, hasData, skeleton, empty, error, slow, label, framed, minHeight, children, ...rest }: RegionStateProps) {
  const busy = status === "loading" || status === "refetching";
  const delayed = useDelayedFlag(busy);
  const showBusy = delayed || (busy && rest["data-force"] === "busy");
  const firstLoad = busy && !hasData;
  const errorEmpty = status === "error" && !hasData && !!error;
  const layout = errorEmpty ? "region-center region-error" : status === "empty" ? "region-center region-empty" : "";
  return (
    <section
      aria-label={label}
      aria-busy={busy}
      className={`relative ${framed ? "region-frame" : ""} ${layout}`}
      style={minHeight ? { minHeight } : undefined}
    >
      {!firstLoad && status !== "error" && status !== "empty" && <ProgressBar active={showBusy} scope="region" />}
      {slow && <SlowNotice reason={slow.reason} size={slow.size} onCancel={slow.onCancel} />}
      {status === "error" && error && hasData && (
        <InlineError message={error.message} queryId={error.queryId} onRetry={error.onRetry} />
      )}
      {firstLoad ? (
        showBusy ? skeleton : <div aria-hidden className="invisible">{skeleton}</div>
      ) : errorEmpty ? (
        <ErrorState
          compact
          title={error.title ?? `Couldn’t load ${label.toLowerCase()}`}
          body={error.message}
          meta={error.queryId}
          onRetry={error.onRetry}
        />
      ) : status === "empty" ? (
        empty
      ) : (
        <div
          // Content fades in once, as it replaces the skeleton (or a tab's other panel), and the
          // keep-and-dim lifts over --dur-base (§11G.B3); polls change neither.
          className={`content-enter transition-opacity duration-base ${showBusy && status === "refetching" ? `region-dim ${slow ? "region-dim-slow" : ""}` : ""}`}
        >
          {children}
        </div>
      )}
    </section>
  );
}
