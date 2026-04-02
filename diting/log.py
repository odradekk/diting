"""Logging configuration for the diting package."""

from __future__ import annotations

import logging
import pathlib
import threading

_LOG_FORMAT = "%(asctime)s [%(levelname)s] %(name)s: %(message)s"
_ROOT_NAME = "diting"
_HANDLER_NAME = "_diting_stream"
_FILE_HANDLER_NAME = "_diting_file"
_setup_lock = threading.Lock()


def setup_logging(level: str) -> None:
    """Configure the *diting* root logger.

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

    # Thread-safe idempotent handler installation keyed off handler name.
    with _setup_lock:
        for h in logger.handlers:
            if h.name == _HANDLER_NAME:
                return
        handler = logging.StreamHandler()
        handler.name = _HANDLER_NAME
        handler.setFormatter(logging.Formatter(_LOG_FORMAT))
        logger.addHandler(handler)

        # File handler — overwrites on each startup for easy debugging.
        log_path = pathlib.Path(__file__).resolve().parent / "data" / "tmp.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        file_handler = logging.FileHandler(log_path, mode="w", encoding="utf-8")
        file_handler.name = _FILE_HANDLER_NAME
        file_handler.setFormatter(logging.Formatter(_LOG_FORMAT))
        logger.addHandler(file_handler)


def get_logger(name: str) -> logging.Logger:
    """Return a child logger under the ``diting`` namespace.

    Example::

        logger = get_logger("modules.google")
        # logger.name == "diting.modules.google"

    Args:
        name: Dotted suffix appended to ``"diting"``.
    """
    return logging.getLogger(f"{_ROOT_NAME}.{name}")
