"""Rules every endpoint follows, so the UI contract stays generatable."""

from aintercept_api.app import create_app
from aintercept_api.openapi import NoStore


def test_every_response_is_a_named_model():
    """200s are Envelope models named *Response; every route declares the typed
    500 error. Bare lists or dicts would leave spec.ts without a named type."""
    spec = create_app(store=NoStore()).openapi()
    schemas = spec["components"]["schemas"]
    for path, ops in spec["paths"].items():
        for method, op in ops.items():
            ok = op["responses"]["200"]["content"]["application/json"]["schema"]
            name = ok.get("$ref", "").rsplit("/", 1)[-1]
            assert name.endswith("Response"), f"{method.upper()} {path}: 200 must be a *Response model, got {ok}"
            assert {"data", "meta"} <= set(schemas[name]["properties"]), f"{name} must extend Envelope"
            err = op["responses"]["500"]["content"]["application/json"]["schema"]["$ref"]
            assert err.endswith("/ErrorResponse"), f"{method.upper()} {path}: 500 must be ErrorResponse"
