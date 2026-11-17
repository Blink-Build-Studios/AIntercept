"""The work vocabulary (handoff §11R, CompTurns): what a turn, a group, a session, a repo or a timeline
bin did, structured; the UI composes the phrases (the work line). Metadata first: tool names and
Claude Code's analytics; Codex reports tool counts and exit codes, no programs or file types."""

from datetime import datetime
from typing import Literal

from aintercept_api.models.common import Model


class CommandRuns(Model):
    """The commands a turn ran with one program (`ran npm test ×4: 3 failed, then passed`)."""

    program: str | None
    """The program and a runner's subcommand (`npm test`, `git diff`), from Claude Code's analytics
    (bash_argv0, bash_subcommand; for `cd … && x`, the last program). None for Codex, which reports no
    program without content."""
    runs: int
    failed: int
    """Runs that exited non-zero. Failures are work, not friction."""
    lastExit: int | None
    """The last run's exit code (`then passed`); None where only totals are kept (a session, a bin)."""


class SubagentSpawn(Model):
    name: str | None
    """The subagent's type (general-purpose, test-writer); None when the client didn't say."""
    count: int


class McpUse(Model):
    server: str
    calls: int


class WebUse(Model):
    searches: int
    fetches: int


class TurnWork(Model):
    """What a turn (a group, a session, a repo, a bin) did. A kind with nothing is 0 or empty: the UI
    leaves it out of the line. Reads, edits and file types are Claude Code's; Codex's edits are its
    file changes (apply_patch)."""

    reads: int
    """Read, Grep, Glob and LS tool calls."""
    readFiles: int | None
    """Distinct files read (Claude Code's hashed paths); None when no file operation was reported."""
    readTypes: dict[str, int]
    """Reads by file extension (`ts`: 5), from Claude Code's analytics."""
    edits: int
    """Edit, Write, MultiEdit and NotebookEdit tool calls; Codex's file changes."""
    editedFiles: int | None
    """Distinct files written (Claude Code's hashed paths); None when no file operation was reported."""
    editTypes: dict[str, int]
    linesAdded: int | None
    """From Claude Code's tengu_file_changed and Codex's accepted lines; None when none was reported."""
    linesRemoved: int | None
    commands: list[CommandRuns]
    """By program, most runs first."""
    subagents: list[SubagentSpawn]
    web: WebUse
    mcp: list[McpUse]
    """By server, most calls first."""
    otherTools: int
    """Tool calls of no kind above (ToolSearch, Skill, AskUserQuestion, …)."""
    commits: int
    """git commits (Claude Code's tengu_git_operation: commit, commit_amend)."""
    pushes: int
    prs: list[int]
    """Pull requests the session was linked to (tengu_session_linked_to_pr)."""


class LastTest(Model):
    program: str
    exitCode: int
    t: datetime


class OutcomeFacts(Model):
    """What a session (or a person's period) shipped, as captured (OutcomeSlot facts). The generated
    summary isn't captured yet."""

    commits: int
    headFrom: str | None
    """The first head_sha seen, and the last (the head range `a41f → 9c02`)."""
    headTo: str | None
    editedFiles: int | None
    editTypes: dict[str, int]
    lastTest: LastTest | None
    """The last run of a test program (`npm test`, `pytest`, `go test`, …); None when none ran."""
    pushes: int
    prs: list[int]


class TurnBlock(Model):
    """A turn on the session timeline's Turns lane: from its prompt to its last step."""

    promptId: str
    n: int | None
    startedAt: datetime
    endedAt: datetime | None
    """None while it's still running (draw to now)."""
    steps: int
    live: bool
    interrupted: bool
    """The person interrupted it (the timeline's ✕, handoff §11S)."""


class WorkBin(Model):
    """The Work lane's bin: edits on top of everything else, and the bin tooltip's counts."""

    t: datetime
    edits: int
    other: int
    """Reads, commands, searches and fetches, MCP calls, spawns and other tool calls."""
    steps: int
    """Captured model calls in the bin."""
    work: TurnWork


class PersonWorkStats(Model):
    """The person header row's work numbers over the range."""

    turns: int
    activeMs: float
    """The sum of the turns' times (prompt to last step) of the turns that started in the range."""
    work: TurnWork
    repos: int
    """Distinct repos among the sessions active in the range."""
    outcome: OutcomeFacts


class WorkingOn(Model):
    """The People page's Working on: the live session's turn and repo, otherwise the repos."""

    live: bool
    sessionId: str | None
    """The live session; None when nothing is live."""
    repo: str | None
    turn: int | None
    """The live session's newest turn number."""
    repos: list[str]
    """The repos of the sessions active in the window, most recent first."""


# --- without the agents' own analytics (handoff §11S) ---------------------------------------------

Recording = Literal["metadata", "content"]
"""Whether a session's (or a person's window's) calls kept their content: `content` when any did."""


class ToolSource(Model):
    """One source of tools (ToolsOfferedUsed's row): `Built-in` or `mcp:<server>`."""

    source: str
    offered: int | None
    """Distinct tools of this source in the requests' tool lists; None when no request carried a list."""
    used: int
    """Distinct tools of this source the model called."""
    calls: dict[str, int]
    """Calls by tool (its bare name: `Bash`, `list_issues`), most first."""


class ToolsInRange(Model):
    """The tools the requests offered against the ones the model called, from the tool list each request
    carries and the tool calls each response makes (handoff §11S, ToolsOfferedUsed)."""

    sources: list[ToolSource]
    """Built-in first, then each MCP server by calls; servers never called last."""
    offered: int | None
    """Distinct tools offered, every source; None when no request carried a list."""
    used: int
    calls: int
    byName: dict[str, int]
    """Calls by tool name as the request sends it (`Bash`, `mcp__linear__list_issues`): the work line."""
    listChanges: int | None
    """How many times the main thread's tool list changed: its distinct lists less one; None without a list."""


class ContextInRange(Model):
    """The session's context in a range, content-free (handoff §11S, KeyValueList/context): its compactions
    and its subagent threads. The peak context is the timeline's context points'."""

    compactions: int
    subagentThreads: int
