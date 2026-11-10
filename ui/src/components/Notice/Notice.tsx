import type { ReactNode } from "react";
import "./Notice.css";

export type NoticeProps = {
  tone: "danger" | "neutral";
  title: string;
  /** Leads the title. Default: ▲ for danger, none for neutral. */
  glyph?: string;
  children: ReactNode;
};

/** A boxed note inside the drawer (handoff §6.3); danger only for coverage gaps. */
export function Notice({ tone, title, glyph = tone === "danger" ? "▲" : undefined, children }: NoticeProps) {
  return (
    <div role="note" className={`notice border bg-bg ${tone === "danger" ? "border-danger" : "border-line-strong"}`}>
      <div className={`type-data-strong ${tone === "danger" ? "text-danger" : "text-ink"}`}>{glyph ? `${glyph} ${title}` : title}</div>
      <div className="type-body text-ink-muted">{children}</div>
    </div>
  );
}
