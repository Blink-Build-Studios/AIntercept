#!/usr/bin/env python3
"""Fail if a tracked file contains a real identifier from captured traffic.

Two lists of SHA-256 hashes (of the lowercased value) of real identifiers:

- scripts/real-data-denylist.sha256 (committed) holds only identifiers random
  enough that their hash can't be reversed by guessing: UUIDs and long random
  device and session ids (about 100 bits or more).
- data/real-data-denylist.local.sha256 (gitignored, read when it exists) holds
  the rest: emails, IPs, names, hostnames, short or structured ids. A hash of
  one of those would give it away to anyone trying the likely values, so it
  never leaves the machine that captured it.

`aintercept capture-fixture -denylist` sorts each identifier it replaces into
the right list. Every token-like run of characters in every tracked text file
(and .gz file) is hashed and checked against both.

A line containing `real-data-ok` is skipped (a deliberate, reviewed exception).

Add an identifier with:  printf '%s' VALUE | tr A-Z a-z | shasum -a 256
(to the committed list only if it is a UUID or a long random id).
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
LOCAL_DENYLIST = REPO / "data" / "real-data-denylist.local.sha256"
TOKEN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._%+@:-]{6,}")
SKIP = {"scripts/real-data-denylist.sha256", "data/real-data-denylist.local.sha256"}


def read_hashes(path: pathlib.Path) -> set[str]:
    return {line.strip() for line in path.read_text().splitlines() if line.strip() and not line.startswith("#")}


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
    deny = read_hashes(DENYLIST)
    if LOCAL_DENYLIST.exists():
        deny |= read_hashes(LOCAL_DENYLIST)
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
