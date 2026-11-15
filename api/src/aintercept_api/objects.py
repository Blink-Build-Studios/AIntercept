"""Where the drawer reads a call's raw member from (docs/PIPELINE.md §2.1, §6.2): the observers' raw
files, in a local directory (`make up`, tests) or an S3-compatible bucket. One ranged GET per call."""

import datetime as dt
import hashlib
import hmac
import os
import pathlib
import urllib.parse
import urllib.request
from typing import Protocol, cast


class Source(Protocol):
    def get_range(self, key: str, offset: int, length: int) -> bytes:
        """length bytes of the object from offset (one ranged GET): a call's gzip member in its raw
        file (docs/PIPELINE.md §2.1)."""
        ...


class LocalSource:
    """A directory laid out like the bucket (tests, a local stack)."""

    def __init__(self, root: pathlib.Path) -> None:
        self.root = pathlib.Path(root)

    def get_range(self, key: str, offset: int, length: int) -> bytes:
        with (self.root / key).open("rb") as f:
            f.seek(offset)
            return f.read(length)


class S3Source:
    """An S3-compatible bucket, read with SigV4-signed GETs (no SDK: one call, GetObject).
    Credentials from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (or the arguments); endpoint for
    S3-compatible stores (path style)."""

    def __init__(
        self,
        bucket: str,
        region: str,
        *,
        endpoint: str | None = None,
        access_key: str | None = None,
        secret_key: str | None = None,
        session_token: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        self.bucket, self.region, self.timeout = bucket, region, timeout
        self.endpoint = (endpoint or f"https://s3.{region}.amazonaws.com").rstrip("/")
        self.access_key = access_key or os.environ.get("AWS_ACCESS_KEY_ID", "")
        self.secret_key = secret_key or os.environ.get("AWS_SECRET_ACCESS_KEY", "")
        self.session_token = session_token or os.environ.get("AWS_SESSION_TOKEN")

    def get_range(self, key: str, offset: int, length: int) -> bytes:
        # Range isn't signed (SigV4 signs host and the x-amz-* headers here), as S3 allows.
        path = "/" + self.bucket + "/" + urllib.parse.quote(key, safe="/~")
        return self._request(path, {}, {"Range": f"bytes={offset}-{offset + length - 1}"})

    def _request(self, path: str, query: dict[str, str], extra: dict[str, str] | None = None) -> bytes:
        now = dt.datetime.now(dt.UTC)
        amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
        host = urllib.parse.urlparse(self.endpoint).netloc
        qs = "&".join(
            f"{urllib.parse.quote(k, safe='~')}={urllib.parse.quote(v, safe='~')}" for k, v in sorted(query.items())
        )
        payload = hashlib.sha256(b"").hexdigest()
        headers = {"host": host, "x-amz-content-sha256": payload, "x-amz-date": amz}
        if self.session_token:
            headers["x-amz-security-token"] = self.session_token
        signed = ";".join(sorted(headers))
        canonical = "\n".join(["GET", path, qs, *(f"{k}:{headers[k]}" for k in sorted(headers)), "", signed, payload])
        scope = f"{day}/{self.region}/s3/aws4_request"
        to_sign = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canonical.encode()).hexdigest()])
        k = ("AWS4" + self.secret_key).encode()
        for part in (day, self.region, "s3", "aws4_request"):
            k = hmac.new(k, part.encode(), hashlib.sha256).digest()
        sig = hmac.new(k, to_sign.encode(), hashlib.sha256).hexdigest()
        auth = f"AWS4-HMAC-SHA256 Credential={self.access_key}/{scope}, SignedHeaders={signed}, Signature={sig}"
        url = self.endpoint + path + ("?" + qs if qs else "")
        if not url.startswith(("https://", "http://")):
            raise ValueError(f"S3 endpoint {self.endpoint!r}: use an http(s) URL")
        req = urllib.request.Request(url, headers={**headers, **(extra or {}), "Authorization": auth})  # noqa: S310 (checked above)
        with urllib.request.urlopen(req, timeout=self.timeout) as r:  # noqa: S310
            return cast(bytes, r.read())
