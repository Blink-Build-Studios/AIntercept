"""Person-scoped shapes (handoff §4 SessionRowData, TimelineData; §5.2.5-5.2.6)."""

from datetime import datetime
from typing import Literal

from pydantic import Field

from aintercept_api.models.common import (
    Agent,
    AgentLimits,
    CapabilityField,
    Coverage,
    CoverageWindow,
    Envelope,
    Model,
    RequestKind,
    UsageMissing,
)


class SessionLabel(Model):
    text: str
    source: Literal["recap", "firstPrompt", "repo", "title", "none"]
    """"title": Codex's generated thread title (recorded bodies only); "none": untitled, the text is the id."""


class Friction(Model):
    frustrated: int
    """Prompts Claude Code flagged as negative (tengu_input_prompt.is_negative)."""
    interrupted: int
    """Interruptions (tengu_cancel); one incident, one marker."""
    destructive: int
    """Shell commands with a destructive_category (rm -f, rm -rf, …)."""


SessionOriginKind = Literal["fork", "resume", "subagent", "review"]


class SessionOrigin(Model):
    """Where a Codex thread came from (handoff §11J gaps 3, 4)."""

    kind: SessionOriginKind
    sessionId: str | None
    """The source thread (the parent, or the thread forked from); None for a resume of itself."""
    turn: int | None
    """The source turn: the parent's prompt number, or the fork's turn ordinal."""
    init: Literal["warm_fork", "cold_fork", "cold_resume"] | None
    agentName: str | None
    """A subagent's name (`/root/edge_tests`)."""


class ThreadChildren(Model):
    """The subagent and review threads started from a session."""

    subagent: int
    review: int
    sessionIds: list[str]


class SessionRow(Model):
    sessionId: str
    agent: Agent
    client: str | None
    """How the agent was run: Codex `exec`, `tui`; Claude Code `cli`, `sdk-cli`. None when no call says."""
    clientVersion: str | None
    origin: SessionOrigin | None
    children: ThreadChildren | None
    """None when the session started none."""
    hiddenCalls: int
    """Calls the user never sees (Codex prewarm and title calls): not in modelCalls or tokens."""
    start: datetime
    end: datetime | None
    """None while the session is live."""
    live: bool
    repo: str | None
    """The repo; for Codex, the workspace's basename."""
    branch: str | None
    commit: str | None
    """The workspace's short commit (Codex reports a commit, not a branch)."""
    label: SessionLabel
    prompts: int | None
    """Distinct prompt ids on model calls; None when no call carries one."""
    capturedFromPrompt: int | None
    """The first prompt number seen, when the capture started mid-session ("seen from #12")."""
    modelCalls: int
    events: int | None
    """Analytics events; None when none were captured (can't tell none-sent from not-parsed)."""
    friction: Friction
    bypassPct: float | None
    """Share of model calls (0-100) made with permissionMode=bypassPermissions."""
    coverage: Coverage | None
    fieldsMissing: bool
    """Rows from before the parser extracted identity (no account/device on model calls)."""


class PersonSessionsResponse(Envelope[list[SessionRow]]):
    """Newest first. Range-filtered by overlap with [from, to]; paged."""


PresenceState = Literal["focused", "away", "asleep"]


class TimelineSession(Model):
    sessionId: str
    start: datetime
    end: datetime | None
    label: str
    row: int
    """Packed so overlapping sessions get different rows."""
    live: bool
    capturedFromPrompt: int | None
    # Fields from here on feed tooltips (handoff §6B.10): tooltips never fetch.
    repo: str | None
    """None until M7 extraction (injected git status)."""
    branch: str | None
    """None until M7 extraction (injected git status)."""
    prompts: int | None
    """Distinct prompt ids on the session's model calls; None when no call carries one."""
    modelCalls: int


class PromptText(Model):
    retained: bool
    """Whether the prompt's text was kept. Content is off by default (BUILD_PLAN §2), so false."""
    text: str | None
    chars: int | None
    """tengu_input_prompt prompt_length."""
    pastedBytes: int | None
    """Text pasted since the previous prompt (tengu_paste_text pastedTextBytes); None when nothing was pasted."""
    images: int | None
    """Images pasted since the previous prompt; None without analytics."""


class TimelinePrompt(Model):
    t: datetime
    index: int | None
    sessionId: str
    promptId: str
    """cc_prompt_id, or "input:<event id>" when the client sent none."""
    text: PromptText
    steps: int
    """Captured model calls carrying the prompt id."""
    durationMs: float | None
    """Until the prompt's last tengu_turn_end; None while running or without one."""
    tokensIn: int | None
    """input + cacheRead + cacheWrite over the prompt's model calls."""
    tokensOut: int | None


class CallClasses(Model):
    main: int
    subagent: int
    auxiliary: int
    """Every other call: auxiliary, compaction, and calls with no request class."""


class ModelCallBin(Model):
    t: datetime
    count: int
    """Captured model calls (0 when the bin only holds calls that bypassed the proxy)."""
    tokens: int
    """Output tokens in the bin (exact, from the response usage)."""
    byClass: CallClasses
    tokensIn: int | None
    """input + cacheRead + cacheWrite over the bin's calls; None when no call carries usage."""
    tokensOut: int | None
    """Output tokens; None when no call carries usage (unlike `tokens`, which counts those as 0)."""
    notCaptured: int
    """Calls Claude Code reported in the bin that the proxy never saw."""


class EventCount(Model):
    name: str
    count: int


class AnalyticsBin(Model):
    t: datetime
    count: int
    top: list[EventCount]
    """The bin's three most frequent event names, most frequent first."""


class GapWindow(Model):
    """A not-captured window (hatched on the timeline), with the cross-check behind it."""

    from_: datetime = Field(alias="from")
    to: datetime
    count: int
    """Reported calls in the window the proxy never saw."""
    reported: int
    """Calls Claude Code reported (tengu_api_success) within the window."""
    captured: int
    """Of those, the ones matched to a captured transaction."""
    cause: str | None
    """Why the calls bypassed the proxy; None until the coverage job attributes causes (§5.4)."""


class PresenceSegment(Model):
    from_: datetime = Field(alias="from")
    to: datetime
    state: PresenceState
    label: str | None = None
    source: Literal["focus", "sleep"]
    """sleep: tengu_event_loop_stall likely_sleep; focus: tengu_return_to_session blur windows."""


class FrictionMark(Model):
    t: datetime
    kind: Literal["frustrated", "interrupted", "destructive"]
    count: int
    promptIndex: int | None
    """The prompt of the mark's first incident; None when its tengu_input_prompt wasn't captured."""
    detail: str
    """What happened: "prompt flagged negative", "turn interrupted", or the destructive category and scope."""
    tool: str | None
    """The tool involved (Bash for destructive commands); None for prompt-level friction."""


class Timeline(Model):
    from_: datetime = Field(alias="from")
    to: datetime
    now: datetime | None
    """Set when `now` falls inside the range (draw the now-line)."""
    binSeconds: int
    sessions: list[TimelineSession]
    prompts: list[TimelinePrompt]
    modelCalls: list[ModelCallBin]
    analytics: list[AnalyticsBin]
    notCaptured: list[GapWindow]
    presence: list[PresenceSegment]
    friction: list[FrictionMark]


class PersonTimelineResponse(Envelope[Timeline]):
    """Bins are only emitted where there is activity; a missing bin is zero."""


# --- person header (handoff §5.2.3) ---------------------------------------------------------


class DeviceInfo(Model):
    deviceId: str
    platform: str | None
    terminal: str | None
    shell: str | None


class Limits(Model):
    status: str | None
    """anthropic-ratelimit-unified-status on the latest response (allowed, allowed_warning, rejected)."""
    utilization7d: float | None
    overage: str | None


class FlagsChanged(Model):
    at: datetime
    count: int


class Flags(Model):
    total: int | None
    """Flags in the latest /api/eval response; None until flag bodies are extracted (M7)."""
    experiments: int | None
    changedSince: FlagsChanged | None


class Posture(Model):
    bypassPct: float | None
    sandbox: Literal["on", "off"] | None
    tokenStorage: str | None
    proxyDetected: str | None
    """The client noticed the interception CA (gzipSkipReason=custom_ca)."""


class LinkedAccount(Model):
    """One account of a person linked across agents (handoff §11J.5)."""

    personId: str
    agent: Agent
    accountIdShort: str
    plan: str | None
    devices: int


class OrgStat(Model):
    """Whether the account's org is the customer's (handoff §11L.4)."""

    id: str
    kind: Literal["customer", "outside", "unset"]
    """"unset" until the deployment names the customer's org ids (CUSTOMER_ORG_IDS)."""


class AgentInfo(Model):
    """What an AgentChip's tooltip says (handoff §6B.7): version, first seen, devices."""

    agent: Agent
    version: str | None
    """The client version on the newest call (claude-cli/<version> in the user-agent)."""
    firstSeenAt: datetime
    devices: int


class PersonHeader(Model):
    personId: str
    name: str | None
    email: str | None
    accountIdShort: str
    orgIdShort: str | None
    orgKind: Literal["corporate", "personal", "unknown"]
    role: str | None
    plan: str | None
    rateLimitTier: str | None
    customerSince: datetime | None
    firstUseAt: datetime | None
    devices: list[DeviceInfo]
    limits: Limits
    flags: Flags
    posture: Posture
    lastSeenAt: datetime | None
    live: bool
    agentInfo: list[AgentInfo]
    """The AgentChip tooltips (handoff §6B.7): one per agent the person uses."""
    accounts: list[LinkedAccount]
    """Every account of the person: one per agent, more when linked by email."""
    linkedBy: Literal["email"] | None
    agentLimits: list[AgentLimits]
    """Each product's usage limits (the header's AgentStatGroup); empty when none reported."""
    capabilities: dict[Agent, list[CapabilityField]]
    """Per agent the person uses, the provider-specific fields it reports (handoff §11L)."""
    org: OrgStat | None
    """Claude Code's Organization stat; None when no call carried an org id."""
    missed: int | None
    """Claude Code: model calls it reported that the proxy didn't capture; None without analytics."""


class PersonResponse(Envelope[PersonHeader]):
    pass


# --- person summary (handoff §5.2.4) --------------------------------------------------------


class TokenCount(Model):
    input: int | None
    """Uncached input tokens."""
    cacheRead: int | None
    cacheWrite: int | None
    """All cache writes (the 5 m / 1 h split comes from analytics, when present)."""
    cacheWrite5m: int | None
    cacheWrite1h: int | None
    output: int | None
    accuracy: Literal["exact", "estimated", "missing"]


class Cost(Model):
    usd: float | None
    basis: Literal["api-equivalent", "billed"]
    estimatedCalls: int
    """Captured model calls with no tengu_api_success: their cost isn't in `usd`."""


class Ribbon(Model):
    modelCalls: int
    sessions: int
    analyticsEvents: int
    analyticsBatches: int
    eventTypes: int
    accountCalls: int
    toolsReachable: int | None
    tokens: TokenCount
    cost: Cost


class McpServer(Model):
    name: str
    tools: int | None
    """Tools the server offers; None until the catalog is extracted (M7)."""
    toolsUsed: int
    riskyVerbs: list[str]


class Capabilities(Model):
    mcpServers: list[McpServer]
    mcpToolsTotal: int | None
    mcpToolsUsed: int | None
    builtinTools: int | None
    builtinUsed: int | None
    skills: int | None
    hooks: int | None
    hookOverheadMs: float | None
    """Mean pre-tool hook time per tool call."""
    memoryWrites: int | None
    variesBySession: dict[str, int] | None


class CommandWord(Model):
    word: str
    count: int


class Destructive(Model):
    total: int
    rmF: int
    rmRf: int
    outsideCwd: int


class CodeShipped(Model):
    headChanges: int | None
    commits: int
    pushes: int
    prs: list[int]


class Failures(Model):
    commands: int
    toolErrors: int
    apiRetries: int


class Footprint(Model):
    prompts: int | None
    toolCalls: int | None
    toolsByName: dict[str, int]
    commands: int | None
    topCommandWords: list[CommandWord]
    destructive: Destructive
    codeShipped: CodeShipped
    failures: Failures
    subagentCalls: int


class PersonSummary(Model):
    ribbon: Ribbon
    coverage: Coverage
    capabilities: Capabilities
    footprint: Footprint


class PersonSummaryResponse(Envelope[PersonSummary]):
    """Every number follows the range (the brushed window), not the whole history."""


# --- requests (handoff §4 RequestRowData, §5.2.7) -------------------------------------------


class RequestError(Model):
    message: str
    """The proxy's error, else the status code's reason phrase, else "HTTP <status>"."""
    afterMs: float | None
    """From the request to the failure; None when the transaction never completed."""
    bytesReceived: int | None
    """Response bytes before the failure; None when no body chunk was seen."""
    retries: int | None
    """None until tengu_api_retry is joined to requests (M4)."""
    retrySucceededAt: datetime | None
    """None until tengu_api_retry is joined to requests (M4)."""


class RequestRow(Model):
    requestId: str
    """The captured transaction id, or "reported:<request-id>" for a call that bypassed the proxy."""
    t: datetime
    kind: RequestKind
    agent: Agent | None
    client: str | None
    hiddenKind: Literal["prewarm", "title"] | None
    """A call the user never sees (Codex): the Requests tab hides these unless asked."""
    sessionId: str | None
    sessionLabel: str | None
    """The session's branch, else its repo (handoff §11A.6). Null until M7 extraction."""
    promptIndex: int | None
    method: str | None
    path: str
    detail: str
    status: int | Literal["ERR"] | None
    durationMs: float | None
    retries: int | None
    """Which retry attempt this request was (`tengu_api_retry`); "retry N" when it has no duration. Null until M4."""
    bytesUp: int | None
    bytesDown: int | None
    background: bool
    captured: bool
    # Cell tooltips (handoff §6B.10): tooltips never fetch.
    statusText: str | None
    """The status code's reason phrase ("OK"), or the proxy's error ("connection reset"); None for reported calls."""
    stopReason: str | None
    """Model calls only; None for reported calls (analytics don't carry it)."""
    ttftMs: float | None
    """Time to first token, from Claude Code's tengu_api_success; None without it."""
    tokens: TokenCount | None
    """Model calls only."""
    contentEncoding: str | None
    """The response's content-encoding header; None when it had none or wasn't captured."""
    error: RequestError | None
    """Set when the request failed (no status, or 4xx/5xx)."""
    model: str | None
    """Model calls only."""
    effort: str | None
    """The reasoning effort the call asked for; None when it didn't say."""
    stepIndex: int | None
    """The call's step number within its prompt (1-based); None outside a prompt."""
    toolCalls: list[str]
    """What the call asked to run, by tool ("exec ×2"), in order."""
    usageMissing: UsageMissing | None
    """Why a model call has no tokens; None when it has them, or isn't a model call."""
    text: str | None
    """The start of a model call's text output; full interception mode only."""


class PersonRequestsResponse(Envelope[list[RequestRow]]):
    """Includes account calls with no session, and reported-but-not-captured model calls."""

    kindCounts: dict[RequestKind, int] = {}
    """Rows of each kind matching every other filter (range, text, captured): the kind chips' counts."""
    hiddenCount: int = 0
    """Hidden calls (Codex prewarm, title) matching the other filters: the hidden-calls checkbox.
    They're in `data` only with `hidden=true`."""


class CoverageDetail(Model):
    reported: int | None
    captured: int
    notCaptured: int
    windows: list[CoverageWindow]
    calls: list[RequestRow]
    """The not-captured calls, oldest first."""


class CoverageResponse(Envelope[CoverageDetail]):
    pass


# --- people page (handoff §5.2.1, §5.2.2) ---------------------------------------------------


class ActivityDay(Model):
    day: str
    """The UTC date, YYYY-MM-DD."""
    count: int
    """Model calls that day."""


class PersonSummaryRow(Model):
    personId: str
    """The account_uuid (or, for other agents, their account id)."""
    name: str | None
    """Needs extraction (skills listing owner, claude.ai account rows): None until M7."""
    email: str | None
    accountIdShort: str
    orgIdShort: str | None
    orgKind: Literal["corporate", "personal", "unknown"]
    """"unknown" until a customer org is configured to compare provider_org_id with."""
    devices: int
    agents: list[Agent]
    linkedBy: Literal["email", "device"] | None
    """"email" when accounts with the same email form this one person (handoff §11J.5)."""
    accounts: int
    """Accounts linked into the row (1 unless linked)."""
    lastSeenAt: datetime
    live: bool
    sessions: int
    prompts: int | None
    """Distinct prompt ids on model calls; None when no call carries one."""
    tokensOut: int | None
    tokensAccuracy: Literal["exact", "estimated", "missing"]
    activity: list[ActivityDay]
    """14 daily bins ending today (UTC), oldest first."""
    bypassPct: float | None
    coverage: Coverage | None
    """None when no session has analytics to compare with."""
    agentInfo: list[AgentInfo]
    agentLimits: list[AgentLimits]
    """Each agent's usage limits on its newest response (the Codex view's `5h · week` and Plan)."""


class PeopleResponse(Envelope[list[PersonSummaryRow]]):
    """Everyone seen, one row per person (handoff §5.2.2)."""


TraceShape = Literal["sessions→prompts→steps", "sessions→turns→steps", "conversations→messages"]


class AgentOverview(Model):
    agent: Agent
    people: int
    sessions: int
    tokenAccuracy: Literal["exact", "estimated", "missing"]
    """"missing" for agents aintercept doesn't parse yet (types and colors reserved)."""
    shape: TraceShape
    # Each card's headline (handoff §11L.1): only the agent's own fields are set.
    missedCalls: int | None
    """Claude Code: model calls it reported that the proxy didn't capture."""
    bypassPct: float | None
    """Claude Code: the share of model calls made in bypass mode, 0-100."""
    highestWeekPct: float | None
    """Codex: the highest weekly limit use among its people, 0-100."""


class Unattributed(Model):
    """Sessions whose rows carry no account id on any call or event (handoff §5.2.1)."""

    sessions: int
    modelCalls: int
    firstSeen: datetime | None
    """None when there are none."""
    reason: str


class Overview(Model):
    people: int
    activeNow: int
    """People with activity in the last 5 minutes."""
    agents: list[AgentOverview]
    """Every agent, in the AgentCard order; reserved ones have zero counts."""
    capabilities: dict[Agent, list[CapabilityField]]
    """Per agent, the provider-specific fields it reports: the only ones the UI renders (handoff §11L)."""
    channels: dict[Agent, list[RequestKind]]
    """Per agent, its request channels, in chip order."""
    devices: int
    """Devices across the people in range (the ribbon's Devices tile)."""
    sessions: int
    requests: int
    """Captured transactions with coding agents (Claude Code, Codex), synthetic probes excluded."""
    notCaptured: int
    unattributed: Unattributed


class OverviewResponse(Envelope[Overview]):
    """The People page's top strip and agent cards (handoff §5.2.1)."""
