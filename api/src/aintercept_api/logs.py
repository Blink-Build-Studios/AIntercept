"""Structured logging: one JSON object per line, with the same top-level keys as
the observer's slog JSON handler (`time`, `level`, `msg`), then the record's
fields. Pass fields with `extra={"fields": {...}}`."""

import json
import logging
from datetime import UTC, datetime

# Attributes every LogRecord has; anything else set via `extra` is a field too.
_STANDARD = set(logging.makeLogRecord({}).__dict__) | {"message", "asctime", "taskName", "color_message"}


_RESERVED = {"time", "level", "msg", "logger", "error"}


class JSONFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        out = {
            "time": datetime.fromtimestamp(record.created, UTC).isoformat(timespec="milliseconds"),
            "level": record.levelname,
            "msg": record.getMessage(),
            "logger": record.name,
        }
        extra = dict(getattr(record, "fields", {}) or {})
        extra.update({k: v for k, v in record.__dict__.items() if k not in _STANDARD and k != "fields"})
        for k, v in extra.items():
            # A field never overwrites time/level/msg/logger/error: it gets a suffix.
            out[f"{k}_" if k in _RESERVED else k] = v
        if record.exc_info:
            out["error"] = self.formatException(record.exc_info)
        return json.dumps(out, default=str)


# For `uvicorn --log-config`: uvicorn's own loggers use the same formatter, and
# its access log is off because the request middleware logs every request.
UVICORN_LOG_CONFIG = {
    "version": 1,
    "disable_existing_loggers": False,
    "formatters": {"json": {"()": "aintercept_api.logs.JSONFormatter"}},
    "handlers": {"stdout": {"class": "logging.StreamHandler", "formatter": "json", "stream": "ext://sys.stdout"}},
    "loggers": {
        "uvicorn": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
        "uvicorn.error": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
        "uvicorn.access": {"handlers": [], "level": "WARNING", "propagate": False},
        "aintercept_api": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
    },
}
