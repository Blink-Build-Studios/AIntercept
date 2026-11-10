import { formatDuration } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { Button } from "../Button/Button";
import "./Banner.css";

export type BannerProps = {
  kind: "delayed" | "refreshFailed";
  /** delayed: when data last arrived; refreshFailed: the time of the data still shown. */
  since: string;
  retryInS?: number;
  onRetry?: () => void;
  retrying?: boolean;
  now?: Date;
  /** Spans the page: 32px sides, aligned with the page gutter. */
  page?: boolean;
};

const ago = (ms: number) => (ms < 3_600_000 ? `${Math.max(1, Math.floor(ms / 60_000))} min` : formatDuration(ms));

/** Full width under the app bar; never hides data (handoff §6.3, §5.3; CompSummary "Banner"). */
export function Banner({ kind, since, retryInS, onRetry, retrying, now = new Date(), page = false }: BannerProps) {
  const at = formatTime(since, now, true);
  const [title, body] =
    kind === "delayed"
      ? [`Last data ${ago(now.getTime() - new Date(since).getTime())} ago`, `Nothing has arrived from the proxy since ${at}. Totals below are as of then.`]
      : ["✕ Couldn’t refresh", `Showing data as of ${at}${retryInS ? `; retrying in ${retryInS} s` : ""}.`];
  return (
    <div role="status" aria-live="polite" className={`banner ${page ? "banner-page" : ""} flex items-center gap-3 border-b border-line-strong bg-surface-raised`}>
      <span className="type-data-strong text-ink">{title}</span>
      <span className="type-body text-ink-muted">{body}</span>
      {kind === "refreshFailed" && onRetry && (
        <>
          <span className="flex-1" />
          <Button size="compact" onClick={onRetry} loading={retrying} loadingLabel="Retrying…">
            Retry now
          </Button>
        </>
      )}
    </div>
  );
}
