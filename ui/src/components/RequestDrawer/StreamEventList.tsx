import { formatOffset } from "../../lib/format";
import "./StreamEventList.css";

/** A decoded server-sent event; `count` collapses a run of the same event ("content_block_delta ×18"). */
export type StreamEvent = {
  tOffsetMs: number | null;
  type: string;
  detail: string;
  count?: number | null;
};


/** The response stream, decoded (handoff §6.6). */
export function StreamEventList({ events, className = "" }: { events: StreamEvent[]; className?: string }) {
  return (
    <ol className={`stream-list ${className}`}>
      {events.map((e, i) => (
        <li key={i} className="stream-row type-data">
          <span className="stream-t text-ink-faint">{e.tOffsetMs === null ? "" : formatOffset(e.tOffsetMs)}</span>
          <span className="stream-type text-ink">{e.count && e.count > 1 ? `${e.type} ×${e.count}` : e.type}</span>
          <span className="stream-detail text-ink-muted">{e.detail}</span>
        </li>
      ))}
    </ol>
  );
}
