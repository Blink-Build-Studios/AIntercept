import type { CSSProperties } from "react";
import { Tooltip } from "../Tooltip/Tooltip";
import "./HeaderList.css";

/** A header; credentials are redacted server-side and arrive with `redacted` ("[redacted · key_hash 9e2…]"). */
export type Header = { name: string; value: string; redacted?: boolean };

/** The name column's cap (§11H.3): 400px, less the 4px the column adds to the longest name. */
const CAP_CH = Math.floor((400 - 4) / 7.5);

/** Over the cap, a middle ellipsis: the start and the end of the name. */
export function middleEllipsis(name: string, max = CAP_CH): string {
  if (name.length <= max) return name;
  const head = Math.ceil((max - 1) / 2);
  return `${name.slice(0, head)}…${name.slice(name.length - (max - 1 - head))}`;
}

/**
 * HTTP headers in wire order (handoff §6.6, §11I.1): a grid per row whose name column
 * is the longest name, capped at 400px; names never wrap (over the cap, a middle
 * ellipsis with the full name in a tooltip); values wrap anywhere. Names are --ink and
 * values --ink-muted, redacted values --ink-faint; no prefix is dimmed (§11L.8 withdrew
 * §11H.3's rule). A --line rule under each row, --surface-raised on hover.
 */
export function HeaderList({ headers, className = "" }: { headers: Header[]; className?: string }) {
  const longest = Math.min(CAP_CH, Math.max(0, ...headers.map((h) => h.name.length)));
  const style = { "--header-name-col": `calc(${longest}ch + 4px)` } as CSSProperties;
  return (
    <dl className={`header-list ${className}`} style={style}>
      {headers.map((h, i) => {
        const shown = middleEllipsis(h.name);
        return (
          <div key={`${h.name}-${i}`} className="header-row">
            <dt className="header-name">
              {shown === h.name ? (
                h.name
              ) : (
                <Tooltip content={{ body: h.name }} kind="truncate">
                  <span>{shown}</span>
                </Tooltip>
              )}
            </dt>
            <dd className={`header-value ${h.redacted ? "text-ink-faint" : "text-ink-muted"}`}>{h.value}</dd>
          </div>
        );
      })}
    </dl>
  );
}
