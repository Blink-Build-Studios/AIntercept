// Story data: CompProviders.dc.html's specimen content (handoff §11L, round 14), shared by the
// parity sheet and each component's stories. Names and ids are the sheet's own (fictional).
import type { PromptItem } from "../../api/types";
import type { AgentCardProps } from "../AgentCard/AgentCard";
import type { AgentRowProps } from "../PersonHeader/AgentRow";
import { orgStat } from "../PersonHeader/OrgStat";
import { lastSeenValue, missedValue, type PersonStatData } from "../PersonHeader/PersonHeaderRow";
import type { Person } from "../PersonRow/PersonRow";
import type { RequestChannel } from "../RequestFilters/RequestFilters";
import type { Header } from "../RequestDrawer/HeaderList";
import type { KeyTileData } from "../RequestDrawer/KeyTile";
import type { RequestDetail, Request } from "../RequestRow/RequestRow";
import type { Session, SessionNesting } from "../SessionRow/SessionRow";
import type { SessionTab } from "../SessionTabs/SessionTabs";
import type { Step } from "../StepRow/StepRow";
import type { StepRunProps } from "../StepRow/StepRun";
import type { StepKind, StepStripProps } from "../StepStrip/StepStrip";

const at = (d: number, h: number, m: number, s = 0) => new Date(2026, 10, d, h, m, s).toISOString();
const days = (counts: number[]) => counts.map((count, i) => ({ day: `2026-10-${String(27 + i).padStart(2, "0")}`, count }));

// --- People (§11L.1) --------------------------------------------------------------------------

export const PEOPLE_NOW = at(10, 10, 0);

/** The four agent cards, Codex selected; `people`, `summary` and `sub` as the sheet has them. */
export const agentCards: (Omit<AgentCardProps, "onClick" | "selected"> & { spec: string; selected: boolean })[] = [
  { spec: "AgentCard/claudeCodeExtras", agent: "claude-code", people: 3, summary: "18 sessions · ▲ 94 calls missed", sub: "bypass on 62% of model calls", selected: false },
  { spec: "AgentCard/codexSelected", agent: "codex", people: 2, summary: "29 sessions · 146 turns", sub: "limits: highest week 83%", selected: true },
  { spec: "AgentCard/chatgpt", agent: "chatgpt", people: 2, summary: "6 conversations", sub: "no token counts", selected: false },
  { spec: "AgentCard/claudeAi", agent: "claude-ai", people: 1, summary: "4 conversations", sub: "3 uploads", selected: false },
];

const base: Person = {
  agentLimits: [],
  personId: "p",
  name: null,
  email: null,
  accountIdShort: "7d2e…a41c",
  accounts: 1,
  orgIdShort: null,
  orgKind: "unknown",
  devices: 1,
  agents: ["claude-code"],
  agentInfo: [],
  linkedBy: null,
  lastSeenAt: PEOPLE_NOW,
  live: false,
  sessions: 1,
  prompts: 1,
  tokensOut: 0,
  tokensAccuracy: "exact",
  activity: [],
  bypassPct: null,
  coverage: null,
};
// Activity on a shared scale of 19 (sparkline bars 4, 8 and 18px for 1, 5 and 15 calls).
const MIXED = days([0, 1, 0, 1, 15, 15, 1, 5, 1, 1, 15, 1, 5, 15]);
const SAM = days([1, 5, 5, 0, 1, 1, 15, 1, 1, 15, 15, 15, 0, 0]);
export const peopleRows = {
  /** A linked person in All agents. */
  mixed: {
    ...base,
    personId: "chris",
    name: "Chris Ortiz",
    email: "chris@acme.dev",
    linkedBy: "email",
    accounts: 2,
    devices: 3,
    agents: ["claude-code", "codex"],
    live: true,
    sessions: 17,
    prompts: 192,
    tokensOut: 1_400_000,
    activity: MIXED,
  } satisfies Person,
  codex: {
    ...base,
    personId: "sam",
    name: "Sam Rivera",
    email: "sam@acme.dev",
    accountIdShort: "9c2e…41",
    agents: ["codex"],
    lastSeenAt: at(9, 11, 40),
    sessions: 4,
    prompts: 6,
    tokensOut: 34_000,
    activity: SAM,
  } satisfies Person,
  chat: {
    ...base,
    personId: "dana",
    name: "Dana Ruiz",
    accountIdShort: "",
    agents: ["chatgpt"],
    lastSeenAt: at(9, 9, 12),
    sessions: 4,
    prompts: 19,
    tokensOut: null,
    tokensAccuracy: "missing",
    activity: days([0, 1, 15, 0, 0, 1, 0, 0, 1, 0, 1, 0, 5, 0]),
  } satisfies Person,
};
/** The linked person, as each agent view counts them. */
export const peopleViewRows = {
  codexLinked: { ...peopleRows.mixed, agents: ["codex"], sessions: 6, prompts: 64, tokensOut: 210_000 } satisfies Person,
  claudeCodeLinked: {
    ...peopleRows.mixed,
    agents: ["claude-code"],
    sessions: 11,
    prompts: 128,
    tokensOut: 1_200_000,
    bypassPct: 99.7,
    coverage: { reported: 1600, captured: 1506, notCaptured: 94, windows: [] },
  } satisfies Person,
};

// --- Person header (§11L.3, §11L.4) ------------------------------------------------------------

const stat = (label: string, value: PersonStatData["value"], sub?: string): PersonStatData => ({ label, value, sub });

export const personHeaders = {
  codexOnly: {
    agents: ["codex" as const],
    name: "Sam Rivera",
    subtitle: "sam@acme.dev · 1 account",
    common: [stat("Sessions", "4"), stat("Prompts", "6"), stat("Tokens", "1.9M → 34k"), stat("Devices", "1 · linux · tmux"), stat("Last seen", lastSeenValue("Nov 9 11:40", false))],
    agentRows: [
      {
        agent: "codex",
        account: "acct 9c2e…41",
        stats: [stat("Plan", "ChatGPT Plus"), stat("5h window", "12%", "resets 16:40"), stat("Week", "31%", "resets Nov 12"), stat("Credits", "50,652", "balance")],
      },
    ] satisfies AgentRowProps[],
  },
  mixed: {
    agents: ["claude-code" as const, "codex" as const],
    linkedBy: "email",
    name: "Chris Ortiz",
    subtitle: "chris@acme.dev · 2 accounts",
    common: [stat("Sessions", "17"), stat("Prompts", "192"), stat("Tokens", "9.8M → 1.4M"), stat("Devices", "3 · darwin, linux"), stat("Last seen", lastSeenValue("now", true))],
    agentRows: [
      {
        agent: "claude-code",
        account: "acct 7d2e…a41c",
        stats: [
          stat("Plan", "Max 20x"),
          orgStat({ kind: "customer", id: "1b9f…e02" }),
          stat("Role", "admin"),
          stat("Using since", "Aug 2025", "15 mo"),
          stat("7-day limit", "83%", "overage off"),
          stat("Flags", "1,108", "12 changed today"),
          stat("Bypass", "99.7%", "of model calls"),
          stat("Missed", missedValue(94), "calls not captured"),
        ],
      },
      {
        agent: "codex",
        account: "acct 9c2e…41",
        stats: [stat("Plan", "ChatGPT Plus"), stat("5h window", "41%", "resets 22:40"), stat("Week", "83%", "resets Nov 12"), stat("Credits", "unlimited")],
      },
    ] satisfies AgentRowProps[],
  },
};

// --- Session (§11L.5–7) ---------------------------------------------------------------------------

export const sessionTabs: SessionTab[] = [
  { id: "prompts", label: "Prompts", count: "1 · 203 steps" },
  { id: "requests", label: "Requests", count: "203 calls · 412 other" },
  { id: "events", label: "Events", count: "58" },
  { id: "raw", label: "Raw" },
];

export const CODEX_REQUEST_CHANNELS: RequestChannel[] = [
  { channel: "model", label: "model", count: 203 },
  { channel: "analytics", label: "analytics", count: 58 },
  { channel: "metrics", label: "metrics", count: 31 },
  { channel: "usage", label: "usage & account", count: 12 },
  { channel: "catalog", label: "catalog", count: 311 },
];

const call: Request = {
  model: null,
  effort: null,
  stepIndex: null,
  toolCalls: [],
  usageMissing: null,
  text: null,
  requestId: "resp_37",
  t: at(9, 11, 2, 14),
  kind: "model",
  agent: "codex",
  client: "tui",
  hiddenKind: null,
  sessionId: "s",
  sessionLabel: "pomodoro",
  promptIndex: 1,
  method: "POST",
  path: "/backend-api/codex/responses",
  detail: "",
  status: 200,
  durationMs: 4100,
  retries: null,
  bytesUp: 6200,
  bytesDown: 6200,
  captured: true,
  background: false,
  statusText: "completed",
  stopReason: "tool_call",
  ttftMs: 2600,
  tokens: { input: 31_200, cacheRead: null, cacheWrite: null, cacheWrite5m: null, cacheWrite1h: null, output: 640, accuracy: "exact" },
  contentEncoding: null,
  error: null,
};
/** CompProviders' Codex request rows: the row, its channel, and its §11L.6 detail. The `#` column holds
 * the prompt, so a model call's detail starts at `step <n>` (§11M 1.5); WebSocket turns read `WS`. */
type RequestSpecimen = { spec: string; row: Request; channel?: RequestChannel["channel"]; detail: RequestDetail };
const modelCall: RequestSpecimen = {
  spec: "RequestRow/codexModelCall",
  row: { ...call, requestId: "ws-01a1~37", bytesUp: 6200, bytesDown: 1100 },
  detail: { head: "step 37", strong: true, facts: ["gpt-6.1-sol medium", "31.2k → 640"], produced: "exec ×2", stop: "tool_call" },
};
const textCall: RequestSpecimen = {
  spec: "RequestRow/codexModelCallText",
  row: { ...call, requestId: "ws-01a1~38", t: at(9, 11, 2, 31), durationMs: 9800, bytesUp: 6400, bytesDown: 11_000, stopReason: "end_turn" },
  detail: { head: "step 38", strong: true, facts: ["gpt-6.1-sol medium", "31.9k → 1.2k"], produced: "“Tests pass now. The flake came from…”", stop: "end_turn" },
};
export const codexRequests: RequestSpecimen[] = [
  modelCall,
  textCall,
  {
    spec: "RequestRow/codexAnalytics",
    row: { ...call, requestId: "ev_1", kind: "analytics", t: at(9, 11, 2, 31), promptIndex: null, path: "/codex/analytics-events/events", status: 204, durationMs: 88, bytesUp: 1400, bytesDown: 0 },
    channel: "analytics",
    detail: { head: "codex_turn_event", facts: ["turn #1", "203 requests", "2 retries"] },
  },
  {
    spec: "RequestRow/codexUsage",
    row: { ...call, requestId: "u_1", kind: "account", t: at(9, 11, 2, 32), method: "GET", path: "/backend-api/wham/usage", promptIndex: null, durationMs: 140, bytesUp: 300, bytesDown: 600 },
    channel: "usage",
    detail: { head: "wham/usage", facts: ["5h 12%", "week 31%"] },
  },
  {
    spec: "RequestRow/codexPrewarmShown",
    row: { ...call, requestId: "ws-01a1~1", t: at(9, 11, 2, 40), hiddenKind: "prewarm", durationMs: 1100, bytesUp: 2000, bytesDown: 200, background: true },
    detail: { head: "prewarm", facts: ["gpt-6.1-sol", "no output"] },
  },
];

/** The same calls without recorded bodies (§11M 3.1): tool counts stay; a text-only call reads `text`. */
export const codexRequestsNoBodies: typeof codexRequests = [
  { ...modelCall, spec: "RequestRow/codexModelCallNoBodies" },
  { ...textCall, spec: "RequestRow/codexModelCallTextNoBodies", detail: { ...textCall.detail, produced: "text" } },
];

/** A linked person's All requests in All agents (§11M 3.3): the shared channel, the row's agent dot, Detail per product. */
export const personRequests: RequestSpecimen[] = [
  {
    spec: "RequestRow/personClaudeCodeModel",
    row: { ...call, agent: "claude-code", client: "cli", requestId: "req_6", t: at(9, 11, 1, 58), promptIndex: 34, sessionLabel: "partner-readiness", path: "/v1/messages", durationMs: 6200, bytesUp: 412_000, bytesDown: 3100, stopReason: "tool_use" },
    detail: { head: "#34 · step 6", strong: true, facts: ["claude-sonnet", "88k → 1.4k"], produced: "Bash ×1", stop: "tool_use" },
  },
  { spec: "RequestRow/personCodexModel", row: { ...modelCall.row, sessionLabel: "pomodoro · Nov 9 09:00" }, detail: { ...modelCall.detail, head: "#1 · step 37" } },
  {
    spec: "RequestRow/personCodexMetrics",
    row: { ...call, kind: "metrics", requestId: "otel_1", t: at(9, 11, 2, 20), promptIndex: null, sessionId: null, sessionLabel: null, path: "/otlp/v1/metrics", durationMs: 60, bytesUp: 2200, bytesDown: 0 },
    channel: "other",
    detail: { head: "otel metrics", facts: ["14 points"] },
  },
  {
    spec: "RequestRow/personClaudeCodeAnalytics",
    row: { ...call, agent: "claude-code", client: "cli", kind: "analytics", requestId: "ev_cc", t: at(9, 11, 2, 21), promptIndex: null, sessionLabel: "partner-readiness", path: "/api/event_logging/v2/batch", durationMs: 70, bytesUp: 3400, bytesDown: 100, background: true },
    detail: { head: "/api/event_logging/v2/batch", facts: ["22 events"] },
  },
];

/** The person's All requests chips: All agents' shared channels, and Codex's own (§11M 3.3). */
export const PERSON_SHARED_CHANNELS: RequestChannel[] = [
  { channel: "model", label: "model", count: 843 },
  { channel: "analytics", label: "analytics", count: 2960 },
  { channel: "account", label: "account & usage", count: 401 },
  { channel: "other", label: "other", count: 1208 },
];
export const PERSON_CODEX_CHANNELS: RequestChannel[] = [
  { channel: "model", label: "model", count: 203 },
  { channel: "analytics", label: "analytics", count: 58 },
  { channel: "metrics", label: "metrics", count: 31 },
  { channel: "usage", label: "usage & account", count: 12 },
  { channel: "catalog", label: "catalog", count: 311 },
];

// A 203-step turn (§11L.7): the strip's kinds and heights as the sheet draws them.
const KIND: Record<string, StepKind> = { e: "exec", p: "patch", m: "mcp", t: "text" };
const STRIP_KINDS =
  "eeeeeeeeeeeeemeeeteeteteeeemeeeeeeeeeeeeeepeeeetpeteepeeeepeteeeeemteeepetmeeeeeeeepeeeteeetmteeetteeeeeeeeeeeteeeeetptpeeeeeeeeeeeeeeeteeeeeemteeeeeemeeteeeeetteeeepepeemttmmpeeeeeeeetettteeeeeetmeepeet";
export const LONG_TURN_STRIP: Omit<StepStripProps, "onJump"> = {
  // Output on a scale of 22 (the strip's height): 6 and 13 px bars, alternating.
  steps: [...STRIP_KINDS].map((k, i) => ({ kind: KIND[k] ?? "exec", output: i % 2 ? 13 : 6 })),
  max: 22,
  span: "2h 4m",
  totals: { exec: 141, patch: 18, mcp: 9, text: 35 },
};

export const longTurnPrompt: PromptItem = {
  type: "prompt",
  steps: [],
  prompt: {
    promptId: "turn-1",
    index: 1,
    t: at(9, 9, 0),
    durationMs: 124 * 60_000,
    text: { retained: true, text: "the timer test is flaky on CI…", chars: 412, pastedBytes: null, images: 0 },
    steps: 203,
    tokensIn: 9_400_000,
    tokensOut: 61_000,
    toolCounts: { exec: 141, apply_patch: 18, mcp: 9 },
    friction: { frustrated: false, interrupted: true, destructive: 0 },
    inFlight: false,
    turnReport: null,
    hidden: {},
  },
};
/** The same turn without recorded bodies (§11M 3.1): the prompt is its length. */
export const longTurnPromptNoBodies: PromptItem = {
  ...longTurnPrompt,
  prompt: { ...longTurnPrompt.prompt, text: { retained: false, text: null, chars: 412, pastedBytes: null, images: 0 } },
};

const turnStep = (n: number, t: string, input: number, output: number): Step => ({
  does: "exec",
  usageMissing: null,
  commands: [],
  patches: [],
  text: null,
  exitCodes: [],
  kind: "model",
  requestId: `step-${n}`,
  t,
  model: "gpt-6.1-sol",
  agentType: null,
  requestClass: "main",
  status: "done",
  stopReason: "tool_call",
  durationMs: null,
  ttftMs: null,
  costUsd: null,
  eventsPending: false,
  tokens: { accuracy: "exact", input, output, cacheRead: null, cacheWrite: null, cacheWrite1h: null, cacheWrite5m: null },
  tools: [],
});
export const longTurnSteps = {
  first: { step: turnStep(1, at(9, 9, 0, 4), 18_000, 410), index: 1, produced: "exec ×1" },
  second: { step: turnStep(2, at(9, 9, 0, 9), 18_600, 230), index: 2, produced: "exec ×1" },
  patch: { step: turnStep(42, at(9, 9, 18, 35), 40_200, 1_800), index: 42, produced: "apply_patch src/timer.ts +12 −3" },
};
/** Without recorded bodies (§11M 3.1): step kind and tool counts stay; patch targets and text go. */
export const longTurnStepsNoBodies = {
  first: longTurnSteps.first,
  patch: { ...longTurnSteps.patch, produced: "apply_patch ×1" },
  text: { step: { ...turnStep(203, at(9, 11, 4, 2), 61_000, 1_200), does: "text" as const, stopReason: "end_turn" }, index: 203, produced: "text · end_turn" },
};
export const longTurnRuns: Record<"collapsed" | "collapsedFailing", Omit<StepRunProps, "onToggle">> = {
  collapsed: { t: at(9, 9, 0, 15), first: 3, last: 41, parts: ["exec ×44 · read-only (rg, cat, ls) · 18m 20s · 1.4M → 9.1k"], expanded: false },
  collapsedFailing: { t: at(9, 9, 18, 51), first: 43, last: 120, parts: ["exec ×81 (npm test ×37, ", { failed: "exit 1 ×31" }, ") · apply_patch ×9 · 52m 2s"], expanded: false },
};
/** Codex runs without recorded bodies: range, count, tool counts, span and tokens; no commands, so no failing variant. */
export const longTurnRunsNoBodies: Record<"collapsed" | "run", Omit<StepRunProps, "onToggle">> = {
  collapsed: { ...longTurnRuns.collapsed, parts: ["exec ×44 · 18m 20s · 1.4M → 9.1k"] },
  run: { ...longTurnRuns.collapsedFailing, parts: ["exec ×81 · apply_patch ×9 · 52m 2s · 3.1M → 22k"] },
};
/** A Claude Code run without recorded bodies (§11N.1): its analytics still report each command's program and exit code. */
export const claudeCodeRunNoBodies: Omit<StepRunProps, "onToggle"> = {
  t: at(9, 10, 12, 40),
  first: 4,
  last: 19,
  parts: ["Bash ×21 (npm ×14, rg ×5, git ×2, ", { failed: "exit ≠ 0 ×6" }, ") · 7m 48s · 640k → 4.2k"],
  expanded: false,
};

// --- Drawer (§11L.8–10) -------------------------------------------------------------------------

export const codexResponseHeaders: Header[] = [
  { name: "content-encoding", value: "br" },
  { name: "content-length", value: "6214" },
  { name: "content-type", value: "application/json" },
  { name: "x-codex-plan-type", value: "plus" },
  { name: "x-codex-primary-used-percent", value: "12" },
  { name: "x-codex-secondary-used-percent", value: "31" },
];

export const summaryLabels = {
  fits: { name: "text", count: null, value: "640 tokens · not retained" },
  truncated: { name: "tool_use · exec_command", count: 2, value: "not retained" },
  long: { name: "tool_use · mcp__filesystem__list_allowed_directories", count: 1, value: "not retained" },
};

export const codexKeyTiles: Record<"call" | "noUsage", (KeyTileData & { spec: string })[]> = {
  call: [
    { spec: "KeyTile/codexModel", label: "Model", value: "gpt-6.1-sol", sub: "effort medium" },
    { spec: "KeyTile/codexTokens", label: "Tokens", value: "31.2k → 640", sub: "cache 28.4k" },
    { spec: "KeyTile/codexReasoning", label: "Reasoning", value: "212", sub: "tokens" },
    { spec: "KeyTile/codexTtft", label: "TTFT", value: "2.6 s", sub: "at proxy" },
    { spec: "KeyTile/codexDuration", label: "Duration", value: "4.1 s", sub: "tool_call" },
  ],
  noUsage: [
    { spec: "KeyTile/codexModelHigh", label: "Model", value: "gpt-6.1-sol", sub: "effort high" },
    { spec: "KeyTile/codexTokensUnknown", label: "Tokens", value: null, sub: "not captured", reason: "Stream ended early" },
    { spec: "KeyTile/codexReasoningUnknown", label: "Reasoning", value: null, sub: "not captured", reason: "Stream ended early" },
    { spec: "KeyTile/codexTtftProxy", label: "TTFT", value: "1.9 s", sub: "at proxy" },
    { spec: "KeyTile/codexDurationLong", label: "Duration", value: "3m 12s", sub: "incomplete" },
  ],
};

// --- Person sessions (§11M 2.2, 2.3, 2.7, 3.4) ----------------------------------------------------

const session: Session = {
  agent: "codex",
  client: "exec",
  clientVersion: null,
  origin: null,
  children: null,
  hiddenCalls: 0,
  commit: null,
  sessionId: "3b9a0000-0000-7000-8000-000000000e21",
  start: at(8, 14, 20),
  end: at(8, 14, 38, 40),
  live: false,
  repo: "pomodoro",
  branch: null,
  label: { source: "firstPrompt", text: "Pomodoro timer web app" },
  capturedFromPrompt: null,
  prompts: 6,
  modelCalls: 41,
  events: null,
  friction: { frustrated: 0, interrupted: 0, destructive: 0 },
  bypassPct: null,
  coverage: null,
  fieldsMissing: false,
};
const span = (d: number, h: number, m: number, mins: number, secs = 0) => ({ start: at(d, h, m), end: new Date(Date.parse(at(d, h, m)) + mins * 60_000 + secs * 1000).toISOString() });

/** A linked person's sessions in All agents, as SessionTable/mixed draws them: the row, how it nests, its thread facts. */
export const mixedSessions: { spec: string; session: Session; nesting?: SessionNesting; threadFacts?: string }[] = [
  {
    spec: "SessionRow/mixedClaudeCode",
    session: {
      ...session,
      agent: "claude-code",
      client: "cli",
      sessionId: "4c1a0000-0000-4000-8000-0000000077e0",
      ...span(8, 9, 4, 372),
      live: true,
      repo: "aintercept",
      branch: "main",
      label: { source: "recap", text: "Codex taxonomy analysis" },
      prompts: 34,
      modelCalls: 640,
      friction: { frustrated: 1, interrupted: 1, destructive: 0 },
    },
  },
  {
    spec: "SessionRow/mixedCodexParent",
    session: { ...session, commit: "3f2a1c9", friction: { frustrated: 0, interrupted: 1, destructive: 0 }, ...span(8, 14, 20, 18, 40) },
    nesting: { kind: "parent", expanded: true, onToggle: () => undefined },
    threadFacts: "· 3 subagent · 1 review",
  },
  {
    spec: "SessionRow/mixedCodexChild",
    session: { ...session, sessionId: "5d01", label: { source: "firstPrompt", text: "subagent of #2 · thread_spawn" }, prompts: 2, modelCalls: 9, ...span(8, 14, 26, 3, 12) },
    nesting: { kind: "child", agentName: "timer_edge_tests" },
  },
  {
    spec: "SessionRow/mixedCodexReview",
    session: { ...session, sessionId: "5d02", label: { source: "firstPrompt", text: "review of #5" }, prompts: 1, modelCalls: 4, ...span(8, 14, 37, 1, 4) },
    nesting: { kind: "child", agentName: "review_slugify" },
  },
  {
    spec: "SessionRow/mixedCodexFork",
    session: {
      ...session,
      client: "tui",
      sessionId: "5d03",
      commit: "9be04d1",
      label: { source: "firstPrompt", text: "Add Expo mobile skeleton" },
      prompts: 9,
      modelCalls: 63,
      friction: { frustrated: 0, interrupted: 0, destructive: 2 },
      ...span(8, 15, 2, 42, 5),
    },
    nesting: { kind: "parent", expanded: false, onToggle: () => undefined },
    threadFacts: "· fork of 3b9a… at #4",
  },
  {
    spec: "SessionRow/mixedCodexUntitled",
    session: {
      ...session,
      client: "tui",
      sessionId: "01a1bd9a-0000-7000-8000-00000000aed1",
      commit: "3f2a1c9",
      label: { source: "none", text: "01a1bd9a-0000-7000-8000-00000000aed1" },
      prompts: 1,
      modelCalls: 203,
      friction: { frustrated: 0, interrupted: 1, destructive: 0 },
      ...span(9, 9, 0, 124),
    },
    threadFacts: "· resumed from 3b9a…",
  },
];
