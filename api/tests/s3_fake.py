"""A minimal S3 for tests: path-style GET (objects, with Range), HEAD and ListObjectsV2 over a
local directory, unauthenticated, logging every request and the bytes it sent. DuckDB's httpfs
and buckets.S3Source both read it as they'd read S3, so tests count exactly what a request reads."""

import email.utils
import pathlib
import threading
import urllib.parse
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from xml.sax.saxutils import escape

BUCKET = "fake"


@dataclass
class Hit:
    method: str
    key: str
    query: str
    sent: int


@dataclass
class FakeS3:
    root: pathlib.Path
    endpoint: str = ""
    hits: list[Hit] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def reset(self) -> None:
        with self.lock:
            self.hits.clear()

    def gets(self) -> list[Hit]:
        """Object reads (GETs of a key, not listings)."""
        with self.lock:
            return [h for h in self.hits if h.method == "GET" and h.key and not h.query.startswith("list-type")]

    def lists(self) -> list[Hit]:
        with self.lock:
            return [h for h in self.hits if "list-type" in h.query]

    def bytes_read(self) -> int:
        with self.lock:
            return sum(h.sent for h in self.hits)

    def url(self, key: str) -> str:
        return f"s3://{BUCKET}/{key}"


def _handler(fake: FakeS3) -> type[BaseHTTPRequestHandler]:
    class H(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format: str, *args: object) -> None:  # noqa: A002
            pass

        def _parse(self) -> tuple[str, dict[str, list[str]], str]:
            u = urllib.parse.urlsplit(self.path)
            parts = urllib.parse.unquote(u.path).lstrip("/").split("/", 1)
            key = parts[1] if len(parts) > 1 else ""
            return key, urllib.parse.parse_qs(u.query), u.query

        def _log(self, key: str, query: str, sent: int) -> None:
            with fake.lock:
                fake.hits.append(Hit(self.command, key, query, sent))

        def _send(self, code: int, body: bytes, headers: dict[str, str] | None = None, *, head: bool = False) -> None:
            self.send_response(code)
            for k, v in (headers or {}).items():
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if not head:
                self.wfile.write(body)

        def _object(self, head: bool) -> None:
            key, q, query = self._parse()
            if "list-type" in q:
                body = self._list(q).encode()
                self._log("", query, len(body))
                return self._send(200, body, {"Content-Type": "application/xml"})
            p = fake.root / key
            if not key or not p.is_file():
                self._log(key, query, 0)
                body = b"<Error><Code>NoSuchKey</Code></Error>"
                return self._send(404, body, {"Content-Type": "application/xml"}, head=head)
            data = p.read_bytes()
            st = p.stat()
            headers = {
                "Last-Modified": email.utils.formatdate(st.st_mtime, usegmt=True),
                "ETag": f'"{st.st_size:x}-{int(st.st_mtime)}"',
                "Accept-Ranges": "bytes",
                "Content-Type": "application/octet-stream",
            }
            rng = self.headers.get("Range")
            if rng and rng.startswith("bytes="):
                a, _, b = rng[6:].partition("-")
                start = int(a) if a else max(0, len(data) - int(b))
                end = int(b) if a and b else len(data) - 1
                chunk = data[start : end + 1]
                headers["Content-Range"] = f"bytes {start}-{start + len(chunk) - 1}/{len(data)}"
                self._log(key, "range", 0 if head else len(chunk))
                return self._send(206, chunk, headers, head=head)
            self._log(key, query, 0 if head else len(data))
            return self._send(200, data, headers, head=head)

        def _list(self, q: dict[str, list[str]]) -> str:
            prefix = q.get("prefix", [""])[0]
            after = q.get("start-after", [""])[0]
            delim = q.get("delimiter", [""])[0]
            keys = sorted(str(p.relative_to(fake.root)) for p in fake.root.rglob("*") if p.is_file())
            keys = [k for k in keys if k.startswith(prefix) and k > after]
            contents, prefixes = [], set()
            for k in keys:
                rest = k[len(prefix) :]
                if delim and delim in rest:
                    prefixes.add(prefix + rest.split(delim, 1)[0] + delim)
                else:
                    contents.append(k)
            xml = [
                '<?xml version="1.0" encoding="UTF-8"?>',
                '<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">',
                f"<Name>{BUCKET}</Name><Prefix>{escape(prefix)}</Prefix><IsTruncated>false</IsTruncated>",
            ]
            xml += [
                f"<Contents><Key>{escape(k)}</Key><Size>{(fake.root / k).stat().st_size}</Size></Contents>"
                for k in contents
            ]
            xml += [f"<CommonPrefixes><Prefix>{escape(p)}</Prefix></CommonPrefixes>" for p in sorted(prefixes)]
            xml.append("</ListBucketResult>")
            return "".join(xml)

        def do_GET(self) -> None:  # noqa: N802
            self._object(head=False)

        def do_HEAD(self) -> None:  # noqa: N802
            self._object(head=True)

    return H


@contextmanager
def fake_s3(root: pathlib.Path) -> Iterator[FakeS3]:
    fake = FakeS3(pathlib.Path(root))
    server = ThreadingHTTPServer(("127.0.0.1", 0), _handler(fake))
    fake.endpoint = f"http://127.0.0.1:{server.server_address[1]}"
    t = threading.Thread(target=server.serve_forever, daemon=True)
    t.start()
    try:
        yield fake
    finally:
        server.shutdown()
        server.server_close()
