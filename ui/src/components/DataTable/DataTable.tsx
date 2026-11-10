import type { CSSProperties, ReactNode } from "react";
import { useRovingFocus } from "../../lib/rovingFocus";
import { Skeleton } from "../Skeleton/Skeleton";
import { TableHeader } from "../TableHeader/TableHeader";
import "./DataTable.css";

export type Column = {
  id: string;
  header: string;
  width: number | "flex";
  align?: "left" | "right";
  /** The header's definition (§6B.6), shown in its tooltip. */
  definition?: string;
};

export type DataTableProps = {
  columns: Column[];
  minWidth: number;
  label: string;
  children?: ReactNode;
  footer?: ReactNode;
  loading?: boolean;
  skeletonRows?: number;
  empty?: ReactNode;
  /** Its rows expand (prompts, events): a treegrid, where rows may carry aria-expanded. */
  expandable?: boolean;
  /** Extra classes on the header row, so it can share its rows' column rules (the People table). */
  headClassName?: string;
};

/**
 * Up to 12 columns, as the per-position flex values every row reads (--c1, --c2, …).
 * Fixed columns may shrink: at a table's min width, if they and the flex column's
 * floor don't fit, they give up width in proportion, as on the canvas (PersonRow).
 */
export const columnVars = (columns: Column[]) =>
  Object.fromEntries(
    columns.flatMap((c, i) => [
      [`--c${i + 1}`, c.width === "flex" ? "1 1 0%" : `0 1 ${c.width}px`],
      // The width too, for rows that size their automatic minimum by it (PersonRow); flex-basis still sizes the cell.
      [`--w${i + 1}`, c.width === "flex" ? "auto" : `${c.width}px`],
    ]),
  ) as CSSProperties;

/**
 * A surface box of fixed-column rows with a micro-caps header (handoff §6.5). The rows are
 * one tab stop (§6B.8): ↑/↓ move rows, Enter clicks, → / ← walk the focused row's targets
 * and cells with tooltips, Home / End jump to the first / last row on the page.
 */
export function DataTable({ columns, minWidth, label, children, footer, loading, skeletonRows = 8, empty, expandable, headClassName = "" }: DataTableProps) {
  const style = { ...columnVars(columns), "--dt-min-width": `${minWidth}px` } as CSSProperties;
  const roving = useRovingFocus({ group: "table", orientation: "vertical" });
  const onKeyDown = (e: React.KeyboardEvent) => {
    const rows = [...(roving.ref.current?.querySelectorAll<HTMLElement>("[data-roving]") ?? [])];
    const target = e.target as HTMLElement;
    const row = target.closest<HTMLElement>("[data-roving]");
    const index = row ? rows.indexOf(row) : -1;
    if (!row || index < 0) return;
    if (target === row && e.key !== "ArrowRight") return roving.props.onKeyDown(e);
    const list = stops(row);
    const at = list.indexOf(target);
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const next = e.key === "ArrowRight" ? list[at + 1] : at > 0 ? list[at - 1] : row;
      next?.focus();
    } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      roving.focusAt(index + (e.key === "ArrowDown" ? 1 : -1));
    }
  };
  return (
    <div className="dt border border-line bg-surface" style={style}>
      {/* Key handling is delegated: the rows and cells are the focusable targets (§6B.8). */}
      {/* A table or treegrid with roving keyboard focus; the rule can't see a role chosen at runtime. */}
      {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions */}
      <div ref={roving.ref} {...roving.props} onKeyDown={onKeyDown} role={expandable ? "treegrid" : "table"} aria-label={label} aria-busy={loading || undefined} className="dt-grid">
        <div role="row" className={`dt-row type-key text-ink-faint ${headClassName}`}>
          {columns.map((c) => (
            <TableHeader key={c.id} header={c.header} align={c.align} definition={c.definition} />
          ))}
        </div>
        {loading
          ? Array.from({ length: skeletonRows }, (_, i) => (
              <div key={i} role="row" className="dt-row">
                {columns.map((c) => (
                  <span key={c.id} role="cell">
                    <Skeleton shape="line" />
                  </span>
                ))}
              </div>
            ))
          : (children ?? empty)}
      </div>
      {footer && <div className="border-t border-line px-3 py-2">{footer}</div>}
    </div>
  );
}

// A focused row's stops for → / ←: its inner targets and cells with tooltips, in order.
const stops = (row: HTMLElement) => [...row.querySelectorAll<HTMLElement>("a[href], button, [data-tip]")];
