import { useId } from "react";
import type { Schemas } from "../../api/types";
import { formatCount, formatDuration } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { useRowLink } from "../../lib/linkedHover";
import { fullText, timeTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import type { Column } from "../DataTable/DataTable";
import { InlineError } from "../InlineError/InlineError";
import { Marker } from "../Marker/Marker";
import { Skeleton } from "../Skeleton/Skeleton";
import { Tooltip } from "../Tooltip/Tooltip";
import "./EventRow.css";

export type Event = Schemas["EventRow"];
export type EventLink = Schemas["EventLink"];

// Handoff §11B.5: time 76 · # 40 · event 290 · linked to 150 · fields flex · marker (content), min width 1100.
export const EVENT_MIN_WIDTH = 1100;
export const EVENT_COLUMNS: Column[] = [
  { id: "t", header: "Time", width: 76 },
  { id: "prompt", header: "#", width: 40 },
  { id: "name", header: "Event", width: 290 },
  { id: "link", header: "Linked to", width: 150, definition: "The request or tool call this event joins to (requestId, toolUseID)." },
  { id: "fields", header: "Fields", width: "flex" },
  // The row sizes this cell to its glyph (EventRow.css); the header keeps a slot.
  { id: "marker", header: "", width: 12 },
];

const KIND_WORD: Record<EventLink["kind"], string> = {
  request: "req",
  tool: "tool",
  prompt: "prompt",
  session: "session",
  device: "device",
};

/** "tool Bash", "req …a10", "prompt #3"; just the kind when the server has no better label. */
export function linkText(l: EventLink): string {
  const word = KIND_WORD[l.kind];
  return l.label === l.kind || l.label === word || l.label === "" ? word : `${word} ${l.label}`;
}

export type EventRowProps = {
  event: Event;
  expanded?: boolean;
  /** Toggles the expansion (the parent fetches `/events/:id` when it opens). */
  onToggle: (event: Event) => void;
  /** The event's full JSON (an object, or text); undefined while it loads. */
  json?: unknown;
  /** Loading the full JSON failed (handoff §6A.4): "Couldn't load fields · Retry" inside the area. */
  jsonError?: { message?: string; onRetry: () => unknown };
  /** Follows "Linked to" (request drawer, tool, prompt, session, device); without it the cell is text. */
  onLink?: (link: EventLink, event: Event) => void;
  /** The reference "now" for same-day times (defaults to the clock). */
  now?: string;
  /** Pins a state for stories: hover, focus, linked from the timeline, or the Linked to target's hover. */
  "data-force"?: "hover" | "focus" | "linked" | "linkHover";
  /**
   * The request drawer's form (§11B.5): offset from the request's start · name ·
   * fields · the key that joined the event to the request, and whether it matched.
   * Not interactive; `onToggle` is ignored.
   */
  compact?: { offsetMs: number; joinKey: string; matched: boolean };
};

/** The JSON area under an expanded row, indented to the Event column (§11B.5). Exported for its parity specimen. */
export function EventJson({ id, json, error }: { id?: string; json?: unknown; error?: EventRowProps["jsonError"] }) {
  if (error || json === undefined) {
    return (
      <div role="cell" id={id} className="ev-json bg-bg type-data text-ink">
        {error ? (
          <InlineError message={error.message ?? "Couldn't load fields"} onRetry={error.onRetry} />
        ) : (
          <div className="ev-json-loading" role="status" aria-busy="true" aria-label="loading fields">
            <Skeleton shape="line" lines={6} />
          </div>
        )}
      </div>
    );
  }
  return (
    <pre role="cell" id={id} className="ev-json bg-bg type-data text-ink">
      {typeof json === "string" ? json : JSON.stringify(json, null, 2)}
    </pre>
  );
}

const offset = (ms: number) => `${ms < 0 ? "−" : "+"}${formatDuration(Math.abs(ms))}`;

/**
 * One analytics event in the Events tab (handoff §6.5, §5.2.9). The event name is
 * the toggle, and its hit area covers the row, so clicking anywhere toggles a `<pre>` of the full JSON under
 * it; "Linked to" is its own control. Collapsed tool-progress ticks dim to 0.55
 * with `×N`; destructive and frustrated events carry their Marker.
 */
export function EventRow({ event: e, expanded = false, onToggle, json, jsonError, onLink, now, "data-force": force, compact }: EventRowProps) {
  const jsonId = useId();
  if (compact) {
    return (
      <div role="row" className="ev-compact type-data text-ink">
        <span role="cell" className="text-ink-faint">
          {offset(compact.offsetMs)}
        </span>
        <Tooltip kind="truncate" content={fullText(e.name)}>
          <span role="cell" className="text-ch-analytics">
            {e.name}
          </span>
        </Tooltip>
        <Tooltip kind="truncate" content={e.summary ? fullText(e.summary) : null}>
          <span role="cell" className="text-ink-muted">
            {e.summary}
          </span>
        </Tooltip>
        <Tooltip content={{ body: compact.matched ? `joined by ${compact.joinKey}` : `${compact.joinKey} did not match this request` }}>
          <span role="cell" className="text-ink-faint">
            {compact.matched ? `${compact.joinKey} ✓` : `${compact.joinKey} · no match ▲`}
          </span>
        </Tooltip>
      </div>
    );
  }
  const collapsedCount = e.collapsedCount !== null && e.collapsedCount > 1 ? e.collapsedCount : null;
  const link = linkText(e.linkedTo);
  return <FullEventRow {...{ e, expanded, onToggle, json, jsonError, onLink, now, force, jsonId, collapsedCount, link }} />;
}

type FullProps = Pick<EventRowProps, "expanded" | "onToggle" | "json" | "jsonError" | "onLink" | "now"> & {
  e: Event;
  force: EventRowProps["data-force"];
  jsonId: string;
  collapsedCount: number | null;
  link: string;
};

// The table form, split out so its hooks don't run for the compact form.
function FullEventRow({ e, expanded = false, onToggle, json, jsonError, onLink, now, force, jsonId, collapsedCount, link }: FullProps) {
  const { linked, handlers } = useRowLink({ source: "table", ids: [e.eventId], t: e.t }, { id: e.eventId, t: e.t });
  const linkTip = { rows: [{ key: e.linkedTo.kind, value: e.linkedTo.id }] };
  return (
    <>
      <div
        role="row"
        tabIndex={-1}
        data-roving
        aria-selected={expanded || undefined}
        data-linked={linked || undefined}
        data-force={force === "linkHover" ? "hover" : force}
        // The name is the toggle; Enter on the focused row does the same (§6B.8).
        onKeyDown={(ev) => {
          if (ev.key === "Enter" && ev.target === ev.currentTarget) onToggle(e);
        }}
        {...handlers}
        className={`dt-row type-data ev-row text-ink ${collapsedCount !== null ? "dt-quiet" : ""}`}
      >
        <Tooltip content={timeTip(e.t)}>
          <span role="cell" className="text-ink-faint">
            {formatTime(e.t, now ? new Date(now) : undefined, true)}
          </span>
        </Tooltip>
        <Tooltip content={e.promptIndex === null ? unknownTip("Prompt", "no prompt id on this event") : null}>
          <span role="cell" className="text-ink-muted" {...(e.promptIndex === null ? unknownAttrs("no prompt id on this event") : {})}>
            {e.promptIndex ?? "—"}
          </span>
        </Tooltip>
        <span role="cell">
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={expanded ? jsonId : undefined}
            onClick={() => onToggle(e)}
            className="ev-control ev-toggle text-ch-analytics"
          >
            {e.name}
            {collapsedCount !== null ? ` ×${formatCount(collapsedCount)}` : ""}
          </button>
        </span>
        <span role="cell">
          {onLink ? (
            <Tooltip content={linkTip}>
              <button
                type="button"
                onClick={() => onLink(e.linkedTo, e)}
                data-force={force === "linkHover" ? "hover" : undefined}
                className="ev-control ev-link dt-inner"
              >
                {link}
              </button>
            </Tooltip>
          ) : (
            <Tooltip content={linkTip}>
              <span className="ev-control text-ink-muted">{link}</span>
            </Tooltip>
          )}
        </span>
        <Tooltip kind="truncate" content={e.summary ? fullText(e.summary) : null}>
          <span role="cell">{e.summary}</span>
        </Tooltip>
        {e.marker ? <Marker kind={e.marker} compact role="cell" /> : <span role="cell" />}
      </div>
      {expanded && (
        <div role="row">
          <EventJson id={jsonId} json={json} error={jsonError} />
        </div>
      )}
    </>
  );
}
