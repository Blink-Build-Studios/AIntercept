import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Tooltip } from "../Tooltip/Tooltip";
import "./KeyTile.css";

/**
 * One tile of a provider's key strip (handoff §11L.10): a key, the value (mono 16) and a
 * sub-line. A null value is one the provider reports that we didn't get: `—` with a dotted
 * underline, the sub `not captured`, and `reason` (from the API) in its tooltip.
 */
export type KeyTileData = { label: string; value: string | null; sub: string | null; reason?: string };

export function KeyTile({ label, value, sub, reason = "not captured" }: KeyTileData) {
  return (
    <div className="key-tile">
      <div className="type-key text-ink-faint">{label}</div>
      {value === null ? (
        <Tooltip content={unknownTip(label, reason)}>
          <div className="key-tile-value key-tile-unknown text-ink-muted" {...unknownAttrs(reason)}>
            —
          </div>
        </Tooltip>
      ) : (
        <div className="key-tile-value">{value}</div>
      )}
      <div className="key-tile-sub text-ink-muted">{value === null ? (sub ?? "not captured") : sub}</div>
    </div>
  );
}

/** A provider's key strip: equal tiles in a bordered row (Codex: Model, Tokens, Reasoning, TTFT, Duration; no Cost). */
export function KeyTiles({ tiles }: { tiles: KeyTileData[] }) {
  return (
    <div className="key-tiles border border-line bg-surface">
      {tiles.map((t) => (
        <KeyTile key={t.label} {...t} />
      ))}
    </div>
  );
}
