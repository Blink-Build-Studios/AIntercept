import { Button } from "../Button/Button";
import "./ErrorState.css";

export type ErrorStateProps = {
  title: string;
  body: string;
  meta?: string;
  onRetry: () => void;
  retrying?: boolean;
  /** `label` is the whole button text ("← Back to person"). */
  back?: { label: string; href: string };
  /** Inside a region or the drawer: no frame or padding; the container places it. */
  compact?: boolean;
};

/** A region (or page) that has no data to fall back on (handoff §6.7, §6A.6). */
export function ErrorState({ title, body, meta, onRetry, retrying, back, compact }: ErrorStateProps) {
  const retry = (
    <Button onClick={onRetry} loading={retrying} loadingLabel="Retrying…">
      Retry
    </Button>
  );
  if (compact) {
    return (
      <div role="alert" className="error-state-compact flex flex-col">
        <div className="error-state-compact-title text-ink">{title}</div>
        <div className="type-body text-ink-muted">{body}</div>
        {meta && <div className="type-data text-ink-faint">{meta}</div>}
        <div>{retry}</div>
      </div>
    );
  }
  return (
    <div role="alert" className="error-state flex flex-col border border-line bg-surface">
      <div className="type-title text-ink">{title}</div>
      <div className="type-body text-ink-muted">{body}</div>
      {meta && <div className="type-data text-ink-faint">{meta}</div>}
      <div className="flex gap-2">
        {retry}
        {back && <Button href={back.href}>{back.label}</Button>}
      </div>
    </div>
  );
}
