"""Tests for diting.log — logging setup and child logger factory."""

from __future__ import annotations

import logging
import re

import pytest

from diting.log import get_logger, setup_logging

# The root namespace all diting loggers live under.
_ROOT_NAME = "diting"


@pytest.fixture(autouse=True)
def _reset_diting_logger():
    """Ensure a clean diting logger for every test."""
    logger = logging.getLogger(_ROOT_NAME)
    original_level = logger.level
    original_propagate = logger.propagate
    original_handlers = logger.handlers[:]
    yield
    # Remove any handlers added during the test and close them.
    for handler in logger.handlers[:]:
        if handler not in original_handlers:
            logger.removeHandler(handler)
            handler.close()
    # Restore any original handlers that were removed.
    for handler in original_handlers:
        if handler not in logger.handlers:
            logger.addHandler(handler)
    logger.level = original_level
    logger.propagate = original_propagate


class TestGetLogger:
    """get_logger must return a child under the 'diting' namespace."""

    def test_get_logger_returns_child(self) -> None:
        child = get_logger("modules.google")
        assert child.name == "diting.modules.google"

    def test_get_logger_returns_logging_logger(self) -> None:
        child = get_logger("pipeline.scorer")
        assert isinstance(child, logging.Logger)


class TestSetupLogging:
    """setup_logging configures the diting root logger."""

    def test_setup_logging_sets_level(self) -> None:
        setup_logging("DEBUG")
        logger = logging.getLogger(_ROOT_NAME)
        assert logger.level == logging.DEBUG

    def test_setup_logging_default_info(self) -> None:
        setup_logging("INFO")
        logger = logging.getLogger(_ROOT_NAME)
        assert logger.level == logging.INFO

    def test_setup_logging_adds_handler(self) -> None:
        setup_logging("INFO")
        logger = logging.getLogger(_ROOT_NAME)
        assert len(logger.handlers) >= 1

    def test_setup_logging_idempotent(self) -> None:
        setup_logging("INFO")
        setup_logging("INFO")
        logger = logging.getLogger(_ROOT_NAME)
        assert len(logger.handlers) == 1

    def test_setup_logging_level_change(self) -> None:
        """Calling setup again with a different level updates the level."""
        setup_logging("INFO")
        setup_logging("DEBUG")
        logger = logging.getLogger(_ROOT_NAME)
        assert logger.level == logging.DEBUG
        # Still only one handler even after two calls.
        assert len(logger.handlers) == 1


class TestLogOutputFormat:
    """Log output must match: %(asctime)s [%(levelname)s] %(name)s: %(message)s"""

    def test_log_output_format(self, capfd: pytest.CaptureFixture[str]) -> None:
        setup_logging("DEBUG")
        logger = get_logger("test.format")
        logger.debug("hello world")

        captured = capfd.readouterr()
        line = captured.err.strip()

        # Expect exactly one log line matching the full format.
        pattern = (
            r"\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3}"  # asctime
            r" \[DEBUG\]"                                     # level
            r" diting\.test\.format:"                    # name
            r" hello world"                                   # message
        )
        assert re.fullmatch(pattern, line), f"Log line did not match expected format: {line!r}"


class TestIsolation:
    """diting logger must not pollute the root or other loggers."""

    def test_does_not_propagate(self) -> None:
        setup_logging("DEBUG")
        logger = logging.getLogger(_ROOT_NAME)
        assert logger.propagate is False

    def test_other_loggers_unaffected(self) -> None:
        other = logging.getLogger("some_other_library")
        level_before = other.level
        handlers_before = other.handlers[:]
        propagate_before = other.propagate

        setup_logging("DEBUG")

        assert other.level == level_before
        assert other.handlers == handlers_before
        assert other.propagate == propagate_before
