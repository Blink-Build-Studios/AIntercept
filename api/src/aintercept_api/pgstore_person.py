"""The person pages over Postgres (docs/PIPELINE.md v7 §4, §5; build step 6): /people/{id}, its
/sessions, /summary, /timeline and /requests, and /coverage?personId=.

A person is an email group: the URL's account and every account with its email (dim_account's
lower(email) index); "unattributed" is the visible sessions no account names. Each statement
resolves that scope itself (the `scope` CTE: the group's dim_session_account rows, by account_id),
in the request's one snapshot (pgstore.PgStore._read), and is bounded by the scope and, when the
URL has one, its range (widened to a superset of the endpoint's exact filter). The rows land in the
request's DuckDB under today's names, and today's code runs over them unchanged:
- person_sql's tables (person_map, person_sessions, account_facts, account_devices) and accounts,
  from the group's dimension rows;
- session_rows, with the labels other keys' rows give them (sql/labels.sql): a Codex thread's
  title over its account-connected component (quirk 2), run once per component with title calls,
  as buckets.py's title runs are; its alone label; its children;
- the ranged session tables, for the sessions a range can show (a superset of rollups.SHOWN: started
  by its end, and active, or live, at its start), and per session its newest event of each `_LATEST`
  name before the range (the summary's carry);
- request_rows and person_last: stream_sql over the request log's inputs for the range (§4): the
  scope's credentials' transactions in it, the other transactions of the same prompts (their step
  numbers), the reports of their ids, those prompts' numbers, the scope's Codex turns, its missed
  calls in the range, the batch parts of the calls, and its sessions' newest times.

The number of statements is fixed per page (PAGES), whatever the person's size.
"""

import urllib.parse
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

import duckdb

from aintercept_api import claude_code as cc
from aintercept_api import state_sql, stream_sql
from aintercept_api import store as legacy
from aintercept_api.person_sql import DIMENSIONS, PERSON_TABLES
from aintercept_api.pgstore import _LABELS_SQL, _WIDEN_NS, SHAPES, PgStore, Status, _instant_ns, _q, _Run, columns

UNATTRIBUTED = cc.UNATTRIBUTED
ENTITY = "*"

# --- the statements' columns ----------------------------------------------------------------------

_PG_TYPES = {
    "VARCHAR": "text",
    "BIGINT": "bigint",
    "INTEGER": "integer",
    "DOUBLE": "double precision",
    "BOOLEAN": "boolean",
    "VARCHAR[]": "text[]",
    "BIGINT[]": "bigint[]",
}


@dataclass(frozen=True)
class _Stored:
    """A stored input as Postgres flattens it (its structs' fields as <column>_<field>) and as the
    API's SQL reads it (state_sql.STATE_SCHEMA: the structs rebuilt)."""

    flat: list[tuple[str, str]]  # (Postgres column, Postgres type)
    rebuild: str  # the select list that rebuilds STATE_SCHEMA's columns from the flat ones


def _stored(table: str, schema: str) -> _Stored:
    con = duckdb.connect()
    try:
        con.execute(f"CREATE TABLE t ({schema})")
        rel = con.table("t")
        flat: list[tuple[str, str]] = []
        rebuild = []
        for name, typ in zip(rel.columns, rel.types, strict=True):
            if typ.id == "struct":
                fields = [(f, str(ft)) for f, ft in typ.children]
                flat += [(f"{name}_{f}", _PG_TYPES[str(ft)]) for f, ft in fields]
                packed = ", ".join(f"{_q(f)} := {_q(name + '_' + f)}" for f, _ in fields)
                rebuild.append(f"struct_pack({packed}) AS {_q(name)}")
            else:
                flat.append((name, _PG_TYPES[str(typ)]))
                rebuild.append(_q(name))
        return _Stored(flat, ", ".join(rebuild))
    finally:
        con.close()


_BATCH_PARTS = "batch_id VARCHAR, key VARCHAR, n BIGINT, min_name VARCHAR, first_ns BIGINT, first_session VARCHAR"
_INPUTS = {
    t: _stored(t, state_sql.STATE_SCHEMA[t])
    for t in (
        *DIMENSIONS,
        "request_txs",
        "request_reports",
        "request_missed",
        "prompt_index",
        "codex_turns",
        "session_last",
    )
}
_INPUTS["batch_parts"] = _stored("batch_parts", _BATCH_PARTS)
_INPUT_SCHEMA = {**state_sql.STATE_SCHEMA, "batch_parts": _BATCH_PARTS}

# What the person pages read of event_rows (the summary's counts and _LATEST, the timeline's
# analytics), and `batch`: the summary counts batches by it (event_id's prefix, stored).
EVENT_COLUMNS = (
    "session_id", "ts_ns", "name", "unnamed", "count", "head_sha", "hook_name", "hook_ms", "cache_5m",
    "cache_1h", "cost_usd", "outside_cwd", "is_mcp", "tool_name", "argv0", "category", "operation", "pr_number",
)  # fmt: skip
# The _LATEST names (routers/people.py): each session's newest of each, up to the range's end.
LATEST = ("tengu_tool_search_mode_decision", "tengu_skills_sync_success")


@dataclass(frozen=True)
class _Arm:
    """One part of a tagged statement: its rows (source selects cols by name)."""

    tag: str
    cols: list[tuple[str, str]]
    source: str


def _union(arms: Sequence[_Arm]) -> str:
    """One statement for several tables: each arm's rows tagged (src), in one shape whose columns
    are <tag>__<column>, NULL outside the arm's own."""
    out = []
    for a in arms:
        sel = [f"'{a.tag}' AS src"]
        for b in arms:
            for c, t in b.cols:
                sel.append(f"x.{_q(c)} AS {_q(b.tag + '__' + c)}" if b is a else f"NULL::{t} AS {_q(b.tag + '__' + c)}")
        out.append(f"SELECT {', '.join(sel)} FROM ({a.source}) x")  # noqa: S608 (fixed names)
    return "\nUNION ALL\n".join(out)


def _cols(cols: Sequence[tuple[str, str]], prefix: str = "") -> str:
    return ", ".join(prefix + _q(c) for c, _ in cols)


# --- the scope: each statement resolves it ----------------------------------------------------------

# The group: the URL's account and every account with its email (lower(email), as person_sql and
# stream_sql link them); its sessions: the ones its accounts' dim_session_account rows name.
_GROUP = """me AS (SELECT lower(email) AS em FROM dim_account WHERE account_id = $1 AND email <> ''),
grp AS (SELECT $1::text AS account_id
    UNION SELECT account_id FROM dim_account WHERE lower(email) IN (SELECT em FROM me)),
scope AS (SELECT DISTINCT session_id FROM dim_session_account WHERE account_id IN (SELECT account_id FROM grp))"""
# "unattributed" ($1): the visible sessions no account names.
_UNATTRIBUTED = """scope AS (SELECT DISTINCT v.session_id FROM dim_session_visible v
    WHERE $1::text = 'unattributed'
      AND NOT EXISTS (SELECT 1 FROM dim_session_account a WHERE a.session_id = v.session_id))"""
# The sessions a range [$2, $3] can show (rollups.SHOWN, whatever `now` is): started by its end, and
# last active at its start, or within the live window of it (a live session ends now).
_RANGED = f"""ranged AS (SELECT session_id FROM session_rows WHERE session_id IN (SELECT session_id FROM scope)
    AND ($2::bigint IS NULL OR last_ns >= $2 - {cc.LIVE_WITHIN_NS}) AND ($3::bigint IS NULL OR start_ns <= $3))"""  # noqa: S608


def _in_range(col: str) -> str:
    return f"($2::bigint IS NULL OR {col} >= $2) AND ($3::bigint IS NULL OR {col} <= $3)"


@dataclass(frozen=True)
class _Sql:
    """The statements for one kind of scope (a person's group, or unattributed)."""

    unattributed: bool

    @property
    def scope(self) -> str:
        return _UNATTRIBUTED if self.unattributed else _GROUP

    def dims(self) -> str:
        """The scope's dimension rows: the group's accounts' (by account_id or person_id), their
        sessions' credentials; for unattributed, its visible sessions and their credentials."""

        def arm(table: str, where: str) -> _Arm:
            cols = _INPUTS[table].flat
            return _Arm(table, cols, f"SELECT {_cols(cols)} FROM {table} WHERE {where}")  # noqa: S608

        in_grp = "IN (SELECT account_id FROM grp)"
        in_scope = "session_id IN (SELECT session_id FROM scope)"
        if self.unattributed:
            arms = [arm("dim_session_visible", in_scope), arm("dim_session_credential", in_scope)]
        else:
            arms = [
                arm("dim_account", f"account_id {in_grp}"),
                arm("dim_session_account", f"account_id {in_grp}"),
                arm("dim_session_person", f"person_id {in_grp}"),
                arm("dim_person_device", f"person_id {in_grp}"),
                arm("dim_session_credential", in_scope),
                arm("dim_account_events", f"account_id {in_grp}"),
                arm("dim_account_device", f"account_id {in_grp}"),
                arm("dim_codex_plan", f"account_id {in_grp}"),
            ]
        return f"WITH {self.scope}\n{_union(arms)}"

    def sessions(self) -> str:
        return (
            f"WITH {self.scope} SELECT {columns('session_rows')}, own_title, prompt_last_ns FROM session_rows"  # noqa: S608
            " WHERE session_id IN (SELECT session_id FROM scope)"
        )

    def call_days(self) -> str:
        """The scope's sessions' call-day folds (§3.5): the header's rollup counts calls by day from them."""
        return (
            f"WITH {self.scope} SELECT session_id, day, n, first_ns, last_ns FROM session_call_days"  # noqa: S608
            " WHERE session_id IN (SELECT session_id FROM scope)"
        )

    def labels(self) -> str:
        """What the scope's sessions' labels need from other keys (§4): its account-connected
        component (closed through session_call_accounts) and those sessions' label inputs, the
        component's accounts' title calls, and the edges naming a scope session as parent."""
        sr = [
            ("session_id", "text"),
            ("start_ns", "bigint"),
            ("product", "text"),
            ("own_title", "text"),
            ("model_calls", "bigint"),
            ("call_accounts", "text[]"),
        ]
        title = [("session_id", "text"), ("ts_ns", "bigint"), ("call_id", "text"), ("account_id", "text"),
                 ("title", "text")]  # fmt: skip
        edge = [("parent_session_id", "text"), ("child_session_id", "text"), ("kind", "text"),
                ("child_first_ns", "bigint")]  # fmt: skip
        arms = [
            _Arm("sr", sr, f"SELECT {_cols(sr)} FROM session_rows WHERE session_id IN (SELECT session_id FROM comp)"),  # noqa: S608
            _Arm(
                "title",
                title,
                f"SELECT {_cols(title)} FROM session_title_calls"  # noqa: S608
                " WHERE account_id IN (SELECT account_id FROM session_call_accounts"
                " WHERE session_id IN (SELECT session_id FROM comp))",
            ),
            _Arm(
                "edge",
                edge,
                f"SELECT {_cols(edge)} FROM session_edges WHERE parent_session_id IN (SELECT session_id FROM scope)",  # noqa: S608
            ),
        ]
        comp = """comp(session_id) AS (
    SELECT session_id FROM scope
    UNION SELECT b.session_id FROM comp
    JOIN session_call_accounts a ON a.session_id = comp.session_id
    JOIN session_call_accounts b ON b.account_id = a.account_id)"""
        return f"WITH RECURSIVE {self.scope},\n{comp}\n{_union(arms)}"

    def ranged(self, table: str, cols: str, time: str, extra: str = "") -> str:
        """table's rows of the sessions the range can show, in the range ($2, $3)."""
        where = f"session_id IN (SELECT session_id FROM ranged) AND {_in_range(time)}{extra}"  # noqa: S608
        return f"WITH {self.scope}, {_RANGED} SELECT {cols} FROM {table} WHERE {where}"  # noqa: S608

    def events(self, carry: bool) -> str:
        """The ranged event rows; with carry, each such session's newest event of each _LATEST name
        before the range too (the partial index; arg_max skips a NULL count)."""
        cols = ", ".join(_q(c) for c in (*EVENT_COLUMNS, "batch"))
        sql = self.ranged("event_rows", cols, "ts_ns")
        if not carry:
            return sql
        for name in LATEST:
            sql += f"""
UNION ALL
SELECT c.* FROM ranged s, LATERAL (
    SELECT {cols} FROM event_rows e WHERE e.session_id = s.session_id AND e.name = '{name}'
      AND e."count" IS NOT NULL AND e.ts_ns < $2 ORDER BY e.ts_ns DESC, e."count" DESC LIMIT 1) c"""  # noqa: S608
        return sql

    def presence(self) -> str:
        where = "session_id IN (SELECT session_id FROM ranged)"
        where += " AND ($3::bigint IS NULL OR from_ns <= $3) AND ($2::bigint IS NULL OR to_ns >= $2)"
        return (
            f"WITH {self.scope}, {_RANGED} SELECT {columns('presence_segments')} FROM presence_segments WHERE {where}"  # noqa: S608
        )

    def request_log(self, what: str) -> str:
        """The request log's inputs for the range (§4), tagged by table. what: "all" (the log),
        "account" (the summary's count: the range's account calls), "missed" (/coverage: the
        not-captured calls and their reports)."""
        ctes = [
            self.scope,
            """hashes AS (SELECT DISTINCT key_hash FROM dim_session_credential
    WHERE session_id IN (SELECT session_id FROM scope) AND key_hash <> '')""",
        ]
        tx = _INPUTS["request_txs"].flat
        kind = " AND kind = 'account'" if what == "account" else ""
        ctes.append(
            f"""inrange AS (SELECT {_cols(tx)} FROM request_txs
    WHERE key_hash IN (SELECT key_hash FROM hashes) AND is_tx{kind} AND {_in_range("row_ts_ns")})"""  # noqa: S608
        )
        # A model call's step number counts its prompt's other model calls, in the range or not.
        ctes.append(
            """steps AS (SELECT DISTINCT prompt_id FROM inrange
    WHERE kind = 'model' AND prompt_id <> '' AND hidden_kind = '')"""
        )
        ctes.append(
            f"""txs AS (SELECT * FROM inrange UNION ALL
    SELECT {_cols(tx)} FROM request_txs WHERE key_hash IN (SELECT key_hash FROM hashes)
      AND prompt_id IN (SELECT prompt_id FROM steps) AND is_tx AND kind = 'model' AND hidden_kind = ''
      AND ({_in_range("row_ts_ns")}) IS NOT TRUE)"""  # noqa: S608
        )
        missed = _INPUTS["request_missed"].flat
        ctes.append(
            f"""missed AS (SELECT {_cols(missed)} FROM request_missed
    WHERE session_id IN (SELECT session_id FROM scope) AND {_in_range("row_ts_ns")})"""  # noqa: S608
        )
        ids = [
            "SELECT req_id AS rid FROM missed WHERE req_id <> ''",
            "SELECT client_req_id FROM missed WHERE client_req_id <> ''",
        ]
        if what == "all":
            ids += [
                "SELECT req_id FROM txs WHERE req_id <> ''",
                "SELECT client_req_id FROM txs WHERE client_req_id <> ''",
            ]
        ctes.append(f"ids AS ({' UNION '.join(ids)})")
        in_scope = "session_id IN (SELECT session_id FROM scope)"

        def arm(tag: str, table: str, where: str) -> _Arm:
            cols = _INPUTS[table].flat
            return _Arm(tag, cols, f"SELECT {_cols(cols)} FROM {table} WHERE {where}")  # noqa: S608

        # The reports of those ids, by either id (each its own index; a row matched by both is one row:
        # rows are distinct by (key, id)).
        rep = _INPUTS["request_reports"].flat
        report = _Arm(
            "request_reports",
            rep,
            f"""SELECT {_cols(rep)} FROM (
    SELECT * FROM request_reports WHERE req_id IN (SELECT rid FROM ids)
    UNION SELECT * FROM request_reports WHERE client_req_id IN (SELECT rid FROM ids)) r WHERE {in_scope}""",  # noqa: S608
        )
        if what == "account":
            arms = [_Arm("request_txs", tx, "SELECT * FROM txs")]
        elif what == "missed":
            arms = [_Arm("request_missed", missed, "SELECT * FROM missed"), report]
        else:
            arms = [
                _Arm("request_txs", tx, "SELECT * FROM txs"),
                arm("batch_parts", "batch_parts", "batch_id IN (SELECT call_id FROM txs)"),
                arm("prompt_index", "prompt_index", f"{in_scope} AND prompt_id IN (SELECT prompt_id FROM txs)"),  # noqa: S608
                arm("codex_turns", "codex_turns", in_scope),
                report,
                _Arm("request_missed", missed, "SELECT * FROM missed"),
                arm("session_last", "session_last", in_scope),
            ]
        return "WITH " + ",\n".join(ctes) + "\n" + _union(arms)


# --- the pages --------------------------------------------------------------------------------------

# What each person page reads, in order (the statements' names; request_log:<what>).
PAGES: dict[str, tuple[str, ...]] = {
    "": ("dims", "sessions", "session_call_days"),
    "sessions": ("dims", "sessions", "labels"),
    "timeline": ("dims", "sessions", "labels", "event_rows", "session_marks", "prompt_rows", "presence_segments"),
    "summary": ("dims", "sessions", "event_rows+carry", "session_marks", "call_tool_uses", "request_log:account"),
    "requests": ("dims", "request_log:all"),
    "coverage": ("dims", "sessions", "session_marks", "request_log:missed"),
    # A person as the table-level harness compares it: everything any of its pages reads.
    ENTITY: (
        "dims",
        "sessions",
        "labels",
        "event_rows+carry",
        "session_marks",
        "prompt_rows",
        "call_tool_uses",
        "presence_segments",
        "request_log:all",
    ),
}


def page_of(bare: str, query: dict[str, list[str]]) -> tuple[str, str] | None:
    """(person id, page) for a person page's path; None for a path no person page serves."""
    if bare == "/api/v1/coverage":
        person = query.get("personId")
        return (person[0], "coverage") if person else None
    pid, _, sub = bare.removeprefix("/api/v1/people/").partition("/")
    if not pid or sub not in PAGES or sub in ("coverage", ENTITY):
        return None
    return urllib.parse.unquote(pid), sub


def load(store: PgStore, run: _Run, pid: str, page: str, lo: int | None, hi: int | None) -> Status | None:
    """The person page's tables, read in run's snapshot; lo and hi are its range's instants (ns)."""
    con = run.con
    sql = _Sql(unattributed=pid == UNATTRIBUTED)
    one = [(pid, "VARCHAR")]
    ranged = [
        *one,
        (None if lo is None else lo - _WIDEN_NS, "BIGINT"),
        (None if hi is None else hi + _WIDEN_NS, "BIGINT"),
    ]
    got: dict[str, str] = {}
    for name in PAGES[page]:
        if name == "dims":
            got[name] = run.fetch(name, sql.dims(), one)
        elif name == "sessions":
            got[name] = run.fetch(name, sql.sessions(), one)
        elif name == "labels":
            got[name] = run.fetch(name, sql.labels(), one)
        elif name == "session_call_days":
            got[name] = run.fetch(name, sql.call_days(), one)
        elif name.startswith("event_rows"):
            got["event_rows"] = run.fetch("event_rows", sql.events(carry=name.endswith("+carry")), ranged)
        elif name == "session_marks":
            got[name] = run.fetch(name, sql.ranged(name, columns(name), "t_ns"), ranged)
        elif name == "prompt_rows":
            got[name] = run.fetch(name, sql.ranged(name, columns(name), "t_ns", " AND typed"), ranged)
        elif name == "call_tool_uses":
            got[name] = run.fetch(name, sql.ranged(name, columns(name), "ts_ns"), ranged)
        elif name == "presence_segments":
            got[name] = run.fetch(name, sql.presence(), ranged)
        elif name.startswith("request_log:"):
            got["request_log"] = run.fetch("request_log", sql.request_log(name.split(":", 1)[1]), ranged)
    _dimensions(con, got["dims"])
    if "sessions" in got:
        _session_rows(store, con, got)
    if "session_call_days" in got:
        days = got["session_call_days"]
        con.execute(f"CREATE TABLE session_call_days AS SELECT * FROM {days} ORDER BY session_id, day")  # noqa: S608
    for name in ("session_marks", "presence_segments", "call_tool_uses"):
        if name in got:
            store._into(con, name, f"SELECT * FROM {got[name]}")  # noqa: S608
    if "prompt_rows" in got:
        cols = ", ".join(
            "coalesce(s.prompt_last_ns, p.session_last_ns) AS session_last_ns"
            if c == "session_last_ns"
            else "p." + _q(c)
            for c, _ in SHAPES["prompt_rows"]
        )
        select = f"SELECT {cols} FROM {got['prompt_rows']} p LEFT JOIN {got['sessions']} s USING (session_id)"  # noqa: S608
        store._into(con, "prompt_rows", select)
    if "event_rows" in got:
        con.execute(legacy._EMPTY["event_rows"])
        con.execute("ALTER TABLE event_rows ADD COLUMN batch VARCHAR")
        con.execute(f"INSERT INTO event_rows BY NAME SELECT * FROM {got['event_rows']} ORDER BY session_id, ts_ns")  # noqa: S608
    if "request_log" in got:
        _request_rows(con, pid, got["request_log"])
    for t in (*DIMENSIONS, *_REQUEST_INPUTS):
        con.execute(f"DROP TABLE IF EXISTS {t}")
    return None


def entity(store: PgStore, pid: str, lo: int | None = None, hi: int | None = None) -> Any:
    """A person's tables for a range, everything any of its pages reads (the table-level harness)."""

    def plan(run: _Run) -> Status | None:
        return load(store, run, pid, ENTITY, lo, hi)

    return store._read(f"person:{pid}", plan)


def range_of(query: dict[str, list[str]]) -> tuple[int | None, int | None]:
    """A request's from= and to= instants (ns), each None when absent."""
    lo, hi = (_instant_ns(query[k][0] if query.get(k) else None) for k in ("from", "to"))
    return lo, hi


# --- into today's tables ------------------------------------------------------------------------------


def _split(con: duckdb.DuckDBPyConnection, loaded: str, tag: str, table: str) -> None:
    """A tagged statement's rows of one arm as table (STATE_SCHEMA's shape: the structs rebuilt)."""
    con.execute(f"CREATE TABLE {table} ({_INPUT_SCHEMA[table]})")
    flat = ", ".join(f"{_q(tag + '__' + c)} AS {_q(c)}" for c, _ in _INPUTS[table].flat)
    con.execute(
        f"INSERT INTO {table} BY NAME SELECT {_INPUTS[table].rebuild} FROM "  # noqa: S608 (fixed names)
        f"(SELECT {flat} FROM {loaded} WHERE src = '{tag}')"
    )


def _dimensions(con: duckdb.DuckDBPyConnection, dims: str) -> None:
    """The dimension tables, and person_sql's tables and accounts over them (today's SQL)."""
    have = {r[0] for r in con.execute(f"SELECT DISTINCT src FROM {dims}").fetchall()}  # noqa: S608
    for t in DIMENSIONS:
        if t in have:
            _split(con, dims, t, t)
        else:
            con.execute(f"CREATE TABLE {t} ({state_sql.STATE_SCHEMA[t]})")
    for sql in PERSON_TABLES.values():
        con.execute(sql)
    con.execute(legacy._EMPTY["accounts"])
    con.execute("INSERT INTO accounts BY NAME SELECT account_id, email, name, org, seen_ns FROM dim_account")


def _session_rows(store: PgStore, con: duckdb.DuckDBPyConnection, got: dict[str, str]) -> None:
    """session_rows (today's columns), with the labels' and children's values (sql/labels.sql)."""
    rows = got["sessions"]
    api_cols = ", ".join(_q(c) for c, _ in SHAPES["session_rows"])
    store._into(con, "session_rows", f"SELECT {api_cols} FROM {rows}")  # noqa: S608
    if "labels" not in got:
        return
    loaded = got["labels"]

    def part(tag: str, cols: str) -> str:
        return f"SELECT {cols} FROM {loaded} WHERE src = '{tag}'"  # noqa: S608

    sr = part(
        "sr",
        "sr__session_id AS session_id, sr__start_ns AS start_ns, sr__product AS product, sr__own_title AS own_title,"
        " sr__model_calls AS model_calls, sr__call_accounts AS call_accounts",
    )
    titles = part(
        "title",
        "title__session_id AS session_id, title__ts_ns AS ts_ns, title__call_id AS call_id,"
        " title__account_id AS account_id, title__title AS title",
    )
    edges = part(
        "edge",
        "edge__parent_session_id AS parent_session_id, edge__child_session_id AS child_session_id,"
        " edge__kind AS kind, edge__child_first_ns AS child_first_ns",
    )
    con.execute(f"CREATE TABLE _lbl_all_sessions AS {sr}")
    con.execute(f"CREATE TABLE _lbl_all_titles AS {titles}")
    con.execute(f"CREATE TABLE _lbl_edges AS {edges}")
    con.execute("CREATE TABLE _lbl_sessions AS SELECT * FROM _lbl_all_sessions WHERE false")
    con.execute("CREATE TABLE _lbl_title_calls AS SELECT * FROM _lbl_all_titles WHERE false")
    con.execute(f"CREATE TABLE _lbl_out AS SELECT * FROM ({_LABELS_SQL}) WHERE false")  # noqa: S608 (our SQL file)
    for sessions, accounts in _title_runs(con):
        con.execute(
            "CREATE OR REPLACE TABLE _lbl_sessions AS SELECT * FROM _lbl_all_sessions"
            " WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
            [sessions],
        )
        con.execute(
            "CREATE OR REPLACE TABLE _lbl_title_calls AS SELECT * FROM _lbl_all_titles"
            " WHERE account_id IN (SELECT unnest(?::VARCHAR[]))",
            [accounts],
        )
        con.execute(f"INSERT INTO _lbl_out BY NAME {_LABELS_SQL}")
    con.execute(
        """UPDATE session_rows s SET label_text = l.label_text, label_source = l.label_source,
               label_text_alone = l.label_text_alone, label_source_alone = l.label_source_alone,
               children_json = l.children_json
           FROM _lbl_out l WHERE l.session_id = s.session_id"""
    )


def _title_runs(con: duckdb.DuckDBPyConnection) -> list[tuple[list[str], list[str]]]:
    """The component's sessions with a row in groups linked by their calls' accounts, as
    buckets._session_rows runs session_rows.sql: each group with title calls on its own (its
    accounts' title calls), the rest together with none. (sessions, title accounts) per run."""
    rows = con.execute("SELECT session_id, call_accounts FROM _lbl_all_sessions").fetchall()
    titled = {a for (a,) in con.execute("SELECT DISTINCT account_id FROM _lbl_all_titles").fetchall()}
    parent = {s: s for s, _ in rows}

    def find(x: str) -> str:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    by_account: dict[str, str] = {}
    for s, accounts in rows:
        for a in accounts or []:
            if a in by_account:
                parent[find(s)] = find(by_account[a])
            else:
                by_account[a] = s
    groups: dict[str, set[str]] = {}
    for s, _ in rows:
        groups.setdefault(find(s), set()).add(s)
    accounts_of = {s: set(accounts or []) for s, accounts in rows}
    runs, rest = [], set[str]()
    for g in groups.values():
        accts = set().union(*(accounts_of[s] for s in g)) & titled
        if accts:
            runs.append((sorted(g), sorted(accts)))
        else:
            rest |= g
    if rest or not runs:
        runs.append((sorted(rest), []))
    return runs


_REQUEST_INPUTS = (
    "request_txs",
    "request_reports",
    "request_missed",
    "prompt_index",
    "codex_turns",
    "session_last",
    "batch_parts",
    "batch_members",
)


def _request_rows(con: duckdb.DuckDBPyConnection, pid: str, loaded: str) -> None:
    """request_rows and person_last: stream_sql over the request log's inputs, as
    state_sql.request_tables builds them."""
    have = {r[0] for r in con.execute(f"SELECT DISTINCT src FROM {loaded}").fetchall()}  # noqa: S608
    for t in _REQUEST_INPUTS:
        if t == "batch_members":
            continue
        if t in have:
            _split(con, loaded, t, t)
        else:
            con.execute(f"CREATE TABLE {t} ({_INPUT_SCHEMA[t]})")
    sql, params = stream_sql.person_request_rows_sql(pid, parts=True)
    con.execute(legacy._EMPTY["request_rows"])
    con.execute(f"INSERT INTO request_rows BY NAME SELECT * FROM ({sql})", params)  # noqa: S608 (stream_sql's)
    con.execute(
        "CREATE OR REPLACE TABLE request_rows AS SELECT * FROM request_rows ORDER BY person_id, ts_ns, request_id"
    )
    con.execute(legacy._EMPTY["person_last"])
    con.execute("INSERT INTO person_last VALUES (?, ?)", [pid, stream_sql.person_last(con, pid)])
