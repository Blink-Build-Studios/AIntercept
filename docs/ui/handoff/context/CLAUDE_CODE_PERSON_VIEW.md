# Person view: Claude Code (design brief)

A brief for a design session: the first page of the aintercept UI. It shows one
person's Claude Code activity, live, as it passes through the proxy. Other
agents and chat apps (Codex, ChatGPT, claude.ai) come later and must fit into
the same structure; this brief notes where to leave room for them.

This page is a person-first, live slice. An earlier brief,
`docs/FORENSICS_CONSOLE_BRIEF.md`, describes org-wide aggregate screens (MCP
servers, model cost) behind a shared filter bar. Those remain the later org
level (§10). Where the two briefs disagree about the data, this one is newer
and checked against real traffic.

Everything here was checked against traffic captured through the DO cluster
on 2026-11-02/03, by two independent reviews: one fact-checked every claim,
and the other inventoried everything captured that the first draft left out.
`docs/TAXONOMY_ANTHROPIC.md` documents the fields.

Each data item is marked with its availability:

| Mark | Meaning |
|---|---|
| **now** | Captured and parsed into a field today |
| **extract** | In the intercepted traffic; the observer needs to parse it into a field (§11) |
| **content** | Part of prompt, file or response text: present only when full content is retained, or with a targeted extraction that needs a policy decision |
| **none** | Not in the traffic; needs another source |

---

## 1. Why this page exists, and the reaction it's built for

The sales motion is a link. Someone sends it to a prospect, who gives it to
their agent; the agent deploys aintercept and routes traffic through it. Then we
open a browser together and watch the traffic arrive.

**The reaction to design for: "holy shit, there's so much here."** Within
seconds, a technical leader should grasp how much flows between one person's
agent and the vendor: what the agent can reach, what it ran, what it sent,
how the person was doing, and how much Claude Code reports about all of it.

**But not alarmism.** The page doesn't lead with "here's everything that
leaked". It leads with a person and makes the depth *visible and explorable*.
The volume and breadth are the impression; the drill-downs are the substance.

**Who is looking:** technical leaders (CTO, CISO, CEO with an engineering
background), plus us. The reference points are Burp Suite and a browser's
network panel, not a finance dashboard.

**What it isn't (yet):** an org dashboard, a cost report, a policy console, or
a multi-provider view.

## 2. How the data is collected (what the page stands on)

The page should say this plainly somewhere (an "About this data" panel, or a
legend). It is also what makes the numbers credible.

### The pipeline

```
Person's machine (Claude Code)
  │  DNS for api.anthropic.com → the proxy; the machine trusts the customer's CA
  ▼
Envoy (TLS interception)
  │  terminates TLS with a certificate minted for api.anthropic.com
  │  (certsvc, signed by a name-constrained intermediate the customer supplies)
  │  forwards the request to Anthropic unchanged, over real verified TLS
  │
  ├─▶ Anthropic (the real API; the response returns the same way)
  │
  └─▶ Observer (a copy of every request and response, out of band)
        observability mode: it never delays or blocks traffic
        parses → rows (model calls, analytics events, transactions)
        writes NDJSON to object storage (S3 / DO Spaces), by date and hour
```

- **Nothing is installed on the person's machine** beyond routing and trusting
  the CA. Claude Code is unmodified; we read what it already sends.
- **Identity** comes from the traffic itself: the account and device IDs
  Claude Code sends, the connection's IP, and a salted hash of the credential.
  The credential itself is never stored.
- **Content retention** is a deployment setting. In experimentation mode
  (current DO cluster), full bodies are kept. In production, bodies are not
  retained by default and the page works from parsed fields (§3, principle 3).

### The five channels Claude Code uses (all on `api.anthropic.com`)

| Channel | Endpoint | What it carries | Volume (one session) |
|---|---|---|---|
| **Model calls** | `POST /v1/messages` | Each step of the agent loop: model, settings, tokens, tool calls, stop reason; on a thread's first request also the tool catalog, MCP servers, skills, injected context (git status, working directory, user email, memory) | 300 requests, **37.2 MB up**, 1.9 MB down |
| **Product analytics** | `POST /api/event_logging/v2/batch` | Claude Code's own event stream: **105 distinct event types**, each carrying ~45 environment fields: every prompt, tool call, command, file operation, git operation, compaction, cancel, focus change, cost | **11,698 events, 28.9 MB up** |
| **Feature flags** | `POST /api/eval/sdk-*` | The person's attributes as Anthropic's flag system sees them (plan tier, org role, subscription start, first-use date) and the flags/experiments they're in | 1,072 flags; 12 experiments the person is enrolled in |
| **Token counting** | `POST /v1/messages/count_tokens` | Whole files' text, sent to measure them | 4 calls |
| **Skills listing** | `GET /api/oauth/organizations/<org>/skills/list-skills` | Installed skills and their owner (full name, email) | 34 calls |

The analytics channel, **28.9 MB of telemetry against 37.2 MB of model
traffic**, is itself a "holy shit" fact worth surfacing: Claude Code reports
almost as much about the session as it sends to the model.

### Two properties that shape the design

1. **Model requests are incremental.** Most `/v1/messages` calls are thread
   continuations carrying only the 1–2 newest messages (259 of 284 main-loop
   requests are under 10 KB). The tool catalog, system prompt, injected
   context and skills arrive only on a thread's *first* request, compactions
   and recaps. So the page builds session-level facts (repo, capabilities,
   identity) from those requests and carries them forward. A single step's
   detail can't show the full context window on its own.
2. **Claude Code reports on itself, and that report is a cross-check.** Every
   model call it makes produces a `tengu_api_success` event with tokens and
   cost, joinable to the captured call by `requestId`. When analytics report
   calls the proxy never saw, traffic went around the proxy. This has already
   happened: about 94 model calls in two windows, most likely over IPv6
   (`docs/INTERCEPTION_ISSUES.md` C6). The page should show this as a
   **coverage check** (§5, header).

## 3. Design principles

1. **Show the breadth, then let people dig.** Say how much exists up front
   (counts, channels, capability size); put the substance one click down.
2. **Progressive disclosure.** Each level summarizes the next: person, then
   sessions, then prompts, then steps, then raw requests. Every number clicks
   through to the rows behind it.
3. **One layout, with or without content.** Where text isn't retained, show
   its *shape* (size, path, tool, command class) and a quiet "not retained"
   chip. Don't design two pages.
4. **Live, but readable.** The page refreshes on an interval; new activity
   arrives without moving what the viewer is reading.
5. **Honest numbers.** Mark estimates, gaps and coverage. Never show zero
   when the truth is "unknown" or "not seen".
6. **Neutral voice.** Never "your": the page is viewed by people other than
   its subject.
7. **Built to extend.** Give each agent/app a consistent identity (icon and
   color); Claude Code is the first of several.

## 4. The data model

```
Person (account)
 └─ Device (machine)
     └─ Session (one Claude Code run)
         └─ Prompt (one thing the person typed)
             └─ Step (one model call, plus the tool calls it made)
                 └─ Raw requests (HTTP transactions on the wire)
   + Session-level state carried forward: capabilities, posture, repo, context
   + Event stream: the analytics events for each level
```

**Prompt vs. model call.** One prompt sets off an agent loop: the model asks
for a tool, Claude Code runs it, and the result goes back in another model
call. In the captured session, 14 prompts produced about 290 model calls. The
unit people think in is the **prompt**; model calls are **steps** inside it.

| Level | Identified by | Available |
|---|---|---|
| Person | `account_uuid`, `provider_org_id` | **now** |
| Device | `device_id`, IP, OS, terminal, Claude Code version | **now** |
| Session | `session_id` | **now** |
| Prompt | `x-claude-code-prompt-id` header (in the `client` map, not yet a top-level field) on 287 of 300 requests; absent on recaps and compactions. Analytics: `cc_prompt_id`; prompt number: `cc_prompt_index` in the billing header | **now** (needs promoting to a field) |
| Step | `message_id`, chained by `previous_message_id`; `request-id` | **now** |
| Subagent | `x-claude-code-agent-id` / `-agent-type` (e.g. `Explore`), `request-class: subagent` | **now** |
| Background model calls | `request-class`: `auxiliary` (recaps, prompt suggestions), `compaction`; `count_tokens` | **now** |

### The captured session, for scale

This is the session this brief was written in. The proxy saw it from prompt
12 onward (the first 11 prompts predate the capture), so these numbers cover
the **capture window**, not the whole session. The page must make that kind of
partial coverage visible too.

| | |
|---|---|
| Capture window | Model calls 14:20–22:40 UTC (8 h 20 m); analytics continue to 00:57 |
| Prompts seen | 14 (prompt numbers 12–25) |
| Model requests | 300: 284 main, 3 subagent (Explore on Haiku), 8 auxiliary (7 recaps, 1 suggestion), 4 token counts, 1 compaction |
| Tokens (299 parsed rows) | 140.9 M cache-read, 650 k cache-write, 384 k output, 3.6 k uncached input |
| Cost (Claude Code's own estimate) | $44.94 across 304 reported calls, **37 of which the proxy never saw** |
| Tools (analytics) | 282 tool calls: Bash 269, plus Read, Write, Edit, Agent, MCP |
| Top command words | `cd` 55, `python` 53, `sed` 24, `cat` 21, `grep` 17, `kubectl` 13, `docker` 8, `git` 8 (mostly the first word of a chain) |
| Analytics | 11,698 events across ~105 types (3,464 are tool-progress ticks) |
| Bytes up | 37.2 MB model requests, 28.9 MB analytics |
| Context | One compaction: 905 k → 21 k tokens, 2,207 messages summarized, 71 s wait |
| Human | 1 frustrated prompt; 1 Esc-cancel mid-response; away up to 3.5 h; laptop sleeps detected |
| Risk posture | `bypassPermissions` on every tool call (no human approvals); sandbox off; 10 commands Claude Code classed destructive (6 `rm -f`, 4 `rm -rf`, mostly inside chains) |
| Failures | 9 failed commands, 11 tool errors, 5 API retries (connection errors) |
| Code shipped | `head_sha` changed 10 times; commits, a push, PR #2 linked and polled 80 times |
| Reach | 11 MCP servers with 381 tools (Gmail, Google Drive, Vercel 244, Playwright, …), 87 skills, 15 hooks |

Design for sessions from 2 prompts and 5 minutes up to this one.

## 5. Page structure

```
┌───────────────────────────────────────────────────────────────────────────┐
│ A. PERSON HEADER   identity · devices · plan · posture       [window][live]│
│   B. activity strip: model calls / analytics / person lane / sessions     │
│   C. scale ribbon: channels and volumes      D. coverage check            │
├───────────────────────────────────────────────────────────────────────────┤
│ E. CAPABILITIES ("what the agent can reach")   F. FOOTPRINT ("what it did")│
├───────────────────────────────────────────────────────────────────────────┤
│ G. SESSION LIST (live, newest first)                                       │
│  ▾ session ── H. session panel (posture, context gauge, repo trail, PRs)  │
│      I. prompt rows                                                        │
│        ▾ J. step rows (model call + tool calls)  → K. detail panel        │
│ [All requests] toggle: Burp-style flat log                                 │
└───────────────────────────────────────────────────────────────────────────┘
```

---

### A. Person header: who is this?

| Element | Source | Available |
|---|---|---|
| Account ID, org ID | `account_uuid`, `provider_org_id` | **now** |
| **Name** | skills listing `owner.full_name`; git user name in injected context; claude.ai account rows (same `account_uuid`, already parsed by `claudeAccount`) | **extract** |
| **Email** | skills listing `owner.email_address`; claude.ai `/api/account`; `userEmail` in Claude Code's injected context | **extract** (skills, claude.ai) / **content** (injected context) |
| Org role | flag attributes `organizationRole` (e.g. `admin`) | **extract** |
| Plan | analytics `subscription_type` (`max`); flag attributes `rateLimitTier` (`default_claude_max_20x`) | `max`: **now**; tier: **extract** |
| Customer since / Claude Code user since | flag attributes `subscriptionCreatedAt`, `firstTokenTime` (e.g. "using Claude Code for 15 months") | **extract** |
| Corporate vs. personal account | `provider_org_id` vs. the customer's org | **now** (with the org configured) |
| Devices | `device_id`; IP; `env.platform`, `terminal`, `shell`, `package_managers`, `runtimes`, Claude Code and Node versions; CI / remote / GitHub Action flags; memory limit | **now** |
| Account limits | `anthropic-ratelimit-unified-7d-utilization`, `-status`, `-representative-claim` (`seven_day`), `-overage-status` (`rejected`, `org_level_disabled`), `-fallback-percentage`, grace status | **now**. The 5-hour header never appeared; don't design a 5 h gauge |
| Security posture | permission mode, sandbox, token storage (`keychain-with-plaintext-fallback`), proxy detected (`gzipSkipReason: custom_ca`) | **now** (analytics) |
| Last seen; live dot | latest request | **now** |

With no name, show the short account ID and "name unknown"; never a blank.

### B. Activity strip: time

One shared time axis (default last hour while live; presets 15 m / 1 h / 24 h / 7 d):

- **Model-call lane**: marks per call, sized by tokens.
- **Analytics lane**: event density (it's the visible proof of how chatty
  the client is).
- **Person lane**: when the person was present: prompts typed, away periods
  (`tengu_return_to_session.blurDurationMs`), laptop sleep
  (`tengu_event_loop_stall.likely_sleep`, `clock_jump_ms`), and the agent's
  working/blocked state (`tengu_bg_classify`, `minsInPrevState`).
- **Sessions** as bands behind the lanes; friction markers on the axis.
- Brushing a range filters everything below.

### C. Scale ribbon: how much is there?

A compact strip of channel counters for the window, each clickable:

`300 model calls · 37 MB sent` · `11,698 analytics events · 105 types · 29 MB` ·
`1,072 flags · 12 experiments` · `381 tools reachable` · `140 M tokens`

This is the main "holy shit" moment. Keep it factual and dense, not decorated.

### D. Coverage check

Compares what Claude Code says it did with what the proxy saw:
"Claude Code reported 304 model calls; the proxy captured 267 of them."
Unmatched calls are listed with times, linking to the issue (C6). Show it
quietly when coverage is complete and prominently when it isn't. Sources:
`tengu_api_success.requestId` / `clientRequestId` against captured
`request-id` / `x-client-request-id` (**now**; the join needs materializing).

### E. Capabilities: what the agent can reach

This was missing from the first draft, and it's likely the strongest moment
for a CISO: "this person's agent can share files from their Google Drive."

| Item | Source | Available |
|---|---|---|
| Connected MCP servers and their tools (with risky verbs visible: `share_file`, `trash_file`, `create_project_env`, `buy_domain`…) | thread-create `tools` (deferred tool names), MCP server instructions; tools added mid-session arrive as `tool_addition` system messages | **extract** |
| Built-in tools | thread-create `tools` (23 core tools) | **extract** |
| Skills (87) and subagent types | thread-create request; skills listing | **extract** |
| Hooks and plugins | `tengu_run_hook.pluginHookCounts`, hook IDs; `preToolHookDurationMs` (~50 ms added to each tool call) | **now** |
| Memory | `tengu_memdir_loaded` (files, type); `tengu_memdir_file_write` / `memdir_pin_write` ("agent wrote 2 long-term memories"); memory contents | events: **now**; contents: **content** |
| Instructions (CLAUDE.md, MEMORY.md) | injected context | presence/size: **extract**; text: **content** |

Show "used vs. available" side by side: 2 MCP tools used out of 381 reachable.

### F. Footprint: what it did

Totals for the window, each clicking through to a filtered list:

| Item | Source | Available |
|---|---|---|
| Sessions, prompts, model calls, subagents | counts | **now** |
| Tokens (cache-read, cache-write, uncached input, output: shown separately) and cost | response `usage`; `costUSD` | **now** |
| Repos and working-directory trail | injected `gitStatus` / branch (snapshot at thread start), `# Environment update` messages ("working directory: …/observer (was …/aintercept)"), `tengu_shell_set_cwd` | **extract** |
| Code shipped | `head_sha` changes; `tengu_git_operation` (`commit`, `push`, `pr_edit`); `tengu_session_linked_to_pr.prNumber` | **now** |
| Files read and written | tool inputs `file_path` | **extract** (today only extension, path length, and `filePathHash` / `contentHash`) |
| Same file across people | `tengu_file_operation.contentHash` (SHA-256 of content) | **now** |
| Commands run | tool inputs `command` | **extract** (today: first word, class, exit code, output sizes) |
| MCP calls, web fetches, searches | tool names; `mcpServerName`, `mcpServerBaseUrl`; URLs in tool inputs | names: **now**; URLs: **extract** |
| Images and screenshots | `imageBlockCount`, `imageTotalPixels`, bytes; `tengu_image_resize` (e.g. 3456×1984 Retina screenshots); pastes | **now** |
| Bytes sent / received | summed body chunks or `content-length` | **extract** (not a field on rows yet) |

### G. Session list (the main surface)

Newest first, live. Lean rows:

```
▸ 14:20  ●  aintercept · partner-readiness    prompts 12–25   8h 20m   ⚡1 ✕1 ⚠10   🔓 bypass
▸ 13:40     web · feat/csv                  3 prompts        6m
```

| Column | Source | Available |
|---|---|---|
| Start; live dot | requests | **now** |
| **Label** | see below | **extract** / **content** |
| Repo · branch | injected context / environment updates | **extract** |
| Prompts (with "from #12" when the capture starts mid-session) | prompt IDs; `cc_prompt_index` | **now** |
| Duration | first to last request | **now** |
| Markers | friction (§I), posture badge | **now** |

**Session label.** No title-generation call was seen, but the capture
started mid-session, so that's not conclusive. Claude Code's **recap** (an
auxiliary call that writes a one- or two-sentence summary when the person
returns) is the best label candidate. It is model output addressed to the
user ("Your call: …"), so it needs light rewriting or quoting, and keeping it
when other content isn't retained is a policy decision. Order of preference:
recap (**extract**, policy), first prompt text (**content**), repo · branch ·
time (**extract**).

### H. Session panel (expanded session)

- **Posture**: permission mode (`bypassPermissions` on every call is a
  headline, not a per-step marker), sandbox state, hooks active.
- **Context gauge**: context size over time with compaction markers
  (`tengu_reactive_compact_succeeded`: 905 k → 21 k tokens; idle compaction;
  thinking dropped), and per-tool share of context (Bash results 24%).
- **Repo trail**: directories and branches visited; commits; PR link.
- **Recaps**: the session's recaps on a timeline: a readable story of the session.
- **Experiments in effect**: e.g. `tengu_curried_trinket = no_scratchpad_capped`,
  which also shows up as the compaction's `summaryPromptArm`.
- **Coverage for this session** (D).

### I. Prompt rows

```
▾ #19  14:02  "fix the flaky auth test"      9 steps  1m 40s  41k→3.2k  Bash·4 Read·3 Edit·2   ⚡
      312 chars · pasted 2.1 KB · 1 screenshot · repo dirty (3 changed)            [not retained]
```

| Field | Source | Available |
|---|---|---|
| Prompt text | request messages | **content** |
| Length, pasted text bytes, pasted images | `prompt_length`, `tengu_paste_text`, `tengu_paste_image` | **now** |
| How it started | `prompt_source`, `turn_origin`, `is_wakeup` (always typed/human so far; keep a slot) | **now** |
| Repo state at start | `changed_file_count_at_start`, `untracked_file_count_at_start`, `is_clean_tree_at_start`, `head_sha` (top-level analytics fields) | **now** |
| Time to first visible text | `ttfvt_first_text_paint_ms` (5.8–22.9 s seen) | **now** |
| Steps, duration, tokens, cost, tools | its model calls and tool events | **now** |
| How it ended | `tengu_turn_end.terminal_reason` (`completed`, `aborted_streaming`), final `stop_reason` | **now** |

**Friction markers** (small; never alarms):

| Marker | Source | Note |
|---|---|---|
| **Frustrated** | `tengu_input_prompt.is_negative` | Seen once |
| **Interrupted** | `tengu_cancel` (Esc), `interrupted_message_id` on the next prompt, `terminal_reason: aborted_streaming` | These are **one incident** seen three ways; show one marker |
| **Nudged** ("keep going") | `is_keep_going` | Never observed true; keep the slot, don't feature it |
| **Destructive command** | `destructive_category`, `destructive_target_scope`, `git_destructive_target` | Mostly `rm` inside chains; target scope usually "unknown", once `outside_cwd` |

Errors (9 failed commands, 11 tool errors, API retries) are plain status in
steps, not friction markers.

### J. Step rows

```
  14:02:11  opus · 41.2k in (40.8k cached) → 612 out · ttft 1.9s · 4.8s · tool_use
      Read   src/auth/session.ts                      4.1 KB result
      Bash   npm test                     exit 1      stdout 18 KB · 12.3s     ⚠ rm -rf (outside cwd)
  14:02:16  opus · 43.0k in → 1.2k out · 9.3s · tool_use
      Edit   src/auth/session.ts                      +14 −3
  14:02:31  ⤷ subagent Explore (haiku) · 3 steps · 22s                         ▸
```

| Field | Source | Available |
|---|---|---|
| Model, effort, thinking | request | **now** |
| Tokens (with 1 h / 5 m cache split, thinking tokens), cost | `usage`; `costUSD` | **now** |
| Service tier, inference geography, speed | `usage` | **now** |
| Timing: time to first token, duration, request prep, retries, stalls | `ttftMs`, `durationMs`, `requestPrepareMs`, `tengu_api_retry` (attempts, backoff), `tengu_streaming_stall_summary` | **now** |
| Per-tool wall time of the previous step's tools | request header `x-claude-code-prev-tool-durations` | **extract** |
| Tool name, MCP server | response `tool_use` blocks | **now** |
| Tool target (path, command, URL, MCP args) | `tool_use.input` | **extract** |
| Tool result size, duration, exit code, output sizes | analytics | **now** |
| Tool result content | the next step's request | **content** |
| Subagents | agent ID; collapsed line | **now** |
| Background calls (recap, suggestion, compaction, token count) | `request-class` | **now**; dimmed |

### K. Detail panel (selected step or request)

Tabs:

1. **Conversation** (default). The messages this step added, tool calls with
   inputs, results, and the response. Because requests are incremental (§2),
   link back to the thread-create request for the system prompt and catalog.
   Without content, show each block's shape.
2. **Raw.** Burp-style request/response with headers, body pretty/raw, and
   decoded encodings. Credentials are always redacted, including third-party
   tokens inside MCP results.
3. **Timing.** Proxy waterfall (accept, upstream connect, first byte,
   streaming) beside Claude Code's own timings.
4. **Events.** Analytics events joined to this step (`requestId`,
   `clientRequestId`, `toolUseID`), including the internal ones hidden
   elsewhere.
5. **Fields.** Every parsed field.

### "All requests" view

A toggle that flattens everything to a chronological, Burp-style request log
for the person: time, kind (model / analytics / flags / skills / count),
method, path, status, timing, size. It's what an engineer reaches for first
in a demo, and it shows the full volume.

## 6. Noise policy

| Traffic or field | Default |
|---|---|
| Model calls (main loop) | Shown |
| Subagent calls | Collapsed under the step that started them |
| Recaps, suggestions, compaction, `count_tokens` | Dimmed; recaps also feed labels and the session story |
| Product analytics | Summarized (lanes, markers, fields); individual events in **Events** |
| Client internals: `tengu_tether_*`, `wire_*`, `reminder_fold_*`, `declared_tool_set_held`, `render_glyph_cardinality`, `spinner_*`, `tree_sitter_parse_abort`, `bash_ast_too_complex`, tool-progress ticks | **Events** tab only. Most of the event volume; meaningless to a viewer, but counted in the scale ribbon |
| The full flag dump (1,072) | Hidden; show the experiments the person is enrolled in |
| Anthropic's boilerplate system prompt and 36 KB tool schemas | Names and sizes only; text collapsed (it's the vendor's, not the person's) |
| IDs used only for joins (`cf-ray`, `x-stainless-*`, hashes) | **Fields** tab only |
| Credentials (auth headers, OAuth refresh details, tokens inside tool results) | Never shown |
| Health probes | Never on a person page |
| claude.ai browser traffic (`/api/directory/servers`, `/v1/code/sessions`) | Not Claude Code; belongs to a future claude.ai chip |

## 7. Live behavior

- Poll every 5 s (30 s when the tab is hidden).
- New sessions appear at the top; new prompts and steps append inside open
  sessions. Never reorder or collapse what the viewer has open.
- Scrolled away from the top: an "N new" pill instead of jumping.
- Active sessions show a live dot and ticking counters; in-flight steps show
  streaming state.
- **Pause** freezes the view; the pill counts arrivals.
- Analytics arrive in batches, seconds after the model call: steps fill in
  (tool timings, cost, markers) after they first appear. Design for that.

## 8. States

| State | Treatment |
|---|---|
| No traffic yet | Skeleton rows and a neutral "Waiting for traffic" |
| Loading | Skeletons in place |
| Capture started mid-session | "Seen from prompt #12" on the session |
| Coverage gap | Coverage check (D) turns prominent; affected sessions marked |
| Content not retained | Shape plus a quiet "not retained" chip; one page-level explanation |
| Field not yet extracted | Em dash with a tooltip; never zero |
| Data delayed | Thin banner: "Last data 2 min ago" |
| Error loading | Inline retry; keep what's on screen |

## 9. Corrections from review (so the design doesn't repeat them)

- The session numbers originally mixed three different row sets; §4 now uses
  one consistent set and labels the capture window.
- There is no 5-hour rate-limit header in the traffic; only 7-day.
- Name and email *are* in the traffic (skills listing, injected context,
  claude.ai account rows); they're not parsed yet.
- Esc-cancel and "interrupting prompt" were one incident, not two.
- "Permission wait" and sandbox violations can't show human behavior when
  the person runs with `bypassPermissions` (approval times were 1–25 ms). They
  belong in **posture**, not friction.
- Repo/branch from the system prompt is a snapshot at thread start (it said
  `main` while the repo was on `partner-readiness`); the working-directory
  trail comes from environment-update messages.
- Byte counts aren't a field on rows yet.

## 10. Extensibility (keep room, don't build)

- **Agent chips** in the header: Codex has the same session → prompt → step
  shape; ChatGPT web and claude.ai have a chat shape (conversation → message,
  uploads, plan limits).
- **Per-surface measurement honesty**: ChatGPT web sends no token counts at
  all, so totals must label exact / estimated / missing per surface.
- **Org level**: the aggregate screens of `docs/FORENSICS_CONSOLE_BRIEF.md`
  (MCP servers, models, cost), a people list, and an agent-type view.
- **Policy**: enforce mode adds "blocked" / "rewritten" outcomes on steps.

## 11. Engineering prerequisites

1. **Session state from thread-create requests:** tool catalog (built-in,
   MCP, deferred), skills, MCP servers, injected git status / branch /
   working directory, instruction and memory file presence and sizes; carried
   forward per session and updated by `tool_addition` and environment-update
   messages.
2. **Tool inputs as fields:** paths, commands, URLs, MCP arguments
   (size-capped), independent of content retention.
3. **Identity:** parse the skills listing owner, flag attributes (org role,
   tier, subscription and first-use dates), and join claude.ai account rows
   by `account_uuid`.
4. **Promote join keys:** `x-claude-code-prompt-id`, `cc_prompt_index`,
   `x-client-request-id` to top-level fields; materialize the step ↔ analytics
   join (`requestId`, `clientRequestId`, `toolUseID`).
5. **Coverage check:** analytics-reported calls vs. captured calls, per session.
6. **Byte counts** on transaction rows.
7. **Recaps** as session labels (policy decision; model-written text).
8. **Registry:** 10 captured event types are missing from
   `client_events/claude_code.json` (compaction, idle compaction, memory load,
   thinking drop, file-read limits, diff panel); run
   `make client-event-registry`.
9. **A query API** over the telemetry store with incremental "since" polling.
10. **Deployment:** routing must cover IPv6 (C6), or coverage silently drops.

## 12. Open questions for the design session

1. How loud should the scale ribbon be? It's the "wow", but it must not read
   as a marketing banner.
2. Capabilities (reach) vs. footprint (use): side by side, or one view with
   a used/available toggle?
3. Session label: recap vs. first prompt vs. repo/branch, and the policy on
   keeping recaps.
4. Detail panel: side panel, drawer, or a separate page per session
   (shareable URL)?
5. Tokens: cache-read dwarfs everything (140.9 M vs 3.6 k uncached). Headline
   output tokens, cost, or all four separately?
6. Cost on a subscription is an API-equivalent estimate, not a bill: how to
   label it?
7. "All requests": a toggle, or a top-level tab with equal billing?
8. Light or dark? Proxy tools are usually dark.

## Appendix: glossary

- `account_uuid`: Anthropic account (the person). `provider_org_id`: the
  Anthropic org billed.
- `device_id`: per-machine hash from Claude Code.
- `session_id`: one Claude Code run (resumes keep it).
- `cc_prompt_id` / `x-claude-code-prompt-id`: one user prompt.
  `cc_prompt_index`: its number in the session.
- `message_id` / `previous_message_id`: chain steps.
- `request-class`: `main`, `subagent`, `auxiliary`, `compaction`.
- `tengu_*`: Claude Code's analytics events (`docs/TAXONOMY_ANTHROPIC.md` §5;
  registry: `observer/internal/events/client_events/claude_code.json`).
- Thread create / continue: Claude Code's incremental request protocol (§2).
