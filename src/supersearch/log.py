"""Logging configuration for the supersearch package."""

from __future__ import annotations

import logging
import threading

_LOG_FORMAT = "%(asctime)s [%(levelname)s] %(name)s: %(message)s"
_ROOT_NAME = "supersearch"
_HANDLER_NAME = "_supersearch_stream"
_setup_lock = threading.Lock()


def setup_logging(level: str) -> None:
    """Configure the *supersearch* root logger.

    Sets the log level and attaches a single :class:`logging.StreamHandler`
    with a consistent format.  Safe to call multiple times — duplicate
    handlers are never added, even under concurrent access.  The owned
    handler is identified by name so that external handlers on the same
    logger do not prevent installation.

    Args:
        level: One of ``"DEBUG"``, ``"INFO"``, ``"WARNING"``, ``"ERROR"``.
    """
    logger = logging.getLogger(_ROOT_NAME)
    logger.setLevel(level.upper())
    logger.propagate = False

    # Thread-safe idempotent handler installation keyed on handler name.
    with _setup_lock:
        for h in logger.handlers:
            if h.name == _HANDLER_NAME:
                return
        handler = logging.StreamHandler()
        handler.name = _HANDLER_NAME
        handler.setFormatter(logging.Formatter(_LOG_FORMAT))
        logger.addHandler(handler)


def get_logger(name: str) -> logging.Logger:
    """Return a child logger under the ``supersearch`` namespace.

    Example::

        logger = get_logger("modules.google")
        # logger.name == "supersearch.modules.google"

    Args:
        name: Dotted suffix appended to ``"supersearch"``.
    """
    return logging.getLogger(f"{_ROOT_NAME}.{name}")
