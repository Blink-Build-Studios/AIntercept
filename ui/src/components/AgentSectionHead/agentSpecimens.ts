// Story data: CompAgents.dc.html's specimen content (handoff §11J, round 13), shared by
// the parity sheet and each component's stories. Codex values are the Nov 8 capture's shape.
import type { PromptItem } from "../../api/types";
import type { AgentPanelRow } from "../AgentPanel/AgentPanel";
import type { PersonAccount } from "../PersonIdentity/PersonIdentity";
import type { Person } from "../PersonRow/PersonRow";
import type { DetailRow } from "../RequestDrawer/DetailSection";
import type { SessionFact } from "../SessionFacts/SessionFacts";
import type { Session } from "../SessionRow/SessionRow";
import type { PatchChange, SpawnedThread, Step, ToolCall } from "../StepRow/StepRow";
import type { ThreadOriginData } from "../ThreadOrigin/ThreadOrigin";
import type { TooltipContent } from "../Tooltip/Tooltip";

export const NOW = new Date(2026, 10, 9, 10, 0).toISOString();
const at = (d: number, h: number, m: number, s = 0) => new Date(2026, 10, d, h, m, s).toISOString();
const plus = (iso: string, ms: number) => new Date(Date.parse(iso) + ms).toISOString();
const MIN = 60_000;


export const CODEX_LIMITS = "5h 41% · week 83%";
export const codexLimitsTip: TooltipContent = {
  title: "Codex limits · ChatGPT Plus",
  rows: [
    { key: "5h window", value: "41% used · resets 22:40 (in 2h 10m)" },
    { key: "Weekly", value: "83% used · resets Nov 12 08:00" },
    { key: "Credits", value: "unlimited" },
    { key: "As of", value: "21:31:07 · last model response" },
  ],
};
export const codexLimitsMore = [
  { label: "resets", value: "5h in 2h 10m · week Nov 12" },
  { label: "credits", value: "unlimited" },
];

export const codexPanelRows: AgentPanelRow[] = [
  { k: "Client", v: "codex exec 0.164" },
  { k: "Approval", v: "on-request" },
  { k: "Sandbox", v: "workspace-write" },
  { k: "Collaboration", v: "single" },
  { k: "Accepted lines", v: "+212 −38 · 18 patches" },
  { k: "Limits at end", v: CODEX_LIMITS },
  { k: "Prewarm calls", v: "6 hidden" },
];
export const codexResponseRows: DetailRow[] = [
  { k: "transport", v: "WebSocket · response.create" },
  { k: "request kind", v: "turn" },
  { k: "reasoning tokens", v: "1,204" },
  { k: "ws timing", v: "queue 12 ms · first event 1,880 ms" },
  { k: "rate limits", v: CODEX_LIMITS },
  { k: "prompt cache key", v: "pck_7f…2a" },
];
export const linkedAccounts: PersonAccount[] = [
  { agent: "claude-code", account: "Anthropic acct 7d2e…a41c · org 1b9f…e02 · Max", devices: 2 },
  { agent: "codex", account: "ChatGPT acct 9c2e…41 · Plus", devices: 1 },
];
export const codexFacts: SessionFact[] = [
  { k: "cwd", v: "/work/pomodoro" },
  { k: "shell", v: "zsh" },
  { k: "tz", v: "America/Los_Angeles" },
  { k: "approval", v: "on-request" },
  { k: "sandbox", v: "workspace-write" },
];
export const forkOrigin: ThreadOriginData = { kind: "fork", threadShort: "3b9a…e21", turn: 4, init: "warm_fork", href: "#" };
export const resumeOrigin: ThreadOriginData = { kind: "resume", threadShort: "3b9a…e21", init: "cold_resume", href: "#" };
export const subagentOrigin: ThreadOriginData = { kind: "subagent", threadShort: "3f2a…c19", turn: 4, init: "thread_spawn", href: "#" };
export const turnReportParts = ["sampling 2.4 s", "7 requests", "1 retry", "tools: shell 4, file change 2, subagent 1", "steers 0", "compaction 0 ms", "trigger user"];

export const person: Person = {
  agentLimits: [],
  personId: "p",
  name: null,
  email: null,
  accountIdShort: "5c11…9d0",
  accounts: 1,
  orgIdShort: null,
  orgKind: "personal",
  devices: 1,
  agents: ["claude-code"],
  agentInfo: [],
  linkedBy: null,
  lastSeenAt: at(3, 2, 15),
  live: false,
  sessions: 1,
  prompts: 1,
  tokensOut: 1800,
  tokensAccuracy: "exact",
  activity: [],
  bypassPct: null,
  coverage: { reported: 4, captured: 4, notCaptured: 0, windows: [] },
};
export const linkedPerson: Person = {
  ...person,
  name: "Chris Ortiz",
  email: "chris@example.com",
  linkedBy: "email",
  accounts: 2,
  accountIdShort: "7d2e…a41c",
  devices: 3,
  agents: ["claude-code", "codex"],
  live: true,
  lastSeenAt: NOW,
  sessions: 17,
  prompts: 192,
  tokensOut: 1_400_000,
  bypassPct: 99.7,
  coverage: { reported: 1600, captured: 1506, notCaptured: 94, windows: [] },
};
export const codexPerson: Person = {
  ...person,
  name: "Dana Ruiz",
  accountIdShort: "9c2e…41",
  orgKind: "unknown",
  agents: ["codex"],
  lastSeenAt: at(8, 16, 2),
  sessions: 29,
  prompts: 64,
  tokensOut: 34_000,
  coverage: { reported: 146, captured: 146, notCaptured: 0, windows: [] },
};

const session: Session = {
  agent: "codex",
  client: "exec",
  clientVersion: "0.164.0",
  origin: null,
  children: null,
  hiddenCalls: 0,
  commit: null,
  sessionId: "s",
  start: at(8, 14, 20),
  end: null,
  live: false,
  repo: "pomodoro",
  branch: "3f2a1c9",
  label: { source: "recap", text: "" },
  capturedFromPrompt: null,
  prompts: 1,
  modelCalls: 1,
  events: null,
  friction: { frustrated: 0, interrupted: 0, destructive: 0 },
  bypassPct: null,
  coverage: { captured: 1, notCaptured: 0, reported: 1, windows: [] },
  fieldsMissing: false,
};
const S = (over: Partial<Session> & { span: number }): Session => {
  const { span, ...rest } = over;
  const start = rest.start ?? session.start;
  return { ...session, ...rest, end: plus(start, span) };
};
export const sessions = {
  claudeCode: S({
    agent: "claude-code",
    start: at(8, 9, 4),
    span: 372 * MIN,
    live: true,
    repo: "aintercept",
    branch: "main",
    label: { source: "recap", text: "Codex taxonomy analysis" },
    prompts: 34,
    modelCalls: 640,
    friction: { frustrated: 1, interrupted: 1, destructive: 0 },
    coverage: { captured: 640, notCaptured: 94, reported: 734, windows: [] },
  }),
  codexParent: S({ span: 18 * MIN + 40_000, label: { source: "recap", text: "Pomodoro timer web app" }, prompts: 6, modelCalls: 41, friction: { frustrated: 0, interrupted: 1, destructive: 0 } }),
  codexChild: S({ start: at(8, 14, 26), span: 3 * MIN + 12_000, label: { source: "recap", text: "subagent of #2 · thread_spawn" }, prompts: 2, modelCalls: 9 }),
  codexReview: S({ start: at(8, 14, 37), span: MIN + 4_000, label: { source: "recap", text: "review of #5" }, prompts: 1, modelCalls: 4 }),
  codexFork: S({
    start: at(8, 15, 2),
    span: 42 * MIN + 5_000,
    branch: "9be04d1",
    label: { source: "recap", text: "Add Expo mobile skeleton" },
    prompts: 9,
    modelCalls: 63,
    friction: { frustrated: 0, interrupted: 0, destructive: 2 },
  }),
};

export const codexPrompt: PromptItem = {
  type: "prompt",
  steps: [],
  prompt: {
    promptId: "t4",
    index: 4,
    t: at(8, 14, 24),
    durationMs: 161_000,
    text: { retained: true, text: "add edge-case tests for the timer", chars: 33, pastedBytes: null, images: 0 },
    steps: 7,
    tokensIn: 88_000,
    tokensOut: 2_100,
    toolCounts: { exec: 4, apply_patch: 2, spawn_agent: 1 },
    friction: { frustrated: false, interrupted: false, destructive: 0 },
    inFlight: false,
    turnReport: null,
    hidden: {},
  },
};
export const codexModelCall: Step = {
  does: "exec",
  usageMissing: null,
  commands: [],
  patches: [],
  text: null,
  exitCodes: [],
  kind: "model",
  requestId: "resp_1",
  t: at(8, 14, 24, 8),
  model: "gpt-6.1-sol",
  agentType: null,
  requestClass: "main",
  status: "done",
  stopReason: "tool_call",
  durationMs: 4100,
  ttftMs: null,
  costUsd: null,
  eventsPending: false,
  tokens: { accuracy: "exact", input: 31_200, output: 640, cacheRead: null, cacheWrite: null, cacheWrite1h: null, cacheWrite5m: null },
  tools: [],
};
export const codexExec: ToolCall = { command: null, toolUseId: "c1", name: "exec", mcpServer: null, target: "npm test -- timer", status: "ok", durationMs: 3200, exitCode: 1, resultBytes: 4100, destructive: null };
export const codexPatch: ToolCall = { ...codexExec, toolUseId: "c2", name: "apply_patch", target: "src/timer.test.ts", durationMs: null, exitCode: null, resultBytes: 1900 };
export const codexPatchChange: PatchChange = { kind: "update", added: 38, removed: 2, acceptedAdded: 38, acceptedRemoved: 2 };
export const codexSpawn: SpawnedThread = { t: at(8, 14, 25, 30), tool: "spawn_agent", agentName: "timer_edge_tests", threadShort: "4d0a…", prompts: 2, calls: 9, durationMs: 192_000, href: "#" };
