import { formatCount } from "../../lib/format";
import { Button } from "../Button/Button";
import { Chip } from "../Chip/Chip";
import "../Button/Button.css";
import "./Pagination.css";
import { Tooltip } from "../Tooltip/Tooltip";

export type PaginationProps = {
  page: number;
  pageCount: number;
  from: number;
  to: number;
  total: number;
  /** The page being loaded; buttons are disabled with a reason until it settles (§6A.4). */
  loading?: boolean;
  /** Live rows that arrived past the end of the range: a chip ahead of the page buttons (CompControls). */
  newAfterEnd?: number;
  /** Inset to the 12px cell padding, as a table footer. */
  inset?: boolean;
  onPage: (page: number) => void;
};

function windowed(page: number, count: number): (number | "…")[] {
  const keep = new Set([1, count, page - 1, page, page + 1].filter((p) => p >= 1 && p <= count));
  const out: (number | "…")[] = [];
  [...keep].sort((a, b) => a - b).forEach((p, i, arr) => {
    if (i > 0 && p - (arr[i - 1] ?? p) > 1) out.push("…");
    out.push(p);
  });
  return out;
}

/**
 * Page through a list (handoff §6.1): the range on the left, the pages on the
 * right. `aria-current` moves to the target page immediately.
 */
export function Pagination({ page, pageCount, from, to, total, loading, newAfterEnd, inset, onPage }: PaginationProps) {
  const why = loading ? `Loading page ${page}…` : undefined;
  const range = total === 0 ? "no rows" : `${formatCount(from)}–${formatCount(to)} of ${formatCount(total)}`;
  const next = (disabled: boolean) => (
    <Button disabled={disabled} disabledReason={why ?? "Last page"} onClick={() => onPage(page + 1)}>
      Next ›
    </Button>
  );
  let controls;
  if (pageCount > 1) {
    controls = (
      <>
        {!!newAfterEnd && <Chip>{formatCount(newAfterEnd)} new ↓</Chip>}
        <Button disabled={loading || page <= 1} disabledReason={why ?? "First page"} onClick={() => onPage(page - 1)}>
          ‹ Prev
        </Button>
        {windowed(page, pageCount).map((p, i) =>
          p === "…" ? (
            <span key={`gap-${i}`} aria-hidden>
              …
            </span>
          ) : (
            // A plain .btn: Button has no aria-current, and the current page is marked by it, not by aria-pressed.
            <Tooltip key={p} kind="disabled" content={why ? { body: why } : null}>
              <button
                type="button"
                aria-label={`Page ${p}`}
                aria-current={p === page ? "page" : undefined}
                aria-disabled={loading || undefined}
                className="btn type-label rounded-sm"
                onClick={() => !loading && onPage(p)}
              >
                {p}
              </button>
            </Tooltip>
          ),
        )}
        {next(!!loading || page >= pageCount)}
      </>
    );
  }
  return (
    <nav aria-label="Pages" className={`flex items-center justify-between type-data text-ink-muted ${inset ? "px-3 py-2" : ""}`}>
      <span>
        {range}
        {pageCount > 1 && page === pageCount && !newAfterEnd && " · end of range"}
        {loading && " · loading…"}
      </span>
      {controls ? <span className="flex items-center gap-1">{controls}</span> : total > 0 && <span>all rows shown</span>}
    </nav>
  );
}
