"""Tests for diting.log — logging setup and child logger factory."""

from __future__ import annotations

import json
import logging
import re

import pytest

from diting.log import ContextLogger, JsonFormatter, get_logger, setup_logging

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
        handler_count = len(logging.getLogger(_ROOT_NAME).handlers)
        setup_logging("INFO")
        logger = logging.getLogger(_ROOT_NAME)
        assert len(logger.handlers) == handler_count

    def test_setup_logging_level_change(self) -> None:
        """Calling setup again with a different level updates the level."""
        setup_logging("INFO")
        setup_logging("DEBUG")
        logger = logging.getLogger(_ROOT_NAME)
        assert logger.level == logging.DEBUG
        # Handler count unchanged after second call.
        assert len(logger.handlers) >= 1


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


class TestJsonFormat:
    """LOG_FORMAT=json emits one JSON object per log line."""

    def test_json_output_contains_standard_fields(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        logger = get_logger("test.json")
        logger.info("hello %s", "world")

        line = capfd.readouterr().err.strip()
        obj = json.loads(line)

        assert obj["level"] == "INFO"
        assert obj["logger"] == "diting.test.json"
        assert obj["msg"] == "hello world"
        assert "ts" in obj

    def test_json_output_includes_extras(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        logger = get_logger("test.extras")
        logger.info(
            "round event", extra={"phase": "round_end", "round": 2, "latency_ms": 42},
        )

        line = capfd.readouterr().err.strip()
        obj = json.loads(line)

        assert obj["phase"] == "round_end"
        assert obj["round"] == 2
        assert obj["latency_ms"] == 42
        assert obj["msg"] == "round event"

    def test_json_output_omits_stdlib_record_noise(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        logger = get_logger("test.clean")
        logger.info("clean record")

        line = capfd.readouterr().err.strip()
        obj = json.loads(line)

        # These stdlib LogRecord attributes must not leak into the payload.
        for noise in ("args", "levelno", "pathname", "filename", "funcName",
                      "created", "msecs", "process", "thread"):
            assert noise not in obj

    def test_setup_switches_format_on_recall(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        """Calling setup again with a different fmt updates the formatter."""
        setup_logging("INFO", fmt="text")
        setup_logging("INFO", fmt="json")
        logger = get_logger("test.switch")
        logger.info("after switch")

        line = capfd.readouterr().err.strip()
        # Must parse as JSON — text format would not.
        obj = json.loads(line)
        assert obj["msg"] == "after switch"


class TestJsonFormatterDirect:
    """Unit tests for JsonFormatter.format()."""

    def test_formats_plain_record(self) -> None:
        fmt = JsonFormatter()
        record = logging.LogRecord(
            name="diting.x", level=logging.WARNING, pathname=__file__, lineno=1,
            msg="something %s", args=("happened",), exc_info=None,
        )
        obj = json.loads(fmt.format(record))
        assert obj["msg"] == "something happened"
        assert obj["level"] == "WARNING"

    def test_formats_exception(self) -> None:
        fmt = JsonFormatter()
        try:
            raise ValueError("boom")
        except ValueError:
            import sys
            exc_info = sys.exc_info()
        record = logging.LogRecord(
            name="diting.x", level=logging.ERROR, pathname=__file__, lineno=1,
            msg="oops", args=(), exc_info=exc_info,
        )
        obj = json.loads(fmt.format(record))
        assert "exc" in obj
        assert "ValueError: boom" in obj["exc"]


class TestContextLogger:
    """ContextLogger merges adapter context with call-site extras."""

    def test_context_fields_flow_to_record(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        base = get_logger("test.ctx")
        ctx = ContextLogger(base, {"query_id": "abc123"})
        ctx.info("first event", extra={"phase": "start"})

        obj = json.loads(capfd.readouterr().err.strip())
        assert obj["query_id"] == "abc123"
        assert obj["phase"] == "start"

    def test_with_context_layers_fields(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        base = get_logger("test.layer")
        ctx = ContextLogger(base, {"query_id": "xyz"})
        round_ctx = ctx.with_context(round=2)
        round_ctx.info("round event", extra={"phase": "round_end"})

        obj = json.loads(capfd.readouterr().err.strip())
        assert obj["query_id"] == "xyz"
        assert obj["round"] == 2
        assert obj["phase"] == "round_end"

    def test_call_site_extras_win_on_conflict(
        self, capfd: pytest.CaptureFixture[str],
    ) -> None:
        setup_logging("DEBUG", fmt="json")
        base = get_logger("test.conflict")
        ctx = ContextLogger(base, {"phase": "outer"})
        ctx.info("inner event", extra={"phase": "inner"})

        obj = json.loads(capfd.readouterr().err.strip())
        assert obj["phase"] == "inner"

    def test_with_context_does_not_mutate_parent(self) -> None:
        base = get_logger("test.immut")
        parent = ContextLogger(base, {"query_id": "q1"})
        child = parent.with_context(round=1)
        assert parent.extra == {"query_id": "q1"}
        assert child.extra == {"query_id": "q1", "round": 1}


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
