"""Serving the built UI from the API (one origin with /api/v1), and the optional
identity-aware-proxy assertion check (aintercept_api.auth)."""

import json
import logging
import time

import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric import rsa
from fastapi.testclient import TestClient

from aintercept_api.app import create_app
from aintercept_api.auth import AuthConfig, Verifier
from tests.conftest import FROZEN_NOW, FROZEN_QUERY_ID, bounded_store

AUDIENCE = "aud-123"
ISSUER = "https://team.example-access.com"
HEADER = "Cf-Access-Jwt-Assertion"


@pytest.fixture
def store(dataset):
    root = dataset.write()
    root.mkdir(parents=True, exist_ok=True)
    s = bounded_store(root)
    yield s
    s.close()


def app_client(store, **kwargs) -> TestClient:
    app = create_app(
        store=store, clock=lambda: FROZEN_NOW, query_id=lambda: FROZEN_QUERY_ID, ingest_interval_s=0, **kwargs
    )
    return TestClient(app)


# ── The UI ────────────────────────────────────────────────────────────────────


@pytest.fixture
def ui_dir(tmp_path):
    d = tmp_path / "dist"
    (d / "assets").mkdir(parents=True)
    (d / "index.html").write_text("<!doctype html><title>AIntercept</title>")
    (d / "assets" / "index-abc123.js").write_text("console.log('ui')")
    (d / "favicon.svg").write_text("<svg/>")
    (tmp_path / "secret.txt").write_text("outside the UI")
    return d


def test_serves_the_ui_beside_the_api(store, ui_dir):
    c = app_client(store, ui_dir=ui_dir)
    r = c.get("/")
    assert r.status_code == 200 and "AIntercept" in r.text and r.headers["cache-control"] == "no-cache"
    r = c.get("/assets/index-abc123.js")
    assert r.status_code == 200 and "console.log" in r.text and "immutable" in r.headers["cache-control"]
    assert c.get("/favicon.svg").text == "<svg/>"
    # Client-side routes get index.html.
    r = c.get("/people/p_1/sessions/s_1")
    assert r.status_code == 200 and "AIntercept" in r.text
    # The API is unchanged, and an unknown API path is a JSON 404, never the UI.
    assert c.get("/api/v1/status").json()["data"]["lastIngestAt"] is None
    r = c.get("/api/v1/nope")
    assert r.status_code == 404 and r.json()["error"]["code"] == "http_404"
    assert c.get("/api").status_code == 404


def test_ui_never_serves_files_outside_its_directory(store, ui_dir):
    c = app_client(store, ui_dir=ui_dir)
    for path in ("/../secret.txt", "/%2e%2e/secret.txt", "/assets/../../secret.txt"):
        r = c.get(path)
        assert "outside the UI" not in r.text, path


def test_ui_is_off_without_a_directory(store):
    c = app_client(store)
    assert c.get("/").status_code == 404
    assert c.get("/api/v1/status").status_code == 200


def test_ui_directory_must_have_an_index(store, tmp_path):
    with pytest.raises(ValueError, match="index.html"):
        app_client(store, ui_dir=tmp_path)


def test_ui_routes_stay_out_of_the_contract(store, ui_dir):
    app = create_app(store=store, ui_dir=ui_dir)
    assert all(p.startswith("/api/v1/") for p in app.openapi()["paths"])


# ── Identity assertions ───────────────────────────────────────────────────────


class FakeJWKS:
    """Stands in for PyJWKClient: the proxy's public key, or an outage."""

    def __init__(self, key, down=False):
        self.key, self.down = key, down

    def get_signing_key_from_jwt(self, token):
        if self.down:
            raise jwt.PyJWKClientConnectionError("connection refused")
        return jwt.PyJWK.from_dict(json.loads(jwt.algorithms.RSAAlgorithm.to_jwk(self.key.public_key())), "RS256")


@pytest.fixture(scope="module")
def keys():
    return rsa.generate_private_key(public_exponent=65537, key_size=2048), rsa.generate_private_key(
        public_exponent=65537, key_size=2048
    )


def token(key, **claims):
    now = int(time.time())
    body = {"aud": AUDIENCE, "iss": ISSUER, "exp": now + 300, "iat": now, "email": "ana@example.com", "sub": "u1"}
    body.update(claims)
    return jwt.encode({k: v for k, v in body.items() if v is not None}, key, algorithm="RS256")


def auth_client(store, keys, down=False):
    cfg = AuthConfig(header=HEADER, jwks_url="https://unused.example/certs", audience=AUDIENCE, issuer=ISSUER)
    return app_client(store, auth=Verifier(cfg, jwks=FakeJWKS(keys[0], down=down)))


def test_valid_assertion_is_served_and_logged(store, keys, caplog):
    c = auth_client(store, keys)
    with caplog.at_level(logging.INFO, logger="aintercept_api"):
        r = c.get("/api/v1/people", headers={HEADER: token(keys[0])})
    assert r.status_code == 200
    logged = [getattr(rec, "fields", {}) for rec in caplog.records if rec.getMessage() == "request"]
    assert logged and logged[-1].get("identity") == "ana@example.com"


@pytest.mark.parametrize(
    "claims, why",
    [
        ({"exp": int(time.time()) - 120}, "expired"),
        ({"aud": "someone-else"}, "audience"),
        ({"iss": "https://evil.example"}, "issuer"),
        ({"aud": None}, "no audience"),
    ],
)
def test_invalid_assertions_get_401(store, keys, claims, why):
    c = auth_client(store, keys)
    r = c.get("/api/v1/people", headers={HEADER: token(keys[0], **claims)})
    assert r.status_code == 401, why
    assert r.json()["error"]["code"] == "unauthenticated"


def test_missing_or_forged_assertion_gets_401(store, keys, ui_dir):
    c = auth_client(store, keys)
    assert c.get("/api/v1/people").status_code == 401
    assert c.get("/api/v1/people", headers={HEADER: "not-a-jwt"}).status_code == 401
    # Signed by a different key than the proxy's.
    assert c.get("/api/v1/people", headers={HEADER: token(keys[1])}).status_code == 401


def test_bearer_prefix_is_accepted(store, keys):
    c = auth_client(store, keys)
    assert c.get("/api/v1/people", headers={HEADER: "Bearer " + token(keys[0])}).status_code == 200


def test_status_stays_open_for_probes(store, keys):
    assert auth_client(store, keys).get("/api/v1/status").status_code == 200


def test_ui_is_behind_the_assertion_too(store, keys, ui_dir):
    cfg = AuthConfig(header=HEADER, jwks_url="https://unused.example/certs", audience=AUDIENCE, issuer=ISSUER)
    c = app_client(store, auth=Verifier(cfg, jwks=FakeJWKS(keys[0])), ui_dir=ui_dir)
    assert c.get("/").status_code == 401
    assert c.get("/", headers={HEADER: token(keys[0])}).status_code == 200


def test_unreachable_keys_fail_closed(store, keys):
    r = auth_client(store, keys, down=True).get("/api/v1/people", headers={HEADER: token(keys[0])})
    assert r.status_code == 503 and r.json()["error"]["code"] == "identity_unavailable"


def test_config_from_env():
    assert AuthConfig.from_env({}) is None
    cfg = AuthConfig.from_env(
        {
            "AINTERCEPT_AUTH_JWT_HEADER": HEADER,
            "AINTERCEPT_AUTH_JWKS_URL": "https://x/certs",
            "AINTERCEPT_AUTH_AUDIENCE": AUDIENCE,
        }
    )
    assert cfg is not None and cfg.issuer is None and cfg.header == HEADER
    with pytest.raises(ValueError, match="AINTERCEPT_AUTH_AUDIENCE"):
        AuthConfig.from_env({"AINTERCEPT_AUTH_JWT_HEADER": HEADER, "AINTERCEPT_AUTH_JWKS_URL": "https://x/certs"})


def test_without_auth_everything_is_open(store):
    assert app_client(store).get("/api/v1/people").status_code == 200
