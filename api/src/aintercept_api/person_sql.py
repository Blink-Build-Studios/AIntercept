"""The person tables, built at query time from the dimension tables (docs/STREAMING_PIPELINE.md
§4.6). Nothing is stored per person: the stream processor writes small facts per key (who an
account is, which accounts and credentials a session names, an account's devices and plans),
and these statements resolve them into the tables the endpoints read: person_map,
person_sessions, account_facts and account_devices. They give exactly what the processor's
reference rebuild does (personTables in aintercept/internal/stream/state/dims_test.go;
tests/test_person_sql.py checks every generated seed).

A dimension row's columns are its table's; a "pick" is a STRUCT(v, ts, ok): the newest wins,
the greatest value at the same time.
"""

DIMENSIONS = (
    "dim_account",
    "dim_session_account",
    "dim_session_visible",
    "dim_session_person",
    "dim_person_device",
    "dim_session_credential",
    "dim_account_events",
    "dim_account_device",
    "dim_codex_plan",
)


PERSON_TABLES = {
    # Accounts linked by email (case-insensitive), each person id's accounts, and the People
    # page's rows: an email group of two or more accounts is one row, under its first account
    # that has a session, whose members are the group's accounts with a session; any other
    # account with a session is a row of its own.
    "person_map": """CREATE OR REPLACE TABLE person_map AS
WITH acct AS (
    SELECT account_id, lower(email) AS email FROM dim_account WHERE coalesce(email, '') <> ''
),
ids AS (
    SELECT account_id AS id FROM dim_account
    UNION SELECT person_id FROM dim_session_person
    UNION SELECT person_id FROM dim_person_device
    UNION SELECT account_id FROM dim_session_account
),
linked AS (
    SELECT id, id AS account_id FROM ids WHERE id <> 'unattributed'
    UNION SELECT i.id, b.account_id FROM ids i JOIN acct a ON a.account_id = i.id JOIN acct b ON b.email = a.email
    WHERE i.id <> 'unattributed'
),
owned AS (SELECT DISTINCT account_id FROM dim_session_account),
grp AS (SELECT email, count(*) AS n FROM acct GROUP BY email),
leader AS (
    SELECT o.account_id,
           CASE WHEN g.n >= 2 THEN min(o2.account_id) ELSE o.account_id END AS leader
    FROM owned o
    LEFT JOIN acct a ON a.account_id = o.account_id
    LEFT JOIN grp g ON g.email = a.email
    LEFT JOIN acct a2 ON g.n >= 2 AND a2.email = a.email
    LEFT JOIN owned o2 ON o2.account_id = a2.account_id
    GROUP BY o.account_id, g.n
)
SELECT l.id AS person_id, l.account_id, coalesce(ld.leader = l.id, false) AS directory
FROM linked l LEFT JOIN leader ld ON ld.account_id = l.account_id
ORDER BY person_id, account_id""",
    "person_sessions": """CREATE OR REPLACE TABLE person_sessions AS
WITH acct AS (
    SELECT account_id, lower(email) AS email FROM dim_account WHERE coalesce(email, '') <> ''
),
ids AS (
    SELECT account_id AS id FROM dim_account
    UNION SELECT person_id FROM dim_session_person
    UNION SELECT person_id FROM dim_person_device
    UNION SELECT account_id FROM dim_session_account
),
linked AS (
    SELECT id, id AS account_id FROM ids WHERE id <> 'unattributed'
    UNION SELECT i.id, b.account_id FROM ids i JOIN acct a ON a.account_id = i.id JOIN acct b ON b.email = a.email
    WHERE i.id <> 'unattributed'
),
owned AS (SELECT DISTINCT account_id FROM dim_session_account),
grp AS (SELECT email, count(*) AS n FROM acct GROUP BY email),
leader AS (
    SELECT o.account_id,
           CASE WHEN g.n >= 2 THEN min(o2.account_id) ELSE o.account_id END AS leader
    FROM owned o
    LEFT JOIN acct a ON a.account_id = o.account_id
    LEFT JOIN grp g ON g.email = a.email
    LEFT JOIN acct a2 ON g.n >= 2 AND a2.email = a.email
    LEFT JOIN owned o2 ON o2.account_id = a2.account_id
    GROUP BY o.account_id, g.n
)
SELECT DISTINCT l.id AS person_id, sa.session_id
FROM linked l JOIN dim_session_account sa ON sa.account_id = l.account_id
UNION
SELECT 'unattributed', v.session_id FROM dim_session_visible v
WHERE v.session_id NOT IN (SELECT session_id FROM dim_session_account)
ORDER BY person_id, session_id""",
    # Each account's earliest org (the least at the same time), newest plan and newest Codex
    # plan, over every key's partial row; an account named only by a device has a row too.
    "account_facts": """CREATE OR REPLACE TABLE account_facts AS
WITH accts AS (
    SELECT account_id FROM dim_account_events
    UNION SELECT account_id FROM dim_codex_plan
    UNION SELECT account_id FROM dim_account_device
),
ev AS (
    SELECT account_id,
           first(org ORDER BY org_ns, org) FILTER (WHERE coalesce(org, '') <> '') AS org,
           first(plan.v ORDER BY plan.ts DESC, plan.v DESC) FILTER (WHERE plan.ok) AS plan
    FROM dim_account_events GROUP BY account_id
),
cx AS (
    SELECT account_id, first(plan.v ORDER BY plan.ts DESC, plan.v DESC) FILTER (WHERE plan.ok) AS codex_plan
    FROM dim_codex_plan GROUP BY account_id
)
SELECT a.account_id, ev.org, ev.plan, cx.codex_plan
FROM accts a LEFT JOIN ev USING (account_id) LEFT JOIN cx USING (account_id)
ORDER BY account_id""",
    # Each account's devices, with the environment (each field's newest value) only when an
    # event names the device.
    "account_devices": """CREATE OR REPLACE TABLE account_devices AS
SELECT account_id, device_id, bool_or(on_events) AS on_events,
       CASE WHEN bool_or(on_events) THEN first(platform.v ORDER BY platform.ts DESC, platform.v DESC)
            FILTER (WHERE platform.ok) END AS platform,
       CASE WHEN bool_or(on_events) THEN first(terminal.v ORDER BY terminal.ts DESC, terminal.v DESC)
            FILTER (WHERE terminal.ok) END AS terminal,
       CASE WHEN bool_or(on_events) THEN first(shell.v ORDER BY shell.ts DESC, shell.v DESC)
            FILTER (WHERE shell.ok) END AS shell
FROM dim_account_device
GROUP BY account_id, device_id
ORDER BY account_id, device_id""",
}
