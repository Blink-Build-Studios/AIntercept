#!/usr/bin/env python3
"""Fail if a tracked file contains a real identifier from captured traffic.

scripts/real-data-denylist.sha256 holds SHA-256 hashes (of the lowercased
value) of real account, org, device and session IDs, client IPs and email
addresses. Only hashes are committed, so the list itself leaks nothing. Every
token-like run of characters in every tracked text file (and .gz file) is hashed
and checked.

A line containing `real-data-ok` is skipped (a deliberate, reviewed exception).

Add identifiers with:  printf '%s' VALUE | tr A-Z a-z | shasum -a 256
Run:                   python3 scripts/check-real-data.py [paths...]
"""

import gzip
import hashlib
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
DENYLIST = REPO / "scripts" / "real-data-denylist.sha256"
TOKEN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._%+@:-]{6,}")
SKIP = {"scripts/real-data-denylist.sha256"}


def candidates(token: str):
    """The token and its variants with trailing punctuation or a :port removed."""
    yield token
    t = token.rstrip(".:-")
    yield t
    if t.count(":") == 1 and t.rsplit(":", 1)[1].isdigit():
        yield t.rsplit(":", 1)[0]
    for part in re.split(r"[:@]", t):  # e.g. user:<uuid>, or the domain of an email
        if len(part) >= 7:
            yield part


def main(argv: list[str]) -> int:
    deny = {line.strip() for line in DENYLIST.read_text().splitlines() if line.strip() and not line.startswith("#")}
    if argv:
        files = argv
    else:
        files = subprocess.run(["git", "ls-files", "-z"], cwd=REPO, capture_output=True, check=True).stdout.decode().split("\0")
    found = []
    for rel in filter(None, files):
        if rel in SKIP:
            continue
        path = REPO / rel
        try:
            # Compressed fixtures are scanned too: a leak inside a .gz is still a leak.
            raw = gzip.decompress(path.read_bytes()) if rel.endswith(".gz") else path.read_bytes()
            text = raw.decode("utf-8")
        except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError, gzip.BadGzipFile, EOFError):
            continue
        for n, line in enumerate(text.splitlines(), 1):
            if "real-data-ok" in line:  # a deliberate, reviewed exception (e.g. a maintainer address)
                continue
            for m in TOKEN.finditer(line):
                for c in candidates(m.group(0)):
                    if hashlib.sha256(c.lower().encode()).hexdigest() in deny:
                        found.append(f"{rel}:{n}: real identifier from captured traffic ({c[:4]}…{c[-2:]})")
                        break
    for f in sorted(set(found)):
        print(f, file=sys.stderr)
    if found:
        print(f"\n{len(set(found))} real identifier(s) committed. Replace them with sanitized values.", file=sys.stderr)
        return 1
    print("real data: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
