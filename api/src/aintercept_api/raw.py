"""Where today's store reads the raw partitions from (the drawer's raw view and request
detail; queries never read them): a local directory (`make up`, tests). Keys are the
processor's lineage keys (`<prefix>/dt=…/hour=…/<pod>/<file>`). In the cluster the API reads a
call's rows from its detail object in the bucket instead (v1.V1Store)."""

import gzip
import pathlib
import re
from datetime import datetime, timedelta
from typing import Protocol


class RawSource(Protocol):
    def folder(self, folder: str) -> list[str]:
        """The `.ndjson.gz` file names directly in a folder key (ending in "/"), sorted."""
        ...

    def read(self, key: str) -> bytes | None:
        """One file's bytes as stored (gzip), or None when it isn't there."""
        ...


class LocalRaw:
    """Raw partitions under a directory. Keys carry the bucket's prefix; a synced
    or local copy may not, so a key also resolves without its first segment."""

    def __init__(self, root: pathlib.Path):
        self.root = pathlib.Path(root)

    def _path(self, key: str) -> pathlib.Path | None:
        for k in (key, key.split("/", 1)[1] if "/" in key else None):
            if k is not None and (self.root / k).exists():
                return self.root / k
        return None

    def folder(self, folder: str) -> list[str]:
        d = self._path(folder.rstrip("/"))
        return sorted(f.name for f in d.glob("*.ndjson.gz")) if d and d.is_dir() else []

    def read(self, key: str) -> bytes | None:
        p = self._path(key)
        try:
            return p.read_bytes() if p and p.is_file() else None
        except OSError:
            return None


def lines(gz: bytes | None) -> list[bytes]:
    """A raw file's non-empty lines (none when it's missing or truncated)."""
    if gz is None:
        return []
    try:
        return [ln.strip() for ln in gzip.decompress(gz).split(b"\n") if ln.strip()]
    except (OSError, EOFError):
        return []


_POD_FOLDER = re.compile(r"^(?P<root>.*?)dt=(?P<day>\d{4}-\d{2}-\d{2})/hour=(?P<hour>\d{2})/(?P<pod>[^/]+)/$")


def folder_of(key: str) -> str:
    return key.rsplit("/", 1)[0] + "/"


def pod_hours(first: str, last: str) -> list[str]:
    """A pod's folder keys (`<prefix>/dt=…/hour=…/<pod>/`) from first's hour through last's, in order."""
    a, b = _POD_FOLDER.match(first), _POD_FOLDER.match(last)
    if not a or not b or a["pod"] != b["pod"] or a["root"] != b["root"]:
        return list(dict.fromkeys([first, last]))
    t = datetime.strptime(f"{a['day']}T{a['hour']}", "%Y-%m-%dT%H")
    end = datetime.strptime(f"{b['day']}T{b['hour']}", "%Y-%m-%dT%H")
    if end < t:
        return list(dict.fromkeys([first, last]))
    out = []
    while t <= end:
        out.append(f"{a['root']}{t:dt=%Y-%m-%d/hour=%H}/{a['pod']}/")
        t += timedelta(hours=1)
    return out
