"""scripts/parity_compare.py on synthetic recordings: data counts, formatting and key order don't."""

import importlib.util
import json
import pathlib

import pytest

_path = pathlib.Path(__file__).resolve().parents[2] / "scripts" / "parity_compare.py"
_spec = importlib.util.spec_from_file_location("parity_compare", _path)
assert _spec is not None and _spec.loader is not None
pc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(pc)


def write(path, rows):
    path.write_text("".join(json.dumps(r) + "\n" for r in rows))
    return str(path)


def rec(url, body, *, now=1, status=200, **dumps):
    return {"now": now, "url": url, "status": status, "body": json.dumps(body, **dumps)}


def test_formatting_and_key_order_are_equal(tmp_path, capsys):
    body = {"data": [{"id": "a", "n": 1, "x": None}], "meta": {"total": 1}}
    reordered = {"meta": {"total": 1}, "data": [{"x": None, "n": 1.0, "id": "a"}]}
    a = write(tmp_path / "a.jsonl", [rec("/api/v1/status", body, sort_keys=True), rec("/api/v1/people", "text")])
    b = write(tmp_path / "b.jsonl", [rec("/api/v1/people", "text"), rec("/api/v1/status", reordered, indent=2)])
    assert pc.main([a, b]) == 0
    assert capsys.readouterr().out.strip() == "2 equal, 0 different"


@pytest.mark.parametrize(
    ("x", "y", "path"),
    [
        ({"d": [{"n": 1}, {"n": 2}]}, {"d": [{"n": 1}, {"n": 3}]}, ".body.d[1].n"),
        ({"d": [1, 2]}, {"d": [2, 1]}, ".body.d[0]"),
        ({"d": [1, 2]}, {"d": [1, 2, 3]}, ".body.d.<len>"),
        ({"a": 1, "b": 2}, {"a": 1}, ".body.b"),
        ({"a": True}, {"a": 1}, ".body.a"),
        ({"a": None}, {"a": 0}, ".body.a"),
    ],
)
def test_names_the_first_differing_field(tmp_path, capsys, x, y, path):
    a = write(tmp_path / "a.jsonl", [rec("/u", x)])
    b = write(tmp_path / "b.jsonl", [rec("/u", y)])
    assert pc.main([a, b]) == 1
    assert capsys.readouterr().out.splitlines() == [f"/u @1: {path}", "0 equal, 1 different"]


def test_status_missing_responses_and_skipped_prefixes(tmp_path, capsys):
    a = write(
        tmp_path / "a.jsonl",
        [
            rec("/api/v1/sessions/s", {"k": 1}, status=200),
            rec("/api/v1/coverage?personId=p", {"k": 1}),
            rec("/api/v1/people/p", {}, now=1),
            rec("/api/v1/people/p", {}, now=2),
        ],
    )
    b = write(
        tmp_path / "b.jsonl",
        [
            rec("/api/v1/sessions/s", {"k": 1}, status=404),
            rec("/api/v1/coverage?personId=p", {"k": 2}),
            rec("/api/v1/people/p", {}, now=1),
        ],
    )
    assert pc.main([a, b, "--skip", "/api/v1/coverage"]) == 1
    assert capsys.readouterr().out.splitlines() == [
        "/api/v1/sessions/s @1: .status",
        "/api/v1/people/p @2: <missing in b>",
        "1 equal, 2 different",
    ]


def test_dropped_fields_leave_every_depth_of_both_sides(tmp_path, capsys):
    """A removed feature's field (here `presence` and `collapsedCount`) doesn't count wherever it is;
    any other difference still does, and a dropped name is only an object key, never a value."""
    a = write(
        tmp_path / "a.jsonl",
        [
            rec("/t", {"data": {"presence": [1], "n": 1}}),
            rec("/e", {"data": [{"name": "x", "collapsedCount": 2}, {"name": "presence"}]}),
            rec("/f", {"data": {"presence": [], "n": 1}}),
        ],
    )
    b = write(
        tmp_path / "b.jsonl",
        [
            rec("/t", {"data": {"n": 1}}),
            rec("/e", {"data": [{"name": "x"}, {"name": "presence"}]}),
            rec("/f", {"data": {"n": 2}}),
        ],
    )
    assert pc.main([a, b]) == 1
    assert capsys.readouterr().out.splitlines()[-1] == "0 equal, 3 different"
    assert pc.main([a, b, "--drop-field", "presence", "collapsedCount"]) == 1
    assert capsys.readouterr().out.splitlines() == ["/f @1: .body.data.n", "2 equal, 1 different"]
