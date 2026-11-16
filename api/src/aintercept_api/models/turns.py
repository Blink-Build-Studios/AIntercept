"""Turns (handoff §11R, CompTurns): a turn is one prompt and all the work it started; a step is one
model call. The Session page's Turns tab and the Person page's Work tab."""

from datetime import datetime
from typing import Literal

from aintercept_api.models.common import Agent, Envelope, Model
from aintercept_api.models.people import Friction, PromptText, SessionLabel
from aintercept_api.models.sessions import PromptFriction, StepData
from aintercept_api.models.work import TurnWork

WorkKind = Literal["read", "edit", "run", "web", "mcp", "spawn", "tool", "think", "reply"]
"""What a group of steps did (CompTurns WorkGroup): read, edited, ran a program, searched or fetched,
used an MCP server, spawned subagents, used another tool, thought (steps with no tool), replied (the
turn's final text)."""


class TurnStep(StepData):
    """One model call in a turn (CompTurns TurnStep). `requestId` opens today's drawer on it."""

    step: int
    """Its number within the turn."""


class WorkGroup(Model):
    """Consecutive steps of a turn that did the same kind of work (a new group starts when the kind,
    the program or the server changes)."""

    kind: WorkKind
    program: str | None
    """A run group's program; None for other kinds (and Codex's runs)."""
    server: str | None
    """An MCP group's server."""
    work: TurnWork
    """The group's own work: its steps' tool calls (lines from recorded patches only)."""
    exits: list[int]
    """A run group's exit codes, in order."""
    paths: list[str] | None
    """The files its steps changed, from recorded content; None without content (file types instead)."""
    startedAt: datetime
    durationMs: float | None
    """From its first step's start to its last step's end; None while one is open."""
    tokensIn: int | None
    tokensOut: int | None
    text: str | None
    """A reply's text (its start), from recorded content; None without content."""
    steps: list[TurnStep]


class SubagentRow(Model):
    """A subagent the turn spawned (`⤷ subagent test-writer`), with its own work."""

    agentId: str | None
    """Claude Code's subagent id; None for a Codex thread (see sessionId)."""
    name: str | None
    """Its type (Claude Code's agent type, Codex's agent name); None when not reported."""
    sessionId: str | None
    """A Codex subagent's own thread (`open thread ›`); None for Claude Code (the sidechain is in this
    session: its steps are below)."""
    steps: int
    work: TurnWork
    tokensIn: int | None
    tokensOut: int | None
    groups: list[WorkGroup]
    """Its steps grouped (Claude Code); empty for a Codex thread, which is a session of its own."""


class TurnRow(Model):
    """One turn (CompTurns TurnRow)."""

    promptId: str
    n: int | None
    """The turn's number (#14): its prompt's, or a Codex turn's place; None when its prompt wasn't captured."""
    startedAt: datetime
    endedAt: datetime | None
    """Its last step's end (prompt to last step); None while a step is open or it has no step."""
    durationMs: float | None
    live: bool
    """Still running: the session is live and the turn is open or has a step in flight."""
    asked: PromptText
    """Prompt size without content; the text with it."""
    steps: int
    tokensIn: int | None
    """input + cacheRead + cacheWrite over its steps."""
    tokensOut: int | None
    costUsd: float | None
    """Claude Code's own cost estimate (its reports' costUSD); None for Codex."""
    friction: PromptFriction
    work: TurnWork
    """The whole turn's work, from its analytics (its subagents' included)."""
    groups: list[WorkGroup]
    """The main thread's steps, grouped."""
    subagents: list[SubagentRow]


class SessionTurnsResponse(Envelope[list[TurnRow]]):
    """The session's turns that started in [from, to], oldest first; paged by turn."""


class SessionWorkRow(Model):
    """SessionRow (work): a session under its repo on the person's Work tab."""

    sessionId: str
    agent: Agent
    start: datetime
    end: datetime | None
    live: bool
    label: SessionLabel
    turns: int
    capturedFromPrompt: int | None
    """Set when the capture started mid-session (the turns count is partial: †)."""
    activeMs: float
    """The sum of its turns' times (prompt to last step)."""
    work: TurnWork
    friction: Friction


class RepoGroup(Model):
    """RepoRow: a repo (Claude Code) or workspace (Codex) and its sessions."""

    repo: str | None
    """None: the sessions whose repo wasn't parsed."""
    agents: list[Agent]
    sessions: int
    turns: int
    activeMs: float
    work: TurnWork
    lastActiveAt: datetime
    rows: list[SessionWorkRow]
    """Newest first."""


class PersonWorkResponse(Envelope[list[RepoGroup]]):
    """The person's sessions that overlap [from, to], grouped by repo; repos by most recent activity."""
