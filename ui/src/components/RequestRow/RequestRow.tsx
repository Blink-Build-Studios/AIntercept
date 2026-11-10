import type { Schemas } from "../../api/types";
import { formatBytes, formatDuration, formatTokens } from "../../lib/format";
import { CHANNEL_TEXT, type Channel } from "../ChannelSwatch/ChannelSwatch";
import { formatTime } from "../../lib/time";
import { useRowLink } from "../../lib/linkedHover";
import { bytesTip, durationTip, fullText, statusTip, timeTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import type { Column } from "../DataTable/DataTable";
import { Marker } from "../Marker/Marker";
import { StatusCode } from "../StatusCode/StatusCode";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import { AGENT_BG, AGENT_NAME } from "../AgentChip/AgentChip";
import "./RequestRow.css";

export type Request = Schemas["RequestRow"];

// Canonical columns for every provider (handoff §11A.6, §11M 1.5): Time, #, Kind, Method,
// Detail, Status, Duration, Sent, Received. Session scope has the prompt column; person scope
// the session column and a dated time instead. A person's All agents requests add a 24px
// agent-dot column (§11M 3.3) where the prompt column would be.
const SENT_RECEIVED = "Request and response bytes on the wire.";
export const requestMinWidth = (showSession: boolean, agentDot = false) => (showSession ? 1180 : 1100) + (agentDot ? 36 : 0);
export const requestColumns = (showSession: boolean, agentDot = false): Column[] => [
  { id: "t", header: "Time", width: showSession ? 108 : 76 },
  ...(showSession ? [] : [{ id: "prompt", header: "#", width: 40 } as Column]),
  ...(agentDot ? [{ id: "agent", header: "", width: 24 } as Column] : []),
  { id: "kind", header: "Kind", width: 84 },
  ...(showSession ? [{ id: "session", header: "Session", width: 150 } as Column] : []),
  { id: "method", header: "Meth", width: 40 },
  { id: "path", header: "Path · detail", width: "flex" },
  { id: "status", header: "Status", width: 44 },
  { id: "duration", header: "Dur", width: 64, align: "right" },
  { id: "up", header: "Up", width: 70, align: "right", definition: SENT_RECEIVED },
  { id: "down", header: "Down", width: 70, align: "right", definition: SENT_RECEIVED },
];

/** A Codex turn on its WebSocket (§11M 1.5): the upgrade (101) or a message on it (`…~N`). Its Method reads `WS`. */
export const isWebSocketTurn = (r: Pick<Request, "kind" | "status" | "requestId">) => r.kind === "model" && (r.status === 101 || /~\d+$/.test(r.requestId));

export type RequestKind = Request["kind"];

// Kind text takes its channel's color (handoff §6.5; the drawer header too); Codex's
// channels (metrics, usage, catalog: §11L.6) included.
export const KIND_COLOR: Record<RequestKind | Channel, string> = CHANNEL_TEXT;

/**
 * What tells calls apart, in the detail cell (handoff §11L.6). A model call (any product):
 * `#<prompt> · step <n>` (bold) · model and effort · tokens in → out · what it produced
 * (`exec ×2`, or the first words of text in quotes) · stop reason. Other channels: the
 * event or endpoint name and one key value. The path is dropped when it is the channel's
 * usual one; it stays in the tooltip and the drawer.
 */
export type RequestDetail = {
  head: string;
  /** The head is a model call's `#1 · step 37`: bold. */
  strong?: boolean;
  /** Muted facts after the head: `gpt-6.1-sol medium`, `31.2k → 640`; `turn #1`, `203 requests`. */
  facts?: string[];
  /** What a model call produced, in ink: `exec ×2`, `“Tests pass now…”`. */
  produced?: string | null;
  /** The stop reason, muted, last. */
  stop?: string | null;
};

/**
 * A model call's detail (§11L.6). In a table with a `#` column the prompt is there, so the
 * head starts at `step <n>` (§11M 1.5); pass `prompt: null`.
 */
export function modelCallDetail(c: {
  prompt: number | null;
  step: number | null;
  model: string | null;
  effort?: string | null;
  tokensIn: number | null;
  tokensOut: number | null;
  produced?: string | null;
  stop?: string | null;
}): RequestDetail {
  const tokens = c.tokensIn === null && c.tokensOut === null ? null : `${c.tokensIn === null ? "—" : formatTokens(c.tokensIn, "call")} → ${c.tokensOut === null ? "—" : formatTokens(c.tokensOut, "call")}`;
  return {
    head: [c.prompt === null ? null : `#${c.prompt}`, c.step === null ? null : `step ${c.step}`].filter(Boolean).join(" · ") || "model call",
    strong: true,
    facts: [[c.model, c.effort].filter(Boolean).join(" ") || null, tokens].filter((f): f is string => !!f),
    produced: c.produced ?? null,
    stop: c.stop ?? null,
  };
}

/** The detail cell's content: the head, its muted facts, what it produced, the stop reason. */
function DetailText({ d }: { d: RequestDetail }) {
  const facts = d.facts?.length ? `· ${d.facts.join(" · ")}${d.produced ? " ·" : ""}` : null;
  return (
    <>
      {d.strong ? <b>{d.head}</b> : d.head}
      {facts && (
        <>
          {" "}
          <span className="text-ink-muted">{facts}</span>
        </>
      )}
      {d.produced && ` ${d.produced}`}
      {d.stop && (
        <>
          {" "}
          <span className="text-ink-muted">{`· ${d.stop}`}</span>
        </>
      )}
    </>
  );
}

export type RequestRowProps = {
  row: Request;
  /** The channel, when the product's is finer than `row.kind` (Codex usage, catalog, metrics), or the
   * shared one a person's All agents requests use (§11M 3.3: model, analytics, account, other). */
  channel?: Channel;
  /** A person's All agents requests (§11M 3.3): the agent-dot column. */
  agentDot?: boolean;
  /** The detail cell as §11L.6 lays it out; without it, `path · detail`. */
  detail?: RequestDetail;
  showSession?: boolean;
  onOpen: (row: Request) => void;
  /** Person scope: the session cell is an inner target to the session page (§6B.6). */
  sessionHref?: (sessionId: string) => string;
  selected?: boolean;
  pending?: boolean;
  /** Arrived since the viewer last looked: starts on surface-raised and fades (handoff §6A). */
  fresh?: boolean;
  /** The reference "now" for same-day times (defaults to the clock). */
  now?: string;
  /** Pins a state for stories: hover, focus, the first frame of a new row, linked from the timeline, or the session cell's hover. */
  "data-force"?: "hover" | "focus" | "new" | "linked" | "innerHover";
};

/** A cell that is itself "—" when the value is unknown; `tip` explains a known value (§6B.6). */
const Val = ({ v, label, reason, tip, className = "" }: { v: string | number | null; label: string; reason: string; tip?: TooltipContent | null; className?: string }) => (
  <Tooltip content={v === null ? unknownTip(label, reason) : (tip ?? null)}>
    <span role="cell" className={className} {...(v === null ? unknownAttrs(reason) : {})}>
      {v ?? "—"}
    </span>
  </Tooltip>
);

/**
 * One request; the whole row opens the drawer (handoff §6.5, columns §11A.6, hover §6B.6).
 * Background calls are quiet rows (§11F.1); not-captured rows are reports only. In person scope
 * the session cell is its own target. Hover and focus point the timeline at the
 * request's instant; the timeline pointing back marks the row (§6B.5).
 */
export function RequestRow({ row: r, channel, detail: shaped, showSession = false, agentDot, onOpen, sessionHref, selected, pending, fresh, now, "data-force": force }: RequestRowProps) {
  const kind = channel ?? r.kind;
  const path = r.path.split("?")[0] ?? r.path;
  const why = (captured: string) => (r.captured ? captured : "not captured");
  const { linked, handlers } = useRowLink({ source: "table", t: r.t, ids: [r.requestId] }, { id: r.requestId, t: r.t });
  const detail = r.detail ? `${path} · ${r.detail}` : path;
  const session = r.sessionId ? (r.sessionLabel ?? r.sessionId.slice(0, 8)) : null;
  return (
    <div
      role="row"
      tabIndex={-1}
      data-roving
      aria-selected={selected || undefined}
      data-pending={pending || undefined}
      data-fresh={fresh || undefined}
      data-linked={linked || undefined}
      data-force={force === "innerHover" ? "hover" : force}
      onClick={() => onOpen(r)}
      onKeyDown={(e) => {
        if (e.key === "Enter" && e.target === e.currentTarget) onOpen(r);
      }}
      {...handlers}
      className={`dt-row dt-click type-data text-ink ${r.background ? "dt-quiet" : ""}`}
    >
      <Tooltip content={timeTip(r.t)}>
        <span role="cell" className="text-ink-faint">
          {formatTime(r.t, now ? new Date(now) : undefined, true)}
        </span>
      </Tooltip>
      {!showSession && <Val v={r.promptIndex} label="Prompt" reason="no prompt id on this call" className="text-ink-muted" />}
      {agentDot && (
        <span role="cell" className="request-agent">
          {r.agent && <span role="img" aria-label={AGENT_NAME[r.agent]} className={`request-agent-dot rounded-pill ${AGENT_BG[r.agent]}`} />}
        </span>
      )}
      <span role="cell" className={`quiet-keep ${KIND_COLOR[kind]}`}>
        {kind}
      </span>
      {showSession &&
        (r.sessionId && session && sessionHref ? (
          <span role="cell" className="text-ink-muted">
            <a
              href={sessionHref(r.sessionId)}
              className="dt-inner"
              data-force={force === "innerHover" ? "hover" : undefined}
              onClick={(e) => e.stopPropagation()}
            >
              {session}
            </a>
          </span>
        ) : (
          <span role="cell" className={session ? "text-ink-muted" : "text-ink-faint"}>
            {session ?? "— no session"}
          </span>
        ))}
      <Val v={isWebSocketTurn(r) ? "WS" : r.method} label="Method" reason={why("no method")} />
      {r.captured ? (
        <Tooltip kind="truncate" content={fullText(detail)}>
          <span role="cell">{shaped ? <DetailText d={shaped} /> : detail}</span>
        </Tooltip>
      ) : (
        <Marker kind="coverageGap" role="cell" label="reported by Claude Code, not captured" />
      )}
      {r.captured ? (
        <Tooltip content={r.status === null ? unknownTip("Status", "no response") : statusTip(r.status, r.statusText, r.stopReason, r.error)}>
          <StatusCode code={r.status} role="cell" />
        </Tooltip>
      ) : (
        <Val v={null} label="Status" reason="not captured" className="text-ink-muted" />
      )}
      <Val
        v={r.durationMs !== null ? formatDuration(r.durationMs) : r.retries ? `retry ${r.retries}` : null}
        label="Duration"
        reason={why("no response")}
        tip={durationTip(r.durationMs, r.ttftMs)}
        className="dt-right text-ink-muted"
      />
      <Val
        v={r.bytesUp === null ? null : formatBytes(r.bytesUp)}
        label="Up"
        reason={why("no body")}
        tip={bytesTip(r.bytesUp, r.bytesDown, r.contentEncoding, why("no body"))}
        className="dt-right"
      />
      <Val
        v={r.bytesDown === null ? null : formatBytes(r.bytesDown)}
        label="Down"
        reason={why("no body")}
        tip={bytesTip(r.bytesUp, r.bytesDown, r.contentEncoding, why("no body"))}
        className="dt-right text-ink-muted"
      />
    </div>
  );
}
