"""Session-scoped shapes (handoff §4 PromptData, StepData, ToolCallData, BetweenData,
EventRowData; §5.2.8-5.2.9)."""

from datetime import datetime
from typing import Annotated, Literal

from pydantic import Field

from aintercept_api.models.common import (
    AgentLimits,
    CapabilityField,
    Envelope,
    Meta,
    Model,
    RequestKind,
    UsageMissing,
)
from aintercept_api.models.people import (
    DeviceInfo,
    McpServer,
    PromptText,
    RequestRow,
    SessionRow,
    Timeline,
    TokenCount,
)
from aintercept_api.models.work import OutcomeFacts, TurnBlock, TurnWork

# --- header (§5.2.8) --------------------------------------------------------------------------


class SessionFacts(Model):
    """Where and how a session ran (handoff §11J gap 1). None fields: not reported."""

    cwd: str | None
    shell: str | None
    tz: str | None
    approval: str | None
    """Codex approval policy (never, on-request, …)."""
    sandbox: str | None
    """Codex sandbox mode (danger-full-access, read-only, workspace-write)."""


class SessionHeader(SessionRow):
    """The session row's fields, plus what the session page header adds."""

    personId: str | None
    """The account the session belongs to (breadcrumb); None when no row carries one (pre-parser)."""
    notCaptured: int | None
    """Model calls Claude Code reported that the proxy never saw; None without analytics."""
    device: DeviceInfo | None
    networks: int | None
    """Distinct client IPs the session's model calls came from; None when no call recorded one."""
    facts: SessionFacts | None
    """None for agents that report none of them."""
    limitsAtEnd: AgentLimits | None
    """The usage limits on the session's last response (Codex); None when it reported none."""
    capabilities: list[CapabilityField]
    """The provider-specific fields the session's agent reports: the only ones drawn (handoff §11L)."""
    channels: list[RequestKind]
    """The agent's request channels, in chip order."""
    effort: str | None
    """The reasoning effort its model calls asked for (the newest); None when none said."""
    tokens: TokenCount
    """Over the session's visible model calls (hidden calls excluded)."""
    toolCalls: int | None
    """Tool calls its model calls asked for; None when no call recorded its tool uses."""
    # The turn ledger's header (handoff §11R).
    turns: int
    """Its turns (the Turns tab's rows)."""
    turnsInterrupted: int
    activeMs: float
    """The sum of its turns' times (prompt to last step)."""
    costUsd: float | None
    """Claude Code's own cost estimate over the session (costUSD); None for agents that report none."""
    work: TurnWork
    """The whole session's work: Edited (files, lines), Commits."""
    outcome: OutcomeFacts
    """The OutcomeSlot's facts."""


class SessionResponse(Envelope[SessionHeader]):
    pass


# --- timeline (§5.2.8) ------------------------------------------------------------------------


class ContextPoint(Model):
    t: datetime
    tokens: int
    """input + cacheRead + cacheWrite on one main-thread model call: the context it sent."""
    requestId: str
    """The captured transaction id (opens the request drawer)."""
    model: str | None
    thread: Literal["main", "subagent"]
    """The lane plots the main thread (handoff §5.2.8), so this is "main" for now."""
    windowTokens: int | None
    """The model's context window; None until the window size is extracted from the request (M7)."""
    promptIndex: int | None
    """The prompt the call belongs to; None when its tengu_input_prompt wasn't captured."""


class Compaction(Model):
    t: datetime
    """When the compaction finished (tengu_reactive_compact_succeeded)."""
    from_: int | None = Field(alias="from")
    """Context before: the last main-thread call before the compaction started."""
    to: int | None
    """Context after: the first main-thread call after it finished."""
    idle: bool
    """Started by idle compaction (tengu_idle_compact_fired just before)."""
    waitMs: float | None
    """From tengu_reactive_compact_attempt to the success."""
    messages: int | None
    """Messages summarized; None until the compaction events' metadata is extracted (M7)."""


class SessionTimeline(Timeline):
    """The whole session (the brush is client-side), with the session-only lanes."""

    context: list[ContextPoint]
    compactions: list[Compaction]
    turns: list[TurnBlock]
    """The Turns lane (handoff §11R): one block per turn, prompt to last step."""


class SessionTimelineResponse(Envelope[SessionTimeline]):
    pass


# --- state (§5.2.8) ---------------------------------------------------------------------------


class ToolTally(Model):
    total: int | None
    """Tools available; None until the tool catalog is extracted from thread-create requests (M7)."""
    used: int | None
    """Distinct tools used so far; None when the session has no analytics."""


class CapabilitiesAt(Model):
    builtin: ToolTally
    mcpServers: list[McpServer]
    """Servers whose tools the session used (the full list needs the M7 catalog)."""
    mcpTools: ToolTally
    skills: int | None
    """None until skills are extracted (M7)."""
    hooks: int | None
    """Distinct hooks that ran in the session."""
    poolChangesInRange: int | None
    """tengu_deferred_tools_pool_change events in the range."""


class RepoInRange(Model):
    branch: str | None
    """None until branch extraction (M7)."""
    headFrom: str | None
    """head_sha on the first event in the range."""
    headTo: str | None
    headChanges: int | None
    commits: int
    pushed: bool
    cwdTrail: list[str] | None
    """None until cwd extraction (M7)."""
    prs: list[int]


class Recap(Model):
    t: datetime
    text: str | None
    """Recap text is model output (content): None unless content is retained."""


class Experiment(Model):
    key: str
    variation: str


class RangeCounts(Model):
    """The RangeReadout numbers; each matches its tab's total for the same range."""

    prompts: int
    modelCalls: int
    requests: int
    events: int
    compactions: int
    turns: int
    """Turns that started in the range (the readout's `4 turns`): the same count as `prompts`."""
    steps: int
    """Steps (model calls) in the range: the same count as `modelCalls`."""


class SessionState(Model):
    capabilitiesAt: CapabilitiesAt
    repoInRange: RepoInRange
    recapsInRange: list[Recap] | None
    """tengu_return_to_session with hadRecap; None when the client didn't report hadRecap."""
    recapsTotal: int | None
    experimentsAt: list[Experiment] | None
    """None until flag bodies are extracted (M7)."""
    flagFetchesInRange: int | None
    """None until flag fetches are tied to a session by their body's attributes.sessionId (M7)."""
    flagsChangedInRange: int | None
    counts: RangeCounts


class SessionStateResponse(Envelope[SessionState]):
    """Capabilities and experiments are the whole session's; everything else is within [from, to]."""


# --- prompts (§5.2.9) -------------------------------------------------------------------------


class PromptFriction(Model):
    frustrated: bool
    interrupted: bool
    destructive: int


class TurnReport(Model):
    """Codex's own report on a turn (codex_turn_event, handoff §11J gap 6). Absent fields: None."""

    status: str | None
    durationMs: float | None
    samplingMs: float | None
    requests: int | None
    """Sampling requests the turn made."""
    retries: int | None
    toolCalls: dict[str, int]
    """By kind: shell, mcp, fileChange, webSearch, subagent, dynamic (non-zero only)."""
    steers: int | None
    compactionMs: float | None
    trigger: str | None
    """exec (non-interactive) or user (the TUI)."""


class PromptData(Model):
    promptId: str
    index: int | None
    """The prompt's number in the session; None when its tengu_input_prompt wasn't captured."""
    t: datetime
    durationMs: float | None
    """Until the prompt's last tengu_turn_end; None while running or without analytics."""
    text: PromptText
    steps: int
    tokensIn: int | None
    """input + cacheRead + cacheWrite over the prompt's model calls."""
    tokensOut: int | None
    toolCounts: dict[str, int]
    friction: PromptFriction
    inFlight: bool
    turnReport: TurnReport | None
    """Codex turns only."""
    hidden: dict[str, int]
    """Hidden calls (prewarm, title) the prompt made, by kind: shown as a HiddenCallsNote."""


class DestructiveMark(Model):
    category: str
    scope: str | None


class PatchFile(Model):
    file: str
    added: int
    removed: int


StepKind = Literal["exec", "patch", "mcp", "spawn", "tool", "text", "interrupt"]
"""What a step did: ran commands (exec), changed files (patch), called an MCP tool, spawned
an agent, used another tool, answered in text only, or was interrupted."""


class ToolCallData(Model):
    toolUseId: str | None
    name: str
    mcpServer: str | None
    target: str | None
    """Path / command / URL from the tool input: None until tool inputs are extracted (M7)."""
    status: Literal["running", "ok", "error"]
    durationMs: float | None
    exitCode: int | None
    command: str | None
    """A Bash call's program, as Claude Code reports it (bash_argv0); None for other tools."""
    program: str | None
    """A Bash call's program as the work line names it (handoff §11R): bash_argv0 with its subcommand
    (`npm test`), or for `cd … && x` the last program; None for other tools."""
    fileType: str | None
    """A file tool's (Read, Edit, Write) file extension, from Claude Code's analytics; None otherwise."""
    resultBytes: int | None
    destructive: DestructiveMark | None


class StepData(Model):
    requestId: str
    """The captured transaction id (opens the request drawer)."""
    t: datetime
    kind: Literal["model", "subagent"]
    model: str | None
    requestClass: Literal["main", "subagent", "auxiliary", "compaction"] | None
    tokens: TokenCount
    durationMs: float | None
    ttftMs: float | None
    stopReason: str | None
    costUsd: float | None
    status: Literal["done", "streaming", "error"]
    eventsPending: bool
    """Captured, but its tengu_api_success hasn't arrived yet (no later analytics either)."""
    agentType: str | None
    tools: list[ToolCallData]
    does: StepKind
    """What the step did, for the step strip and runs (handoff §11L.7)."""
    # From the recorded bodies, in full interception mode only; empty (and None) otherwise.
    commands: list[str]
    """Each command the step ran, as its program plus a runner's subcommand ("npm test")."""
    patches: list[PatchFile]
    """The files the step changed, with added and removed lines."""
    text: str | None
    """The start of the step's text output."""
    exitCodes: list[int]
    """The exit codes its commands returned (from the request that carried the results back)."""
    usageMissing: UsageMissing | None
    """Why the call has no tokens; None when it has them."""


class BetweenData(Model):
    kind: Literal["compaction", "idle-compaction", "recap", "call"]
    """`call`: a model call outside any prompt (auxiliary suggestions, recaps, classifiers)."""
    t: datetime
    detail: str
    requestId: str | None
    """The model call behind the row, when one was captured (opens the drawer)."""
    tokensFrom: int | None
    tokensTo: int | None
    waitMs: float | None


class PromptItem(Model):
    type: Literal["prompt"]
    prompt: PromptData
    steps: list[StepData]


class BetweenItem(Model):
    type: Literal["between"]
    between: BetweenData


SessionItem = Annotated[PromptItem | BetweenItem, Field(discriminator="type")]


class SessionPromptsResponse(Envelope[list[SessionItem]]):
    """Prompts (steps inline) and between rows, oldest first within the range; paged by item."""


# --- requests (§5.2.7) ------------------------------------------------------------------------


class SessionRequestsResponse(Envelope[list[RequestRow]]):
    """The session's requests, oldest first by default, including not-captured model calls."""

    kindCounts: dict[RequestKind, int] = {}
    hiddenCount: int = 0
    """Hidden calls (Codex prewarm, title) matching the other filters; in `data` only with `hidden=true`."""
    """Rows of each kind matching every other filter (range, text, captured): the kind chips' counts."""


# --- events (§5.2.9) --------------------------------------------------------------------------


class EventLink(Model):
    kind: Literal["request", "tool", "prompt", "session", "device"]
    id: str
    """request: a RequestRow requestId; tool: toolUseID; prompt: cc_prompt_id; session/device: their ids."""
    label: str


class EventRow(Model):
    eventId: str
    t: datetime
    name: str
    promptIndex: int | None
    linkedTo: EventLink
    summary: str
    """The event's metadata as "k v · k v" (ids left out)."""
    marker: Literal["destructive", "frustrated"] | None
    internal: bool
    """A client internal (hidden by default): client bookkeeping, including tool-progress ticks."""


class EventsMeta(Meta):
    shown: int
    """Events after hiding internals and the text filter (what the pager pages)."""


class SessionEventsResponse(Envelope[list[EventRow]]):
    """`meta.total` counts every event in the range; `meta.shown` the rows left to page."""

    meta: EventsMeta
