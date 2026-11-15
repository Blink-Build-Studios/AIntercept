"""Compares two parity recordings response by response, as parsed JSON values (docs/PIPELINE.md §8):
formatting and key order never count, only data does.

A recording is JSON lines, one per response: {"now": ..., "url": ..., "status": ..., "body": ...}, the body
a JSON text (or plain text when the response wasn't JSON). Responses pair up by (now, url).

  python3 scripts/parity_compare.py <a.jsonl> <b.jsonl> [--skip /api/v1/coverage ...]

Prints the count equal, the count different, and per different response the path to its first
differing field. Exits 1 if any differ.
"""

import argparse
import json
import sys
from typing import Any


def load(path: str) -> dict[tuple[Any, str], Any]:
    out = {}
    with open(path) as f:
        for line in f:
            r = json.loads(line)
            try:
                body = json.loads(r["body"]) if isinstance(r["body"], str) else r["body"]
            except ValueError:
                body = r["body"]
            out[(r["now"], r["url"])] = {"status": r["status"], "body": body}
    return out


def first_diff(a: Any, b: Any, path: str = "") -> str | None:
    """The path to the first field where a and b differ, or None if they're equal."""
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(a.keys() | b.keys()):
            if k not in a or k not in b:
                return f"{path}.{k}"
            d = first_diff(a[k], b[k], f"{path}.{k}")
            if d is not None:
                return d
        return None
    if isinstance(a, list) and isinstance(b, list):
        for i, (x, y) in enumerate(zip(a, b, strict=False)):
            d = first_diff(x, y, f"{path}[{i}]")
            if d is not None:
                return d
        return None if len(a) == len(b) else f"{path}.<len>"
    # 1 and 1.0 are the same JSON number; true and 1 aren't the same value.
    if a != b or isinstance(a, bool) != isinstance(b, bool):
        return path or "."
    return None


def compare(a: dict[tuple[Any, str], Any], b: dict[tuple[Any, str], Any], skip: list[str]) -> list[tuple[str, str]]:
    """Every compared response that differs, as (label, path); the count compared is the rest."""
    diffs = []
    for key in sorted(a.keys() | b.keys(), key=str):
        now, url = key
        if any(url.startswith(p) for p in skip):
            continue
        if key not in a or key not in b:
            diffs.append((f"{url} @{now}", "<missing in " + ("a" if key not in a else "b") + ">"))
            continue
        d = first_diff(a[key], b[key])
        if d is not None:
            diffs.append((f"{url} @{now}", d))
    return diffs


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("a")
    p.add_argument("b")
    p.add_argument("--skip", nargs="*", default=[], help="URL prefixes to leave out")
    args = p.parse_args(argv)
    a, b = load(args.a), load(args.b)
    diffs = compare(a, b, args.skip)
    total = sum(1 for _, url in a.keys() | b.keys() if not any(url.startswith(p) for p in args.skip))
    for label, path in diffs:
        print(f"{label}: {path}")
    print(f"{total - len(diffs)} equal, {len(diffs)} different")
    return 1 if diffs else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
