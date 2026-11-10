// From the drawer's payloads (handoff §5.2.10) to what its parts show: every
// number goes through lib/format here, so the parts take finished strings.
import type { Schemas } from "../../api/types";
import { formatBytes, formatCount, formatDuration, formatExact, formatPct, formatTokens, formatUsd } from "../../lib/format";
import { formatStamp, formatTime } from "../../lib/time";
import type { ContentRowData } from "./ContentRow";
import type { DetailRow } from "./DetailSection";
import type { DrawerTabsProps } from "./DrawerTabs";
import type { Header } from "./HeaderList";
import type { KeyStripCellData } from "./KeyStrip";
import type { KeyTileData } from "./KeyTile";
import { modelShort } from "../StepRow/StepRow";

export type Detail = Schemas["RequestDetail"];
export type Raw = Schemas["RequestRaw"];
export type Details = Schemas["RequestDetails"];

type StatusFacts = Pick<Detail, "kind" | "status" | "durationMs" | "bytesUp" | "bytesDown"> & Partial<Pick<Detail, "notice" | "retry">>;

/** The status line: "200 · 9.3 s · 8.2 KB ↑ · 3.4 KB ↓", or the kind's own words. */
export function statusLine(d: StatusFacts): string {
  if (d.kind === "notCaptured") return "▲ reported by Claude Code · not captured";
  if (d.kind === "failed") {
    const what = d.notice?.title.replace(/^✕\s*/, "");
    const after = d.durationMs === null ? "" : ` after ${formatDuration(d.durationMs)}`;
    return `✕ ERR${what ? ` · ${what}${after}` : ""}${d.retry ? " · retried" : ""}`;
  }
  return [
    d.status === null ? "—" : String(d.status),
    d.durationMs === null ? null : formatDuration(d.durationMs),
    d.bytesUp === null ? null : `${formatBytes(d.bytesUp)} ↑`,
    d.bytesDown === null ? null : `${formatBytes(d.bytesDown)} ↓`,
  ]
    .filter(Boolean)
    .join(" · ");
}

/** What the drawer can show from the clicked row before the request resolves (§6A.4). */
export type Seed = Schemas["RequestRow"];

export const seedKind = (r: Seed): Detail["kind"] => (!r.captured ? "notCaptured" : r.status === "ERR" ? "failed" : "normal");

/** The request's channel, from its path, when there is no row to say. */
export function channelOf(path: string): Seed["kind"] {
  if (/count_tokens/.test(path)) return "count";
  if (/^\/v1\/messages/.test(path) || /^\/backend-api\/codex\/responses(\?|$)/.test(path)) return "model";
  if (/event_logging|^\/backend-api\/codex\/analytics-events/.test(path)) return "analytics";
  if (/eval\/sdk|\/flags/.test(path)) return "flags";
  // Codex's other channels (§11L.6): OTel metrics, usage and account, the catalog.
  if (/\/otlp\//.test(path)) return "metrics";
  if (/^\/backend-api\/(wham|accounts)\//.test(path)) return "usage";
  if (/^\/backend-api\/(ps|plugins)\/|^\/backend-api\/codex\/models/.test(path)) return "catalog";
  return "account";
}

/** The meta line's parts: time to the ms, prompt, step, request class, id. */
export function metaParts(d: Detail): string[] {
  const step = d.stepIndex === null ? null : d.stepCount === null ? `step ${d.stepIndex}` : `step ${d.stepIndex} of ${d.stepCount}`;
  const attempt = d.retry ? `attempt ${d.retry.attempt}${d.retry.of ? ` of ${d.retry.of}` : ""}` : null;
  return [
    formatStamp(d.t),
    d.promptIndex === null ? null : `prompt #${d.promptIndex}`,
    step,
    attempt,
    d.requestClass,
    d.kind === "notCaptured" ? `requestId ${d.requestId.replace(/^reported:/, "")} (from analytics)` : d.requestId,
    d.contentRetained ? "content retained (experimentation mode)" : null,
  ].filter((x): x is string => !!x);
}

/** Why a call has no usage (§11L.10, §11M Part 4), for the unknown tiles' tooltip. A prewarm isn't unknown: its output is 0. */
export const USAGE_MISSING: Record<NonNullable<Detail["keyStrip"]["usageMissing"]>, string> = {
  parseBuffer: "Response exceeded the old 512 KB parse buffer (before Nov 9)",
  streamCut: "Stream ended early",
  interrupted: "Turn interrupted",
  failed: "Call failed",
};

/** A Codex call (its own key strip, §11L.10): by its path, since the detail names no agent. */
export const isCodexCall = (d: Pick<Detail, "path">) => /^\/backend-api\/codex\//.test(d.path);

/**
 * Codex's key strip (§11L.10): Model (sub effort) · Tokens (in → out, sub cache read) ·
 * Reasoning · TTFT (sub its source) · Duration (sub stop reason). No Cost tile. A call with no
 * usage reads `—` in Tokens and Reasoning, sub `not captured`, its reason in the tooltip.
 */
export function keyTiles(d: Detail): KeyTileData[] {
  const k = d.keyStrip;
  const t = k.tokens;
  const missing = k.usageMissing !== null || t.accuracy === "missing" || t.output === null;
  const reason = k.usageMissing ? USAGE_MISSING[k.usageMissing] : "no usage on this response";
  const ttft = k.ttftSource === "client" ? k.ttftMs : (k.proxyTtftMs ?? k.ttftMs);
  const by = k.ttftSource === "client" ? "by Codex" : "at proxy";
  return [
    { label: "Model", value: k.model, sub: k.effort ? `effort ${k.effort}` : null, reason: "no model on this call" },
    missing
      ? { label: "Tokens", value: null, sub: "not captured", reason }
      : {
          label: "Tokens",
          value: `${t.input === null ? "—" : formatTokens(t.input, "call")} → ${formatTokens(t.output ?? 0, "call")}`,
          sub: t.cacheRead ? `cache ${formatTokens(t.cacheRead, "call")}` : null,
        },
    missing
      ? { label: "Reasoning", value: null, sub: "not captured", reason }
      : { label: "Reasoning", value: k.reasoningTokens === null ? null : formatCount(k.reasoningTokens), sub: k.reasoningTokens === null ? "not reported" : "tokens", reason: "no reasoning tokens in the usage" },
    { label: "TTFT", value: ttft === null ? null : formatDuration(ttft), sub: ttft === null ? "no first byte" : by, reason: "no first byte timed" },
    { label: "Duration", value: d.durationMs === null ? null : formatDuration(d.durationMs), sub: k.stopReason ?? (k.usageMissing ? "incomplete" : null), reason: "no response end" },
  ];
}

/** The six key cells; an unknown value carries its reason as the sub-line. */
export function keyCells(d: Detail): KeyStripCellData[] {
  const k = d.keyStrip;
  const reported = d.kind === "notCaptured";
  const t = k.tokens;
  const input = (t.input ?? 0) + (t.cacheRead ?? 0) + (t.cacheWrite ?? 0);
  const noResponse = d.kind === "failed";
  return [
    {
      label: "Model",
      value: modelShort(k.model),
      sub: reported ? "from analytics" : [k.effort && `effort ${k.effort}`, k.thinking === null ? null : `thinking ${k.thinking ? "on" : "off"}`].filter(Boolean).join(" · "),
    },
    {
      label: "Tokens",
      value: noResponse || t.output === null ? null : `${formatTokens(input, "call")} → ${formatTokens(t.output, "call")}`,
      sub: noResponse ? "no response" : reported ? "from analytics" : k.cachedPct === null ? "" : `${formatPct(Math.floor(k.cachedPct))} from cache`,
    },
    {
      label: "Cost est.",
      value: k.cost.usd === null ? null : formatUsd(k.cost.usd),
      sub: noResponse ? "not billed" : reported ? "Claude Code costUSD" : k.cost.usd === null ? "no cost reported" : "API-equivalent",
    },
    {
      label: "TTFT",
      value: k.ttftMs === null ? null : formatDuration(k.ttftMs),
      sub: noResponse ? "no first byte" : reported ? "Claude Code only" : k.proxyTtftMs === null ? "not reported" : `proxy ${formatDuration(k.proxyTtftMs)}`,
    },
    {
      label: "Duration",
      value: d.durationMs === null ? null : formatDuration(d.durationMs),
      sub: noResponse ? "until reset" : reported ? "Claude Code only" : k.streamEvents === null ? "" : `${formatCount(k.streamEvents)} stream events`,
    },
    d.retry
      ? {
          label: "Outcome",
          value: "retried",
          sub: d.retry.nextAt ? `→ ${formatTime(d.retry.nextAt)}${d.retry.nextStatus === null ? "" : ` · ${d.retry.nextStatus}${d.retry.nextStatus === 200 ? " ✓" : ""}`}` : "",
        }
      : {
          label: "Stop",
          value: k.stopReason,
          sub: k.stopReason === null ? "not reported" : k.toolCalls.length ? `${k.toolCalls.length} tool call${k.toolCalls.length > 1 ? "s" : ""}: ${k.toolCalls.join(", ")}` : "",
        },
  ];
}

const SIZE: Record<NonNullable<Schemas["ContentRow"]["unit"]>, (n: number) => string> = {
  bytes: formatBytes,
  tokens: (n) => `${formatCount(n)} tok`,
  chars: (n) => `${formatCount(n)} chars`,
};

export const contentRow = (r: Schemas["ContentRow"]): ContentRowData => ({
  label: r.label,
  detail: r.detail,
  size: r.size === null || !r.unit ? "—" : SIZE[r.unit](r.size),
  tone: r.tone,
  body: r.body,
});

/**
 * A received block as a SummaryLabel row (§11L.9): `tool_use · exec_command` with its `×2`
 * apart (it never truncates), and the size and shape as the value: `640 tokens · not retained`.
 */
export function summaryLabel(r: Schemas["ContentRow"]): { name: string; count: number | null; value: string; body: string | null } {
  const m = /^(.*?)\s*×(\d+)$/.exec(r.label);
  const size = r.size === null || !r.unit ? null : SIZE[r.unit](r.size);
  return {
    name: m?.[1] ?? r.label,
    count: m ? Number(m[2]) : null,
    value: [size, r.detail].filter(Boolean).join(" · "),
    body: r.body ?? null,
  };
}

/** Which wire data the Raw tab has. */
export const rawKind = (d: Detail): NonNullable<DrawerTabsProps["raw"]> => (d.kind === "notCaptured" ? "none" : d.kind === "failed" ? "requestOnly" : "full");

/** A section value with its unit; unknown is "—" with its note, muted. */
export function detailRow(r: Schemas["DetailRow"]): DetailRow {
  if (r.v === null) return { k: r.k, v: r.note ? `— ${r.note}` : "—", tone: "muted" };
  const v =
    typeof r.v === "boolean"
      ? String(r.v)
      : typeof r.v === "string"
        ? r.v
        : r.unit === "bytes"
          ? formatBytes(r.v)
          : r.unit === "ms"
            ? formatDuration(r.v)
            : r.unit === "usd"
              ? formatUsd(r.v)
              : r.unit === "pct"
                ? formatPct(r.v)
                : r.unit === "chars"
                  ? `${formatExact(r.v)} chars`
                  : formatExact(r.v);
  return { k: r.k, v: r.note ? `${v} · ${r.note}` : v, tone: r.tone };
}

/** Headers, with a redacted credential shown by its key hash. */
export const headers = (hs: Schemas["HeaderEntry"][], keyHash: string | null): Header[] =>
  hs.map((h) => ({
    name: h.name,
    value: h.redacted ? `[redacted${keyHash ? ` · key_hash ${keyHash}` : ""}]` : (h.value ?? "—"),
    redacted: h.redacted,
  }));

/** The key that joined an analytics event to the request. */
export function joinKey(name: string): string {
  if (name === "tengu_api_success" || name === "tengu_api_error") return "requestId";
  if (name === "tengu_api_query" || name === "tengu_api_retry") return "clientRequestId";
  return "toolUseID";
}

/** A bash-quoted curl for the request, from its raw line and headers (credentials stay redacted). */
export function curl(raw: Raw, host = "api.anthropic.com"): string {
  const [method = "POST", target = "/"] = (raw.requestLine ?? "").split(" ");
  const q = (x: string) => `'${x.replace(/'/g, `'\\''`)}'`;
  const hs = raw.requestHeaders.map((h) => `-H ${q(`${h.name}: ${h.redacted ? "[redacted]" : (h.value ?? "")}`)}`);
  return ["curl", "-X", method, q(`https://${host}${target}`), ...hs, ...(raw.requestBody ? ["--data-raw", q(raw.requestBody)] : [])].join(" \\\n  ");
}
