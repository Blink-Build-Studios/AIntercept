"""The tools offered against the ones called (handoff §11S, ToolsOfferedUsed), from a range's key rows: the
tool lists its requests carried (`offer`, `offer_main`: hashes, the lists in tool_lists) and the tools its
calls named (`call_builtin`, `call_mcp`)."""

from aintercept_api.tools import KINDS, tools_in_range


def test_one_row_per_source_built_in_first_then_servers_by_calls_unused_last() -> None:
    keys = [
        ("offer", "h1", 3),
        ("offer_main", "h1", 3),
        ("offer", "h2", 1),
        ("call_builtin", "Bash", 488),
        ("call_builtin", "Read", 402),
        ("call_mcp", '["linear", "list_issues"]', 2),
        ("call_mcp", '["linear", "get_issue"]', 1),
    ]
    lists = {
        "h1": [
            "Bash",
            "Read",
            "Edit",
            "mcp__linear__list_issues",
            "mcp__linear__get_issue",
            "mcp__linear__create_issue",
            "mcp__sentry__a",
        ],
        "h2": ["Read", "mcp__sentry__b"],
    }
    t = tools_in_range(keys, lists)
    assert [(s.source, s.offered, s.used, s.calls) for s in t.sources] == [
        ("Built-in", 3, 2, {"Bash": 488, "Read": 402}),
        ("mcp:linear", 3, 2, {"list_issues": 2, "get_issue": 1}),
        ("mcp:sentry", 2, 0, {}),
    ]
    assert (t.offered, t.used, t.calls, t.listChanges) == (8, 4, 893, 0)
    assert t.byName == {"Bash": 488, "Read": 402, "mcp__linear__list_issues": 2, "mcp__linear__get_issue": 1}


def test_list_changes_count_the_main_threads_distinct_lists() -> None:
    keys = [("offer_main", "a", 1), ("offer_main", "b", 1), ("offer_main", "c", 1), ("offer", "a", 1)]
    assert tools_in_range(keys, {"a": ["Read"], "b": ["Read"], "c": ["Read"]}).listChanges == 2


def test_without_a_tool_list_offered_is_unknown_and_a_called_tool_still_shows() -> None:
    t = tools_in_range([("call_builtin", "Bash", 2)], {})
    assert (t.offered, t.listChanges) == (None, None)
    assert [(s.source, s.offered, s.used) for s in t.sources] == [("Built-in", None, 1)]


def test_a_tool_called_but_never_listed_counts_as_offered_once_lists_exist() -> None:
    t = tools_in_range([("offer", "a", 1), ("call_builtin", "Task", 1)], {"a": ["Read"]})
    assert [(s.source, s.offered, s.used) for s in t.sources] == [("Built-in", 2, 1)]


def test_kinds_are_the_rows_it_reads() -> None:
    assert set(KINDS) == {"offer", "offer_main", "call_builtin", "call_mcp"}
