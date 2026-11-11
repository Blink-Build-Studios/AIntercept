"""Optional verification of an identity-aware proxy's signed assertion.

Behind Cloudflare Access, Google IAP, oauth2-proxy and the like, the proxy
authenticates people and forwards each request with a signed JWT in a header
(`Cf-Access-Jwt-Assertion`, `X-Goog-IAP-JWT-Assertion`, …). With
AINTERCEPT_AUTH_* set, the API checks that JWT on every request (signature
against the proxy's JWKS, audience, issuer, expiry), so a misconfigured or
bypassed proxy doesn't hand everyone on the network the per-person data. It's
defense in depth: the API has no login of its own.

Off unless AINTERCEPT_AUTH_JWT_HEADER is set. /api/v1/status stays open for
probes.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, Protocol

import jwt

# Asymmetric algorithms only: the proxy signs, the API only verifies.
ALGORITHMS = ["RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"]

# Paths served without an assertion: kubelet probes reach the pod directly.
OPEN_PATHS = frozenset({"/api/v1/status"})


@dataclass(frozen=True)
class AuthConfig:
    header: str
    jwks_url: str
    audience: str
    issuer: str | None

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> AuthConfig | None:
        """The configuration, or None when verification is off. A partial
        configuration is an error: failing to start beats serving unchecked."""
        header = env.get("AINTERCEPT_AUTH_JWT_HEADER", "").strip()
        jwks_url = env.get("AINTERCEPT_AUTH_JWKS_URL", "").strip()
        audience = env.get("AINTERCEPT_AUTH_AUDIENCE", "").strip()
        issuer = env.get("AINTERCEPT_AUTH_ISSUER", "").strip() or None
        if not (header or jwks_url or audience or issuer):
            return None
        missing = [n for n, v in (("JWT_HEADER", header), ("JWKS_URL", jwks_url), ("AUDIENCE", audience)) if not v]
        if missing:
            names = ", ".join(f"AINTERCEPT_AUTH_{m}" for m in missing)
            raise ValueError(f"AINTERCEPT_AUTH_* is partly set; also set {names}")
        return cls(header=header, jwks_url=jwks_url, audience=audience, issuer=issuer)


class AuthError(Exception):
    """The request's assertion is missing or invalid (401)."""


class KeysUnavailable(Exception):
    """The proxy's signing keys couldn't be fetched (503: nothing can be verified)."""


class SigningKeys(Protocol):
    """Where verification keys come from: the proxy's JWKS (PyJWKClient)."""

    def get_signing_key_from_jwt(self, token: str) -> jwt.PyJWK: ...


class Verifier:
    def __init__(self, config: AuthConfig, jwks: SigningKeys | None = None) -> None:
        self.config = config
        # Keys are cached and refetched when a token names a key id not seen yet.
        self.jwks: SigningKeys = jwks or jwt.PyJWKClient(config.jwks_url, cache_keys=True, lifespan=600, timeout=5)

    def verify(self, headers: Mapping[str, str]) -> dict[str, Any]:
        """The verified claims of the request's assertion. Blocking (may fetch
        the JWKS): call it off the event loop."""
        token = headers.get(self.config.header.lower(), "")
        if token.lower().startswith("bearer "):
            token = token[7:]
        if not token:
            raise AuthError(f"missing {self.config.header}")
        try:
            key = self.jwks.get_signing_key_from_jwt(token)
        except jwt.PyJWKClientConnectionError as e:
            raise KeysUnavailable(str(e)) from e
        except (jwt.PyJWKClientError, jwt.InvalidTokenError) as e:
            raise AuthError(str(e)) from e
        try:
            claims: dict[str, Any] = jwt.decode(
                token,
                key.key,
                algorithms=ALGORITHMS,
                audience=self.config.audience,
                issuer=self.config.issuer,
                options={"require": ["exp", "aud"] + (["iss"] if self.config.issuer else [])},
                leeway=30,
            )
        except jwt.InvalidTokenError as e:
            raise AuthError(str(e)) from e
        return claims


def identity(claims: Mapping[str, Any]) -> str:
    """Who the assertion names, for the request log: email, else subject."""
    return str(claims.get("email") or claims.get("sub") or "")


def verifier_from_env(env: Mapping[str, str] | None = None) -> Verifier | None:
    config = AuthConfig.from_env(os.environ if env is None else env)
    return Verifier(config) if config else None
