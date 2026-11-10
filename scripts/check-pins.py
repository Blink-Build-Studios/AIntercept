#!/usr/bin/env python3
"""Fail unless dependencies are pinned exactly and the two-week release-age rule
is configured (docs/ui/BUILD_PLAN.md §11).

- ui/package.json: every dependency is an exact x.y.z version; packageManager is exact.
- ui/pnpm-workspace.yaml: minimumReleaseAge >= 20160 minutes (14 days), so pnpm
  refuses to install anything released in the last two weeks.
- api/pyproject.toml: every runtime and dev dependency is `name==x.y.z`, and
  [tool.uv] exclude-newer is at least 14 days in the past, so uv resolves nothing newer.
Lockfile freshness is checked by CI's `pnpm install --frozen-lockfile` and `uv sync --locked`.
"""

import json
import pathlib
import re
import sys
import tomllib
from datetime import UTC, datetime, timedelta

REPO = pathlib.Path(__file__).resolve().parents[1]
EXACT = re.compile(r"^\d+\.\d+\.\d+$")
MIN_AGE = timedelta(days=14)


def main() -> int:
    errors: list[str] = []

    pkg = json.loads((REPO / "ui" / "package.json").read_text())
    for section in ("dependencies", "devDependencies"):
        for name, spec in pkg.get(section, {}).items():
            if not EXACT.match(spec):
                errors.append(f"ui/package.json {section}.{name} = {spec!r}: pin an exact version")
    if not re.match(r"^pnpm@\d+\.\d+\.\d+$", pkg.get("packageManager", "")):
        errors.append("ui/package.json packageManager must be an exact pnpm@x.y.z")

    ws = (REPO / "ui" / "pnpm-workspace.yaml").read_text()
    m = re.search(r"^minimumReleaseAge:\s*(\d+)\s*$", ws, re.M)
    if not m or int(m.group(1)) < MIN_AGE.days * 24 * 60:
        errors.append("ui/pnpm-workspace.yaml minimumReleaseAge must be >= 20160 (14 days)")

    py = tomllib.loads((REPO / "api" / "pyproject.toml").read_text())
    deps = list(py["project"].get("dependencies", []))
    for group in py.get("dependency-groups", {}).values():
        deps += group
    for d in deps:
        if not re.match(r"^[A-Za-z0-9_.-]+==\d+(\.\d+)*$", d):
            errors.append(f"api/pyproject.toml dependency {d!r}: pin as name==x.y.z")
    cutoff = py.get("tool", {}).get("uv", {}).get("exclude-newer")
    if not cutoff:
        errors.append("api/pyproject.toml [tool.uv] exclude-newer is missing")
    elif datetime.now(UTC) - datetime.fromisoformat(cutoff.replace("Z", "+00:00")) < MIN_AGE:
        errors.append(f"api/pyproject.toml exclude-newer {cutoff} is less than 14 days ago")

    for e in errors:
        print(e, file=sys.stderr)
    if errors:
        return 1
    print("pins: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
