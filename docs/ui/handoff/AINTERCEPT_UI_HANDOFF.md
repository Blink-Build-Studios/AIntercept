# aintercept UI — React handoff (components → Storybook → screens → data)

This document is the complete build brief for the first aintercept UI: the **People** page (landing), the **Person** page, the **Session** page and the **Request drawer**, with every loading, empty, error and live state. It is written for a coding agent (Claude Code) to execute end to end.

Visual source of truth: the Design canvas **"aintercept — Person & session pages"**. It has two pages:

- **Screens**: every screen and state as an artboard.
- **Components**: an index of all components plus specimen sheets showing every state, including three sheets for hover, tooltips and keyboard (`CompTooltip`, `CompTimelineHover`, `CompTableHover`; §6B).

Design system: **aintercept Wire** (dark-first, IBM Plex Mono/Sans, square corners, hairlines, color only as a data channel).

Where this document and the canvas disagree, **this document wins** (§11 lists the known differences).

---

## 0. How to execute this document

Work in four phases. Do not start a phase until the previous one meets its acceptance criteria.

| Phase | Output | Done when |
|---|---|---|
| 1. Foundations & components | `src/design/` tokens and primitives; all components in §6 | Every component has a story for every state listed for it; a11y addon shows no violations; tokens come only from `tokens.css` |
| 2. Storybook | One story file per component, plus composed "screen" stories using fixtures | Storybook builds; each canvas artboard has a matching screen story (§8.6) |
| 3. Screens | Routes, layouts, URL state, live polling, all page states | Each route renders every state from §7 against MSW mocks |
| 4. Data | API client, typed hooks, backend endpoints per §5 | Screens run against the real query API; MSW mocks still pass the same contract tests |

Rules for the agent:

1. **Build components, not screens.** No screen may contain styling that isn't a token or a component.
2. **Never invent a number in the UI.** Unknown is `—` with a tooltip saying why; never `0` (§3.3).
3. **Keep fixtures real.** Use the numbers in §9 (from captured traffic). Mark anything invented `illustrative` in the fixture file.
4. **Ask before changing the API contract in §5.** It is shared with the backend.
5. **Interaction is part of the component, not polish for later.** Every clickable component ships with its pending, disabled and error states, and every query-backed region renders through `RegionState` (§6A). Phase 1 is not done until the §6A.11 tests pass for the components involved, and phase 3 is not done until every screen has been reviewed under the `normal`, `slow` and `error` latency settings.
6. **Hover, tooltips and keyboard are part of the component too.** No native `title` attributes anywhere; every explanation goes through `Tooltip` (§6B). Phase 1 is not done until the §6B.11 tests pass.

---

## 1. Product context (read once)

aintercept is a TLS-intercepting proxy for AI agent traffic. A customer routes an agent's traffic to the vendor (e.g. `api.anthropic.com`) through Envoy. The observer gets an out-of-band copy of every request and response, parses it into rows, and writes NDJSON to object storage. Nothing is installed on the person's machine.

The UI's job is the demo reaction *"there's so much here"*: a technical leader sees, live, how much one person's agent sends and does. The tone is factual, not alarmist. The reference points are Burp Suite and a browser network panel.

Claude Code uses **five channels**, all on `api.anthropic.com`:

| Channel | Endpoint on the wire | Carries |
|---|---|---|
| Model calls | `POST /v1/messages` | Each agent-loop step; the first request of a thread also carries the tool catalog, MCP servers, skills and injected context |
| Product analytics | `POST /api/event_logging/v2/batch` | ~128 event types (`tengu_*`), ~45 env fields each |
| Feature flags | `POST /api/eval/sdk-*` | Account attributes, flag values, experiments |
| Token counting | `POST /v1/messages/count_tokens` | Whole files' text |
| Account / setup | skills list, MCP registry, bootstrap, profile… | No session |

Other agents (Codex, ChatGPT web, claude.ai) come later. The UI is agent-agnostic from day one: they appear on the People page with their own color and data shape.

### 1.1 Data hierarchy (authoritative)

```
Account (person)            account_uuid                 plan, role, rate limits, feature flags
 └─ Device                  device_id                    OS, terminal, shell; SLEEP
     └─ Session             session_id                   repo/branch/PR, capabilities, context window, recaps, FOCUS
         └─ Prompt          cc_prompt_id (+ cc_prompt_index)   what the person typed; friction
             └─ Step        request-id / x-client-request-id   one model call: model, tokens, cost, stop reason
                 └─ Tool call   toolUseID                tool, target, duration, exit, file ops
Account-level requests (no session): skills list, MCP registry, bootstrap… (shown only in "All requests")
```

Facts that shape the UI (all verified against captured traffic):

- **Rolls up across sessions** (person page): prompts, model calls, tokens, cost, analytics counts, friction counts, coverage, presence (combined per device), capabilities ("seen in any session").
- **Session-only** (session page): context window and compactions, repo trail, recaps, permission-mode timeline, network changes, experiments in effect.
- **Presence**: focus is per session (`tengu_return_to_session.blurDurationMs`); sleep is per device (`tengu_event_loop_stall.likely_sleep`, `clock_jump_ms`). On the person page:
  - *away* = every open session was blurred at once;
  - *asleep* = the union of sleep windows;
  - *none* = no session open.
- **Permission mode changes within a session** (`permissionMode` on every `tengu_api_query`/`tengu_api_success`). Show it as a timeline band. The person-level summary is "bypassed approvals for X% of model calls".
- **Flags are account-level but change during the day.** They are fetched at session start and then about every 6 hours. Show the current set plus "N changed since <time>". No timeline lane.
- **Capabilities differ by session** (by project). Lazy tool loading means they accumulate over the session (`tengu_deferred_tools_pool_change`).
- **`key_hash` is not an identity.** OAuth tokens refresh. Identify people by `account_uuid` and devices by `device_id`.
- **A session can span days and networks**, but never devices.
- **Model requests are incremental.** Most carry only the newest 1–2 messages. The system prompt, tool catalog and injected context arrive on thread-create requests and compactions, so session state is carried forward.
- **Coverage check**: every model call produces `tengu_api_success` with `requestId`. A reported call with no matching captured `request-id` means traffic bypassed the proxy. This really happened: 94 calls over IPv6, Nov 3 00:00–00:59 (37) and 15:00–17:59 (57).

---

## 2. Tech stack

| Concern | Choice |
|---|---|
| Framework | React 18 + TypeScript (strict), Vite |
| Routing | React Router 6 (URL is the state store for range, tab, filters, drawer) |
| Data | TanStack Query 5 with polling (§5.3) |
| Styling | CSS Modules + CSS custom properties from `tokens.css`. No Tailwind, no CSS-in-JS runtime, no hard-coded hex values outside `tokens.css` |
| Charts | None. The timeline is hand-built with absolutely positioned divs (or a single SVG per track); no chart library |
| Storybook | Storybook 8 (`@storybook/react-vite`), addons: a11y, interactions, viewport, themes (dark/light) |
| Mocks | MSW 2, used by Storybook, tests and local dev |
| Tests | Vitest + Testing Library; Storybook interaction tests for stateful components |
| Fonts | IBM Plex Mono 400/500/600, IBM Plex Sans 400/500/600 (self-host via `@fontsource`) |

Suggested tree:

```
src/
  design/tokens.css          # generated from tokens.json (§3.1)
  design/global.css          # body, focus ring, reduced-motion
  components/<Name>/<Name>.tsx · <Name>.module.css · <Name>.stories.tsx · index.ts
  screens/PeoplePage/ · PersonPage/ · SessionPage/
  api/client.ts · types.ts · hooks/*.ts · mocks/handlers.ts · mocks/fixtures/*.ts
  lib/format.ts · lib/time.ts · lib/presence.ts
```

---

## 3. Foundations

### 3.1 Tokens

Generate `tokens.css` from the Wire `tokens.json`: dark under `:root` and `[data-theme="dark"]`, light under `[data-theme="light"]`. Variable names are `--<token>`.

| Token | Dark | Light | Use |
|---|---|---|---|
| `bg` | `#0c0e0f` | `#f6f6f3` | page ground |
| `surface` | `#131618` | `#ffffff` | rows, panels, tiles |
| `surface-raised` | `#1b1f22` | `#ecece7` | hover, selected, open, banners |
| `line` | `#2a3034` | `#d6d6cf` | hairlines |
| `line-strong` | `#60686d` | `#8b9094` | control borders |
| `ink` | `#e6e3db` | `#141617` | primary text |
| `ink-muted` | `#a3a9ad` | `#4f5559` | secondary text, units |
| `ink-faint` | `#848c90` | `#62686c` | labels, timestamps (4.5:1 floor) |
| `ch-model` | `#7db3ff` | `#1f5fbf` | model calls |
| `ch-analytics` | `#f0b43c` | `#8a5a00` | analytics events |
| `ch-flags` | `#c9a3ec` | `#7a3fa8` | flags, experiments |
| `ch-skills` | `#e8916f` | `#a3431f` | reach: MCP, skills, tools, tool_use |
| `ch-count` | `#8fd17e` | `#356f1e` | tokens, token counting, context |
| `ch-account` **(new; add to the Wire system)** | `#b0b8bd` | `#555d62` | account/setup requests with no session |
| `live` | `#57d68d` | `#117336` | live dot and live badge only |
| `danger` | `#ff7a6b` | `#b3261e` | destructive commands and coverage gaps only, always with ▲ and a word |
| `focus` | `#ffd36b` | `#0b57d0` | 2px focus ring; brush/selection |
| `skeleton` | `#20252a` | `#e4e4de` | placeholders |
| `agent-claude-code` | `#e8916f` | `#a3431f` | agent identity |
| `agent-codex` | `#9fb4c7` | `#45607a` | agent identity |
| `agent-chatgpt` | `#7fd1b9` | `#1f6e58` | agent identity |
| `agent-claude-ai` | `#d9b98c` | `#7a5a2a` | agent identity |

Type (`font` shorthand tokens):

| Style | Spec | Use |
|---|---|---|
| `num-xl` | Mono 22/26 500 | tile values |
| `data` | Mono 12.5/20 400 | every row |
| `data-strong` | Mono 12.5/20 600 | repo names, tool names, leading counts |
| `micro` | Mono 11/14 500, +0.04em, uppercase | chips, column heads, labels (labels may use 10.5px) |
| `title` | Sans 18/24 600 | person/session name, page titles |
| `body` | Sans 13/20 400 | sentences |
| `label` | Sans 12/16 500 | buttons, tabs |

Spacing (`--space-1` 4, `-2` 8, `-3` 12, `-4` 16, `-6` 24, `-8` 32), radius (`--radius-0` 0 for rows, lanes, panels; `--radius-sm` 2px for chips and buttons; `--radius-pill` live dot only). No shadows.

### 3.2 Visual rules

- **Hover**: `surface-raised` background.
- **Selected**: `surface-raised` with `ink` text, or an `ink` border on cards.
- **Focus**: `outline: 2px solid var(--focus)`, with offset 1px (−2px inset on rows).
- **Disabled**: `ink-faint` text with a `line` border.
- **Background calls** (recaps, suggestions, compaction, count_tokens, analytics batches in request lists): the **quiet row** treatment (§11F.1): every text color drops to `--ink-faint`; the channel kind label keeps its channel color. No opacity.
- **Color is a channel.** Use `ch-*` only to say where something came from, `agent-*` only for agent identity, `live` only for live, and `danger` only for destructive commands and coverage gaps. `✓` uses `ink`/`ink-muted`, never green. HTTP errors use **bold ink with ✕**, never red.
- **Glyphs** are mono text, always beside a word:
  - `▾ ▸` disclosure · `›` opens a page · `⤷` nested/subagent
  - `●` live · `↯` frustrated · `✕` interrupted/failed · `▲` destructive/gap
  - `✓` match/done · `○` pending · `⇣` compaction · `—` unknown
  - No emoji, no icon set.

### 3.3 Content rules (implement in `lib/format.ts`)

- **Voice**: neutral, never "your". Example: "Claude Code reported 304 model calls; the proxy captured 267."
- **Numbers**:
  - Use tabular figures (`font-variant-numeric: tabular-nums`) and thousands separators: `21,305`.
  - Abbreviate at 100k and above only: `140.9M`, `384k`, `1.1M`. This applies to counts other than tokens; tokens follow §11B.3.
  - Units are lowercase after a space: `37.2 MB`, `4.8 s`, `71 ms`.
- **Unknown**:
  - Render `—` with a `Tooltip` (§6B) that says why: "not extracted yet", "not sent by this surface", "no response".
  - Never render `0` for unknown. The API sends `null` for unknown.
- **Not retained**: show the content's shape (`188 chars`, `4.1 KB`, `132 lines`) plus a `NOT RETAINED` chip. One page-level explanation lives in "About this data".
- **Token accuracy**: values carry `accuracy: 'exact' | 'estimated' | 'missing'`. Show estimated as `≈ 610k`, and missing as `—`.
- **Times** use the viewer's local zone: `HH:mm` within today, `MMM D HH:mm` otherwise; durations like `8h 20m`, `2d 0h`, `1m 40s`.
- **IDs** are middle-truncated: `7d2e…a41c`.
- **Labels and values**: never run them together in one same-weight line. A label is `ink-faint` micro caps above its value; groups are split by `line` hairlines.

### 3.4 Accessibility

- Use real `<button>`, `<a href>`, `<input>` + `<label>`.
- Rows that navigate are `<a>`; rows that open the drawer are `<button>`.
- The drawer is `role="dialog"` with `aria-modal`, traps focus, closes on Esc and returns focus to the row.
- Loading regions carry `aria-busy="true"`.
- Live regions: the "N new" pill and banners use `aria-live="polite"`.
- Each timeline track has an `aria-label` summary (e.g. "Model calls per hour, peak 36"). The brush is operable by keyboard: arrows move it, Shift+arrows resize, Esc clears.
- Respect `prefers-reduced-motion`; there are no shimmer animations anyway.

---

## 4. Domain types (`src/api/types.ts`)

```ts
export type ISO = string;                       // RFC 3339
export type Agent = 'claude-code' | 'codex' | 'chatgpt' | 'claude-ai';
export type Channel = 'model' | 'analytics' | 'flags' | 'count' | 'account' | 'skills';
export type Accuracy = 'exact' | 'estimated' | 'missing';
export type Unknown<T> = T | null;              // null = unknown → render "—"

export interface Envelope<T> {
  data: T;
  meta: {
    asOf: ISO;                  // when the query ran
    lastDataAt: ISO | null;     // newest ingested row for this scope (drives the "delayed" banner)
    from?: ISO; to?: ISO;       // the effective range
    nextSince?: string;         // cursor for incremental polling
    page?: number; pageSize?: number; total?: number;
  };
}

export interface TokenCount {
  input: Unknown<number>;          // uncached input
  cacheRead: Unknown<number>;
  cacheWrite5m: Unknown<number>;
  cacheWrite1h: Unknown<number>;
  output: Unknown<number>;
  thinking?: Unknown<number>;
  accuracy: Accuracy;
}
export interface Cost { usd: Unknown<number>; basis: 'api-equivalent' | 'billed'; estimatedCalls: number }  // estimatedCalls: calls with no analytics event (6B)

export interface Coverage {
  reported: Unknown<number>;   // from tengu_api_success
  captured: number;            // matched captured model calls
  notCaptured: number;         // reported − matched
  windows: { from: ISO; to: ISO; count: number; cause?: string }[];
}

export interface PersonSummaryRow {
  personId: string;            // = account_uuid (or the vendor account id for other agents)
  name: Unknown<string>; email: Unknown<string>;
  accountIdShort: string; orgIdShort: Unknown<string>; orgKind: 'corporate' | 'personal' | 'unknown';
  devices: number;
  agents: Agent[];
  linkedBy?: 'email' | 'device' | null;
  lastSeenAt: ISO; live: boolean;
  sessions: number; prompts: Unknown<number>;
  tokensOut: Unknown<number>; tokensAccuracy: Accuracy;
  activity: { day: string; count: number }[];   // 14 daily bins (model calls or messages)
  bypassPct: Unknown<number>;                   // 0..100, null when N/A
  coverage: Coverage | null;                    // null when the surface has no self-report
  agentInfo: { agent: Agent; version: Unknown<string>; firstSeenAt: ISO; devices: number }[];   // AgentChip tooltip (6B)
}

export interface PersonHeader {
  personId: string; name: Unknown<string>; email: Unknown<string>;
  accountIdShort: string; orgIdShort: Unknown<string>; orgKind: 'corporate' | 'personal' | 'unknown';
  role: Unknown<string>; plan: Unknown<string>; rateLimitTier: Unknown<string>;
  customerSince: Unknown<ISO>; firstUseAt: Unknown<ISO>;
  devices: { deviceId: string; platform: string; terminal: Unknown<string>; shell: Unknown<string> }[];
  limits: { status: Unknown<string>; utilization7d: Unknown<number>; overage: Unknown<string> };
  flags: { total: number; experiments: number; changedSince: { at: ISO; count: number } | null };
  posture: { bypassPct: Unknown<number>; sandbox: Unknown<'on' | 'off'>; tokenStorage: Unknown<string>; proxyDetected: Unknown<string> };
  lastSeenAt: ISO; live: boolean;
}

export type PresenceState = 'focused' | 'away' | 'asleep';
export interface TimelineData {
  from: ISO; to: ISO; now: ISO | null; binSeconds: number;
  // Fields marked (6B) feed tooltips; tooltips never fetch (§6B.10).
  sessions: { sessionId: string; start: ISO; end: ISO | null; label: string; row: number; live: boolean; capturedFromPrompt: Unknown<number>;
    repo: Unknown<string>; branch: Unknown<string>; prompts: Unknown<number>; modelCalls: number }[];                    // (6B)
  prompts: { t: ISO; index: Unknown<number>; sessionId: string;
    promptId: string; text: PromptData['text']; steps: number; durationMs: Unknown<number>;
    tokensIn: Unknown<number>; tokensOut: Unknown<number> }[];                                                            // (6B)
  modelCalls: { t: ISO; count: number; tokens: number;
    byClass: { main: number; subagent: number; auxiliary: number };
    tokensIn: Unknown<number>; tokensOut: Unknown<number>; notCaptured: number }[];       // per bin (6B)
  analytics: { t: ISO; count: number; top: { name: string; count: number }[] }[];        // per bin; top 3 (6B)
  notCaptured: { from: ISO; to: ISO; count: number; reported: number; captured: number; cause: Unknown<string> }[]; // hatched gaps (6B)
  presence: { from: ISO; to: ISO; state: PresenceState; label?: string; source: 'focus' | 'sleep' }[];                  // (6B)
  friction: { t: ISO; kind: 'frustrated' | 'interrupted' | 'destructive'; count: number;
    promptIndex: Unknown<number>; detail: string; tool: Unknown<string> }[];                                              // (6B)
  // session page only:
  context?: { t: ISO; tokens: number; requestId: string; model: string; thread: 'main' | 'subagent'; windowTokens: Unknown<number>; promptIndex: Unknown<number> }[]; // (6B)
  compactions?: { t: ISO; from: number; to: number; idle: boolean; waitMs: Unknown<number>; messages: Unknown<number> }[];  // (6B)
  permission?: { from: ISO; to: ISO; mode: string }[];
  permissionChanges?: { t: ISO; from: string; to: string }[];                                                            // (6B)
  network?: { from: ISO; to: ISO; label: string; ipv6: boolean; bypassed: boolean; address: Unknown<string>; notCaptured: number }[]; // (6B)
}

export interface SessionRowData {
  sessionId: string; agent: Agent; start: ISO; end: ISO | null; live: boolean;
  repo: Unknown<string>; branch: Unknown<string>;
  label: { text: string; source: 'recap' | 'firstPrompt' | 'repo' | 'none' };
  prompts: Unknown<number>; capturedFromPrompt: Unknown<number>;
  modelCalls: number; events: Unknown<number>;
  friction: { frustrated: number; interrupted: number; destructive: number };
  bypassPct: Unknown<number>; coverage: Coverage | null;
  fieldsMissing: boolean;         // pre-parser rows
}

export type RequestKind = 'model' | 'analytics' | 'flags' | 'count' | 'account';
export interface RequestRowData {
  requestId: string;              // captured transaction id, or "reported:<requestId>" for not-captured
  t: ISO; kind: RequestKind; sessionId: string | null; promptIndex: Unknown<number>;
  method: Unknown<string>; path: string; detail: string;   // e.g. "main · opus · tool_use", "96 events"
  status: Unknown<number | 'ERR'>; durationMs: Unknown<number>;
  bytesUp: Unknown<number>; bytesDown: Unknown<number>;
  background: boolean;            // dim it
  captured: boolean;              // false = reported by analytics only
  // (6B) cell tooltips:
  statusText: Unknown<string>;    // "OK", "connection reset"
  stopReason: Unknown<string>;
  ttftMs: Unknown<number>;
  tokens: TokenCount | null;      // model calls only
  contentEncoding: Unknown<string>;
  error: { message: string; afterMs: Unknown<number>; bytesReceived: Unknown<number>; retries: number; retrySucceededAt: ISO | null } | null;
}

export interface EventRowData {
  eventId: string; t: ISO; name: string; promptIndex: Unknown<number>;
  linkedTo: { kind: 'request' | 'tool' | 'prompt' | 'session' | 'device'; id: string; label: string };
  summary: string;                // server-built "k v · k v"
  marker?: 'destructive' | 'frustrated' | null;
  internal: boolean;              // client internals (hidden by default)
  collapsedCount?: number;        // >1 when ticks are collapsed
}

export interface StepData {
  requestId: string; t: ISO; kind: 'model' | 'subagent';
  model: string; requestClass: 'main' | 'subagent' | 'auxiliary' | 'compaction';
  tokens: TokenCount; durationMs: Unknown<number>; ttftMs: Unknown<number>;
  stopReason: Unknown<string>; costUsd: Unknown<number>;
  status: 'done' | 'streaming' | 'error';
  eventsPending: boolean;         // analytics not yet joined
  agentType?: string; subSteps?: number;
  tools: ToolCallData[];
}
export interface ToolCallData {
  toolUseId: string; name: string; mcpServer: Unknown<string>;
  target: Unknown<string>;        // path / command / URL (extract)
  status: 'running' | 'ok' | 'error';
  durationMs: Unknown<number>; exitCode: Unknown<number>; resultBytes: Unknown<number>;
  diff?: { added: number; removed: number };
  destructive?: { category: string; scope: Unknown<string> } | null;
}
export interface PromptData {
  promptId: string; index: Unknown<number>; t: ISO; durationMs: Unknown<number>;
  text: { retained: boolean; text?: string; chars: Unknown<number>; pastedBytes?: number; images?: number };
  steps: number; tokensIn: Unknown<number>; tokensOut: Unknown<number>;
  toolCounts: Record<string, number>;
  friction: { frustrated: boolean; interrupted: boolean; destructive: number };
  inFlight: boolean;
}
export type BetweenData = { kind: 'compaction' | 'recap' | 'idle-compaction'; t: ISO; detail: string };
```

The drawer type (`RequestDetail`) is defined in §5.2.10.

---

## 5. API contract

Base: `/api/v1`. JSON only. Every response is an `Envelope<T>`.

Common query parameters:
- `from`, `to`: ISO range. Defaults per endpoint.
- `since`: polling cursor.
- `page` and `pageSize` (default 20, max 200), with `order=asc|desc`.

Errors return `{ error: { code, message, queryId } }` with HTTP 4xx/5xx. The UI shows `message` and `queryId` in `ErrorState`.

### 5.1 Endpoint overview

| # | Endpoint | Feeds |
|---|---|---|
| 1 | `GET /overview?from&to` | People page ribbon, agent cards, unattributed notice |
| 2 | `GET /people?from&to&agent&q&sort` | People table |
| 3 | `GET /people/{personId}?from&to` | Person header (identity, limits, flags, posture) |
| 4 | `GET /people/{personId}/summary?from&to` | Person ribbon, coverage line, capabilities, footprint |
| 5 | `GET /people/{personId}/timeline?from&to&bin` | Person activity timeline |
| 6 | `GET /people/{personId}/sessions?from&to&page` | Sessions tab |
| 7 | `GET /people/{personId}/requests?from&to&kind&q&page&order` | All requests tab (includes no-session account calls) |
| 8 | `GET /sessions/{sessionId}` | Session header |
| 9 | `GET /sessions/{sessionId}/timeline?bin` | Session timeline (whole session; the brush is client-side) |
| 10 | `GET /sessions/{sessionId}/state?from&to` | Capabilities/experiments at `to`; repo trail and recaps within range; range readout counts |
| 11 | `GET /sessions/{sessionId}/prompts?from&to&page` | Prompts tab (prompts + between rows; steps inline) |
| 12 | `GET /sessions/{sessionId}/requests?from&to&kind&q&page` | Requests tab |
| 13 | `GET /sessions/{sessionId}/events?from&to&q&internal&collapseTicks&page` | Events tab |
| 14 | `GET /requests/{requestId}` | Drawer Summary + key strip |
| 15 | `GET /requests/{requestId}/raw` | Drawer Raw tab |
| 16 | `GET /requests/{requestId}/details` | Drawer All details tab |
| 17 | `GET /coverage?personId|sessionId&from&to` | "view N calls" list of not-captured calls |
| 18 | `GET /status` | Proxy and ingest health; the waiting checklist |

### 5.2 Endpoint details, with field sources

Source legend (from the design brief):
- **now**: parsed today.
- **extract**: in the traffic; the observer must parse it into a field.
- **content**: only when bodies are retained.
- **derived**: computed by the query API from other fields.

#### 5.2.1 `GET /overview`

```ts
{ people: number; activeNow: number;            // live = request within last 5 min
  agents: { agent: Agent; people: number; sessions: number; tokenAccuracy: Accuracy;
            shape: 'sessions→prompts→steps' | 'sessions→turns→steps' | 'conversations→messages' }[];
  sessions: number; requests: number; notCaptured: number;
  unattributed: { sessions: number; modelCalls: number; firstSeen: ISO; reason: string } }
```

Sources:
- `people`: distinct `account_uuid` (now). For other agents, their account id.
- `notCaptured`: derived (§5.4).
- `unattributed`: sessions whose rows have no `account_uuid`.

#### 5.2.2 `GET /people` → `PersonSummaryRow[]`

Sort: `lastSeen` (default) | `activity` | `name`. Filter `agent` (repeatable) and `q`, which matches name, email, account id and device id.

| Field | Source |
|---|---|
| `name` | Skills listing `owner.full_name`; claude.ai account rows joined by `account_uuid` (extract) |
| `email` | Skills listing `owner.email_address`; claude.ai `/api/account` (extract) |
| `orgKind` | `provider_org_id` compared with the configured customer org (now) |
| `devices` | Distinct `device_id` (now) |
| `agents` | Agent detected per transaction (now; Claude Code by user-agent `claude-cli` + host) |
| `linkedBy` | **Open question** (§10): cross-vendor identity link. Return `null` until decided |
| `lastSeenAt`, `live` | Newest request (now) |
| `prompts` | Distinct `cc_prompt_id` (now; needs promoting to a top-level field) |
| `tokensOut` | Sum of response `usage.output_tokens` (exact). Estimated/missing for surfaces without usage |
| `activity` | Daily model-call counts (messages for chat surfaces) |
| `bypassPct` | Derived: model calls with `permissionMode=bypassPermissions` ÷ calls with a known mode (`tengu_api_query`) |
| `coverage` | Derived (§5.4) |

#### 5.2.3 `GET /people/{personId}` → `PersonHeader`

| Field | Source |
|---|---|
| `role` | Flag attributes `organizationRole` (extract) |
| `plan` | Analytics `subscription_type` (now) |
| `rateLimitTier` | Flag attributes `rateLimitTier` (extract) |
| `customerSince`, `firstUseAt` | Flag attributes `subscriptionCreatedAt`, `firstTokenTime` (extract) |
| `devices[]` | `device_id` plus analytics `env.platform`, `terminal`, `shell` (now) |
| `limits.status` | Response header `anthropic-ratelimit-unified-status` (now) |
| `limits.utilization7d` | `anthropic-ratelimit-unified-7d-utilization` (now) |
| `limits.overage` | `-overage-status` plus the disabled reason (now). There is no 5-hour header; don't add one |
| `flags.total`, `flags.experiments` | Latest `/api/eval/sdk-*` body (extract) |
| `flags.changedSince` | Derived: diff against the previous fetch |
| `posture.bypassPct` | Derived (as in §5.2.2) |
| `posture.sandbox` | Analytics (now) |
| `posture.tokenStorage` | Analytics (now), e.g. `keychain-with-plaintext-fallback` |
| `posture.proxyDetected` | Analytics `gzipSkipReason: custom_ca` (now) |

#### 5.2.4 `GET /people/{personId}/summary`

```ts
{ ribbon: { modelCalls: number; sessions: number; analyticsEvents: number; analyticsBatches: number; eventTypes: number;
            accountCalls: number; toolsReachable: Unknown<number>; tokens: TokenCount; cost: Cost };
  coverage: Coverage;
  capabilities: { mcpServers: { name: string; tools: Unknown<number>; riskyVerbs: string[] }[];
                  mcpToolsTotal: Unknown<number>; mcpToolsUsed: Unknown<number>;
                  builtinTools: Unknown<number>; builtinUsed: Unknown<number>; skills: Unknown<number>; hooks: Unknown<number>;
                  hookOverheadMs: Unknown<number>; memoryWrites: Unknown<number>;
                  variesBySession: { min: number; max: number } | null };
  footprint: { prompts: Unknown<number>; toolCalls: Unknown<number>; toolsByName: Record<string, number>;
               commands: Unknown<number>; topCommandWords: { word: string; count: number }[];
               destructive: { total: number; rmF: number; rmRf: number; outsideCwd: number };
               codeShipped: { headChanges: number; commits: number; pushes: number; prs: number[] };
               failures: { commands: number; toolErrors: number; apiRetries: number };
               subagentCalls: number } }
```

Sources:
- Capabilities: thread-create `tools` plus `tool_addition` / `tengu_deferred_tools_pool_change`. Take the **union over the window, labeled "seen in any session"** (extract).
- `hooks`: `tengu_run_hook.pluginHookCounts` (now).
- `memoryWrites`: `tengu_memdir_file_write` (now).
- Footprint: analytics `tengu_tool_use_*`, `tengu_bash_tool_command_executed`/`_failed` (first word, `destructive_category`, `destructive_target_scope`), `tengu_git_operation`, `tengu_session_linked_to_pr`, `tengu_api_retry` (now).
- `toolCalls`: return `null` until tool inputs are extracted, if the count can't be trusted.

#### 5.2.5 `GET /people/{personId}/timeline` → `TimelineData` (person scope)

- `bin` is chosen by the client from the track width (§11G.A5) and passed explicitly; `auto` remains as a fallback.
- Returns `sessions`, `prompts`, `modelCalls`, `analytics`, `notCaptured`, `presence` and `friction`.
- **No context lane** on the person page.

Presence algorithm (`lib/presence.ts` on the server, or ship the segments ready-made):

1. Per session, the focused periods are the open interval minus the blur windows. Each `tengu_return_to_session` at time t gives a blur window `[t − blurDurationMs, t]`.
2. Per device, sleep windows come from `tengu_event_loop_stall` with `likely_sleep=true`: `[t − clock_jump_ms, t]`. Merge overlapping windows.
3. For each instant, on that device: **asleep** if inside a sleep window; else **focused** if any open session is focused; else **away** if at least one session is open; else no segment.

Session `row` is assigned server-side: greedy packing so that overlapping sessions get different rows.

#### 5.2.6 `GET /people/{personId}/sessions` → `SessionRowData[]`

Newest first. The label falls back through three sources:
1. The latest recap: the `auxiliary` request-class response text. This is model output; it is a **policy flag**, so return `source: 'recap'` only when recaps are allowed by the retention policy.
2. The first prompt text (content).
3. `repo · branch · start time` (extract, from injected git status and `# Environment update` messages).

`capturedFromPrompt` is the lowest `cc_prompt_index` seen when it is greater than 1. The UI shows it as "seen from #12".

#### 5.2.7 `GET /people/{personId}/requests` and `GET /sessions/{id}/requests` → `RequestRowData[]`

- Person scope includes account calls with `sessionId: null` (skills list/downloads, MCP registry/directory, bootstrap, profile, settings, usage, plugins, quota).
- `kind` is detected from the path. `background` is true for `request-class` auxiliary/compaction, for `count_tokens` and for analytics batches.
- Not-captured calls are included as `captured: false` rows, with `requestId: "reported:<tengu requestId>"`, `method: null` and `status: null`.
- `detail` is built server-side:
  - model: `"<request-class> · <model family> · <stop_reason>"`;
  - analytics: `"<n> events"`;
  - flags: `"6-hourly refresh · <n> changed"`.
- Bytes come from `content-length` or summed body chunks (extract; not on rows yet). Return `null` until available.
- Default order: person page `desc` (newest first); session page `asc` (oldest first within the range).

#### 5.2.8 `GET /sessions/{sessionId}` (header) and `/timeline` and `/state`

Header:
```ts
{ sessionId; agent; repo; branch; label; start; end; live; prompts; modelCalls; notCaptured; events;
  device: { deviceId; platform; shell }; networks: number; bypassPct; capturedFromPrompt }
```

Timeline: `TimelineData` with `context`, `compactions`, `permission` and `network` filled in.
- `context`: per main-thread model call, `input + cacheRead + cacheWrite` tokens.
- `compactions`: from `tengu_reactive_compact_succeeded` (from → to tokens, messages summarized, wait) and `tengu_idle_compact_fired`.
- `permission` segments: from `permissionMode` changes.
- `network`: from the connection IP. Mark `bypassed` when a not-captured window overlaps.

State (`?from&to`):
```ts
{ capabilitiesAt: { builtin: { total; used }; mcpServers; mcpTools: { total; used }; skills; hooks; poolChangesInRange: number };
  repoInRange: { branch; headFrom; headTo; commits; pushed: boolean; cwdTrail: string[]; prs: number[] };
  recapsInRange: { t: ISO; text: string | null }[];  recapsTotal: number;
  experimentsAt: { key: string; variation: string }[]; flagFetchesInRange: number; flagsChangedInRange: number;
  counts: { prompts; modelCalls; requests; events; compactions } }       // RangeReadout
```

#### 5.2.9 `GET /sessions/{id}/prompts` and `GET /sessions/{id}/events`

Prompts returns `{ items: ({type:'prompt', prompt: PromptData, steps: StepData[]} | {type:'between', between: BetweenData})[] }`, oldest first within the range.

| Field | Source |
|---|---|
| `PromptData.text` | `tengu_input_prompt` (`prompt_length`, `is_negative`, `prompt_source`) and `tengu_paste_text`/`_image` (now); text is content |
| `steps` grouping | `x-claude-code-prompt-id` (now; promote). Subagent calls share the parent's prompt id and carry `x-claude-code-agent-id` / `agent-type` |
| `inFlight` | Live: a request with no response yet. `StepData.status='streaming'` |
| `eventsPending` | The model call is captured but `tengu_api_success` is not yet joined. Cost shows `—` with "events pending…" |
| Tool calls | Response `tool_use` blocks (name: now; `target` from `input`: extract), joined to `tengu_tool_use_*` by `toolUseID` (now) |
| `friction.interrupted` | `tengu_cancel` + `interrupted_message_id` + `terminal_reason: aborted_streaming`. **One incident, one marker** |
| `friction.frustrated` | `is_negative` |
| `friction.destructive` | `destructive_category` |
| Between rows | Compaction and idle-compaction events; auxiliary recap and suggestion calls without a prompt id |

Events returns `EventRowData[]`, oldest first.
- `internal=false` (the default) hides client internals: `tengu_tether_*`, `wire_*`, `reminder_fold_*`, `declared_tool_set_held`, `render_glyph_cardinality`, `spinner_*`, `tree_sitter_parse_abort`, `bash_ast_too_complex`.
- `collapseTicks=true` (the default) folds consecutive tool-progress ticks into one row with `collapsedCount`.
- The response `meta` carries both `total` (all events in range) and `shown` (after hiding).
- `GET /events/{eventId}` returns the full JSON for the expanded row.

Every event carries `session_id` (100%); 98.5% carry `cc_prompt_id`. Join an event to a request by `requestId` / `clientRequestId`, and to a tool call by `toolUseID`.

#### 5.2.10 Request drawer: `GET /requests/{id}`, `/raw`, `/details`

```ts
export type DrawerKind = 'normal' | 'notCaptured' | 'failed';
export interface RequestDetail {
  requestId: string; kind: DrawerKind; t: ISO;
  method: Unknown<string>; path: string;
  status: Unknown<number | 'ERR'>; durationMs: Unknown<number>; bytesUp: Unknown<number>; bytesDown: Unknown<number>;
  sessionId: string | null; promptIndex: Unknown<number>; stepIndex: Unknown<number>; stepCount: Unknown<number>;
  requestClass: Unknown<string>; prevRequestId: string | null; nextRequestId: string | null;
  keyStrip: { model: Unknown<string>; effort: Unknown<string>; thinking: Unknown<boolean>;
              tokens: TokenCount; cachedPct: Unknown<number>; cost: Cost;
              ttftMs: Unknown<number>; proxyTtftMs: Unknown<number>; streamEvents: Unknown<number>;
              stopReason: Unknown<string>; toolCalls: string[] };
  crossCheck: { matched: boolean; costAgrees: Unknown<boolean> };
  notice?: { tone: 'danger' | 'neutral'; title: string; body: string };   // notCaptured / failed
  retry?: { attempt: number; of: number; backoffMs: number; nextRequestId: string; nextStatus: number };
  sent: ContentRow[]; received: ContentRow[];
  contentRetained: boolean;                       // deployment mode
}
export interface ContentRow { label: string; detail: string; size: string; tone?: 'tool'; body?: string | null } // body only when retained
```

Raw (`/raw`): `{ requestLine, requestHeaders: {name, value, redacted}[], requestBody: string | null, responseLine, responseHeaders: [...], stream: {tOffsetMs, type, detail, count?}[] }`, or `{ unavailable: string }` for notCaptured/failed.
- **Credentials are always redacted server-side**: auth headers, OAuth details, and tokens inside MCP results.

Details (`/details`): `{ composition: TokenCount; sections: { title, accent?: Channel, rows: {k, v, tone?: 'muted'|'ok'}[] }[]; tool: ToolCallDetail[]; timing: { proxy: {label, startMs, durMs}[], client: {k, v}[] }; events: EventRowData[]; fields: {key, value}[] }`.

The 8 sections, in order:
1. Tokens & cost
2. Request settings
3. Thread position
4. What the model could use
5. Response service
6. Account limits at response
7. Network & proxy
8. Cross-check & posture

| Drawer data | Source |
|---|---|
| model, effort, thinking, max_tokens | Request body (now) |
| usage incl. 1h/5m cache split, thinking tokens, service tier, inference geo | Response `usage` (now) |
| ttftMs, durationMs, requestPrepareMs, retries, stalls | `tengu_api_success`, `tengu_api_retry`, `tengu_streaming_stall_summary` (now) |
| prev/next step | `message_id` / `previous_message_id` (now) |
| What the model could use | Session state carried from thread-create (extract) |
| prev tool durations | Header `x-claude-code-prev-tool-durations` (extract) |
| Proxy timing (accept, upstream connect, first byte, streaming) | Envoy/observer transaction timings (now) |
| Network | Client IP, network label, TLS minted cert, upstream verified (now) |
| Content bodies | Retained only in experimentation mode (content) |

`requestId` prefixed `reported:` resolves to a notCaptured detail built from analytics only.

#### 5.2.11 `GET /coverage` and `GET /status`

- Coverage returns `{ reported, captured, windows[], calls: RequestRowData[] }`, where `calls` are the not-captured rows.
- Status returns `{ proxy: 'up'|'down', certIssued: boolean, firstRequestAt: ISO | null, waitingSince: ISO | null, lastIngestAt: ISO | null }`. It drives `WaitingChecklist` and the delayed banner.

### 5.3 Live behavior and polling

- Poll every 5 s while the tab is visible and every 30 s while hidden (`refetchInterval` + `refetchIntervalInBackground`).
- **Pause** stops polling. The `LiveBadge` then shows `PAUSED · N NEW`, where N comes from a cheap `HEAD`-style `?since=` count.
- List endpoints accept `since` and return only newer rows plus `meta.nextSince`. Merge on the client and **never reorder or collapse what the viewer has open**. When the viewer is scrolled away from the edge where new rows arrive, show an "N new" pill instead of inserting rows.
- Data is **delayed** when `now − meta.lastDataAt > 90 s` while the scope is live: show the delayed `Banner` and set `LiveBadge` to `DELAYED · Nm`.
- A failed poll keeps the last data. Show the `refreshFailed` `Banner` with backoff 5 s → 10 s → 30 s and a **Retry now** button.
- Steps fill in after they first appear, because analytics arrive seconds later. Re-fetch open prompts on each poll; `eventsPending` turns false on its own.

### 5.4 Coverage algorithm (server)

1. Collect `tengu_api_success` events (`requestId`, `clientRequestId`, timestamp, tokens, cost) for the scope.
2. Match each to a captured model-call transaction by response `request-id` == `requestId`, else `x-client-request-id` == `clientRequestId`.
3. Unmatched events are not-captured calls. Group them into windows: consecutive unmatched calls with gaps under 15 min form one window, with its count.
4. `coverage.reported = matched + unmatched`; `captured = matched`.
5. Rows from before the parser existed have no analytics. Return `reported: null`; the UI shows `539/—`.

### 5.5 Backend prerequisites (from brief §11; the API depends on them)

1. Session state from thread-create requests (tool catalog, MCP servers, skills, injected git status / cwd / branch, CLAUDE.md and MEMORY.md presence and sizes), carried forward and updated by `tool_addition` and environment-update messages.
2. Tool inputs as size-capped fields (paths, commands, URLs, MCP args), independent of content retention.
3. Identity: the skills-listing owner, flag attributes (role, tier, subscription and first-use dates), and claude.ai account rows joined by `account_uuid`.
4. Promote `x-claude-code-prompt-id`, `cc_prompt_index` and `x-client-request-id` to top-level fields. Materialize the step ↔ analytics join (`requestId`, `clientRequestId`, `toolUseID`).
5. The coverage check per session (§5.4).
6. Byte counts on transaction rows.
7. Recaps as session labels (policy decision).
8. Register the 10 missing event types (`make client-event-registry`).
9. A query API with incremental `since` polling (this contract).
10. IPv6 routing in deployment.

---

## 6. Components

Each entry gives the file, the props (TypeScript), the states (each needs a story), the tokens, the behavior and the data source.
- Canvas specimen sheets: *Foundations, Controls, Chips, Summary, Timeline, Rows, Drawer, States*.
- Shared conventions:
  - Every component accepts `className`.
  - Loading variants are props (`loading`), not separate components, unless noted.
  - Stories live in `Components/<Group>/<Name>`.

### 6.1 Controls

**Button** (`components/Button`)
```ts
type ButtonProps = { variant?: 'default' | 'ghost' | 'icon'; pressed?: boolean; loading?: boolean; disabled?: boolean;
  as?: 'button' | 'a'; href?: string; 'aria-label'?: string; onClick?(): void; children: ReactNode };
```
- States: default, hover, focus, pressed, disabled, loading (spinner plus "…" label, `aria-busy`, disabled), ghost, icon, icon-disabled.
- Look: `label` type on `surface` with a `line-strong` border and `radius-sm`; padding 5/10 (icon 2/8). Ghost is transparent with `ch-model` link color.

**SegmentedControl**
```ts
{ options: { value: string; label: string }[]; value: string; onChange(v: string): void; disabled?: boolean; 'aria-label': string }
```
- States: default, hover, focus, disabled.
- A group of Buttons with hairline separators; the selected one is `surface-raised` with `aria-pressed`.
- Used for the time window: `1h | 24h | 7d | 14d | All` on the person page, `24h | 7d | 14d | All` on People.

**Tabs / Tab**
```ts
{ tabs: { id: string; label: string; count?: number | string; disabled?: boolean }[]; value: string; onChange(id: string): void }
```
- States: default, hover (`line-strong` underline), selected (`ink` text and 2px `ink` underline), focus, disabled.
- `role="tablist"`; arrow keys move between tabs.

**TextInput**
```ts
{ id: string; label: string; placeholder?: string; value: string; onChange(v: string): void; disabled?: boolean; invalid?: boolean; message?: string }
```
- States: empty, focus, filled, disabled, invalid (an `ink` ✕ message linked by `aria-describedby`).
- The label is micro caps to the left. The input uses `data` type on `bg`.
- Filter syntax: free text, or `field=value`. Debounce 250 ms, then write to the URL.

**ToggleChip**
```ts
{ label: string; pressed: boolean; onChange(p: boolean): void; swatch?: Channel; count?: number; disabled?: boolean }
```
- States: off (`line` border, muted), on (`surface-raised`, ✓ suffix), hover, focus, disabled, with-swatch.
- Used for "hide client internals", "collapse ticks" and channel filters.

**Pagination**
```ts
{ page: number; pageCount: number; from: number; to: number; total: number; loading?: boolean; newAfterEnd?: number; onPage(p: number): void }
```
- States: first (Prev disabled), middle (ellipsis windows), last (Next disabled, "end of range"), single page ("all rows shown"), loading (all disabled, "loading…"), live with new rows past the end (an `N NEW ↓` chip).

### 6.2 Chips, badges & markers

**Chip**: `{ tone: 'neutral'|'muted'|'range'|'notRetained'|'selected'; swatch?: Channel; dot?: Agent; children }`.
- Tones:
  - `neutral`: `line-strong` border, ink;
  - `muted`: `line` border, ink-muted;
  - `range`: `focus` border and text;
  - `notRetained`: `line` border, ink-faint;
  - `selected`: `surface-raised`.
- Micro type, `radius-sm`.

**LiveBadge**: `{ status: 'live'|'connecting'|'waiting'|'delayed'|'retrying'|'paused'; intervalS?: number; delayedMin?: number; newCount?: number }`.
- Only `live` uses the `live` color (dot, border, text).
- Every other status is neutral with an `ink-faint` dot; `paused` uses a hollow dot.
- Labels: `LIVE · 5s`, `CONNECTING`, `WAITING`, `DELAYED · 2m`, `RETRYING`, `PAUSED · 12 NEW`.

**AgentChip**: `{ agent: Agent; reserved?: boolean; variant?: 'row'|'header' }`.
- `row` (in tables): mixed case with a `line` border. `header`: caps.
- `reserved`: ink-faint, `· SOON`.

**ChannelSwatch**: `{ channel: Channel }`, an 8px square with `radius-sm`, always next to a label.

**Marker**: `{ kind: 'frustrated'|'interrupted'|'destructive'|'coverageGap'|'match'|'live'; count?: number; label?: string }`.
- Glyphs: ↯, ✕, ▲ (danger), ▲ (danger), ✓ (ink), ● (live).
- With `count` it renders compact (`↯1`, `▲10`).

**StatusCode**: `{ code: number | 'ERR' | null }`. 2xx is `ink-muted`; errors are bold `ink` prefixed with `✕`; `null` renders `—`.

### 6.3 Summary blocks

**StatTile / StatRibbon**
```ts
StatTile: { label: string; value: ReactNode; sub?: ReactNode; swatch?: Channel; href?: string; tone?: 'default'|'gap'; loading?: boolean; unknownReason?: string }
StatRibbon: { tiles: StatTileProps[]; loading?: boolean }
```
- States: default, hover (when `href`), no swatch, unknown, gap (`danger` value with ▲), loading (3 skeleton lines).
- The ribbon joins tiles edge to edge with hairlines. Each tile is `flex: 1 1 160–180px`.

**HeaderStat**: `{ label: string; value: ReactNode; live?: boolean; unknownReason?: string; qualifier?: ReactNode }`. Put several inside `HeaderStatGroup` (left border, hairline separators). States: default, live, unknown, with qualifier (e.g. `640 +94 not captured`).

**CoverageLine**: `{ coverage: Coverage; onView?(): void }`.
- Complete: quiet `✓ coverage complete`, ink-muted.
- Gap: `▲ coverage gap` in danger, a sentence naming each window, and a "view N calls" ghost button.

**Banner**: `{ kind: 'delayed'|'refreshFailed'; since: ISO; retryInS?: number; onRetry?(): void }`.
- Full width under the AppBar, `surface-raised`, with a `line-strong` bottom border.
- Never hides data. `aria-live="polite"`.

**Notice** (drawer): `{ tone: 'danger'|'neutral'; title: string; children }`. `bg` fill with a 1px border in `danger` (coverage gap only) or `line-strong`.

**UnattributedNotice**: `{ sessions: number; modelCalls: number; firstSeen: ISO; reason: string; href: string }`.

**AgentCard**: `{ agent: Agent; people: number; summary: string; shape: string; selected: boolean; onClick(): void }`. States: default, hover, selected (`ink` border), focus. It is a `<button aria-pressed>` that filters the People table.

**KeyValueList**: `{ title: string; accent?: Channel; action?: { label: string; href: string }; rows: { k: string; d?: string; v: ReactNode }[]; footer?: string; loading?: boolean }`. Used for capabilities, footprint and the session state panels.

**Sparkline**: `{ values: number[]; color: Agent | Channel; height?: 22; 'aria-label': string }`. Bars are flexed; zero bins render 1px in `line`. States: active, sparse, no data.

**InputCompositionBar**: `{ tokens: TokenCount }`. A stacked bar (cache read 55% opacity, uncached solid, cache writes outlined) with a legend line.

### 6.4 Timeline

**Timeline** (container)
```ts
{ from: ISO; to: ISO; now?: ISO | null; selection?: { from: ISO; to: ISO } | null; onSelect?(r: {from: ISO; to: ISO} | null): void;
  loading?: boolean; header?: ReactNode; legend?: LegendItem[]; children: ReactNode /* Lane[] */ }
```
- Owns the x-scale (`scaleTime(from, to) → %`) through React context, so tracks only read `useTimeScale()`.
- Renders a 132px label column, the tracks, a `TimeAxis` with ticks and gridlines (§11G.A6) and the `Brush` overlay. No legend (§11G.A7).
- The `NowLine` (1px `live`) is drawn when `now` falls inside the range.
- Wide content scrolls horizontally inside the panel (minimum 960px).

**Lane**: `{ label: string; sublabel?: string; height: number; children }`.

Tracks (all read the scale from context):

| Track | Props | Look |
|---|---|---|
| `BarTrack` | `bins: {t, v}[]; color: Channel; gaps?: {from,to,count}[]; maxV?` | Bars ~80% of the bin width. Gaps are hatched (`danger` at 30%, 135° stripes) labeled `▲ N reported, not captured`. Auxiliary bars use `line-strong` |
| `DensityTrack` | `bins; color` | Same as BarTrack but 85% opacity and no gaps |
| `BandTrack` | `bands: {from,to,label,row,live,preCapture?}[]; inner?: (band) => ReactNode` | Bands 26–30px tall on `surface-raised` with a 2px `agent-*` left edge and a mono 10.5px label. `preCapture` gives a faded left gradient. Live bands carry a ● dot. `inner` is used **only on the session page** (context bars) |
| `StateTrack` | `segments: {from,to,state,label?}[]; palette` | Presence: focused = `ink` fill with `bg` text; away = `line`; asleep = `ink-faint` fill with `bg` text (§11F.3); none = blank. Permission and network reuse it with outlined segments, a `focus` tick for mode changes, and `danger` text for a bypassed network |
| `MarkerTrack` | `markers: {t, glyph?, tone?}[]; labels?: {t, text}[]` | Prompt ticks are 1px ink, 16px tall; friction uses glyphs |

**Brush**: `{ value: {from,to} | null; onChange; mode: 'idle'|'range'|'live' }`.
- Drag on any track to select; dragging the 8×22 `focus` handles resizes.
- Outside the selection is dimmed with `bg` at 45%. The selection fill is `focus` at 8%.
- The keyboard model is in §3.4.
- `live` mode pins the right edge to now (following).

**TimeAxis**: `{ ticks?: number }`. Uses day or hour labels depending on the span.

**Legend**: `{ items: { swatch: ReactNode; label: string }[] }`.

**RangeReadout**: `{ mode: 'range'|'live'|'empty'; range: {from,to}; counts?: {prompts, modelCalls, requests, events, compactions}; onPrev; onNext; onClear; nearest?: {earlier?: ISO; later?: ISO} }`.
- Attached under the timeline: a `focus` 1px border on `surface`.
- `live` disables "›". `empty` shows nearest-activity buttons.

Loading: `Timeline loading` keeps the lane labels and heights and fills the tracks with skeleton.

### 6.5 Tables & rows

**DataTable**
```ts
{ columns: { id: string; header: string; width?: number | 'flex'; align?: 'left'|'right' }[]; minWidth: number;
  children: ReactNode; footer?: ReactNode; loading?: boolean; skeletonRows?: number; empty?: ReactNode }
```
- `surface` box with a micro-caps header row, `line` hairlines, and horizontal scroll under `minWidth`.
- `loading` renders skeleton rows matched to the column widths.

**PersonRow** (`<a href="/people/:id">`): `PersonSummaryRow` + `href`.
- Columns: Person (name in title-ish Sans 13/600, with ids in mono 11 faint) · Agents (AgentChip[]) · Last seen · Sessions · Prompts · Tokens · Activity (Sparkline 14d) · Bypass · Coverage · ›.
- States: default, hover, focus, live (● now in `live`), coverage gap, multi-agent with estimated tokens (`≈`), no token counts (`—`), unidentified ("name unknown"), loading.

**SessionRow** (`<a href="/sessions/:id">`): `SessionRowData`.
- Columns: Start · ● · Repo · branch · Label · Prompts · Span · Calls · Events · Friction (Marker compact) · Bypass · Coverage · ›.
- States: live with gap, ended, hover, fields missing (pre-parser: `—` and the label "identity and repo not parsed"), no recap (repo label), seen-from (`SEEN FROM #12` muted chip).

**RequestRow** (`<button>`, opens the drawer): `RequestRowData` plus `showSession`.
- Columns: Time · # (prompt) · Kind (colored by ChannelSwatch color) · Session (person scope only) · Method · Path · detail · Status · Duration · Up · Down.
- States: model, background (quiet row, §11F.1), flags, count, account (session `— no session` faint), error (StatusCode ERR), not captured (`▲ reported by Claude Code, not captured`, no method or bytes), selected.

**EventRow** (`<button>`, toggles expansion): `EventRowData`.
- Columns: Time · # · Event name (`ch-analytics`) · Linked to · Fields · marker.
- States: default, collapsed ticks (quiet row, `×N`), marked (▲), selected/expanded (a `bg` `<pre>` of the full JSON, lazy-loaded from `/events/:id`).

**PromptRow** (`<button aria-expanded>`): `PromptData`.
- Text shows a quoted prompt when retained, otherwise its shape plus `NOT RETAINED`.
- States: collapsed, expanded (`surface-raised`, followed by a detail line: chars · pasted · images · repo dirty · ttfvt), in flight (● duration), interrupted (✕), frustrated (↯).

**StepRow** (`<button>`, opens the drawer): a `StepData` model row, followed by indented `ToolCallRow`s.
- States: model done, tool, tool marked (▲ destructive), tool running (● running Ns), streaming (● streaming · N tokens so far), events pending ("cost — · events pending…"), subagent (collapsed ⤷ row, expandable), auxiliary (0.55).
- Indent: model 40px, tool 70px (100px inside an expanded prompt).

**BetweenRow**: `BetweenData`. 0.6 opacity; compaction text in `ch-count`, recap in ink-muted.

### 6.6 Request drawer

**RequestDrawer**
```ts
{ requestId: string | null; onClose(): void; onNavigate(id: string): void; tab: 'summary'|'raw'|'all'; onTab(t): void }
```
- 820px (`max-width: 100%`) right dialog over a `bg` 60% scrim; it scrolls internally.
- Fetches `/requests/:id` immediately; `/raw` and `/details` load only when their tab is opened.
- URL: `?req=<id>&dtab=summary`.
- Shows an inline skeleton while loading and an `ErrorState` (compact) on failure.

Parts:

| Component | Props | States |
|---|---|---|
| `DrawerHeader` | `kind, method, path, status, sizes, onPrev, onNext, onClose, onCopyCurl, onCopyLink` | normal (muted status), notCaptured (danger `▲ reported by Claude Code · not captured`), failed (`✕ ERR · connection reset after 0.6s · retried`) |
| `DrawerMeta` | `t, promptIndex, step, requestClass, requestId` | one muted mono line |
| `KeyStrip` / `KeyStripCell` | 6 cells `{label, value, sub}` | values; unknown (`—` with a reason in the sub) |
| `DrawerTabs` | Summary · Raw · All details | Raw relabels to `Raw · none` or `Raw · request only` |
| `SummaryView` | context line, optional `Notice`, Sent ContentRows, Received ContentRows, footer | normal, notCaptured, failed, retained |
| `ContentRow` | `ContentRow` | shape only, tool call (`ch-skills` label), retained (`bg` `<pre>` body; diffs prefixed `-`/`+`) |
| `RawView` | `/raw` payload | headers, body (pretty/raw toggle), stream events; unavailable message |
| `DetailsView` | `/details` payload | InputCompositionBar, a 2-column grid of `DetailSection`s, the Tool block, `TimingWaterfall`, events, `FieldList` (filterable) |
| `DetailSection` | `title, accent, rows` | default, with ✓ check rows |
| `HeaderList` | `headers` | default, redacted (`[redacted · key_hash …]` faint) |
| `StreamEventList` | `stream` | default |
| `TimingWaterfall` | `phases` | proxy phases (accept, connect, first byte, streaming, total) + a Claude Code timings list. **To scale** (§11D) |
| `FieldList` | `fields, filter` | default, not extracted (`— not on rows yet`) |

### 6.7 Page chrome & states

**AppBar**: `{ breadcrumb: { label: string; href?: string }[]; agents?: { agent: Agent; reserved?: boolean }[]; window?: SegmentedControlProps; live: LiveBadgeProps; onPause?(): void; actions?: ReactNode }`. States: default, loading (`CONNECTING`, window disabled).

**PageHeader**: `{ title: string; subtitle?: string; stats?: ReactNode; posture?: ReactNode }`.

**Skeleton**: `{ shape: 'line'|'block'|'row'|'lane'; width?; height? }`. A static `skeleton` fill with no shimmer.

**EmptyState**: `{ chip?: ReactNode; title: string; body?: string; actions?: ReactNode }`. Variants: empty range, no filter matches.

**ErrorState**: `{ title: string; body: string; meta?: string; onRetry(): void; back?: { label: string; href: string }; compact?: boolean }`.

**WaitingChecklist**: `{ steps: { status: 'done'|'active'|'pending'; title: string; detail: string }[]; checkedAt: ISO }`. Glyphs: ✓ ink, ● live, ○ faint.

**AboutThisData**: a 640px right-side drawer (spec in §11E; canvas `AboutThisData.dc.html`). It opens from the AppBar's "About this data" button and from any NOT RETAINED chip.

---

## 6A. Interaction, feedback & loading choreography (mandatory)

**The bar for this section:** at every moment after any click, key press or drag, the viewer can tell, without guessing:
1. that the input registered;
2. that something is in progress, and where;
3. what they can and cannot do meanwhile;
4. when it finished, or that it failed and what to do next.

A UI that looks correct when static but goes silent after a click **fails acceptance**, even if the data eventually renders correctly.

Canvas reference: Components page → sheet **"Interaction & loading"**.

### 6A.1 Timing rules

| Elapsed since the input | Required feedback |
|---|---|
| **0 ms, same frame** (on `pointerdown` / `keydown`, not on `click`) | The pressed or selected visual state is applied. Tabs, toggles, segmented options, agent cards, sort and filters switch their **selected state immediately** (optimistic), before any data returns |
| **0 ms** | The trigger enters `pending` if it started a request (§6A.2) |
| **0–150 ms** | Nothing else. Do **not** flash a skeleton or spinner for fast responses |
| **≥ 150 ms** | The region-level indicator appears: a progress bar plus dimmed content for refetches (§6A.3), or a skeleton for first loads |
| **once shown** | Indicators stay visible **at least 400 ms**, so they never flicker on and off |
| **≥ 8 s** | The indicator adds a "Still loading…" line with the reason and size ("2 days, 21,305 events") and a **Cancel** button |
| **≥ 30 s** | Abort. The region shows `InlineError` (§6A.6) with **Retry**. Existing data stays |

Implement these once:
- `useDelayedFlag(pending, { delay: 150, minVisible: 400 })` drives every spinner, skeleton, progress bar and dim.
- `useSlowFlag(pending, 8000)` drives "Still loading…".
- The 30 s timeout is enforced with an `AbortController`.

### 6A.2 Pending triggers (buttons and anything clickable that starts work)

Every control that starts a request or navigation implements this lifecycle. It is built into `Button`, and `ToggleChip`, `Tab`, `SegmentedControl` options, `AgentCard`, `Pagination` buttons and row buttons reuse it through a `usePendingAction()` hook.

| State | Visual | Behavior |
|---|---|---|
| idle | normal | — |
| pressed (pointer down) | `surface-raised`, 1px inset (translateY 1px) | — |
| **pending** | Label changes to its gerund ("Retry now" → "Retrying…", "Copy as curl" → "Copying…"). A 12px spinner replaces the leading glyph, or is prepended. `aria-busy="true"`, `aria-disabled="true"`, cursor `progress` | **Ignores further clicks** (no double submit); keyboard activation is ignored too. Width is locked to the idle width (`min-width` measured on mount) so the layout doesn't jump |
| success | For actions with no visible result (copy, retry that returns identical data): a ✓ and the past tense ("Copied ✓", "Updated ✓") for 1.5 s, then idle. For actions with a visible result (open drawer, change page): straight back to idle the moment the result renders | — |
| failure | Back to idle. The error appears **next to the control or in the affected region** (§6A.6), never only in the console | Focus stays on the trigger |

Rules:
- A control is disabled only while **its own** action is pending, or while a conflicting action is pending (listed per action in §6A.4). Never freeze the whole page.
- **Never disable silently.** Every disabled control uses `aria-disabled` (not `disabled`, so it stays focusable) and has a reason in a `Tooltip` (§6B), e.g. "Loading page 3…" or "Following live: no later range".
- Rows that open the drawer or navigate show `surface-raised` with a 2px `focus` left inset bar on the clicked row while pending, so the person sees *which* row they clicked.

### 6A.3 Region loading: first load vs refetch

A **region** is any block that renders one query: ribbon, header, timeline, each panel, each table, the drawer body.

| Situation | What renders |
|---|---|
| **First load** (no data yet for this key) | A skeleton matched to the final layout (`Skeleton`; sizes in §6.7 and the canvas "loading" artboards). Same height as the loaded content, so nothing shifts. `aria-busy` on the region |
| **Refetch after a viewer action** (range, filter, sort, page, tab with cached data) | **Keep the previous content.** Dim it to 55% opacity with `pointer-events: none`, and run a 2px indeterminate `ProgressBar` along the region's top edge (`ink-muted`). Fades in after 150 ms (§6A.1). Never replace existing content with a skeleton |
| **Tab or page with no cached data** | Skeleton rows *inside the table body*. Headers, filters and pagination stay visible; pagination shows the target page as pending |
| **Background poll** (5 s live refresh) | **No indicator.** New rows animate in (§6A.7), numbers update in place, and the `LiveBadge` dot does a single 300 ms opacity blip on each successful poll as its heartbeat |
| **Settled, empty** | `EmptyState`, **only after the query succeeded**. Never show "No activity" while a query is in flight or failed |
| **Settled, error** | Keep the stale content if there is any, with an `InlineError` bar at the top of the region. With no content, show `ErrorState` in the region's footprint |

Independent regions load independently:
- Don't gate the page on its slowest query. The AppBar renders immediately with the breadcrumb; the header, ribbon, timeline, panels and table resolve as their own data arrives.
- Exception: the person and session page **headers** gate their own name and breadcrumb text only. The breadcrumb shows a skeleton word until then.

A **global top `ProgressBar`** (2px, `ink-muted`, fixed to the top of the viewport) runs during **route changes** until the destination page's header query settles. It follows the same 150 ms / 400 ms rule.

### 6A.4 Action-by-action specification

**Global**

| Action | Immediate (0 ms) | While pending | Locked while pending | Done | Failure |
|---|---|---|---|---|---|
| Change time window (SegmentedControl) | The new option is selected; the URL updates | Every range-scoped region refetches (§6A.3, keep and dim); the global top bar runs | The window control shows the target as pending; another window click **cancels and replaces** the request (latest wins) | Regions undim together as each resolves | Per-region `InlineError`; the window stays on the new value with a "Retry" in the error |
| Pause | The badge turns `PAUSED` immediately | — | — | Polling stops; the badge counts new arrivals (`PAUSED · 12 NEW`) by a cheap count poll | — |
| Resume | The badge reads `LIVE` with its heartbeat | One fetch with `since`; new rows animate in | — | — | `RETRYING` badge plus Banner |
| Retry now (Banner / InlineError) | Button pending: "Retrying…" | Region progress bar | The Retry button itself | Banner/error disappears with a 150 ms fade; "Updated ✓" if the content didn't visibly change | Button back to idle; the banner text updates to "Still couldn't refresh · 11:32:10" |
| Copy link / Copy as curl | Pending "Copying…" (curl needs `/raw`) | — | That button | "Copied ✓" for 1.5 s | "Couldn't copy" inline for 3 s |
| About this data | Opens immediately (static content) | — | — | — | — |
| Browser back/forward | The URL state applies immediately | As for the equivalent action | — | Scroll position restored | — |

**People page**

| Action | Immediate | While pending | Locked | Done | Failure |
|---|---|---|---|---|---|
| Click AgentCard | The card turns selected (`ink` border); the filter chip label updates | The table refetches (keep and dim). The count chip shows `… PEOPLE` until resolved | Other cards stay clickable (latest wins) | — | Table `InlineError` |
| Type in Search | Characters appear; a debounce of 250 ms | After the debounce, the table refetches (dim). A 12px spinner sits inside the input's right edge | — | — | `InlineError` |
| Click PersonRow | The row shows pending (inset bar); global top bar | Route change → the person page renders its skeletons | Other rows are clickable; the last click wins | Focus moves to the person `h1` | The person page renders `ErrorState` |

**Person page**

| Action | Immediate | While pending | Locked | Done | Failure |
|---|---|---|---|---|---|
| Brush drag (pointer down → move) | The selection draws live under the pointer at 60 fps; the readout updates live. **No fetch while dragging** | — | — | — | — |
| Brush release | The selection is committed to the URL; range chips update | 200 ms debounce, then the ribbon, footprint, capabilities and tabs refetch (keep and dim) | Starting another drag cancels in-flight range queries | — | Per-region `InlineError` |
| Brush Esc / Clear range | The selection is removed immediately | As above, back to the whole window | — | — | — |
| Click a Tab (Sessions / All requests) | The tab is selected; its panel shows **instantly** (from cache if fetched, otherwise skeleton rows) | Skeleton rows or dim | — | — | `InlineError` in the panel |
| Click SessionRow | Row pending; global top bar | Route change | — | Focus `h1` | `ErrorState` |
| Click RequestRow | Row pending | The **drawer opens immediately** (slide-in, §6A.7) with a skeleton body. It does not wait for data | — | The skeleton is replaced by content (no slide again) | Drawer body shows a compact `ErrorState` with Retry; the header shows what is known from the row |
| Channel ToggleChips / filter input | The chip is on/off immediately | Table refetch (dim) | — | — | `InlineError` |
| Pagination click | The target page button is pending (`aria-current` moves immediately), Prev/Next disabled with the tooltip "Loading page N…" | Table body dimmed with a progress bar, or skeleton rows if not cached. **Prefetch the next page** after each page settles | All pagination buttons until settled | The table scrolls so its header is at the top of the viewport, if it was scrolled away | Pagination back to the previous page; `InlineError` with Retry |
| "view N calls" (coverage) | Button pending | The All requests tab opens filtered to `captured=false` | — | — | — |

**Session page**

| Action | Immediate | While pending | Locked | Done | Failure |
|---|---|---|---|---|---|
| Brush release / ‹ › / Whole session | The range commits; the RangeReadout shows the new range text immediately; counts show `…` | `/state`, `/prompts`, `/requests` and `/events` refetch (keep and dim), each region independently | While following live, `›` is disabled with the tooltip "Following live: no later range" | Counts fill in | Per-region errors |
| Expand a PromptRow | The disclosure turns `▾` and the row selects immediately | Steps come inline from `/prompts`, so there is no wait. If lazily loaded: 2 skeleton step rows, then the content | — | — | One inline error row: "Couldn't load steps · Retry" |
| Expand a subagent row | As above | 1–3 skeleton rows | — | — | Inline error row |
| Click StepRow | Row pending; the drawer opens immediately with a skeleton | As for RequestRow | — | — | As for RequestRow |
| Expand an EventRow | The row selects; a `<pre>` area opens at **its final height of 6 lines** showing a skeleton | Lazy `/events/:id` | — | The JSON replaces the skeleton | "Couldn't load fields · Retry" inside the area |
| Events toggles (internals, ticks) | The chip changes immediately; "N in range · M shown" shows `…` for M | Table refetch (dim) | — | — | `InlineError` |

**Request drawer**

| Action | Immediate | While pending | Locked | Done | Failure |
|---|---|---|---|---|---|
| Open | Scrim fade 150 ms + panel slide-in 220 ms. The header renders from the row data the viewer clicked (method, path, status), so it is **never blank**; the key strip and body show skeletons | `/requests/:id` | — | Focus moves to the drawer heading | Body `ErrorState` (compact) with Retry |
| Switch to Raw / All details | The tab is selected immediately | First visit: a skeleton in the tab body (lazy query); later visits are instant from cache. **Prefetch `/raw` on hover of the Raw tab** | — | — | Tab-body `ErrorState` |
| ↑ / ↓ (prev/next request), or `[` / `]` | The header updates immediately from the neighbor row data; the drawer stays open (no slide) | The body dims with a progress bar (keep and dim), then swaps. Neighbors are prefetched | Arrow buttons at the ends are disabled with tooltips "First request in range" / "Last request in range" | The list row highlight follows the drawer | Body `InlineError` |
| Close (×, Esc, scrim click) | Slide-out 150 ms; focus returns to the originating row | — | — | — | — |

### 6A.5 Stale-request rules

- Every query key includes all its parameters. A new parameter set **cancels** the previous in-flight request (`AbortController`) or ignores its result. **Out-of-order responses must never overwrite newer state.**
- Rapid repeated input (window clicks, typing, arrow keys in the drawer): the latest wins and intermediate requests are cancelled.
- Use `keepPreviousData` / `placeholderData: keepPreviousData` for every range-, filter- or page-driven list. This is what makes "keep and dim" possible.

### 6A.6 Errors at the point of action

- **No toasts** for errors. An error appears where the work was supposed to land:
  - `InlineError` for a region with stale content;
  - a compact `ErrorState` for an empty region;
  - the Banner only for global polling failures.
- `InlineError`: `{ message: string; queryId?: string; onRetry(): void }`. A 1-line bar on `surface-raised` with a `line-strong` border, `✕ <message> · <queryId>` and a **Retry** Button (with its own pending lifecycle).
- Validation errors (filter syntax) show under the input immediately on blur or after the debounce, with the `invalid` state. The table keeps its last valid results and does not refetch.

### 6A.7 Motion

Add these tokens to `tokens.css`:

```css
--dur-instant: 0ms; --dur-fast: 80ms; --dur-base: 150ms; --dur-slow: 220ms;
--ease-out: cubic-bezier(.2, 0, 0, 1); --ease-in: cubic-bezier(.4, 0, 1, 1);
```

| Element | Motion |
|---|---|
| Hover, pressed background | `--dur-fast` background-color |
| Dim on refetch, undim | opacity over `--dur-base` (fade in after the 150 ms delay) |
| ProgressBar | Indeterminate: a 30%-wide segment sliding left → right in 1.2 s linear, infinite. Fades in and out over `--dur-base` |
| Skeleton → content | Content fades in over `--dur-base`. No shimmer, no pulse |
| Drawer | Open: `transform: translateX(100%) → 0` over `--dur-slow` `--ease-out`; scrim opacity 0 → 0.6 over `--dur-base`. Close: `--dur-base` `--ease-in` |
| Expand/collapse rows | Height animates over `--dur-base` when under 400px; otherwise instant |
| New rows (live append, or rows that are new since the last view) | Insert without moving rows the viewer is reading (§5.3). Each new row starts on `surface-raised` and fades to `surface` over 1.2 s (`--new-row-flash`) |
| Numbers that change on poll | Update in place (tabular figures, so no width jitter). No count-up animation |
| LiveBadge heartbeat | Dot opacity 1 → 0.4 → 1 over 300 ms once per successful poll |
| Brush | Follows the pointer with no easing; on keyboard nudge, 80 ms |
| Streaming step "● streaming · N tokens" | The token count updates as data arrives. The ● dot pulses (opacity 1 ↔ 0.4, 1 s) only while streaming |

`prefers-reduced-motion: reduce`:
- the drawer appears and disappears with no slide;
- the ProgressBar is a static 2px bar at 100% with `ink-muted`;
- there is no row flash (use a 2px `focus` left bar for 1.2 s instead);
- there is no pulse;
- all durations except opacity fades become 0.

### 6A.8 Layout stability (no jumps)

- Skeletons and empty and error states occupy the **same height** as the loaded region. Use fixed lane heights; tables reserve `pageSize` × row height (or the current row count when dimming).
- Table column widths are fixed by `columns[].width`; they don't reflow between loading and loaded.
- Buttons lock their width while pending (§6A.2).
- Banners push content down only once, at the moment they appear; their height is constant (32px).
- The drawer is an overlay: opening it never reflows the page underneath.

### 6A.9 Scroll and focus

- Route change: scroll to top and focus the page `h1`. Back/forward restores the previous scroll position (React Router `ScrollRestoration`).
- Page change in a table: if the table header is off-screen, scroll it to the top of the viewport (with smooth scroll unless reduced motion).
- Drawer open: focus goes to the drawer heading and focus is trapped inside. Close: focus returns to the originating row. The list keeps its scroll position underneath.
- Live append while the viewer is scrolled away from the insert edge: show `NewRowsPill` ("12 new ↓" or "3 new ↑", `aria-live="polite"`) pinned to the table edge. Clicking it scrolls to the new rows and clears the count.
- Keyboard: `Esc` closes the drawer or clears the brush; `[` / `]` go to the previous/next request in the drawer; `/` focuses the filter input on list pages.

### 6A.10 New components this section adds

| Component | Props | States |
|---|---|---|
| `ProgressBar` | `{ active: boolean; scope: 'global' \| 'region' }` | hidden, indeterminate, reduced motion |
| `RegionState` (wrapper) | `{ status: 'loading' \| 'refetching' \| 'error' \| 'empty' \| 'ready'; skeleton: ReactNode; empty?: ReactNode; error?: {message, queryId, onRetry}; slow?: { reason: string; onCancel(): void }; children }` | first load (skeleton), refetching (dim + bar), slow (+ "Still loading…" + Cancel), error with stale data (InlineError on top), error empty (ErrorState), empty, ready |
| `InlineError` | `{ message; queryId?; onRetry }` | default, retrying |
| `NewRowsPill` | `{ count: number; direction: 'up' \| 'down'; onClick }` | hidden, visible |
| `SlowNotice` | `{ reason: string; onCancel(): void }` | default |
| `CopyButton` | `{ getText(): Promise<string> \| string; label: string }` | idle, copying, copied, failed |

All of them are on the "Interaction & loading" canvas sheet. **`RegionState` is mandatory**: every query-backed region renders through it, so these rules can't be skipped region by region.

### 6A.11 Storybook and tests for interaction

- **Latency toolbar** in Storybook (a global `parameters.latency`, read by the MSW handlers): `instant | fast (120 ms) | normal (600 ms) | slow (9 s) | timeout (35 s) | error (500) | offline`. Every screen story must be reviewed under `normal`, `slow` and `error`.
- **Interaction tests** (`play` functions plus Vitest) that must exist and pass:
  1. Clicking `Retry now` puts the button in `aria-busy` within the same frame; a second click is ignored (exactly one request).
  2. Changing the time window keeps the old table content (dimmed) and never renders `EmptyState` while pending.
  3. A response under 150 ms never renders a skeleton or progress bar (assert it is absent at 100 ms and after settling).
  4. A response of 200 ms shows the progress bar for at least 400 ms in total.
  5. Two quick window changes: only the second result renders, even if the first resolves later.
  6. The drawer opens with the header already populated from row data before `/requests/:id` resolves.
  7. Pagination: during loading, buttons are disabled with tooltips; on failure, `aria-current` returns to the previous page.
  8. At 8 s, "Still loading…" with Cancel appears; Cancel restores the previous content.
  9. Under reduced motion, the drawer has no transform transition.
  10. A live append while scrolled away shows `NewRowsPill` and does not move the visible rows (assert `scrollTop` and the first visible row id are unchanged).


## 6B. Hover, tooltips, linked highlighting & keyboard (mandatory)

Canvas: **CompTooltip**, **CompTimelineHover**, **CompTableHover** (Components page). Every hover and tooltip state is drawn open there as a static specimen with its own `data-spec` (`Tooltip/*`, `Lane/*Hover`, `*/hoverLinked`, `*/focus`), so it can be measured like everything else.

Content rules still apply: no "your"; unknown is `—` plus the reason; color only for reserved meanings (channel, live, danger, range/focus); numbers use the §11A/§11B formats in the visible cell and **exact values** in tooltips (thousands separators, ms, bytes).

### 6B.1 `Tooltip`

```ts
type TooltipRow = { key: string; value: ReactNode; tone?: 'danger' | 'live' | 'muted' };
type TooltipProps = {
  content: { title?: string; rows?: TooltipRow[]; body?: ReactNode; hint?: string };
  kind?: 'mark' | 'cell' | 'truncate' | 'disabled';   // sets delays (§6B.2) and max width
  placement?: 'top' | 'bottom';                        // default top; flips and shifts automatically
  anchor?: 'element' | 'mark';                          // 'mark' = chart marks (snap to the mark, not the pointer)
  children: ReactElement;                               // the trigger; must be focusable or in a roving group
};
```

| Part | Spec |
|---|---|
| Surface | `--surface-raised`, 1px `--line-strong`, radius 2px, shadow `--shadow-pop` (new token) |
| Box | padding 8px 10px; `width: max-content`; max-width 360px (`truncate`: 480px); no arrow; z-index above the drawer scrim |
| Title | optional; Plex Mono 12.5/18, 600, `--ink`; 4px below |
| Rows | CSS grid `88px 1fr`, column gap 12px; key = micro caps (`.k`, 10.5px, `--ink-faint`, line-height 18px); value Plex Mono 12/18 `--ink`; `tone` colors only reserved meanings |
| Body | optional free text, Plex Mono 12/18 |
| Hint | optional, what a click does; 11/16 `--ink-faint`; hairline `--line` above; 6px margin and padding |
| Unknown | value `—` then two spaces and the reason in `--ink-muted` |
| Placement | 8px from the anchor, centred; flip to the other side when it doesn't fit; shift along the edge to stay 8px inside the viewport. Use Floating UI (`@floating-ui/react`: `offset(8)`, `flip()`, `shift({padding: 8})`). Charts: anchor to the hovered mark's box (bin column, band, tick), so the box moves bin to bin, not with the pointer; timeline tooltips sit above the timeline |
| Never | buttons, links or inputs inside a tooltip; images; scroll |

Variants on the canvas: `Tooltip/plain`, `/keyValue`, `/unknownValue`, `/disabledReason`, `/truncated`, `/columnHeader`, `/anchoredTop`, `/flippedBottom`, `/chartAnchored`, `/full`.

**Native `title` is removed everywhere** (lint: `react/forbid-dom-props` for `title`; the only exception is `document.title`). Every former use maps to a variant:

| Former `title` use | Becomes |
|---|---|
| Unknown `—` reason | `Tooltip` cell, rows `[{key, value: '—  reason'}]` |
| Disabled control reason | `kind="disabled"`, `body` = reason; control uses `aria-disabled` |
| Truncated text | `kind="truncate"`, `body` = full value; only attached when `scrollWidth > clientWidth` (check on hover) |
| Column header definition | `title` + `hint` = definition (§6B.6 copy) |

### 6B.2 Timing

| Kind | Open | Neighbour (skip delay) | Close | Hoverable |
|---|---|---|---|---|
| `mark` (bins, bands, ticks, segments, hatch, context bars) | 120 ms | 0 ms if another tooltip closed < 300 ms ago | 80 ms | no; follows the mark |
| `cell` (cells, headers, chips, stats, crumbs, LiveBadge) | 400 ms | 0 ms within 300 ms | 100 ms | yes: the pointer can move into it; text selectable |
| `truncate` | 400 ms, only when truncated | 0 ms within 300 ms | 100 ms | yes |
| `disabled` | 400 ms | — | 100 ms | yes |
| Brush drag readout | on drag start | — | on release | no |
| Keyboard focus | 0 ms | — | on blur or Esc | — |

Fade: opacity 0→1 over `--dur-fast`; none under reduced motion. One `TooltipProvider` at the app root owns the skip-delay timer, so only one tooltip is open at a time.

### 6B.3 Accessibility

- Tooltip: `role="tooltip"` with an id; the trigger gets `aria-describedby` while it is open. No live regions, except `RangeReadout` (`aria-live="polite"`) which already announces brush changes.
- Focus shows the tooltip at once. **Esc** closes it and keeps focus; a second Esc runs the element's own Esc action (clear the brush, close the drawer).
- Touch: tap a mark → tooltip; tap again → click result. Long-press a cell → tooltip.
- Disabled controls: `aria-disabled="true"`, focusable, click is a no-op, tooltip explains why.

### 6B.4 Timeline hover (person and session pages)

**Guide.** Pointing anywhere on a track draws `TimelineGuide`: a 1px `--ink-muted` vertical line through every lane down to the axis, plus an axis readout chip (`--ink` background, `--bg` text, 11/16 mono, radius 2px, centred on the guide; `TimeAxis/withReadout`). The guide snaps to the centre of the bin under the pointer; on marker lanes it snaps to a marker within 6px. The guide and readout appear with no delay; the mark's tooltip follows the `mark` timing. Axis labels themselves have no hover.

| Trigger | Hover visual | Tooltip (exact copy; fields) | Click / Enter | Specimen |
|---|---|---|---|---|
| Model-call bin | bin column `--surface-raised` behind the bars + guide | title `Nov 3 10:00–10:30` (bin range); rows `Model calls` `38` / `main 31 · subagent 5 · auxiliary 2` (`byClass`); `Tokens` `1.2M in → 41k out` (totals format); `Not captured` `0` or `▲ N` danger; hint `Click to select this half hour · drag to select a range` (bin length in words) | Range = that bin | `Lane/modelCallsHover`, `Tooltip/barBin` |
| Not-captured hatch | 1px `--danger` outline on the window | title `▲ 57 calls not captured`; `Window` `Nov 3 15:02–15:48 · 46m 0s`; `Reported` `57 by Claude Code`; `Captured` `0 by the proxy`; `Why` `cause` or `—  reason not determined`; hint `Click to list these calls (Requests · not captured)` | Range = window; Requests tab with the not-captured filter | `Lane/notCapturedHover`, `Tooltip/notCaptured` |
| Analytics bin | column + guide | title bin range; `Events` `412`; `Top` three `name count` lines (`top`); hint `Click to select this half hour` | Range = bin | `Lane/analyticsHover`, `Tooltip/densityBin` |
| Session band (person) | band `--skeleton` fill + 1px `--line-strong` inset outline; cursor pointer | title = label; `Repo` `repo · branch`; `Span` `Oct 23 17:00 → 20:30 · 3h 30m` (`→ now` when live); `Prompts` `8 · 282 calls`; `Status` `ended` or `● live` (live green); hint `Click to open the session` | Navigate to the session page | `Lane/sessionsHover`, `Tooltip/sessionBand` |
| Context bar (session) | that bar `--ink` + faint column | title `Call 10:42:16 · opus`; `Context` `412.3k tokens · 41% of 1M` (single-call format; `windowTokens`); `Thread` `main`; `Prompt` `#19`; hint `Click to open this request` | Open the drawer for `requestId` | `Lane/contextHover`, `Tooltip/contextBar` |
| Compaction label | resting dotted underline | title `Compaction · Nov 3 11:18`; `Tokens` `905k → 21k`; `Messages` `2,207 summarized`; `Wait` `1m 11s`; hint `Click to jump to it in Prompts` | Prompts tab, scroll to that `BetweenRow` | `Tooltip/compaction` |
| Presence segment | 1px `--ink-muted` outline | title state (`Away`); `Span` `Nov 3 12:40 → 14:20 · 1h 40m`; `Source` `focus events · this session` or `device sleep · this device` | Range = segment | `Lane/presenceHover`, `Tooltip/stateSegment` |
| Permission tick | tick widens 3→5px | title `Permission changed ×2 in 20 s`; one row per change: key `15:22:08`, value `bypassPermissions → default` (`permissionChanges`); hint `Click to select these 20 s` | Range = first→last change | `Lane/permissionHover`, `Tooltip/permissionChanges` |
| Network segment | 1px outline (`--danger` when bypassed) + `--skeleton` fill | title `net 2 · IPv6`; `Span`; `Address`; `Proxy` `▲ bypassed · 57 calls not captured` (danger) or `via proxy`; hint `Click to select this span` | Range = segment | `Lane/networkHover`, `Tooltip/network` |
| Prompt tick | tick 1→3px `--ink` + `#N` label 600 | title `#19 · Nov 3 10:42`; `Prompt` the text in quotes if retained, else `96 chars · not retained`; `Steps` `9 · 1m 40s`; `Tokens` `41k → 3.2k`; hint `Click to jump to #19 in Prompts` | Prompts tab, scroll to, expand and select the row | `Lane/promptsHover`, `Tooltip/promptMarker` |
| Friction glyph | glyph in a `--skeleton` box with 1px `--line-strong` outline | title `▲ Destructive command` / `↯ Frustrated` / `✕ Interrupted`; `When` `Nov 3 10:02:37 · #17`; detail row (`Command`, `Said`, `Tool run`); `Tool` | Prompts tab, jump to the prompt and expand it | `Lane/frictionHover`, `Tooltip/frictionMarker` |
| Now line | none | body `Now · 11:32:10 · following live` (or `· paused`) | none | `Tooltip/nowLine` |
| Lane label | dotted underline, cursor help | title = lane name; hint = definition (copy in §6B.6) | none | `Tooltip/laneLabel` |
| ~~Legend item~~ | removed with the legend (§11G.A7) | | | |

**Brush.** Cursors: empty track `crosshair`; inside the selection `grab` (drag moves it), `grabbing` while dragging; 6px either side of an edge `ew-resize`. Handle hover: 8×22 → 10×28 with a 2px `--focus-halo` outline (`Brush/handleHover`). While dragging, `Tooltip/brushDrag` sits 8px above the moving edge: `Nov 3 10:00 → 12:30 · 2h 30m`; the query runs on release (as §6A.4). Double-click inside the selection = Clear.

**Pressed (§6A.1).** On pointer-down the mark takes `--skeleton` (bin column, band) in the same frame, then the click result runs (`Lane/modelCallsPressed`).

### 6B.5 Linked highlighting (`useLinkedHover`)

One client store `{ source: 'table' | 'timeline'; t?: ISO; span?: [ISO, ISO]; ids?: string[] } | null`, written on hover and keyboard focus, cleared on leave. It applies the same way to every table.

| Pointing at | Timeline shows | Specimen |
|---|---|---|
| SessionRow | its band in hover style + a span fill `--link-fill` (new token) with 1px `--ink-muted` edges across all lanes | `SessionRow/hoverLinked`, `BandTrack/linkedBand` |
| PromptRow | its tick in hover style + the guide | `PromptRow/hoverLinked`, `MarkerTrack/linkedTick` |
| RequestRow, EventRow, StepRow, ToolCallRow | the guide at its instant + its bin column | `RequestRow/hoverLinked` |
| A timeline mark | its rows **that are on screen** take the hover fill + a 2px `--ink-muted` inset left rule | `RequestRow/timelineHovered` |

Hovering never scrolls. Clicking does (§6B.4 click results). Rows outside the current page or range are not highlighted.

### 6B.6 Tables

**Targets.** The whole row is the click target (cursor pointer; hover fill `--surface-raised` on all cells). Inner targets: the session cell in person-scope `RequestRow` (opens the session page) and `Linked to` in `EventRow` (opens that request, or scrolls to the tool step). Inner-target hover: `--ink` + 1px underline, 3px offset; the row keeps its hover fill (`RequestRow/innerTargetHover`, `EventRow/linkedToHover`).

| Row | Click | Inner target |
|---|---|---|
| SessionRow | Session page | — |
| PromptRow | Expand / collapse | — |
| StepRow | Open its request in the drawer | — |
| ToolCallRow | Open its step's request, scrolled to the tool | — |
| BetweenRow | none | — |
| RequestRow | Open the drawer | session cell (person scope) |
| EventRow | Expand JSON | Linked to |

**Cell tooltips** (`kind="cell"`; exact values; specimens `Tooltip/<name>`):

| Cell | Tooltip | Fields |
|---|---|---|
| status | `Status` `200 OK`; `Stop` `tool_use` | `status`, `statusText`, `stopReason` |
| status error | title `Connection reset`; `After` `4.1 s · 1.2 KB received`; `Retried` `1 · succeeded 10:03:12` | `error` |
| duration | `Total` `9,312 ms`; `First byte` `1,904 ms`; `Streaming` `7,408 ms` | `durationMs`, `ttftMs` |
| tokens | `Input` `43,012`; `Cache read` `40,811`; `Cache write` `1,204 · 5m`; `Uncached` `997`; `Output` `1,207` (Input = sum of the three below it) | `TokenCount` |
| bytes | `Up` `8,214 B`; `Down` `3,402 B · gzip` | `bytesUp`, `bytesDown`, `contentEncoding` |
| time | body `Tue Nov 3 2026 · 10:42:16.204 PST` (viewer time zone, abbreviation) | `t` |
| coverage | title `94 calls not captured` (or `All calls captured`); `Captured` `640 by the proxy`; `Reported` `734 by Claude Code`; `Why` first window `cause` | `Coverage` |
| friction | one row per kind: `↯ 1` `frustrated (user said so)`; `✕ 1` `interrupted by the user`; `▲ 10` `destructive commands` (danger) | `friction` |
| agent chip | title agent name; `Version`; `First seen` `Oct 22 · 1 device` | `agentInfo` |
| unknown | `<Column>` `—  <reason>` | the field's reason |
| truncated | full value, `kind="truncate"` | the cell text |

Rows themselves show no tooltip.

**Column headers.** Headers with a definition get a dotted underline (`--line-strong`, 3px offset) and cursor `help`; tooltip title = header, hint = definition. **No sorting anywhere**: tables stay time-ordered.

| Header | Definition |
|---|---|
| Coverage | Calls the proxy captured / calls Claude Code reported. ▲ marks a gap. |
| Bypass | Share of calls made with permission mode bypassPermissions. |
| Friction | ↯ frustrated · ✕ interrupted · ▲ destructive commands, counted per session. |
| Span | First to last activity in the session. |
| Steps | Model calls made for the prompt, subagents included. |
| Tokens (prompt) | Input → output, summed over the prompt's calls. |
| Up / Down | Request and response bytes on the wire. |
| Linked to | The request or tool call this event joins to (requestId, toolUseID). |
| Lane: Model calls | Bars: calls the proxy captured, per bin. Hatched: calls Claude Code reported that the proxy didn't capture. |
| Lane: Analytics | Claude Code analytics events per bin. |
| Lane: Presence | Focus per session; sleep per device. Blank = no session open. |

**Copy.** No hover copy buttons in rows and no context menu. Tooltips are hoverable and selectable. The drawer keeps `CopyButton` (ids, curl, link); the expanded event JSON gets a `CopyButton` at its top right.

### 6B.7 Header stats, tiles, chips, chrome

Every number says how it is computed and where it comes from (`kind="cell"`; hover fill `--surface-raised`; cursor help).

| Element | Tooltip | Specimen |
|---|---|---|
| HeaderStat `Model calls` | title; `Captured` `640 by the proxy`; `Not captured` `94: reported by Claude Code, no matching request-id` (danger); hint `Click +94 to list them` | `HeaderStat/hover`, `Tooltip/headerStat` |
| StatTile `Cost` | title; `Source` `costUSD from Claude Code analytics`; `Estimated` `3 calls had no analytics event` (`Cost.estimatedCalls`; row omitted at 0) | `StatTile/hover`, `Tooltip/statTile` |
| CoverageLine | title `94 calls not captured`; `Windows` `3 · longest Nov 3 15:02–15:48`; `Why`; hint `Click to list them` | `CoverageLine/hover`, `Tooltip/coverageLine` |
| KeyValueList row | `Source` (field path); `Seen` `on 412 of 640 calls` | `KeyValueList/rowHover`, `Tooltip/keyValueRow` |
| NOT RETAINED chip | body `Content not retained. aintercept stores its shape (size, counts, types), not the text.`; hint `Click: About this data` (click opens it) | `Tooltip/notRetained` |
| Posture chip | `Mode` `bypassPermissions`; `Share` `99.7% of calls` | `Tooltip/postureChip` |
| Range chip | body `Selected range · 2h 30m`; hint `× or Esc clears it` | `Tooltip/rangeChip` |
| LiveBadge | `Last poll` `11:32:10`; `Newest data` (`meta.lastDataAt`); `Interval` `5 s visible · 30 s hidden` | `Tooltip/liveBadge` |
| Breadcrumb | link crumbs: `--ink` + underline on hover; current crumb: no hover; truncated crumbs get the truncate tooltip | `Breadcrumb/hover` |

The copy for each stat's explanation lives in the client (keyed by stat); only the numbers come from the API.

### 6B.8 Keyboard and focus (beyond §3.4)

**Page order:** AppBar crumbs → LiveBadge → header stats (one roving group, ←/→) → timeline (one roving group) → RangeReadout buttons → tabs → filters → table (one roving group) → pagination. Each group is one tab stop (`useRovingFocus`).

**Timeline** (`Lane/modelCallsFocus`, `BandTrack/focusBand`, `Tooltip/barBinFocus`):

| Key | Result |
|---|---|
| Tab | Enters on the bin at the brush start, else the latest bin |
| ← / → | Previous / next bin. On marker lanes (prompts, friction, permission): previous / next marker. Bands and segments: previous / next one |
| ↑ / ↓ | Lane above / below; keeps the time |
| Home / End | First / last bin in view |
| Enter | The click result in §6B.4 |
| Shift + ← / → | Grow or shrink the selection from the focused bin (keyboard brush) |
| Esc | Close the tooltip; again: clear the selection; again: leave the timeline |

The focused mark shows a 2px `--focus` outline with 1px offset, plus the guide and its tooltip (immediately).

**Tables** (`RequestRow/focus`): ↑/↓ move rows; the focused row has a 2px `--focus` inset ring, the hover fill and the linked highlight on the timeline. Enter = row click. → or Tab moves into the row's inner targets and then across cells that have tooltips (each shows its tooltip at once); ← or Shift+Tab goes back. Home / End: first / last row on the page. On a collapsed PromptRow, → expands, ← collapses, ↓ walks into its steps. Focusing a row itself shows no tooltip.

### 6B.9 Cursors

| Element | Cursor | Hover visual |
|---|---|---|
| Row | pointer | `--surface-raised`, all cells |
| Inner link (session cell, Linked to, crumb) | pointer | `--ink` + 1px underline |
| Clickable data mark | pointer | §6B.4 |
| Empty track | crosshair | guide only |
| Brush inside / edges / dragging | grab / ew-resize / grabbing | handle grows |
| Selectable text (ids, JSON, tooltip text) | text | — |
| Disabled (`aria-disabled`) | not-allowed | none |
| Busy (`aria-busy`) | progress | §6A |
| Header, lane label or stat with a definition | help | dotted underline |

### 6B.10 States and data

- **Tooltips never fetch.** Every field they show ships with the list or timeline payload (the `(6B)` fields in §4). No tooltip skeletons. Event JSON is not previewed on hover; it opens on click.
- **Dimmed or refetching regions** (§6A.3) stay interactive; tooltips show the kept data.
- **Live updates under an open tooltip** change its values in place; the box stays anchored to the same mark. If the mark disappears (a bin rolls off), the tooltip closes.
- **Backend:** the `(6B)` fields are additions to §5.2.2 (`agentInfo`), §5.2.4 (`Cost.estimatedCalls`), §5.2.5 and §5.2.8 (timeline), §5.2.7 (request rows). `notCaptured[].cause` and `network[].address` come from the coverage job (§5.4); `cause` is `null` when it can't be attributed, and the UI then shows `—  reason not determined`.

### 6B.11 New components, tokens and tests

| Component / hook | Props | States |
|---|---|---|
| `Tooltip` + `TooltipProvider` | §6B.1 | plain, key/value, unknown, disabled reason, truncated, column header, flipped |
| `TimelineGuide` | `{ t: ISO \| null; readout: string }` | hidden, visible (with axis readout) |
| `useLinkedHover` | store in §6B.5 | row → timeline, timeline → rows |
| `useRovingFocus` | `{ group: 'timeline' \| 'table' \| 'stats'; items; orientation }` | focused bin / band / tick / row / stat |

**New tokens** (in `design/tokens.css`; also to add to Wire):
- `--shadow-pop`: dark `0 6px 16px rgba(0,0,0,.45)`; light `0 6px 16px rgba(20,22,23,.18)`.
- `--link-fill`: dark `rgba(230,227,219,.06)`; light `rgba(20,22,23,.05)`.
- `--focus-halo`: `--focus` at 30%: dark `rgba(255,211,107,.3)`; light `rgba(11,87,208,.3)`. Brush handle hover (`outline: 2px solid var(--focus-halo)`).

**Stories:** one per specimen on the three sheets (the manifest lists them). Tooltip stories render open (`open` prop forced) so they can be compared numerically.

**Tests** (`play` functions plus Vitest), continuing §6A.11:
11. No element in the rendered app has a `title` attribute (DOM scan in every screen story).
12. Hovering a bin opens its tooltip after 120 ms, not before 100 ms; moving to the next bin opens the next tooltip at once.
13. Hovering a cell opens at 400 ms; the pointer can move into the tooltip without it closing; its text can be selected.
14. Focusing a timeline mark with the keyboard shows the tooltip at once, and `aria-describedby` points to it; Esc closes it and keeps focus.
15. Clicking a bin sets the range to that bin; Enter on a focused bin does the same.
16. Hovering a SessionRow puts its band in hover style; hovering the band highlights the visible row; neither scrolls (`scrollTop` unchanged).
17. A `null` field renders `—` and its tooltip says why; no tooltip ever renders `0` for `null`.
18. A disabled control is focusable, ignores clicks and shows its reason.

---

## 7. Screens

Routes:

| Route | Screen | Default range |
|---|---|---|
| `/` → `/people` | PeoplePage | 14d |
| `/people/:personId` | PersonPage | 14d (window selector) |
| `/sessions/:sessionId` | SessionPage | live session: following live, last 15 min; ended: whole session |

Shared URL state (React Router search params; `useUrlState` hook):
- `from`, `to`: the brush range.
- `win`: window preset.
- `tab`
- `q`, `kind`, `agent`: filters.
- `page`
- `internal`, `ticks`: Events toggles.
- `req`, `dtab`: the drawer.

Changing the range resets `page`. Back and Forward must work for all of these.

### 7.1 PeoplePage

```
AppBar(breadcrumb [people], window 24h|7d|14d|All, LiveBadge, About)
PageHeader("People", "everyone whose AI traffic passed through the proxy · <range>")
StatRibbon[People, Agents seen, Sessions, Requests, Coverage(gap tone)]          ← /overview
AgentCard × 4 (grid 4 / 2 / 1)                                                    ← /overview.agents; click → ?agent=
UnattributedNotice (only if unattributed.sessions > 0)                            ← /overview.unattributed
TextInput(Search) · Chip(selected filter label) · "sorted by last seen"
DataTable(PersonRow…)                                                             ← /people
footnote: token accuracy legend
```

States: loading (skeleton ribbon, cards and rows); waiting (`/status.firstRequestAt == null` → full-page WaitingChecklist); delayed/refresh-failed banners; empty filter (EmptyState "No people match").

### 7.2 PersonPage

```
AppBar(breadcrumb [people › name], agents, window, LiveBadge, Pause, About)
Banner? (delayed | refreshFailed)
PageHeader(name | "name unknown", ids · org kind, HeaderStatGroup[Role, Plan, Claude Code user, Devices, 7-day limit, Flags, Last seen])  ← /people/:id
Posture chips [bypassed approvals X% of model calls, sandbox, token storage, proxy detected]                                    ← /people/:id.posture
StatRibbon[Model calls, Analytics, Account calls, Reach, Tokens out, Cost est.] + CoverageLine                                  ← /summary
Timeline(person): Lanes Sessions(BandTrack) · Prompts(MarkerTrack) · Model calls(BarTrack+gaps) · Analytics(DensityTrack) ·
                  Presence(StateTrack) · Friction(MarkerTrack) + TimeAxis + Legend + NowLine + Brush                          ← /timeline
KeyValueList "Capabilities · seen in any session" | KeyValueList "Footprint · all sessions"                                    ← /summary
Tabs[Sessions · n, All requests · n]
  Sessions:     DataTable(SessionRow…) + Pagination                                                                             ← /sessions
  All requests: TextInput, ToggleChip[ALL, MODEL, ANALYTICS, FLAGS, COUNT, ACCOUNT], DataTable(RequestRow…) + Pagination       ← /requests
  Empty range:  EmptyState(range chip, nearest earlier/later)
RequestDrawer (when ?req)
```

- The brush filters both tabs **and** the ribbon/footprint numbers: every query receives `from`/`to`.
- A request row opens the drawer in place. "Open session ›" in the drawer meta goes to the session page.
- States (each is a canvas artboard): loading, waiting, delayed, refresh failed, empty range.

### 7.3 SessionPage

```
AppBar(breadcrumb [people › name › session], LiveBadge, Pause, Copy link)
Banner?
PageHeader(agent dot + repo·branch, session id, latest recap; HeaderStatGroup[Span, Prompts, Model calls (+N not captured), Events, Device, Approvals bypassed])  ← /sessions/:id
Timeline(session): Prompts · Model calls(+gaps) · Context(BarTrack ch-count + compaction labels) · Permission(StateTrack) ·
                   Focus(StateTrack, this terminal) · Device sleep(StateTrack) · Network(StateTrack) · Friction · TimeAxis · Brush      ← /sessions/:id/timeline
RangeReadout(range | live | empty)                                                                                ← /state.counts
Grid 4: KeyValueList "Capabilities · at <to>" · "Repo · in range" · "Recaps · in range" · "Experiments · at <to>"  ← /state
Tabs[Prompts · n, Requests · n, Events · n]   (all oldest first, within the range, 20 per page)
  Prompts:  DataTable(PromptRow, StepRow, ToolCallRow, BetweenRow…)                                               ← /prompts
  Requests: filters + DataTable(RequestRow…) + Pagination                                                          ← /sessions/:id/requests
  Events:   TextInput + ToggleChip[hide client internals ✓, collapse ticks ✓] + "N in range · M shown" + DataTable(EventRow…) + Pagination  ← /events
RequestDrawer (StepRow / RequestRow click; ?req)
```

- The session timeline fetches the whole session once and is brushed client-side.
- Range-scoped queries (`/state`, `/prompts`, `/requests`, `/events`) refetch when the range changes, debounced 200 ms after the drag ends.
- Live: the brush follows the last 15 min. New rows append below; show the "N new" pill if the viewer has scrolled up.
- States: loading, live (an in-flight step, a running tool, events pending), failed to load (ErrorState), drawer kinds normal / notCaptured / failed / retained.

### 7.4 Responsive

- At ≥1280px the capabilities and footprint panels sit side by side, and the 4 session state panels form one row.
- Below 960px:
  - panels stack;
  - the timeline scrolls horizontally inside its panel;
  - tables scroll horizontally;
  - the AppBar wraps;
  - the drawer becomes full width.
- There must be no horizontal page scroll at 390px.

---

## 8. Storybook

1. **Theme decorator**: dark (default) / light, setting `data-theme` on `<html>`.
2. **Fixture decorator**: MSW handlers from `src/api/mocks/handlers.ts`, with a per-story override via `parameters.msw`. Add the **latency toolbar** from §6A.11 (`instant | fast | normal | slow | timeout | error | offline`); every handler honors it.
3. **Story naming**: `Components/<Group>/<Name>` with one story per state listed in §6 (e.g. `Button/Loading`). Use `args` for the props; add `play` functions for interactive ones (Tabs keyboard, Brush keyboard, Pagination, drawer open/close).
4. **Composites**: `Patterns/` for `StatRibbon`, `HeaderStatGroup`, `Timeline (person)`, `Timeline (session)`, `RequestDrawer (each kind)`.
5. **Screens**: `Screens/PeoplePage/{Default, Loading, Waiting, Delayed, RefreshFailed, FilteredCodex}`, `Screens/PersonPage/{Sessions, AllRequests, Loading, Waiting, Delayed, RefreshFailed, EmptyRange}`, `Screens/SessionPage/{Prompts, Requests, Events, Live, Loading, LoadFailed, Drawer, DrawerNotCaptured, DrawerFailed, DrawerRetained}`.
6. **Mapping to the canvas**: each screen story corresponds 1:1 to a canvas artboard of the same name. Put the artboard title in the story's `parameters.docs.description`.

---

## 9. Fixtures (real numbers from captured traffic, Oct 22 – Nov 4)

Use these in `mocks/fixtures`. Anything not listed is illustrative; mark it so.

- **Totals (Claude Code only)**:
  - 1,548 model calls;
  - 969 analytics batches holding 21,353 events of 128 types;
  - 9 feature-flag fetches;
  - 384 account/setup requests with no session (skills ≈185, MCP registry ≈170, the rest bootstrap, profile, settings, usage, plugins, quota);
  - 12 `count_tokens` calls.
- **Person 1** (`acct 7d2e…a41c`, org `1b9f…e02`, corporate, admin, plan `max`, tier `default_claude_max_20x`, 1 device darwin/zsh): 11 sessions.
  - **Main session**: `aintercept · partner-readiness`, Nov 2 → Nov 4, live, 3 networks (home, another network with IPv6, IPv6 test).
    - 640 captured calls + 94 not captured (Nov 3 00:00–00:59: 37; Nov 3 15:00–17:59: 57); 21,305 events.
    - `permissionMode` toggled bypass → default → bypass twice within 20 s on Nov 3.
    - Compaction 905k → 21k tokens (2,207 messages, 71 s wait); idle compaction at 904k.
    - 18 `tengu_return_to_session` events (blur 6 min – 14 h); sleep stalls up to ~18 min each.
  - **Oct 23 sessions**: 539 calls (20 tools, MCP playwright) and 282 calls (51 tools, 6 MCP servers: Docs, Gmail, Drive, Vercel, taza, playwright).
  - **Older sessions (Oct 23–29)** lack identity and repo fields (pre-parser); 8 small sessions total 45 calls.
- **Person 2** (other org, other device): 1 session, 4 model calls Oct 29, events Nov 2–3 (48).
- **Unattributed**: 1 session Nov 2, 38 calls, from a different IP, identity not parsed.
- **Brief-era capture window (main session, Nov 3 14:20–22:40 UTC)**:
  - 300 requests (284 main, 3 subagent Explore/Haiku, 8 auxiliary, 4 count, 1 compaction);
  - 37.2 MB up / 1.9 MB down;
  - tokens 140.9M cache read, 650k cache write, 384k output, 3.6k uncached;
  - cost $44.94 across 304 reported calls;
  - 282 tool calls (Bash 269); top command words `cd 55 · python 53 · sed 24 · cat 21 · grep 17 · kubectl 13 · docker 8 · git 8`;
  - 10 destructive (6 `rm -f`, 4 `rm -rf`, 1 outside cwd);
  - failures: 9 commands, 11 tool errors, 5 API retries (connection errors);
  - `head_sha` changed ×10; PR #2 polled 80×;
  - reach: 11 MCP servers, 381 tools (Vercel 244), 87 skills, 15 hooks (~50 ms per tool call), 23 built-in tools;
  - flags 1,072 → 1,108 over two days, 0–12 changed per 6-hourly fetch, 12 experiments (`tengu_curried_trinket = no_scratchpad_capped`).
- **Illustrative only** (label them in fixtures):
  - People B–D and all Codex, ChatGPT and claude.ai data;
  - the person-level token and cost totals (1.1M out, $118);
  - the 99.7% bypass figure;
  - the 66-prompt person total;
  - specific timestamps and step-level numbers in the drawer;
  - the `anthropic-beta`, `user-agent` and `x-stainless-*` values.

---

## 10. Open questions (resolve before phase 4; build behind props until then)

1. **Cross-agent identity**: is one person = one vendor account, or are accounts linked (by email or by device)? Until decided, the People table shows one row per account and `linkedBy: null`.
2. **Recaps as labels**: allowed under the retention policy? If not, the label source is `firstPrompt` (content) → `repo`.
3. **Content retention mode**: experimentation keeps bodies; production keeps shape only. The UI reads `contentRetained` per request and a global flag in `/status`.
4. **Other agents' data shapes**: Codex (sessions → turns → steps) and chat surfaces (conversations → messages, no token counts for ChatGPT web) need their own row variants later. Keep `SessionRow` and `PromptRow` agent-aware through `agent`.
5. **Live detection threshold**: currently "a request within the last 5 min".

---

## 11. Known differences between the canvas and this document (follow this document)

1. ~~Green ✓ for "matches"~~: fixed on the canvas (§11A).
2. ~~HTTP error status in red~~: fixed on the canvas (§11A).
3. ~~Account requests color~~: the canvas now uses `ch-account`. Add the token to the Wire system itself.
4. **Example name "Dana Ruiz"** on the Rows sheet is a placeholder; never ship it in fixtures.
5. **The brush on the canvas is drawn statically.** The real component is draggable and keyboard operable (§6.4).
6. **The person-page ribbon and footprint** follow the brushed range; the canvas shows whole-window numbers.

---

## 11A. Parity rulings, round 1 (canvas updated to match)

These settle the places where the canvas disagreed with itself or with this document. The canvas now follows every rule below; parity runs should treat any remaining difference as a bug.

1. **Pagination window.**
   - Always show the first page, the last page, and the current page ±1, with `…` for each gap. The prev/next text buttons (`‹ Prev` / `Next ›`) sit at the ends.
   - Examples: page 1 of 9 → `1 2 … 9`; page 2 → `1 2 3 … 9`; page 5 → `1 … 4 5 6 … 9`; page 9 → `1 … 8 9`.
   - The range text (`21–40 of 170`) is on the left.
2. **Pagination while loading.**
   - Text: `<from>–<to> of <total> · loading…`, showing the *target* range.
   - The full window stays visible; every button is `disabled` (ink-faint on a `line` border).
   - The target page carries `aria-current` with the `surface-raised` fill. Its text is faint like the others; no ink override.
3. **Busy (pending) button.** It uses `aria-busy="true"` + `aria-disabled="true"`, **not** `disabled`, so it keeps focus. The border stays `line-strong`; text is `ink-muted`; cursor is `progress`. `disabled` is only for buttons that are unavailable, not busy.
4. **Button base.** `.btn` is `display: inline-flex; align-items: center; gap: 6px` everywhere.
5. **AppBar.** `padding: 8px 32px`, `surface` background, **bottom border only** (`line`). The screens were right.
6. **Row columns (canonical).** Gap 12px. Widths in px; `flex` takes the rest.

   | Row | Padding | Min width | Columns |
   |---|---|---|---|
   | `PersonRow` | 8px 12px (loading 11px 12px) | 1240 | person 250 (name + id, both ellipsis) · agents 260 (chips wrap, gap 4) · last seen 100 · sessions 70 (right) · prompts 70 (right) · tokens 90 (right) · activity flex, min 160 (14-day sparkline, 22px tall) · bypass 90 (right) · coverage 84 (right) · › 12. People.dc.html is canonical; CompRows redrawn to match (round 5). Focus follows the table rule (§6B.8): `--surface-raised` fill + `box-shadow: inset 0 0 0 2px var(--focus)`, no outline (round 6) |
   | `SessionRow` | 6px 12px | 1200 | start 92 · dot 10 · repo·branch 240 · label flex · prompts 64 (right) · span 64 (right) · calls 56 (right) · events 64 (right) · friction 84 · bypass 72 (right) · coverage 84 (right) · › 12 |
   | `RequestRow` (person scope) | 4px 12px | 1180 | time 108 · kind 84 · session 150 · method 40 · path flex · status 44 · duration 64 (right) · up 70 (right) · down 70 (right) |
   | `RequestRow` (session scope) | 4px 12px | 1100 | time 76 · # 40 · kind 84 · method 40 · path flex · status 44 · duration 64 (right) · up 70 (right) · down 70 (right) |

7. **Rows sheet fixes.**
   - `SessionRow` has an Events column and `RequestRow` a Down column.
   - Status renders the status code: 2xx in `ink-muted`; errors `✕ ERR` in bold `ink`.
   - The not-captured marker (`▲ reported by Claude Code, not captured`) is `danger`.
8. **Overlay colors: new tokens** (add to Wire; already in `design/tokens.css`):

   | Token | Dark | Light | Use |
   |---|---|---|---|
   | `--brush-dim` | `rgba(12,14,15,.45)` | `rgba(246,246,243,.6)` | outside the brushed range |
   | `--brush-fill` | `rgba(255,211,107,.08)` | `rgba(11,87,208,.08)` | inside the brushed range |
   | `--scrim` | `rgba(12,14,15,.6)` | `rgba(20,22,23,.32)` | drawer backdrop |
   | `--gap-hatch` | `rgba(255,122,107,.3)` | `rgba(179,38,30,.22)` | not-captured hatching (135°, 4px on / 4px off) |
   | `--dim-opacity` | `0.55` | `0.55` | refetching regions and legend hover only (transient); never for resting rows (§11F) |

9. **Formatting.**
   - **Durations of 1 min or more**: the two largest units, compact, keeping a zero second unit: `24m 0s`, `1m 11s`, `8h 20m`, `2d 0h`. No zero-padding (`4m 3s`, not `4m 03s`).
   - **Durations under 1 min**: a space before the unit; one decimal under 10 s, integers above: `4.8 s`, `22 s`, `0.6 s`. Under 1 s use milliseconds: `71 ms`. Offsets keep the sign: `+9.3 s`.
   - **Badge labels are literal**, not formatted: `LIVE · 5s`, `DELAYED · 2m`.
   - **Sizes** use decimal units (1 KB = 1,000 B):
     - under 1,000 B: `400 B`;
     - under 10 KB: one decimal, `4.1 KB`;
     - under 1 MB: integer, `15 KB`, `412 KB`;
     - MB and above: one decimal, `37.2 MB`.
   - **RangeReadout**: include seconds when the range is under 4h (`Nov 3 10:00:00 → 12:00:00 · 2h 0m`); otherwise minutes (`Oct 30 02:00 → 08:00 · 6h 0m`). Live is `Last 15 min · following live`.
   - **Banner height**: exactly **32px including its 1px bottom border** (`height: 32px; box-sizing: border-box; padding: 0 32px`, single line, overflow hidden). Its Retry button is compact (`padding: 3px 10px`).
10. **Illustrative specimens.** Elements with `data-spec-illustrative="true"` show an intermediate moment or an annotation. Exclude them from numeric parity (check them visually only):
    - `RequestRow/newFlash`, `RequestRow/newFlashFading`: frames of the 1.2 s flash; the "NEW" label and `#171a1d` are not part of the component;
    - `SessionRow/pendingNavigation`: a sketch of the pending-row treatment (`surface-raised` + 2px `focus` inset bar);
    - the "(blank = no session open)" note on the presence lane.

    **Lane labels** are always `div.k` (micro caps) inside `.lab`; the sublabel is a `div.m` at 11px. Prompts and friction now match.

Also resolved on the canvas in this round (previously listed in §11):
- ✓ "matches" is no longer green;
- HTTP errors are bold ink with ✕, not red;
- account requests use `ch-account` (`#b0b8bd` / `#555d62`);
- the person page's All requests footer is a real `Pagination`.

---

## 11B. Parity rulings, round 2 (canvas updated to match)

1. **Prompts that aren't retained always say so.** The prompt cell reads `<N> chars · not retained` in every state, including in flight and interrupted. `PromptRow/inFlight` and `PromptRow/interrupted` now include it.
2. **§11A.9 wins.** Durations of 10 s or more are whole seconds everywhere, including tool results: `stdout 18 KB · 12 s`. Every `1x.x s` on the canvas is fixed (CompRows, Session request log, Main request log).
3. **Tokens have their own rule.** It depends on scope:

   | Scope | Rule | Examples |
   |---|---|---|
   | **Totals** (prompt totals, session and person totals, compactions) | under 1,000 exact; 1k–9.9k one decimal; 10k–999k whole k; M one decimal | `850`, `2.6k`, `39k`, `905k`, `1.1M` |
   | **One model call** (StepRow model detail, drawer key strip and meta, InputCompositionBar) | under 1,000 exact; one decimal in k up to 1M | `380`, `1.2k`, `43.0k`, `41.2k in (40.8k cached)` |

   Never write `0.9k`; under 1,000 is exact (the drawer strip now reads `38.1k → 900`). §3.3's 100k rule covers counts other than tokens.
4. **Prompt, step and between rows: Session.dc.html is canonical.** CompRows now draws these columns. All rows use padding `4px 12px`, a 12px gap and `min-width: 1100px`.

   | Row | Columns (px) |
   |---|---|
   | **PromptRow** | twisty 18 · # 40 (600) · Time 92 · Prompt 240 · Steps 70 · Dur 64 · Tokens 110 · Tools flex · Friction 80 |
   | **StepRow** (model call, subagent) | `padding-left: 70px`; time 64 · actor 72 · detail flex · result (content) · marker (content, danger) |
   | **ToolCallRow** | `padding-left: 100px`; same columns as StepRow, time blank |
   | **BetweenRow** | 18 (blank) · 40 (`—`, faint) · Time 92 · detail (content); quiet row (§11F.2), no opacity |

   The header row uses PromptRow's widths. The tool rows on the sheet are now `ToolCallRow/default|marked|running`, replacing `StepRow/tool|toolMarked|toolRunning`.
5. **EventRow: Session.dc.html is canonical.** Columns are Time 76 · # 40 · Event 290 · Linked to 150 · Fields flex · marker (content), with `min-width: 1100px`. `EventRow/expandedJson` is indented `140px` on the left so it aligns with the Event column. **EventRow/compact** (drawer only) is time offset 64 · name 250 · detail flex · join key (content, faint), with padding `4px 12px` and no min-width.
6. **The panel header (`.ph`) uses `8px 12px` padding** on both pages.
7. **Static specimens** on CompRows: `EventRow/default` (screen instance `EventRow`), `EventRow/expandedJson`, `EventRow/compact` and `EventRow/compactNoMatch`. `NewRowsPill` matches `NewRowsPill/visible` on CompInteraction: the default chip with no overrides. Main's inline colors are removed. The arrow follows the insert edge: `↑` for newest-first lists, `↓` for oldest-first.

---

## 11C. Parity rulings, round 3: hover sheets (canvas updated to match)

**General rule: a hover or focus specimen is the component at rest plus only the change named in §6B.** Wherever a hover sheet disagreed with a resting specimen, the resting specimen wins, and the hover sheets have been redrawn on the resting geometry.

1. **Hover specimens redrawn at rest + hover change.** Your default in every case.
   - **Bands:** 10.5/20 nowrap labels; bands `top 8px; height 26px; border-box` in a 44px lane, as on Main. The live band shows its label (`aintercept · partner-readiness · live`) like any other band.
   - **Permission:** 6px padding. On hover the tick widens 3→5px **about its centre** (`left: calc(x - 1px)`); its label stays at its resting position.
   - **Bypassed network:** danger text (resting), and on hover the danger border and `--skeleton` fill.
   - **Legend:** gap `4px 16px` with the resting swatches. The legend-hover lane keeps its state text.
   - **SessionRow/hoverLinked:** min-width 1200. The `›` **stays faint** on hover, as in CompRows. Only the fill changes.
   - **RequestRow/innerTargetHover:** min-width 1180.
   - **EventRow/linkedToHover:** has the (empty) marker cell.
   - **HeaderStat/hover:** a flex item inside `HeaderStatGroup/withHover`.
   - **Redrawn as their own components, with the hover fill only:**
     - `StatTile/hover` (`.tile`, 200px);
     - `KeyValueList/rowHover` (a row of the default list);
     - `CoverageLine/hover` (the gap line);
     - `Breadcrumb/hover` (the AppBar crumb span, `people` link in ink + underline).
2. **Context lane:** the Session lane is canonical: `--ch-count` bars at .7, ink labels, 56px. On hover, the hovered bar is `--ink` at full opacity over a `--link-fill` column one slot wide (`BarTrack/contextHoverBar`). Compaction label hover (`ContextTrack/compactionLabelHover`): ink with a dotted `--ink-muted` underline, on the same lane.
3. **Bin column:** spans the bin exactly (bin 28 on the 60-bin grid: left 46.667%, width 1.667%). The guide, the axis readout and the tooltip anchor snap to the bin centre.
4. **Guide:** stops at the top of the axis (`calc(100% - 31px)`).
5. **Sketches:**
   - redrawn as real instances in 1100px frames on the 60-bin grid:
     - `Lane/modelCallsPressed`;
     - `Lane/presenceLegendHover`;
     - `Brush/handleHover` (in `Brush/hoverHandleSelection`);
     - `Brush/dragging`;
     - `BandTrack/focusBand` (in `Lane/sessionsFocus`).
   - The motion frames on CompInteraction are renamed and marked `data-spec-illustrative="true"`: `LiveBadge/heartbeatPeak`, `LiveBadge/heartbeatMid`, `LiveBadge/pausedNoPulse`, `LiveBadge/delayedNoPulse` and `StepRow/streamingPulse`.
6. **`--focus-halo` token:** added (§6B.11): `--focus` at 30%, dark and light.
7. **Unique names:**
   - CompTooltip's button is now `Button/disabledFocusable`; CompInteraction keeps `Button/disabledWithReason`.
   - With the item 5 renames, every sheet specimen name is unique across all sheets (366).
8. **Also fixed (found while doing this):**
   - The canvas no longer has any native `title` attributes. CompSummary and CompInteraction had four.
   - Every disabled button on the canvas now uses `aria-disabled="true"` instead of `disabled`, matching §6B.3. One CSS rule covers both: faint text, `--line` border, cursor `not-allowed`, and it never applies to `aria-busy` buttons.
   - Main's first-page `‹ Prev` previously had no disabled styling; it is now faint.

## 11D. Parity ruling, round 4: TimingWaterfall (canvas updated)

**The waterfall is to scale**, which is what your build already does:
- Every phase is drawn at its real start and length on the total's scale.
- Minimum width is 1%, so a 4 ms phase still shows.
- A minimum-width bar does not push later bars along; each starts at its real offset.

The old specimen was a sketch and has been redrawn, both on CompDrawer (`TimingWaterfall/default`) and in the Session drawer.

**Phases the proxy doesn't record** (accept + TLS, upstream connect) keep their rows, with no bar:
- the track shows `—  not recorded by the proxy` (11/20 mono, `--ink-faint`, nowrap, positioned over the 10px track without changing the row height);
- the value is `—`.

`first byte` starts at 0, when the proxy received the request, so it includes the unrecorded connect time. The drawer's "request body received" row is removed; the proxy doesn't record it either.

**Values follow §11A.9:** `1.9 s`, `7.4 s`, `9.3 s` (the sketch had `1.87 s`). For the specimen numbers (first byte 1.87 s and streaming 7.43 s of 9.30 s):

| Phase | Bar left | Bar width |
|---|---|---|
| first byte | 0% | 20.1% |
| streaming | 20.1% | 79.9% |
| total | 0% | 100% |

## 11E. Round 8: About this data, the not-captured filter (canvas updated)

### 11E.1 AboutThisData (`AboutThisData.dc.html`, Screens page)

**Shell.** A right-side drawer, **640px** wide (`max-width: 100%`).
- Same chrome as RequestDrawer: `--scrim` overlay; `--surface` body; 1px `--line-strong` left border; `role="dialog"`, `aria-modal`, labelled by its title.
- Header: `--surface-raised`, padding 10px 16px, hairline below. Title `About this data` (mono 12.5, 600); a close button (`Button/iconOnlyClose`) on the right.
- Body: scrolls on its own; padding 16px; sections 24px apart. It opens with a one-line intro in `--ink-muted`.
- URL `?about=1`. It opens immediately (static content, no fetch). Esc and × close it, focus is trapped inside, and focus returns to the opener.

**Sections, in order.** Each section title is micro caps (`.k`, an `h2`), 8px above its body.

1. **How the data gets here** (`PipelineDiagram`). A CSS grid `1fr 24px 1fr 24px 1fr`, row gap 6px:
   - **Nodes:** `--surface-raised` fill, 1px `--line-strong`, square corners, padding 8px 10px. Name in mono 12.5/600; one sub-line in mono 11/16 `--ink-muted`.
   - **Row 1:** `Agent` (the person's machine · nothing installed; **dashed** border) → `Envoy proxy` (TLS intercept · customer network; **`--ink` border**, where observation happens) → `api.anthropic.com` (the vendor).
   - **Row 2:** under the proxy, `↓ copy of every request and response` (mono 11, `--ink-faint`).
   - **Row 3:** `aintercept UI` (this page · polls every 5 s) ← `Observer` (parses rows · writes NDJSON to object storage).
   - **Arrows:** text glyphs `→ ←` in `--ink-faint`, centred in the 24px columns. No SVG.
   - **Bypass note** (full width): 1px dashed `--danger` border, padding 6px 10px, mono 11/16 `--danger`. Text: `▲ Traffic that skips the proxy (for example over IPv6) is never seen. aintercept finds it later in Claude Code's own analytics and lists it as "reported, not captured".` This uses red under the coverage-gap rule.
2. **Claude Code's five channels · all on api.anthropic.com** (`ChannelTable`).
   - A DataTable-style box with a micro-caps header row and hairlines.
   - Columns: Channel 150 (8px `--ch-*` swatch + name, 600) · On the wire 170 (mono 11, muted, wraps) · Carries (flex, mono 12, muted).
   - Rows and copy as in §1.
3. **What is kept** (`RetentionList`). Rows: key 110 (`--ink-muted`) + value (mono 12):
   - `Mode`: chip `SHAPE ONLY` or `CONTENT` (from `deployment.contentRetained`), then `set for this deployment`.
   - `Kept`: headers, token counts, timings, model and tool names, size-capped tool inputs (paths, commands, URLs).
   - `Not kept`: prompt text, responses, file contents, shown as their size with the NOT RETAINED chip. In content mode this row reads `nothing: bodies are kept and shown in the request drawer`.
   - `Credentials`: always redacted; keys appear only as a hash.
4. **What the marks mean** (`MarkLegend`). Rows: mark 110 + meaning (mono 12):
   - `—` (muted): not known; point at it for the reason; never shown as 0.
   - `≈ 610k` (muted): estimated from text size, because the agent doesn't report usage.
   - The NOT RETAINED chip: content not kept; its shape is shown instead.
   - `▲ 94` (`--danger`): coverage gap; red means only this, or a destructive command.

### 11E.2 The not-captured filter on All requests (`PersonRequestsNotCaptured`, `PersonRequestsNoUncaptured`)

- **Where it comes from.** "view N calls" on the CoverageLine, `+94` on the Model calls HeaderStat, and the hatch's click (§6B.4) all open `?tab=requests&captured=false`. On the hatch, the range is set too.
- **Toolbar.** A removable **`FilterChip`** sits right after the filter input and before the kind chips (`CompChips` → FilterChip):
  - **Text and color:** `▲ NOT CAPTURED`, with `--danger` text and border on `--surface-raised`.
  - **Remove button:** the × is a separate button inside the chip: 1px `--line` divider, `--ink`, `aria-label="Remove filter: not captured"`. Hover `--line` fill; focus is the 2px `--focus` ring.
  - **Removing it:** clears `captured` from the URL and refetches (keep and dim, §6A.3).
  - **Not a segment:** it is not a toggle in the toolbar; it only appears while the filter is active.
- **Kind chips** keep their selection. Their counts follow the active filters (`MODEL 94`, the others `0`), and they stay clickable.
- **Rows.** Not-captured rows look the same as anywhere else: method `—`, path `▲ reported by Claude Code, not captured · main · opus` in `--danger`, status `—`, duration from analytics, up and down `—`.
- **Footer.** Pagination counts the filtered set: `1–20 of 94 · newest first`.
- **No matches** (`EmptyState/noFilterMatches`): the header row and toolbar stay; the body is replaced by a centred empty state, padding 40px 16px, with:
  - the active chips (`Chip/range` if a range is set, plus a static `▲ NOT CAPTURED`);
  - title `No uncaptured calls in this range`;
  - body `Claude Code reported 41 model calls between Nov 4 08:00 and 10:10, and the proxy captured all 41.` (the counts come from coverage);
  - the buttons `Clear filter`, and `Clear range` when a range is set.

  There is no pagination in this state.

### 11E.3 PersonRow focus

PersonRow focus was ruled in round 6 (§11A.6 PersonRow note): a `--surface-raised` fill plus `box-shadow: inset 0 0 0 2px var(--focus)`, no outline. CompRows `PersonRow/focus` and People.dc.html `.prow:focus-visible` already match.

## 11F. Round 9: accessibility rulings (canvas updated)

All four answers keep WCAG 2 AA (4.5:1 for text) in both themes. They replace opacity with color.

1. **Background rows become "quiet" rows. No opacity.**
   - **Rule:** every text color in the row drops to `--ink-faint`, including ink, muted and the session column. Contrast: dark 5.31:1 on `--surface` and 4.85:1 on `--surface-raised` (hover); light 5.65:1 and 4.77:1.
   - **Exception:** the **kind label keeps its channel color** (`--ch-*`, at least 6.9:1 on both surfaces). This is the one cue that still says which channel it is.
   - **Also kept:** an `✕ ERR` status keeps `--ink` 600, and `▲` danger keeps `--danger`, even on a quiet row. A failed or bypassed call is never quiet.
   - **Applies to:** recaps, suggestions, count_tokens, analytics batches, collapsed tick events, `tengu_event_loop_stall`. Everything that was 0.55 before.
   - **Implementation:** a `quiet` modifier on the row (`.quiet *:not(.ch) { color: var(--ink-faint) }`). Canvas specimen renamed `RequestRow/backgroundQuiet` (was `backgroundDimmed`).
   - **Other cue:** none needed. The path text already names the class (`auxiliary · recap`, `count_tokens`, `batch · N events`). Faint versus normal is a de-emphasis, not information carried by color alone.
   - **Still on `--dim-opacity`:** only transient states (refetch keep-and-dim, which has `pointer-events: none` and `aria-busy`; legend hover). Resting content never uses it.
2. **BetweenRow:** the same quiet treatment. Time and `—` are `--ink-faint`; the detail keeps its tone (`--ch-count` for compaction, `--ink-muted` for recap: 7.6:1). Opacity .6 is removed.
3. **Asleep band:** the fill changes from `--line-strong` to **`--ink-faint`**, with the label staying `--bg`.
   - Dark: `#0c0e0f` on `#848c90` is 5.65:1. Light: `#f6f6f3` on `#62686c` is 5.22:1.
   - It still reads as "between focused (ink) and away (line)".
   - Updated: the band, the legend swatch, the person-page presence lane, the session sleep lane, and the hover sheets.
4. **Breadcrumb links:** your resting underline is accepted, and it's the canvas rule now.
   - Rest: underline 1px `--line-strong`, `text-underline-offset: 3px`, text `--ink-muted`. Hover: text and underline `--ink`.
   - The crumb colors stay as they are; the underline is the non-color cue WCAG 1.4.1 asks for.
   - Canvas: the AppBar crumbs on CompStates, Main and Session.

## 11G. Round 10: the first review on real data (canvas updated)

New sheet: **`CompDensity.dc.html`** ("Timelines at density"). Main, Session, People, CompTimeline, CompRows, CompControls, CompChips and the hover sheets are updated to agree with it. Every text color keeps 4.5:1 in both themes, and there is no opacity on resting content.

### A. Timelines

**A1 Friction** (`Lane/frictionSplit`; rows `FrictionRow/frustrated|interrupted|destructive`):
- **Rows.** One row per kind, always in this order: ↯ frustrated, ✕ interrupted, ▲ destructive. A kind with no marks keeps its row, with count `0` in `--ink-muted`.
- **Row label.** Glyph, kind and the count in the visible window. ▲ is `--danger`; the others are `--ink-muted`.
- **Marks.** Mono 11px, centred on their instant. ↯ and ✕ are `--ink`; ▲ is `--danger`.
- **Collisions.** Marks closer than **14px** merge into a cluster drawn as glyph + count (`▲12`, weight 600) at the first mark. Clusters whose text would overlap merge again.
- **No binning.** A lone incident is never hidden in a bin.
- **Cluster tooltip.** The total, then up to 8 rows (`time · #prompt · command`) and `+N more`. Click: range = the cluster's span, and the Prompts tab jumps to the first one.

**A2 Presence (person page)** (`Lane/presenceRows`; `PresenceRow/focused|away|asleep`):
- Three rows: focused `--ink`, away `--ink-muted`, device asleep `--ink-faint`.
- Bars are 10px tall in 16px rows, min 2px wide.
- **No text inside segments** in these lanes; the row names the state and the tooltip gives span and source.

**A3 Session page.**
- **Focus** (`Lane/focusRows`): rows focused and blurred.
- **Device** (`Lane/deviceSleepRow`): one row, asleep.
- **Network** (`Lane/networkRow`): one 28px row of outlined segments; bypassed segments get a `--danger` border and text. A label shows only when the segment is at least its text + 16px wide; otherwise the segment is bare and the tooltip carries it. Min width 2px.
- **Label rule (both pages):**
  - Lane label: one line, micro caps, ellipsis + full-text tooltip; never wraps.
  - Optional one-line sublabel (mono 11/16 `--ink-muted`, units or scope only: `per 15m`, `main thread`).
  - Multi-row lanes list each row's name under the lane label, aligned to its row (mono 10.5/16 `--ink-muted`, ellipsis).
  - Heights: single-row lane 28px (44px with a sublabel); multi-row lane 6 + 14 + 16 × rows + 6; bars lane 60px; bars with a gap row 76px.
  - The session lanes' sublabels are shortened accordingly: `this terminal` moves into the Focus tooltip, `tokens · main thread` becomes `main thread`.

**A4 Coverage gaps** (`Lane/modelCallsWithGapRow`, `GapRow/merged`, `Tooltip/gapMerged`). Options (c) and (a) combined:
- **Own row.** Gaps move to their own 16px row under the bars. The row label `▲ not captured N` (`--danger`, the total in the visible window) is the only text.
- **Merging.** Gaps closer than **4px** on screen merge into one hatch with the summed count.
- **Hatch.** 10px tall, `--gap-hatch` stripes with 1px `--danger` ends, min 3px wide.
- **Tooltip.** The total, then up to 8 windows (`start · count · length`) and `+N more`. Click: range = the merged span, and Requests opens filtered to not captured (§11E.2).
- **No gaps.** The row stays, with `not captured 0` in `--ink-muted`.
- The in-lane `▲ N reported, not captured` labels are removed.

**A5 Bins** (`Lane/modelCalls24h|7d|14d`):
- **Ladder:** 1m · 5m · 15m · 1h · 3h · 6h · 12h · 1d.
- **Choice:** the client picks the bin whose on-screen width is closest to **4px**, never under **2px** (ties go to the larger bin). It recomputes on resize (debounced 150 ms) and on window change, and passes `bin=` to the API.
- **Examples** (1212px track): 24h → 5m, 7d → 1h, 14d → 1h, 30d → 3h, a 6.5-day session → 1h, a 2-day session → 15m.
- **Bar width:** bin width − 1px gap. Below 3px per bin, no gap.
- **Sublabel:** names the bin (`per 1h`).

**A6 Axis** (`TimeAxis/ticks14d`, `TimeAxis/ticks24h`; screens `TimeAxis/ticks`, `Gridlines`):
- **Ticks** hang from the axis top: minor 3px `--line-strong`; major 6px `--line-strong`; day boundary 9px `--ink-faint`. Ticks only fall on round local times.
- **Labels** on majors only, mono 11/16 `--ink-faint`, centred. The first and last labels align inward.
  - Midnight labels show the date in `--ink` 500.
  - Labels closer than 80px drop alternate ones; midnights are kept.
- **Gridlines:** majors 1px `--line` through every lane, behind marks; day boundaries 1px `--line-strong`.
- **Ladder:**

  | Window | Minor | Major |
  |---|---|---|
  | ≤3h | 5m | 30m |
  | ≤12h | 15m | 1h |
  | ≤36h | 1h | 3h |
  | ≤4d | 3h | 12h |
  | ≤10d | 6h | 1d |
  | ≤21d | 12h | 1d |
  | ≤90d | 1d | 7d |
  | longer | 7d | month |

- **Brush:** the axis never rescales for a brush.

**A7 Legend:** removed everywhere: CompTimeline `Legend/default`, the screens' legends, and the hover sheet's legend-hover specimen. Every state has its own labelled row and every mark has a tooltip. `--dim-opacity` is now only used for refetch keep-and-dim.

**A8 Clearing a range** (`Brush/clearButton|Hover|Focus`, `RangeReadout/withClear`, `RangeReadout/wholeSession`):
- **× on the brush.** A 16×16 button inside the selection's top-right corner: `--focus` fill, `--bg` ×, weight 600. It shows when the brush is at least 40px wide. Hover: `--ink` fill. Focus: 2px `--ink` ring. `aria-label="Clear range"`.
- **Readout button.** Always last in the RangeReadout: `× Clear range` (person page) or `× Whole session` (session page; same action).
- **Click on empty track.** A click (under 3px movement) outside the selection clears it.
- **Keyboard.** Esc, or Delete/Backspace on the brush or a handle. Tab order: handles → × → readout buttons.
- **After clearing,** focus returns to the timeline and the readout announces `Range cleared`.
- **Double-click to clear is dropped.**

### B. Interaction

**B1 One rule for everything clickable** (`ClickRule/*` on CompDensity; CompControls and CompChips updated):

| State | Rule |
|---|---|
| Cursor | `pointer` on everything with a click result (buttons, segments, toggle and filter chips, tabs, links, rows, marks, brush ×); `not-allowed` with `aria-disabled`; `progress` with `aria-busy`; never `pointer` on inert things |
| Hover | `--surface-raised` fill; border rises `--line-strong` → `--ink-muted` (borderless segments get a 1px inset `--ink-muted` ring); text `--ink`; transition `--dur-fast` |
| Pressed | `--skeleton` fill, same frame (§6A.1) |
| Focus | 2px `--focus` ring (offset 1px; −2px inside segmented controls) |
| Links | rest: underline `--line-strong`; hover: link and underline lighten together |

Contrast: hover `--ink` on `--surface-raised` is 12.9:1; unselected chips `--ink-muted` on `--surface` 7.6:1.

**B2 Selected = ink fill.** Applies to segmented controls (the window) and toggle/kind chips: `--ink` fill, `--bg` text, weight 600, which is 15.1:1 dark and 16.8:1 light.
- Kind chips add `✓` and ring their swatch in `--bg` so the channel color still reads.
- Selected + hover: `--ink-muted` fill (8.1:1 / 7.0:1).
- Unselected chips: `--line-strong` border, `--ink-muted` text.
- `Chip/selectedFilter` uses the same fill.

**B3 Motion** (CompDensity table):

| Change | Motion | Reduced motion |
|---|---|---|
| Drawer | as §6A.7 | fade only, 80 ms |
| Page change | AppBar stays still; the body fades 0 → 1 over 160 ms `--ease-out` with a 4px upward settle; the old page isn't animated out (render the new route's skeleton or cache at once, then fade) | 80 ms fade, no movement |
| Tab switch | underline slides 150 ms `--ease-out`; panel fades in 120 ms; no slide | underline jumps, no fade |
| Skeleton → content | content fades in 150 ms; skeleton removed, no cross-fade | instant |
| Keep-and-dim → new data | rows replace under the dim; the dim lifts over 150 ms | instant |

Never animate rows, bars, marks, counting numbers, or anything on a poll (except the LiveBadge pulse).

### C. People page

**C1 Window** (`AppBar/peopleWindow`, `PageHeader/peopleWindow`; People.dc.html):
- **Label.** The control is labelled `ACTIVE IN THE LAST` (`aria-labelledby`), with the selected segment in the B2 ink fill.
- **Subtitle.** `active in the last 14 days · Oct 25 – Nov 8 · counts below cover that window only`. `All` reads `active since <first capture>`.
- **No LIVE badge on this page.** It's replaced by **`UpdatedAgo`**: `updated 12 s ago` (mono 12.5 `--ink-muted`, ticking every 5 s).
  - While a poll is running: `updating…`.
  - On failure: `—  last update failed · retrying`, in `--ink`.
- The person and session pages keep LIVE.

**C2 Coverage → `Missed`** (`CoverageCell/gap|complete|notApplicable`, `TableHeader/missed`; PersonRow, SessionRow, People, Main, CompRows):
- **Column.** Renamed `Missed`. Header definition: `Model calls Claude Code reported that the proxy didn't capture, in this window.`
- **Cell.** `▲ 96` in `--danger`; `0` in `--ink-muted` when complete; `—` when there's no self-report. 84px fits `▲ 99,999`.
- **Tooltip.** The fraction moves here (captured, reported, %).
- **Ribbon tile.** The People ribbon tile becomes `Missed ▲ 94`.

**C3 Unattributed** (`PageHeader/unattributed`): option (a).
- **Page.** `/people/unattributed` uses the person-page layout with breadcrumb `people / unattributed` and title `Unattributed traffic`.
- **Subtitle.** `N sessions with no account or device identity · <span>`.
- **Content.** Identity header stats show `—`. The timeline, Sessions and All requests work as for a person.
- **Link.** "view traffic" goes there.

### D. Tables

**D1 "from #7"** (`SessionRow/partialCapture`, `PartialMark`, `Tooltip/partialCapture`; Main row 1):
- **Chip removed.** The `FROM #N` chip is gone.
- **Prompts cell.** Shows the full prompt count with a dagger: `34†`. The † is `--ink-muted`, dotted underline, cursor help.
- **Tooltip.** `† Partial capture`, then `Capture began at prompt #7. Prompts #1–#6 happened before aintercept was recording; tokens, calls and events start at #7.`
- **Session page header.** Gets the same mark beside Prompts.
- **Timeline.** The band's pre-capture fade stays, with the same tooltip. The CompTimeline live band now shows its session label instead of "seen from #12".

### E. Next (not for build)

**E1 Capabilities drill-down** (`Exploration/capabilities`, marked illustrative):
- **What it covers:** which built-in tools, MCP servers and tools, skills and hooks a session used; when each was first and last used; how often; p50 latency; and cost where attributable.
- **Sketch:** a Capabilities tab on the session page after Events, one row per item. A click opens a drawer listing every use (time, prompt, latency, result size). The Capabilities panel rows link to it.
- **Status:** to be specified in a later round.

### Manifest

420 sheet specimens, all unique.
- **Removed:** `Lane/presence`, `StateTrack/presence`, `Lane/network`, `StateTrack/network`, `Lane/friction`, `MarkerTrack/glyphs`, `TimeAxis/default`, `Legend/default`, `Lane/presenceLegendHover`, `Legend/hoverItem`.
- **Added:** 61, mostly on CompDensity.
- **Screen instances renamed:** `Lane/presenceRows`, `Lane/frictionSplit`, `Lane/focusRows`, `Lane/deviceSleepRow`, `Lane/networkRow`, `TimeAxis/ticks`, `Gridlines`, `GapRow`, `UpdatedAgo`, `PartialMark`.

## 11H. Round 11: brand, empty-space hover, headers, freshness (canvas updated)

New sheet: **`CompR11.dc.html`**. Main, Session, People, CompStates, AboutThisData, CompInteraction, CompDensity, CompTableHover and CompIndex are updated.

### 1. Brand: AIntercept (`Wordmark`, `Wordmark/appBar|large`, `TabMark/16|32`, `TabTitle`)

- **Wordmark.** `AIntercept` in Plex Mono 12.5/20, `--ink`, letter-spacing .01em.
  - `AI` is weight 600 and `ntercept` weight 400. Weight is the only distinction: same color, no glyph.
  - It replaces the `aintercept` text in every AppBar (`data-spec="Wordmark"`). The large size (18px) is for the About title.
- **Tab mark** (`favicon.svg` + a 32px PNG):
  - 16px `--bg` square, 1px `--line-strong` border, radius 2;
  - `AI` in Plex Mono 600 `--ink`, 8.5px;
  - a 10×1.2px `--focus` underline.
  - Dark only.
- **Tab title.** Page first, brand last:

  | Page | Title |
  |---|---|
  | People | `AIntercept` |
  | Person | `<person> · AIntercept` |
  | Session | `<repo or session> · AIntercept` (prefixed `● ` while live) |
  | Unattributed | `Unattributed traffic · AIntercept` |

- **Copy:**
  - About intro: `What AIntercept sees, how it gets it, what it keeps, and what the marks on every page mean.`
  - Pipeline node: `AIntercept UI` / `this page · polls every 5 s`.
  - Bypass note: `… is never seen. AIntercept finds it later in Claude Code's own analytics and lists it as "reported, not captured".`
  - NOT RETAINED tip: `Content not retained. AIntercept stores its shape (size, counts, types), not the text.`
  - Partial capture tip: `Capture began at prompt #7. Prompts #1–#6 happened before AIntercept was recording; tokens, calls and events start at #7.`
- **Unchanged:** repo names in the data (e.g. `aintercept · partner-readiness`) and internal names (binary, chart, images, CLI). The design system keeps its name, "aintercept Wire".

### 2. Hover over empty space: option (c), snap or readout (`BandTrack/snapped`, `Tooltip/sessionSnapped`, `Tooltip/laneReadout`, `Tooltip/emptyBin`, `Tooltip/stateReadout`)

- **Snap.** If a mark is within **16px** horizontally of the pointer, the nearest one snaps. For multi-row lanes, only marks in the hovered row count.
  - The snapped mark takes its hover style, opens its own tooltip, and a click does that mark's click.
- **Readout.** Otherwise, a one-line `Tooltip/laneReadout` appears at the guide's time: `--ink-muted` text, plus an optional hint naming the nearest mark.
  - Same 120 ms delay as marks. It has no click result: a click there clears the range (§11G.A8), and a drag brushes.
- **Per lane:**

  | Lane | Snap | Readout |
  |---|---|---|
  | Sessions | inside any band's span → that band (hovered row first); else the nearest band edge within 16px | `14:20 · no session open` · hint `nearest: <id> starts in Δ / ended Δ ago` |
  | Prompt ticks | within 16px | `14:20 · no prompt` · hint `previous: #12 at 10:41 · 6m before` |
  | Bar lanes | none needed: the bin under the pointer always answers; empty bins show `0`; click selects the bin | — |
  | Presence / focus / device | within 16px in the hovered row | the state across rows at that instant: `focused` / `away` / `device asleep` / `no session open` |
  | Friction rows | within 16px in the hovered row (cluster or single) | `14:20 · no ▲ destructive nearby · 77 in view` |
  | Network | the segment containing the instant, even if too narrow for a label | `14:20 · no network recorded` |
  | Gap row | within 16px | `14:20 · all reported calls captured` |

- **Guide and axis chip:** unchanged. They follow the pointer (snapping to the bin centre in bar lanes), whether or not a mark is snapped.

### 3. HeaderList (`HeaderList/short|anthropicResponse|longValue|redacted`; the Session Raw tab is updated)

- **Layout.** A two-column grid per list.
  - **Name column:** as wide as the longest name in the list (measured), capped at **400px**.
  - **Value column:** the rest.
  - **Spacing:** rows padded 3px (12px sides inside the box), 16px column gap.
- **Names** never wrap: `--ink`, mono 12.5/18. Over the cap they get a middle ellipsis, with the full name in the truncate tooltip.
- **Shared prefix.** A hyphen prefix of at least 8 characters, shared by 3 or more consecutive headers (e.g. `anthropic-ratelimit-unified-`), is drawn in `--ink-faint`. *Withdrawn in §11L.8: no dimming.*
  - Headers stay in wire order: no grouping, no sorting.
- **Values.** `--ink-muted`, `overflow-wrap: anywhere` (ids, JSON and URLs wrap inside the value column), no truncation. Redacted values are `--ink-faint`.
- **Rows.** A 1px `--line` rule under each row, `--surface-raised` on row hover, text selectable, no zebra striping.
- **Summary tab:** uses the same component.

### 4. Freshness slot (`UpdatedAgo/idle|polling|failed`, `Tooltip/updatedAgo`; People.dc.html)

- **Format.** An absolute time, `updated 11:32:05` (HH:MM:SS, 24h, local), so the length never changes. Older than 24h: `updated Nov 7`.
- **Slot.** A fixed **19ch** mono slot: a 10px indicator cell, a 6px gap, then the text, all on one line.
- **States.**
  - **Polling:** a 6px `--ink-muted` dot in the indicator cell, after 150 ms (§6A). The text stays.
  - **Failed:** `✕` (`--ink`, 600) in the indicator cell; the text keeps the last successful time. The reason and the retry are in the existing `Banner/refreshFailed`.
- **Tooltip.** The relative age and polling interval.
- **Replaces:** `updated N s ago`, `updating…` and `— last update failed · retrying`.

### 5. Slow-load copy (`SlowNotice/nameOnly|nameAndSize`; CompInteraction `SlowNotice/default` and `RegionState/slow` updated)

- **Pattern:** `Still loading <region>…`, optionally followed by `<size>` in `--ink-muted` (parts joined with ` · `).
- **Region names:** `the timeline`, `the summary`, `capabilities`, `footprint`, `sessions`, `requests`, `events`, `prompts`, `this request`.
- **Size:** only when it's already known (span · row count), never estimated.
- **Example:** `Still loading the timeline… 2 days · 21,305 events`.

### 6. Landing on a prompt (`PromptRow/arrived`)

- **What changes.** After a tick or friction click, the target row becomes **selected**: `--surface-raised` fill plus a 2px `--ink` inset left rule, expanded and centred.
  - Its timeline tick keeps its hover style while selected.
- **How long.** Until the next pointer-down, a tab or page change, or Esc.
- **Motion.** No flash, so reduced motion is the same.
- **Focus.** Keyboard focus moves to the row, with the ring visible only for keyboard arrivals.

## 11I. Round 12: parity rulings on round 11 (canvas updated)

1. **HeaderList: §11H.3 wins.** CompDrawer's HeaderList specimens are redrawn on the new layout and keep their names (`HeaderList/default`, `rowHost`, `rowAuthorizationRedacted`, `rowXClaudeCodeSessionId`, `rowRequestClass`):
   - a measured grid with a 184px name column (the longest name);
   - names in `--ink`, values in `--ink-muted` (redacted in `--ink-faint`);
   - a 16px column gap, padding 3px 12px, and a `--line` rule under every row except the last.
2. **Stacked bands: CompTimeline and the build win.** In lanes where bands stack, each band is **28px tall, every 34px, starting at 6px**. 26px from 8px is only for single-row lanes. CompR11 `BandTrack/snapped`, `Lane/sessionsSnap` and `Lane/sessionsReadout` are redrawn on that grid.
3. **Empty bin: snap to the grid.** `Lane/modelCallsEmptyBin`'s column now sits exactly on bin 211 (`211 × trackWidth / 336`, one bin wide). The tooltip and guide anchor to its centre.
4. **Tooltip corners: 2px everywhere.** CompR11's square corners were a mistake; all its tooltips now have the 2px radius.
5. **Snapped band tooltip: Status stays, and there is no Snapped row.** A snapped mark shows **its own tooltip, unchanged** (§11H.2), so `Tooltip/sessionSnapped` ends with `Status`, like `Tooltip/sessionBand`. The mark's hover style already shows the snap; the "Snapped" row is removed.

## 11J. Round 13: one schema across providers; Codex as the second agent (canvas: CompAgents)

The UI stays agent-neutral. Every screen takes `provider`, `product` and the common shaped fields. Provider extras come from `ext` and appear in exactly four marked places. Specimens are on **CompAgents** (`Agents: Codex & provider-neutral · round 13`).

### Screen rulings

1. **Not applicable ≠ unknown.**
   - Unknown keeps `—` (muted, dotted underline, tooltip with the reason; §3.3).
   - Not applicable needs a per-product capability map from the API: `na: true` on the field, or `capabilities[product][field] = false`.
   - Lanes, panels and header stats that are n/a are **not drawn**, and the layout closes up.
   - In mixed tables an n/a cell shows `n/a` in `--ink-faint`, mono 11, with no underline (`Value/notApplicable`). Its tooltip reads `<Product> doesn't report <field>`.
   - The session page ends its side column with `NotReportedLine`: `Not reported by Codex: presence, recaps, experiments, feature flags, hooks, skills, memory writes, cost.`
2. **Provider extras: four places, nowhere else.**
   - **Person header:** an `AgentStatGroup` after the common stats. It has a 1px `--line-strong` left rule, a `--surface` fill and an `AgentSectionHead` (7px agent dot + `CODEX · LIMITS` in micro caps).
   - **Session page:** one `AgentPanel` (`Codex · this thread`), last in the side column.
   - **Drawer → All details:** one `DetailSection` per agent (`Codex · response`), after the common sections.
   - **Timeline:** agent-only lanes go after the common lanes.
   - The UI renders a declared field list per product (label, `ext` path, formatter). Unknown `ext` keys are not shown.
   - Claude Code's extras (recaps, experiments, flags, hooks, skills, memory writes) move into the same places. Cost stays a common column and is n/a for Codex.
3. **Mixed lists.**
   - **People:** columns are unchanged; Agents shows one chip per agent. Tokens are summed across agents, and the tooltip splits them per agent. Missed and Bypass show `n/a` where the product can't report them.
   - **Person sessions and requests tables:** a new **Agent** column (140px) after the live dot, shown only when the person has more than one agent. It holds an `AgentChip` with the client after a muted middle dot (`Codex · exec`, `Codex · tui`); the minimum width becomes 1300.
   - **Repo cell:** Claude Code shows `repo · branch`; Codex shows `workspace basename · short sha`, with no branch.
   - **Label cell:** Claude Code shows the recap or first prompt; Codex shows its generated title. Thread facts follow in `--ink-faint`: `· 3 subagent · 1 review`, `· fork of 3b9a… at #4`, `· resumed from 3b9a…`.
4. **Chat variant (browsers) is kept open, not designed.**
   - `SessionPage variant="chat"` maps conversation to session, message to prompt, completion to model call, and web search, files, canvas and artifacts to tool calls.
   - It drops the repo, approval/permission, focus/device, context and friction lanes. It keeps the header, Prompts, Model calls, Network, the tabs and the drawer.
   - Nothing on the agent pages may assume a repo or a terminal (the repo cell and facts are optional).
5. **Linked person.**
   - Accounts with the same email form one person: one People row and one page.
   - The id line reads `linked by email · N accounts · N devices` (`PersonRow/linked`).
   - The person header lists each account under the name: agent chip, account and plan, devices (`PersonIdentity/linked`).
   - Accounts are not merged on anything weaker than `linkedBy`.

### Codex gaps

1. **`<environment_context>`:** strip it from prompt text.
   - cwd, shell and timezone become `SessionFacts` in the session header, after the title row. Approval and sandbox go there too.
   - If a later turn carries a changed block, that prompt row shows `env changed` in `--ink-faint`, with the diff in the tooltip.
2. **Prewarm and title calls:** hidden by default.
   - They are excluded from steps, bars, call counts and token totals.
   - Each prompt gets a `HiddenCallsNote` line (`+1 prewarm call hidden · show`), and the Requests tab gets a toolbar toggle `Show hidden calls (35)`.
   - When shown, they are quiet rows (§11F.1) with kind `prewarm` or `title`.
   - The session stat reads `63 +6 prewarm hidden`.
3. **Subagent and review threads:** each is its own session, linked to its parent.
   - **Parent:** the step row reads `⤷ spawn_agent <agent_name> · thread … · counts` with `open thread ›`.
   - **Child:** the header origin reads `subagent of <parent> · turn #N · thread_spawn | review`.
   - **Person sessions table:** children nest under the parent. The parent has a twisty, children are indented 28px with `⤷ <agent_name>`, and the parent's label adds `· 3 subagent · 1 review`. The table is collapsed by default.
4. **Forks and resumes:**
   - `ThreadOrigin` in SessionFacts reads `origin fork of <thread> at turn #N · warm fork | cold fork` or `resumed from <thread> · cold resume`. It links to the source thread at that turn.
   - The label cell adds `· fork of 3b9a… at #4`. `new` shows nothing.
5. **Usage limits:** a person-header `AgentStatGroup` (`Codex · limits`: `5h 41% · week 83%`, resets, credits). It is not a lane.
   - The tooltip (`Tooltip/codexLimits`) shows each window with its reset, plus `As of <time>`.
   - The session panel shows `Limits at end`, and the drawer section shows the snapshot taken on that response.
   - At 100% the value gets `▲` in `--danger`.
   - Claude Code uses the same group: `7d 83% · overage off`.
6. **Codex turn report:** `TurnReport` is the first line inside the expanded PromptRow, with an `AgentSectionHead`.
   - It is one mono 12 line in `--ink-muted`: sampling, requests, retries, tool counts, steers, compaction, trigger.
   - OTel timings join it when present. Fields that are absent are left out.
7. **Client on the session row:** the AgentChip carries the client after a muted middle dot. The session header adds the version (`Codex · tui 0.167`).
8. **Codex Browser (chatgpt.com):** out of scope. When it comes, it is ChatGPT web on the chat variant, not the Codex agent.

### Codex-specific pieces

- **Lanes:** Prompts · Model calls · Context (when cached tokens allow) · **Approval** (replaces Permission; segments read `approval policy · sandbox mode`) · Network · Friction.
  - Presence, Focus and Device are not drawn.
  - Analytics density is not drawn; Codex events are listed in Events.
- **Friction rows:** `✕ interrupted` and `▲ destructive`, plus the Codex-only `↻ steered` (steer count, `--ink`). `↯ frustrated` is not drawn for Codex.
- **Model-call step:** the model is shown in `--ch-model`; the detail adds `reasoning N · effort low|medium|high`.
- **Header:** a `Reasoning` stat is shown for any agent that reports reasoning tokens. There is no Cost stat for Codex.
- **Tool rows (ToolCallRow):**
  - `exec`/`exec_command`: command, exit, duration, approval outcome.
  - `apply_patch`: path, kind, `+38 −2 · accepted +38 −2`.
  - `mcp__server__tool`, `web__run`, `view_image` and dynamic tools: shown by name.
  - `spawn_agent`/`send_message`/`wait_agent`: thread steps as in gap 3.
- **AgentChip colours:** Codex `#9fb4c7`, Claude Code `#e8916f` (tokens unchanged).

### Data the API must provide

- `product`, `client`, `clientVersion` on sessions and requests.
- `capabilities[product]` (the n/a map).
- `ext` with declared keys per product.
- `person.accounts[]` + `linkedBy`.
- `session.facts{cwd, shell, tz, approval, sandbox}`.
- `session.origin{kind: fork | resume | subagent | review, threadId, turn, init}`.
- `session.children[]` (count by kind).
- `request.hidden` + `hiddenKind: prewarm | title`.
- `prompt.turnReport`.
- `person.limits[product]{windows[{name, usedPct, resetsAt}], credits, plan, asOf}`.

## 11K. Round 12 parity: CompR11 matched to CompTimeline (canvas updated)

1. **Stacked bands: CompTimeline is right.** In `BandTrack/snapped`, `Lane/sessionsSnap` and `Lane/sessionsReadout`:
   - the label top is **4px**;
   - bands are **content-box**, so the 2px left rule adds to the width (160 + 2 = 162px).

   CompR11 is redrawn to match.
2. **Bins use the track's real width, 1210px.** A bordered frame's track is 1344 − 2 − 132 = **1210px**, and every bin is `i × 1210 / n`.
   - The bars and the empty-bin column in CompR11 are redrawn on that grid. Bin 211 now starts at **759.9px** and nothing overhangs the track.
   - Rule: compute bins from the measured track width, never from a constant.
   - CompAgents is regenerated on the same grid. Older sheets drawn at 1212 are illustrative where they differ by 2px or less; the measured width wins.
3. **Tooltip text.** `.tipb` on CompR11 and CompAgents now sets `text-align: left; box-sizing: border-box; white-space: normal`, the same as `.tip` and the Tooltip component.

## 11L. Round 14: every screen by provider (canvas: CompProviders, People, Main, Session, CompStates, CompChips)

**P1:** a screen that rolls up several providers shows only fields every provider fills.
**P2:** a provider's page shows only that provider's fields, and says which provider it is.

Neither rule allows a placeholder: no `n/a` and no "not reported". A field the product doesn't report is simply absent. `—` remains for a field the product *does* report that we failed to get (§3.3).

CompProviders supersedes CompAgents (§11J) wherever they differ:
- `PersonRow/linked`, `PersonRow/codexOnly` and `PersonRow/claudeCodeOnly` → the CompProviders `PersonRow/*` rows;
- `Value/notApplicable` and `NotReportedLine` → retired;
- `HeaderStatGroup/withAgentExtras` and `AgentStatGroup/*` → `PersonHeader/*` and `AgentRow/*`.

Treat the superseded CompAgents specimens as illustrative.

### Field audit

The full table is the first block on CompProviders. In summary:

| Area | Common | Claude Code only | Codex only |
|---|---|---|---|
| People row | person, agents, last seen, sessions, prompts, tokens, activity, devices | bypass, missed, org | plan, 5h / week |
| Person header | name, email, accounts, sessions, prompts, tokens, devices, last seen | plan, org, role, using since, 7-day limit, flags, bypass, missed | plan, 5h, week, resets, credits |
| Session header | title, agent, device, span, prompts, model calls, tokens, tool calls | repo · branch, recap, permission, cost | workspace · sha, cwd, shell, tz, approval, sandbox, origin, client, reasoning |
| Timeline | prompts, model calls, network | presence, focus, device, analytics, permission, frustrated | approval, steered |
| Requests | time, channel, status, bytes, duration, model, tokens | channels: analytics, flags, count, account | channels: analytics, metrics, usage, catalog; hidden calls |
| Key strip | model, tokens, TTFT, duration | cost | reasoning, effort |

- **Reported by no provider yet:** org for Codex accounts, ChatGPT token counts. These are shown nowhere.
- **API:** serve `capabilities[product]` as the list of fields to render. The UI never renders a field that isn't in it.

### Rulings

1. **People table: Bypass and Missed (P1).** The All agents view, the default, has only the common columns. Selecting an agent card scopes the table to that agent and appends that agent's columns before the chevron:
   - Claude Code view: `Bypass`, `Missed`.
   - Codex view: `5h · week`, `Plan`.

   Other changes on the People page:
   - The agent cards carry each agent's headline. Claude Code: `▲ 94 calls missed`, sub `bypass on 62% of model calls`. Codex: `limits: highest week 83%`.
   - The ribbon's `Missed` tile is replaced by `Devices`.
   - The id line under a name is common: `email · N accounts · N devices` (or `linked by email · …`). Org and account ids leave the People table.
   - People.dc.html is updated (`ccCols` / `cxCols`).
   - CompRows `PersonRow/*` states are Claude Code persons in the Claude Code view. In All agents, drop their last two cells.
2. **AppBar agent chips.** They are removed: `AgentChip/headerActive` and `AgentChip/headerReserved` are retired, and Main, CompStates and CompChips are updated.
   - The AppBar is global chrome: wordmark, breadcrumb, window, live, freshness, About this data.
   - Coverage (which products we capture) lives in About this data, in a `Products` section, and in the People agent cards.
   - Scope is the People agent cards, or the sessions-table agent filter on a linked person.
   - The breadcrumb's person and session segments carry the agent dot(s).
   - Nothing says "soon".
3. **Person header.** It is laid out as:
   - `ProviderMark`;
   - name + subtitle;
   - one bordered block of rows. Each row has a 200px label column and a wrapping stat area: **All agents** (Sessions, Prompts, Tokens, Devices, Last seen), then one **AgentRow** per agent.

   The agent rows:
   - **Codex:** Plan, 5h window, Week (resets under each), Credits (formatted, e.g. `50,652` or `unlimited`; a window with zero minutes is omitted).
   - **Claude Code:** Plan, Organization, Role, Using since, 7-day limit, Flags, Bypass, Missed.

   Groups never sit side by side, so nothing wraps under another group. Stats wrap within their own row, aligned to the stat area's left edge, at any width (specimen at 1024). A one-agent person uses the same block with one AgentRow. Main.dc.html is updated.
4. **Org kind.**
   - The subtitle is `email · N accounts`; `org kind unknown` is gone.
   - **Organization** is a Claude Code AgentRow stat:
     - `your org` (sub: id);
     - `outside` (sub: `personal` or the id);
     - when no customer org ids are configured, the value is the org id with sub `ours? set org IDs` in `--ink-faint`, and the tooltip `Tooltip/orgUnset` names `CUSTOMER_ORG_IDS`.
   - Organization doesn't apply to Codex (the account is the workspace), so it isn't shown there.
5. **Provider on the page.** `ProviderMark` is an eyebrow directly above every person and session title:
   - the agent dot(s) + name(s) in caps (`--ink`, 600), then `· PERSON` or `· SESSION · <client version>` in `--ink-muted`;
   - mono 11, letter-spacing .06em;
   - linked person: `CLAUDE CODE + CODEX · PERSON · LINKED BY EMAIL`.

   It is the one place that spells out the provider; rows and sections repeat only the dot. Main and Session are updated.
6. **Session Requests tab.**
   - **Filter chips:** each product declares its channels. Show chips only where the count is above 0, plus ALL.
     - Codex channels: Model calls, Analytics, Metrics (OTel), Usage & account (`wham/*`, accounts), Catalog (`ps/*`, `codex/models`).
     - Hidden prewarm and title calls are a `show hidden calls (N)` checkbox, not a chip.
   - **Detail column:** shows what tells calls apart; the path is dropped when it is the channel's usual one (it stays in the tooltip and the drawer).
     - A model call (any product) reads `#<prompt> · step <n>` (bold) · model and effort · tokens in → out · what it produced (`exec ×2`, or the first words of text in quotes) · stop reason.
     - Other channels show the event or endpoint name plus one key value (`codex_turn_event · turn #1 · 203 requests · 2 retries`, `wham/usage · 5h 12% · week 31%`).
7. **Prompts vs requests.**
   - **Word:** keep **Prompts** for every provider. On Codex, the tab hint adds `Codex calls a prompt a turn`; the turn report keeps Codex's word.
   - **Tab labels:** each label carries count + unit: `Prompts 1 · 203 steps`, `Requests 203 calls · 412 other`. The shared 203 links the two tabs.
   - **TabHint:** one always-on line under the tab bar (mono 11, `--ink-faint`) describing the active tab:
     - Prompts: `What <person> asked, and the work each message started…`;
     - Requests: `Every call on the wire for this session, in time order, including calls no prompt started (…)`.
   - **Long turns** (more than 20 steps): a **StepStrip** under the prompt row.
     - One bar per step, coloured by what it did: exec `--ink`, apply_patch `--ch-count`, mcp `--ch-flags`, text only `--ch-model`. Height is set by output tokens.
     - Totals by kind sit beside it. Click or drag jumps the list.
   - **StepRun:** consecutive command-only steps (5 or more) collapse into one row: range, count, tool counts, notable commands, failures in `--danger`, span, tokens.
     - These steps break a run and show on their own: file changes, agent spawns, interrupts, and text output.
   - **Paging:** the list renders 120 rows (a run counts as one), then `show N more`. The strip always covers the whole turn.
8. **HeaderList prefixes.** No dimming. §11H.3's prefix rule is withdrawn: names are `--ink`, values `--ink-muted`, redacted values `--ink-faint`, in wire order. The §11I.1 grid stays.
9. **Summary labels.**
   - The label column is fixed: 220px in the 520px Summary column, 240px in a 640px drawer.
   - The name truncates with an ellipsis; the count suffix (`×2`) never truncates and stays at the visible end.
   - The full label is a tooltip on hover and focus.
   - Rows don't wrap.
10. **Key strip, Codex.**
   - The tiles are Model (sub: effort) · Tokens (in → out, sub: cache read) · Reasoning · TTFT · Duration (sub: stop reason), five equal tiles. There is **no Cost tile** for Codex; Claude Code's strip is unchanged.
   - TTFT always shows a value when one exists; the sub names its source (`by Codex` or `at proxy`). It shows `—` only when neither exists.
   - A call with no usage shows `—` (unknown, dotted underline) in Tokens and Reasoning, sub `not captured`, and a tooltip with the API's reason.

### Round 13 conflicts

`brief-round13-conflicts.md` wasn't attached to this round, so those 9 aren't ruled on here. Where a conflict is in a CompAgents specimen this round supersedes, follow CompProviders. Send the file for the rest.

## 11M. Rounds 13–14 open items (canvas: CompProviders, CompAgents, Session, People, CompRows, AboutThisData)

**Standing rule:** where CompProviders and a canonical sheet (CompRows, CompControls, CompDrawer, CompStates, CompTimeline, CompDensity) disagree on geometry, **the canonical sheet wins**. CompProviders now uses their markup.

The manifest gains an `illustrative` map. Specimens listed there are sketches or superseded, and aren't parity targets.

### Part 1: round 14 conflicts

| # | Ruling | Sheet changed |
|---|---|---|
| 1.1 | Copy is `customer org` / `outside org`. Tooltip: "AIntercept can't tell the customer organization's accounts from outside ones until it's given the customer org IDs. Set them in deployment config (`CUSTOMER_ORG_IDS`)." The build is right. | CompProviders |
| 1.2 | Agents column 260. Bypass 90 (People.dc.html updated too). chatCommon tokens `—` in `--ink-faint`, not nested. CompRows wins. | CompProviders, People |
| 1.3 | AppBar = CompStates markup: SegmentedControl, inline breadcrumb, bottom border only. People keeps `Active in the last` + UpdatedAgo. | CompProviders |
| 1.4 | **Every** crumb carries the dot(s) of what it points to: the person crumb the person's agents, the session crumb the session's agent. A Codex session under a Codex person shows the Codex dot twice. New specimen `AppBar/mixedPersonCodexSession`. | CompProviders, Session |
| 1.5 | Canonical RequestRow columns and headings for every provider (Time, #, Kind, Meth, Path · detail, Status, Dur, Up, Down; amended in §11O.5). Only the Detail text follows §11L.6. Since `#` holds the prompt, Detail starts at `step <n>`. Codex WebSocket turns read `WS`. | CompProviders |
| 1.6 | Canonical PromptRow columns. The step count goes in the steps column (`203`). | CompProviders |
| 1.7 | ToggleChip (§11G.B2): pressed = ink fill + ✓, unpressed muted, square swatches. ALL is pressed by default. | CompProviders |
| 1.8 | HeaderList = §11I.1: one grid per row, name column = longest name capped at 400px (corrected in §11N.3), 12.5px, rule between rows. | CompProviders |
| 1.9 | Reasoning unknown uses the 400 dotted unknown style, the same as Tokens. | CompProviders |
| 1.10 | Session stats sit **under** the title, subtitle, recap and facts, in one wrapping HeaderStatGroup, for every provider. This matches the person header. The build is right. | Session.dc.html redrawn; `SessionHeader/codexMarked` now carries facts and stats |
| 1.11 | Marked illustrative: `SessionHeader/codex`, `SessionFacts`, `ThreadOrigin`, `PersonRow/linked`, `PersonRow/codexOnly`, `PersonRow/claudeCodeOnly`, `Value/notApplicable`, `NotReportedLine`, `HeaderStatGroup/withAgentExtras`, `AgentStatGroup/*`, `AgentPanel/codexSession`, and `SessionRow/claudeCode`, `codexParent`, `codexChild`, `codexReview`, `codexFork`. | CompAgents |

### Part 2: round 13 rulings

| # | Ruling |
|---|---|
| 2.1 | Confirmed: §11L.1 supersedes it. CompRows `PersonRow` id lines now read `email not seen · 1 account · 1 device`. |
| 2.2 | The indent goes **inside** the Repo cell; columns stay aligned. The build is right. Redrawn as `SessionRow/mixedCodexChild` and `mixedCodexReview`. |
| 2.3 | Destructive is the danger ▲ Marker in `--danger` (CompRows). Redrawn as `SessionRow/mixedCodexFork`. |
| 2.4 | Prompt ticks: 16px, starting 5px down, in a 26px lane (CompTimeline). CompAgents fixed. |
| 2.5 | The network row follows CompDensity: segments 5px down, each sized to its span, label clipped inside. Approval is styled like Permission: `--surface-raised` fill, `--line-strong` outline, 6px down, 6px label inset, in a 30px lane. CompAgents fixed. |
| 2.6 | `Timeline/codex` gains `TimeAxis/codex` (24px), the same as every timeline. |
| 2.7 | Under P1 the mixed sessions table (`SessionTable/mixed`) keeps only common columns: Started, live, Agent 140, Repo **240** (canonical), Label, Prompts, Span, Calls, Friction.<br>Events, Bypass and Missed are dropped. Scoped to Claude Code, the table is the canonical SessionRow with no Agent column. Scoped to Codex, the Agent column goes. |

### Part 3: new states

**3.1 No recorded bodies.** This is the default for customer installs.
- Rows show what the call carries, closing up where content is missing, with no placeholders.
- **Step kind and tool counts are metadata**, so `exec ×2` and `apply_patch ×1` stay. A text-only step reads `text`.
- These drop: commands, exit codes and failures, patch targets and line counts, text excerpts, and prompt text. Prompt text becomes its length (`412 chars`).
- A StepRun keeps its range, step count, tool counts, span and tokens. There is no failing variant without exit codes.
- Specimens: `PromptRow/longTurnNoBodies`, `StepRow/longTurnNoBodies`, `longTurnPatchNoBodies`, `longTurnTextNoBodies`, `StepRun/collapsedNoBodies`, `runNoBodies`, `RequestRow/codexModelCallNoBodies`, `codexModelCallTextNoBodies`.
- **Where it's said:** once, in About this data → **What is kept** (`RetentionList/metadataOnly`): Mode `METADATA ONLY`, "bodies are not recorded on this deployment", and a Not kept row naming what never appears. Nowhere else.
- **Check this on your side:** the current What is kept says SHAPE ONLY keeps "size-capped tool inputs (paths, commands, URLs)". If that's true, those commands *are* metadata in that mode and StepRun shows them. The rule is to render what the call carries.

**3.2 About this data → Products.**
- A new first section, `AboutSection/products`. AboutThisData.dc.html is updated.
- One row per captured product (from `capabilities`): agent dot + name, shape, hosts. A product with no traffic adds `· no traffic in this window`.
- `Claude Code's five channels` becomes `Channels`, with one ChannelTable per captured product.

**3.3 Person → All requests.**
- **Scope:** the person's `AgentScope` SegmentedControl (`All agents · Claude Code · Codex`, shown only when the person has more than one agent) scopes both the sessions table and All requests.
- **All agents:**
  - Chips are the shared channels: Model, Analytics, Account & usage, Other.
  - The hidden-calls checkbox shows when any in-scope agent has hidden calls.
  - Rows add a 24px agent-dot column after `#`, and Detail follows each row's product.
- **One agent:** that agent's own chips, with no dot column.
- Requests aren't grouped by agent, so the time order is kept.
- Specimens: `RequestFilters/personAllAgents`, `RequestTable/personAllAgents`, `RequestFilters/personCodex`, `AgentScope/person*`.

**3.4 Untitled Codex thread.**
- **Label:** `untitled · thread 01a1…aed1` in `--ink-faint`, followed by any thread facts (`SessionRow/mixedCodexUntitled`).
- **Session title and breadcrumb:** `<workspace> · <first-turn start>` (`pomodoro · Nov 9 09:00`).
- **Subtitle:** `untitled · thread … · sha`.

### Part 4: data notes, applied

- **TTFT:** Codex reads `at proxy` today. Keep `by Codex` for when OTel can be matched to a call.
- **Usage missing:** the tooltip reasons are `Response exceeded the old 512 KB parse buffer (before Nov 9)`, `Stream ended early`, `Turn interrupted` and `Call failed`.
  - Prewarm isn't unknown: Tokens reads `in → 0`.
- **Codex facts without bodies:** cwd, approval, sandbox, origin. Shell and timezone appear only with bodies; otherwise they're left out (`SessionFacts/codexNoBodies`).

## 11N. §11M follow-ups (canvas: CompProviders, CompAgents, CompRows, Session, CompR11, People, AboutThisData)

1. **What is kept: the build's copy is right.** Metadata-only keeps no tool inputs. `RetentionList/metadataOnly` and the AboutThisData screen now read:
   - **Mode:** `METADATA ONLY`, with "bodies are not recorded on this deployment".
   - **Kept:** headers, token counts, timings, model and tool names with counts per call, and each agent's own analytics events as sent. Claude Code's events include each command's program and exit code.
   - **Not kept:** tool inputs (full commands, paths, URLs), Codex's exit codes, prompt text, responses, file contents, Codex titles, shell and timezone.

   Runs follow "render what the call carries":
   - **Claude Code:** a StepRun shows programs and failures with or without bodies. New specimen: `StepRun/claudeCodeNoBodies`, reading `Bash ×21 (npm ×14, rg ×5, git ×2, exit ≠ 0 ×6)`, with the failures in `--danger`.
   - **Codex:** a StepRun has tool counts only.
2. **Prompt cell without bodies:** just `N chars`. `· not retained` is dropped from CompRows, Session.dc.html and CompR11. The NOT RETAINED chip stays only on drawer content rows.
3. **CompProviders aligned to the canonical sheets:**
   - The person-scope request table uses the canonical person RequestRow: Time 108 dated (faint), Kind, Session 150, Meth, Path · detail, Status, Dur, Up, Down.
     - The agent dot (24px) sits right after Time.
     - There's no `#` column, so a model call's detail starts `#<prompt> · step <n>`.
   - The session request `#` cell is bare (`1`), and its Time is faint.
   - **HeaderList name column:** the longest name in the list (measured, +4px), capped at 400px. **§11M 1.8's "184px" was wrong**; the canonical rule stands.
   - The expanded prompt row uses the raised background (`--surface-raised`).
   - Unpressed ToggleChips are `background: transparent`.
   - SegmentedControls (AgentScope) are `display: flex`.
   - The `people` crumb is faint. People.dc.html is reverted to `#848c90`.
   - About this data sections put the `h2.k` title outside the box (Products, What is kept).
4. **Geometry and counts:**
   - `TimeAxis/codex` is redrawn in the canonical TimeAxis markup: % positions, 3px/6px ticks, 11px labels 8px down, centred.
   - The Approval bar is `left: 0%; right: 0.5%`, like Permission.
   - Network segments use % left and width, like CompDensity.
   - Bytes use the canonical format: `300 B`, `600 B`, `200 B`.
   - `RequestFilters/personCodex` ALL = 615, the sum of its chips.
5. **Codex header Tokens and Tool calls:** noted. They were already in the specimen.

## 11O. Last sheet-against-sheet fixes (canvas: CompProviders, CompAgents)

1. **Bytes:** a trailing `.0` is dropped. `RequestRow/codexPrewarmShown` Sent now reads `2 KB`.
2. **Network segments:** CompDensity markup.
   - A 5px-down wrapper, `left: 0; right: 0`, 18px tall.
   - Inside it, bordered segments with `overflow: hidden`.
   - Each label is a span at `left: 4px`.
   - `Lane/codexNetwork` is redrawn this way.
3. **Codex time axis:**
   - It covers the lanes' window: 42m 30s, 170 bins × 15 s, 14:20:00 → 15:02:30.
   - Ticks every 5 min, including the start tick at 14:20 (6px at 10-minute marks, 3px otherwise).
   - Labels every 10 min. There's no label at the window's left edge (14:20), since a centred label would spill into the label column; this matches the canonical axes.
4. **People AppBar:** row gap 12px when the window control is shown (`gap: 12px 16px`). UpdatedAgo keeps its 10px indicator cell inside the 19ch slot. The build is right.
5. **One set of request headings:** the canonical `Time · # · Kind · Meth · Path · detail · Status · Dur · Up · Down`, which Session.dc.html and Main.dc.html already use. Person scope swaps `#` for `Session`.
   - CompProviders' session-scope headings are corrected (I had written `Method · Detail · Duration · Sent · Received`).
   - §11M 1.5 is amended.

## 12. Acceptance checklist (whole project)

- [ ] `tokens.css` generated; no hex literals outside it (lint rule `stylelint-declaration-strict-value`).
- [ ] Every component in §6 exists, is exported from `components/index.ts`, and has a story per listed state.
- [ ] Storybook a11y: zero violations. Keyboard: tabs, brush, drawer focus trap, rows.
- [ ] All screen stories in §8 render against MSW with fixture data from §9.
- [ ] Unknown values render `—` with a tooltip everywhere; a contract test fails if `null` renders as `0`.
- [ ] Every clickable control shows its pressed state on pointer-down. Every control that starts work shows `pending` (spinner, gerund label, `aria-busy`, ignores repeat clicks) and then success or an error at the point of action (§6A.2, §6A.4).
- [ ] Every query-backed region renders through `RegionState`:
  - first load → skeleton;
  - refetch → previous content dimmed plus a progress bar after 150 ms, shown for at least 400 ms;
  - slow (8 s) → "Still loading…" with Cancel;
  - 30 s → `InlineError` with Retry, keeping the data;
  - `EmptyState` only after success.
- [ ] The 10 interaction tests in §6A.11 pass. Each screen story has been reviewed under the `normal`, `slow` and `error` latency settings.
- [ ] Motion tokens are used for every transition, and `prefers-reduced-motion` is honored (§6A.7). No layout shift between loading, loaded, empty and error (§6A.8).
- [ ] No native `title` attributes anywhere; every explanation is a `Tooltip` with the §6B.2 timing, and the 18 interaction tests (§6A.11, §6B.11) pass.
- [ ] Timeline: the guide, axis readout, every lane's hover and tooltip, click results, roving keyboard focus and keyboard brush work as §6B.4 and §6B.8.
- [ ] Linked highlighting works in both directions for every table and never scrolls (§6B.5).
- [ ] Every `(6B)` field in §4 is served by the API (MSW and real), so tooltips never fetch.
- [ ] URL state round-trips (reload restores the range, tab, filters, page and open drawer).
- [ ] Polling: 5 s visible, 30 s hidden; Pause works; the delayed banner after 90 s without data; a refresh failure keeps the data and backs off.
- [ ] Live rows never reorder open content; the "N new" pill appears when scrolled away.
- [ ] Credentials never appear in Raw (server-redacted; a client test asserts `authorization` is redacted).
- [ ] Runs at 390px without horizontal page scroll; the drawer goes full width below 960px.
- [ ] Phase 4: API endpoints in §5 implemented with the same TypeScript types (share `types.ts` or generate from OpenAPI); the MSW handlers and the real API pass the same contract tests.
