"""A transaction's channel and a model's family, as the request rows and the drawer name them
(handoff §5.2.7, §11L.6)."""

import re


def kind_of(path: str) -> str:
    p = path.split("?", 1)[0]
    # Codex's channels (handoff §11L.6): model calls, analytics events, OTel metrics,
    # usage & account (wham/*, accounts), catalog (plugins, models).
    if p == "/backend-api/codex/responses":
        return "model"
    if p.startswith("/backend-api/codex/analytics-events"):
        return "analytics"
    if p.startswith("/otlp/"):
        return "metrics"
    if p.startswith(("/backend-api/wham/", "/backend-api/accounts/")):
        return "usage"
    if p.startswith(("/backend-api/ps/", "/backend-api/plugins/", "/backend-api/codex/models")):
        return "catalog"
    if p.startswith("/v1/messages/count_tokens"):
        return "count"
    if p.startswith("/v1/messages"):
        return "model"
    if p.startswith("/api/event_logging"):
        return "analytics"
    if p.startswith("/api/eval/"):
        return "flags"
    return "account"


def model_family(model: str | None) -> str:
    if not model:
        return "—"
    m = re.match(r"claude-(opus|sonnet|haiku)", model)
    return m.group(1) if m else model
