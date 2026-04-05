"""Logging configuration for the diting package.

Text format (default) is human-friendly for terminal reading. JSON format
(``LOG_FORMAT=json``) emits one line per record with structured fields,
which makes it feasible to correlate events across a search request:

* ``query_id``  — stable per search request
* ``round``     — search iteration (1, 2, 3...)
* ``phase``     — logical stage (search_start, scoring, evaluation, ...)
* ``latency_ms``, ``success``, ``score``, ``engine`` — phase-specific

``engine`` is used instead of ``module`` because Python's ``LogRecord``
already defines ``module`` (the calling file's basename) and refuses to
let callers overwrite it via ``extra=``.
"""

from __future__ import annotations

import json
import logging
import pathlib
import threading
from collections.abc import MutableMapping
from typing import Any

_TEXT_FORMAT = "%(asctime)s [%(levelname)s] %(name)s: %(message)s"
_ROOT_NAME = "diting"
_HANDLER_NAME = "_diting_stream"
_FILE_HANDLER_NAME = "_diting_file"
_setup_lock = threading.Lock()

# Fields the stdlib populates on every LogRecord — we don't want to echo
# these as "extras" in JSON output, they are either already projected into
# the top-level output or are internal bookkeeping.
_STD_LOGRECORD_FIELDS: frozenset[str] = frozenset({
    "name", "msg", "args", "levelname", "levelno", "pathname",
    "filename", "module", "exc_info", "exc_text", "stack_info",
    "lineno", "funcName", "created", "msecs", "relativeCreated",
    "thread", "threadName", "processName", "process", "message",
    "asctime", "taskName",
})


class JsonFormatter(logging.Formatter):
    """One-line JSON record with level, logger, message, and any extras."""

    def format(self, record: logging.LogRecord) -> str:
        obj: dict[str, Any] = {
            "ts": self.formatTime(record, self.datefmt),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        for key, value in record.__dict__.items():
            if key not in _STD_LOGRECORD_FIELDS and not key.startswith("_"):
                obj[key] = value
        if record.exc_info:
            obj["exc"] = self.formatException(record.exc_info)
        return json.dumps(obj, ensure_ascii=False, default=str)


class ContextLogger(logging.LoggerAdapter):
    """LoggerAdapter that *merges* adapter context with call-site extras.

    The stock :class:`logging.LoggerAdapter` overwrites ``kwargs['extra']``
    wholesale, which makes nested scopes (request → round → phase) lose
    their outer context. This adapter merges instead, letting call-site
    extras override the adapter's context on key conflicts.
    """

    def process(
        self, msg: Any, kwargs: MutableMapping[str, Any],
    ) -> tuple[Any, MutableMapping[str, Any]]:
        merged: dict[str, Any] = dict(self.extra) if self.extra else {}
        caller_extra = kwargs.get("extra")
        if caller_extra:
            merged.update(caller_extra)
        kwargs["extra"] = merged
        return msg, kwargs

    def with_context(self, **fields: Any) -> ContextLogger:
        """Return a new adapter layering *fields* on top of this context."""
        merged: dict[str, Any] = dict(self.extra) if self.extra else {}
        merged.update(fields)
        return ContextLogger(self.logger, merged)


def _make_formatter(fmt: str) -> logging.Formatter:
    return JsonFormatter() if fmt.lower() == "json" else logging.Formatter(_TEXT_FORMAT)


def setup_logging(level: str, fmt: str = "text") -> None:
    """Configure the *diting* root logger.

    Sets the log level and attaches a single :class:`logging.StreamHandler`
    (plus a best-effort :class:`FileHandler`) with a consistent formatter.
    Safe to call multiple times — duplicate handlers are never added, even
    under concurrent access. Subsequent calls update the level and
    formatter on existing handlers rather than re-creating them.

    Args:
        level: One of ``"DEBUG"``, ``"INFO"``, ``"WARNING"``, ``"ERROR"``.
        fmt:   ``"text"`` (default, human-friendly) or ``"json"``.
    """
    logger = logging.getLogger(_ROOT_NAME)
    logger.setLevel(level.upper())
    logger.propagate = False

    formatter = _make_formatter(fmt)

    with _setup_lock:
        stream_handler = next(
            (h for h in logger.handlers if h.name == _HANDLER_NAME), None,
        )
        if stream_handler is None:
            stream_handler = logging.StreamHandler()
            stream_handler.name = _HANDLER_NAME
            logger.addHandler(stream_handler)
        stream_handler.setFormatter(formatter)

        file_handler = next(
            (h for h in logger.handlers if h.name == _FILE_HANDLER_NAME), None,
        )
        if file_handler is None:
            # Best-effort file handler — skip silently on read-only installs.
            try:
                log_path = pathlib.Path(__file__).resolve().parent / "data" / "tmp.log"
                log_path.parent.mkdir(parents=True, exist_ok=True)
                file_handler = logging.FileHandler(log_path, mode="w", encoding="utf-8")
                file_handler.name = _FILE_HANDLER_NAME
                logger.addHandler(file_handler)
            except OSError:
                file_handler = None
        if file_handler is not None:
            file_handler.setFormatter(formatter)


def get_logger(name: str) -> logging.Logger:
    """Return a child logger under the ``diting`` namespace.

    Example::

        logger = get_logger("modules.google")
        # logger.name == "diting.modules.google"

    Args:
        name: Dotted suffix appended to ``"diting"``.
    """
    return logging.getLogger(f"{_ROOT_NAME}.{name}")
