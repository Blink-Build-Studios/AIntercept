import type { CSSProperties } from "react";
import type { StepData, ToolCallData } from "../../api/types";
import { formatBytes, formatDuration, formatTokens } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { useRowLink } from "../../lib/linkedHover";
import { durationTip, fullText, timeTip, tokensTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Marker } from "../Marker/Marker";
import { StatusCode } from "../StatusCode/StatusCode";
import { Tooltip } from "../Tooltip/Tooltip";
// The table row styles (.dt-row: hover, focus, pending, dimming), also when drawn outside a DataTable.
import "../DataTable/DataTable.css";
import "./StepRow.css";

export type Step = StepData;
export type ToolCall = ToolCallData;

/** "claude-opus-5-5" → "opus"; other names as sent. */
export const modelShort = (model: string | null) => (model ? (/opus|sonnet|haiku|fable/i.exec(model)?.[0].toLowerCase() ?? model) : null);

const sum = (vals: (number | null)[]) => (vals.some((v) => v !== null) ? vals.reduce<number>((a, v) => a + (v ?? 0), 0) : null);
const end = (s: Step) => Date.parse(s.t) + (s.durationMs ?? 0);
/** A step's own clock time (HH:mm:ss); the prompt row above carries the day. */
const clock = (iso: string) => formatTime(iso, new Date(iso), true);

/** What an agent adds to a model call (handoff §11J, Codex): reasoning tokens and effort, after the tokens. */
export type StepExtras = { reasoning?: number | null; effort?: string | null };

/** "43.0k in (40.8k cached) → 1.2k out · ttft 1.9 s · 9.3 s · tool_use": the known parts only. */
export function stepText(s: Step, extras: StepExtras = {}): string {
  const tk = s.tokens;
  const parts: string[] = [];
  if (tk.accuracy !== "missing") {
    const approx = tk.accuracy === "estimated" ? "≈ " : "";
    const input = sum([tk.input, tk.cacheRead, tk.cacheWrite]);
    const io = [
      input === null ? null : `${approx}${formatTokens(input, "call")} in${tk.cacheRead ? ` (${formatTokens(tk.cacheRead, "call")} cached)` : ""}`,
      tk.output === null ? null : `${approx}${formatTokens(tk.output, "call")} out`,
    ].filter((p) => p !== null);
    if (io.length) parts.push(io.join(" → "));
  }
  if (extras.reasoning != null) parts.push(`reasoning ${formatTokens(extras.reasoning, "call")}`);
  if (extras.effort) parts.push(`effort ${extras.effort}`);
  if (s.ttftMs !== null) parts.push(`ttft ${formatDuration(s.ttftMs)}`);
  if (s.durationMs !== null) parts.push(formatDuration(s.durationMs));
  if (s.stopReason) parts.push(s.stopReason);
  return parts.join(" · ");
}

/** "subagent (haiku) · 3 steps · 22 s · same prompt ID". */
export function subagentText(run: Step[]): string {
  const [first] = run;
  if (!first) return "subagent";
  const model = modelShort(first.model);
  const timed = run.every((s) => s.durationMs !== null);
  return [
    model ? `subagent (${model})` : "subagent",
    `${run.length} ${run.length === 1 ? "step" : "steps"}`,
    timed ? formatDuration(Math.max(...run.map(end)) - Date.parse(first.t)) : null,
    "same prompt ID",
  ]
    .filter((p) => p !== null)
    .join(" · ");
}

// Keeps the exit code apart from the command inside one text (white-space collapses ordinary runs).
const EM_SPACE = String.fromCharCode(0x2003);

const DESTRUCTIVE: Record<string, string> = { rm_recursive_force: "rm -rf", rm_recursive: "rm -r", rm_force: "rm -f" };
/** "rm -rf (outside cwd)" from `rm_recursive_force` / `outside_cwd`; unknown categories as words. */
export function destructiveLabel(d: NonNullable<ToolCall["destructive"]>): string {
  const what = DESTRUCTIVE[d.category] ?? d.category.replace(/_/g, " ");
  return d.scope && d.scope !== "unknown" ? `${what} (${d.scope.replace(/_/g, " ")})` : what;
}

type RowState = {
  /** Nesting below a prompt's own steps: a subagent's calls are 1 (+30px per level). */
  depth?: number;
  selected?: boolean;
  pending?: boolean;
  "data-force"?: "hover" | "focus";
};

const indent = (depth: number) => ({ "--step-depth": depth }) as CSSProperties;

/** A patch's change (Codex apply_patch, §11J): its kind, lines, and the lines Codex counted as accepted. */
export type PatchChange = { kind: string; added: number; removed: number; acceptedAdded: number | null; acceptedRemoved: number | null };

/** What an agent adds to a tool call (§11J, Codex's command and file-change events). */
export type ToolExtras = {
  /** The approval outcome and policy: "approved (on-request)". */
  approval?: string | null;
  patch?: PatchChange | null;
};

export type ToolCallRowProps = RowState & {
  tool: ToolCall;
  /** Opens the drawer on the model call that made this tool call. */
  onOpen?: () => void;
  /**
   * An agent's tool row (§11J, Codex): the actor column is 120px, the duration and
   * approval (or the patch's change) join the detail, and the result is just its size.
   */
  extras?: ToolExtras;
};

const lines = (a: number, r: number) => `+${a} −${r}`;

/** Codex's detail: `npm test   exit 1 · 3.2 s · approved (on-request)`, `src/a.ts · update · +38 −2 · accepted +38 −2`. */
function agentToolDetail(c: ToolCall, x: ToolExtras, target: string, exit: string): string {
  const p = x.patch;
  const parts = [
    `${target}${exit}`,
    p ? p.kind : null,
    p ? lines(p.added, p.removed) : null,
    p && p.acceptedAdded !== null && p.acceptedRemoved !== null ? `accepted ${lines(p.acceptedAdded, p.acceptedRemoved)}` : null,
    !p && c.durationMs !== null ? formatDuration(c.durationMs) : null,
    x.approval ?? null,
  ];
  return parts.filter((v) => v !== null).join(" · ");
}

/**
 * A tool call, indented under the model call that made it (handoff §6.5): name · target
 * (exit code when non-zero) · result size and duration, or `● running`; a destructive
 * command carries `▲ rm -rf (outside cwd)`.
 */
export function ToolCallRow({ tool: c, onOpen, depth = 0, selected, pending, extras, "data-force": force }: ToolCallRowProps) {
  const mcpPrefix = c.mcpServer ? `mcp__${c.mcpServer}__` : null;
  const name = mcpPrefix && c.name.startsWith(mcpPrefix) ? c.name.slice(mcpPrefix.length) : c.name;
  const target = [c.mcpServer, c.target ?? "—"].filter(Boolean).join(" · ");
  const exit = c.exitCode !== null && c.exitCode !== 0 ? `${EM_SPACE}exit ${c.exitCode}` : "";
  const shell = c.name === "Bash" || c.name === "exec" || c.name === "exec_command";
  const result = extras
    ? [c.resultBytes === null ? null : shell ? `stdout ${formatBytes(c.resultBytes)}` : formatBytes(c.resultBytes)].filter((p) => p !== null)
    : [
        c.resultBytes === null ? null : c.name === "Bash" ? `stdout ${formatBytes(c.resultBytes)}` : `${formatBytes(c.resultBytes)} result`,
        c.durationMs === null ? null : formatDuration(c.durationMs),
      ].filter((p) => p !== null);
  const detail = extras ? agentToolDetail(c, extras, target, exit) : `${target}${exit}`;
  const cells = (
    <>
      <span role="cell" />
      <Tooltip content={name === c.name ? null : fullText(c.name)}>
        <span role="cell" className="type-data-strong">
          {name}
        </span>
      </Tooltip>
      <Tooltip kind={c.target === null ? "cell" : "truncate"} content={c.target === null ? unknownTip("Target", "not extracted yet") : fullText(detail)}>
        <span role="cell" {...(c.target === null ? unknownAttrs("not extracted yet") : {})}>
          {detail}
        </span>
      </Tooltip>
      <span role="cell" className="text-ink-muted">
        {c.status === "running" ? `● running${c.durationMs === null ? "" : ` ${formatDuration(c.durationMs)}`}` : result.join(" · ")}
        {c.status === "error" && <span className="type-data-strong text-ink">{result.length ? " · ✕ error" : "✕ error"}</span>}
      </span>
      {c.destructive ? <Marker kind="destructive" role="cell" label={destructiveLabel(c.destructive)} /> : !extras && <span role="cell" />}
    </>
  );
  const className = `dt-row type-data text-ink step-row step-tool ${extras ? "step-wide" : ""}`;
  if (!onOpen) {
    return (
      <div role="row" className={className} style={indent(depth)} data-force={force}>
        {cells}
      </div>
    );
  }
  return (
    <button
      type="button"
      role="row"
      tabIndex={-1}
      data-roving
      aria-selected={selected || undefined}
      data-pending={pending || undefined}
      data-force={force}
      onClick={onOpen}
      className={className}
      style={indent(depth)}
    >
      {cells}
    </button>
  );
}

type Opening = {
  /** Opens the request drawer on a model call (by its requestId); rows become buttons with `open ›`. */
  onOpen?: (requestId: string) => void;
  /** The request open in the drawer, and the one being opened (§6A.2). */
  selectedRequestId?: string | null;
  pendingRequestId?: string | null;
};

/** A thread an agent spawned (Codex spawn_agent, §11J gap 3): its own session, linked from the step. */
export type SpawnedThread = {
  t: string;
  tool: string;
  agentName: string;
  threadShort: string;
  prompts: number | null;
  calls: number | null;
  durationMs: number | null;
  href: string;
};

export type StepRowProps = RowState &
  Opening &
  (
    | {
        step: Step;
        /**
         * An agent's model call (§11J, Codex): the actor column is 120px and shows the model
         * as sent, the detail adds reasoning tokens and effort.
         */
        extras?: StepExtras;
        /** The extras for each of the step's tool calls, by index (§11J tool rows). */
        toolExtras?: (ToolExtras | undefined)[];
      }
    | {
        /**
         * A step of a long turn (handoff §11L.7): one line, numbered, `model · in → out ·
         * what it produced` (`exec ×1`, `apply_patch src/timer.ts +12 −3`, or quoted text).
         */
        numbered: { step: Step; index: number; produced: string | null };
      }
    | { spawn: SpawnedThread }
    | {
        /** A subagent's run: its consecutive calls in the prompt, shown as one collapsed `⤷` row. */
        subagent: Step[];
        expanded: boolean;
        onToggle: () => void;
      }
  );

/**
 * One step of a prompt (handoff §6.5): a model call row followed by its indented
 * tool calls, or a subagent's run collapsed to one `⤷` row that expands to its calls.
 * Auxiliary and compaction calls are dimmed. Streaming calls pulse their `●`.
 */
export function StepRow(props: StepRowProps) {
  const { depth = 0, onOpen, selectedRequestId, pendingRequestId, "data-force": force } = props;
  // Hover and focus point the timeline at the call's instant; a bin or context bar pointing back marks the row.
  const own = "step" in props ? props.step : "numbered" in props ? props.numbered.step : "subagent" in props ? props.subagent[0] : undefined;
  const { linked, handlers } = useRowLink({ source: "table", ids: own ? [own.requestId] : [], t: own?.t }, { id: own?.requestId ?? "", t: own?.t });
  if ("numbered" in props) {
    const { step: s, index, produced } = props.numbered;
    const tk = s.tokens;
    const input = sum([tk.input, tk.cacheRead, tk.cacheWrite]);
    const io = tk.accuracy === "missing" ? null : `${input === null ? "—" : formatTokens(input, "call")} → ${tk.output === null ? "—" : formatTokens(tk.output, "call")}`;
    const rest = [io, produced].filter((p) => p !== null).map((p) => ` · ${p}`).join("");
    const cells = (
      <>
        <Tooltip content={timeTip(s.t)}>
          <span role="cell" className="text-ink-faint">
            {clock(s.t)}
          </span>
        </Tooltip>
        <span role="cell" className="text-ink-muted">{`#${index}`}</span>
        <span role="cell">
          <span className="quiet-keep text-ch-model">{s.model ?? "—"}</span>
          {rest}
        </span>
      </>
    );
    const className = "dt-row type-data text-ink step-row step-numbered";
    return onOpen ? (
      <button
        type="button"
        role="row"
        tabIndex={-1}
        data-roving
        aria-selected={selectedRequestId === s.requestId || undefined}
        data-pending={pendingRequestId === s.requestId || undefined}
        data-linked={linked || undefined}
        data-force={force}
        onClick={() => onOpen(s.requestId)}
        {...handlers}
        className={className}
        style={indent(depth)}
      >
        {cells}
      </button>
    ) : (
      <div role="row" className={className} style={indent(depth)} data-linked={linked || undefined} data-force={force} {...handlers}>
        {cells}
      </div>
    );
  }
  if ("spawn" in props) {
    const sp = props.spawn;
    const counts = [
      sp.agentName,
      `thread ${sp.threadShort}`,
      sp.prompts === null ? null : `${sp.prompts} ${sp.prompts === 1 ? "prompt" : "prompts"}`,
      sp.calls === null ? null : `${sp.calls} ${sp.calls === 1 ? "call" : "calls"}`,
      sp.durationMs === null ? null : formatDuration(sp.durationMs),
    ].filter((v) => v !== null);
    return (
      <div role="row" className="dt-row type-data text-ink step-row step-wide" style={indent(depth)} data-force={force}>
        <Tooltip content={timeTip(sp.t)}>
          <span role="cell" className="text-ink-faint">
            {clock(sp.t)}
          </span>
        </Tooltip>
        <span role="cell" className="type-data-strong">{`⤷ ${sp.tool}`}</span>
        <span role="cell">{counts.join(" · ")}</span>
        <span role="cell">
          <a href={sp.href} className="step-thread-link text-ink">
            open thread ›
          </a>
        </span>
      </div>
    );
  }
  if ("subagent" in props) {
    const { subagent: run, expanded, onToggle } = props;
    const first = run[0];
    if (!first) return null;
    return (
      <>
        <button
          type="button"
          role="row"
          tabIndex={-1}
          data-roving
          aria-expanded={expanded}
          data-linked={linked || undefined}
          data-force={force}
          onClick={onToggle}
          {...handlers}
          className="dt-row type-data text-ink step-row"
          style={indent(depth)}
        >
          <span role="cell" className="text-ink-faint">
            {clock(first.t)}
          </span>
          <span role="cell" className="type-data-strong">{`⤷ ${first.agentType ?? "subagent"}`}</span>
          <span role="cell">{subagentText(run)}</span>
          <span role="cell" className="text-ink-muted" aria-hidden>
            {expanded ? "▾" : "▸"}
          </span>
          <span role="cell" />
        </button>
        {expanded &&
          run.map((s) => (
            <StepRow
              key={s.requestId}
              step={s}
              depth={depth + 1}
              onOpen={onOpen}
              selectedRequestId={selectedRequestId}
              pendingRequestId={pendingRequestId}
            />
          ))}
      </>
    );
  }
  const s = props.step;
  const extras = props.extras;
  const streaming = s.status === "streaming";
  const selected = props.selected ?? selectedRequestId === s.requestId;
  const pending = props.pending ?? pendingRequestId === s.requestId;
  // An agent's model call shows the model as sent ("gpt-6.1-sol").
  const model = extras ? s.model : modelShort(s.model);
  const right = streaming ? "" : s.status === "error" ? null : s.eventsPending ? "cost — · events pending…" : onOpen && !streaming ? "open ›" : "";
  const timing = durationTip(s.durationMs, s.ttftMs);
  const callTip = { rows: [...(tokensTip(s.tokens).rows ?? []), ...(timing?.rows ?? [])] };
  const cells = (
    <>
      <Tooltip content={timeTip(s.t)}>
        <span role="cell" className="text-ink-faint">
          {clock(s.t)}
        </span>
      </Tooltip>
      <Tooltip content={model === null ? unknownTip("Model", "no model on this call") : s.model && s.model !== model ? fullText(s.model) : null}>
        <span role="cell" className="quiet-keep text-ch-model" {...(model === null ? unknownAttrs("no model on this call") : {})}>
          {model ?? "—"}
        </span>
      </Tooltip>
      {streaming ? (
        <span role="cell" className="step-streaming">{`● streaming${s.tokens.output === null ? "" : ` · ${formatTokens(s.tokens.output, "call")} tokens so far`}`}</span>
      ) : (
        <Tooltip content={callTip}>
          <span role="cell">{stepText(s, extras)}</span>
        </Tooltip>
      )}
      {right === null ? <StatusCode code="ERR" role="cell" /> : <span role="cell" className="text-ink-muted">{right}</span>}
      {!extras && <span role="cell" />}
    </>
  );
  const dim = s.requestClass === "auxiliary" || s.requestClass === "compaction" ? "dt-quiet" : "";
  const className = `dt-row type-data text-ink step-row ${dim} ${extras ? "step-wide" : ""}`;
  const openStep = onOpen ? () => onOpen(s.requestId) : undefined;
  return (
    <>
      {openStep ? (
        <button
          type="button"
          role="row"
          tabIndex={-1}
          data-roving
          aria-selected={selected || undefined}
          data-pending={pending || undefined}
          data-linked={linked || undefined}
          data-force={force}
          onClick={openStep}
          {...handlers}
          className={className}
          style={indent(depth)}
        >
          {cells}
        </button>
      ) : (
        <div role="row" className={className} style={indent(depth)} data-linked={linked || undefined} data-force={force} {...handlers}>
          {cells}
        </div>
      )}
      {s.tools.map((c, i) => (
        <ToolCallRow key={c.toolUseId ?? i} tool={c} depth={depth} onOpen={openStep} extras={props.toolExtras?.[i]} />
      ))}
    </>
  );
}
