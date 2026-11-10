import "./ContentRow.css";

/** One block sent or received (handoff §4 `ContentRow`): its shape, and its body only when content is retained. */
export type ContentRowData = {
  label: string;
  detail: string;
  size: string;
  tone?: "tool" | "muted" | null;
  body?: string | null;
};

/**
 * A content block (handoff §6.6): label, detail, size. Tool calls take the
 * skills color; a retained body follows in a `bg` block, diff lines prefixed `-` / `+`.
 */
export function ContentRow({ row: r }: { row: ContentRowData }) {
  return (
    <>
      <div className="content-row type-data">
        <span className={`content-row-label ${r.tone === "tool" ? "text-ch-skills" : r.tone === "muted" ? "text-ink-muted" : "text-ink"}`}>{r.label}</span>
        <span className="content-row-detail text-ink-muted">{r.detail}</span>
        <span className="text-ink-muted">{r.size}</span>
      </div>
      {r.body && <pre className="content-row-body type-data bg-bg text-ink">{r.body}</pre>}
    </>
  );
}
