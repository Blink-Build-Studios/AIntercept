import { KeyTiles, type KeyTileData } from "./KeyTile";
import "./KeyStrip.css";

/** One cell: a label, a value and a sub-line. A null value is unknown: "—", with the reason as its sub. */
export type KeyStripCellData = {
  label: string;
  value: string | null;
  sub: string;
};

/**
 * The request's key facts (handoff §6.6). Claude Code's strip: six equal cells (model,
 * tokens, cost, TTFT, duration, stop), unchanged. A provider strip (§11L.10, `tiles`):
 * KeyTiles, only the fields the provider reports (Codex: Model, Tokens, Reasoning, TTFT,
 * Duration; no Cost tile).
 */
export function KeyStrip(props: { cells: KeyStripCellData[] } | { tiles: KeyTileData[] }) {
  if ("tiles" in props) return <KeyTiles tiles={props.tiles} />;
  return (
    <dl className="key-strip border border-line bg-surface">
      {props.cells.map((c) => (
        <KeyStripCell key={c.label} {...c} />
      ))}
    </dl>
  );
}

export function KeyStripCell({ label, value, sub }: KeyStripCellData) {
  return (
    <div className="key-strip-cell">
      <dt className="type-key text-ink-faint">{label}</dt>
      <dd className={`key-strip-value ${value === null ? "text-ink-muted" : "text-ink"}`}>{value ?? "—"}</dd>
      <dd className="key-strip-sub text-ink-muted">{sub}</dd>
    </div>
  );
}
