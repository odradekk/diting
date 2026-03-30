"""Tests for diting.modules.base — BaseSearchModule ABC."""

from __future__ import annotations

import asyncio
import logging

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.base import BaseSearchModule


# ---------------------------------------------------------------------------
# Concrete test subclass
# ---------------------------------------------------------------------------


class StubSearchModule(BaseSearchModule):
    """Concrete subclass for testing the abstract base class."""

    def __init__(
        self,
        name: str = "stub",
        timeout: int = 5,
        *,
        results: list[SearchResult] | None = None,
        exception: Exception | None = None,
        delay: float = 0.0,
    ) -> None:
        super().__init__(name, timeout)
        self._results = results or []
        self._exception = exception
        self._delay = delay

    async def _execute(self, query: str) -> list[SearchResult]:
        if self._delay:
            await asyncio.sleep(self._delay)
        if self._exception is not None:
            raise self._exception
        return self._results


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_results(count: int = 2) -> list[SearchResult]:
    """Build a list of sample SearchResult objects."""
    return [
        SearchResult(
            title=f"Result {i}",
            url=f"https://example.com/{i}",
            snippet=f"Snippet for result {i}.",
        )
        for i in range(count)
    ]


# ---------------------------------------------------------------------------
# Name property
# ---------------------------------------------------------------------------


class TestNameProperty:
    def test_name_returns_constructor_value(self) -> None:
        module = StubSearchModule(name="brave")
        assert module.name == "brave"

    def test_timeout_returns_constructor_value(self) -> None:
        module = StubSearchModule(timeout=42)
        assert module.timeout == 42


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_module_output_with_results(self) -> None:
        results = _make_results(3)
        module = StubSearchModule(results=results)

        output = await module.search("test query")

        assert isinstance(output, ModuleOutput)
        assert output.module == "stub"
        assert output.error is None
        assert len(output.results) == 3

    async def test_results_are_search_result_instances(self) -> None:
        results = _make_results(2)
        module = StubSearchModule(results=results)

        output = await module.search("test query")

        for r in output.results:
            assert isinstance(r, SearchResult)
        assert output.results[0].title == "Result 0"
        assert output.results[1].url == "https://example.com/1"

    async def test_empty_results_are_valid(self) -> None:
        module = StubSearchModule(results=[])

        output = await module.search("no results query")

        assert output.results == []
        assert output.error is None


# ---------------------------------------------------------------------------
# Timeout handling
# ---------------------------------------------------------------------------


class TestTimeoutHandling:
    async def test_timeout_returns_timeout_error(self) -> None:
        module = StubSearchModule(timeout=1, delay=10.0)

        output = await module.search("slow query")

        assert output.module == "stub"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "TIMEOUT"
        assert output.error.retryable is True

    async def test_timeout_error_message_contains_module_name(self) -> None:
        module = StubSearchModule(name="brave", timeout=1, delay=10.0)

        output = await module.search("slow query")

        assert "brave" in output.error.message

    async def test_timeout_uses_configured_value(self) -> None:
        """A fast response within the timeout window succeeds normally."""
        module = StubSearchModule(timeout=5, delay=0.01, results=_make_results(1))

        output = await module.search("fast query")

        assert output.error is None
        assert len(output.results) == 1


# ---------------------------------------------------------------------------
# Generic exception handling
# ---------------------------------------------------------------------------


class TestExceptionHandling:
    async def test_generic_exception_returns_error(self) -> None:
        module = StubSearchModule(exception=RuntimeError("connection refused"))

        output = await module.search("failing query")

        assert output.module == "stub"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        assert output.error.retryable is False

    async def test_error_message_contains_exception_text(self) -> None:
        module = StubSearchModule(exception=ValueError("bad API key"))

        output = await module.search("query")

        assert "bad API key" in output.error.message

    async def test_keyboard_interrupt_is_not_caught(self) -> None:
        """BaseException subclasses other than Exception should propagate."""
        module = StubSearchModule(exception=KeyboardInterrupt())

        with pytest.raises(KeyboardInterrupt):
            await module.search("query")


# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------


class TestLogging:
    async def test_logs_debug_on_start(self, caplog: pytest.LogCaptureFixture) -> None:
        module = StubSearchModule(results=_make_results(1))

        with caplog.at_level(logging.DEBUG, logger="diting.modules.stub"):
            await module.search("hello")

        debug_messages = [r for r in caplog.records if r.levelno == logging.DEBUG]
        assert any("hello" in r.message for r in debug_messages)

    async def test_logs_info_on_success(self, caplog: pytest.LogCaptureFixture) -> None:
        module = StubSearchModule(results=_make_results(2))

        with caplog.at_level(logging.DEBUG, logger="diting.modules.stub"):
            await module.search("hello")

        info_messages = [r for r in caplog.records if r.levelno == logging.INFO]
        assert any("2" in r.message for r in info_messages)

    async def test_logs_warning_on_timeout(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        module = StubSearchModule(timeout=1, delay=10.0)

        with caplog.at_level(logging.DEBUG, logger="diting.modules.stub"):
            await module.search("slow")

        warning_messages = [r for r in caplog.records if r.levelno == logging.WARNING]
        assert len(warning_messages) >= 1
        assert any("timed out" in r.message.lower() for r in warning_messages)

    async def test_logs_warning_on_exception(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        module = StubSearchModule(exception=RuntimeError("boom"))

        with caplog.at_level(logging.DEBUG, logger="diting.modules.stub"):
            await module.search("query")

        warning_messages = [r for r in caplog.records if r.levelno == logging.WARNING]
        assert len(warning_messages) >= 1
        assert any("boom" in r.message for r in warning_messages)


# ---------------------------------------------------------------------------
# ABC enforcement
# ---------------------------------------------------------------------------


class TestABCEnforcement:
    def test_cannot_instantiate_base_class_directly(self) -> None:
        with pytest.raises(TypeError):
            BaseSearchModule("test", 10)  # type: ignore[abstract]

    def test_subclass_without_execute_raises(self) -> None:
        class IncompleteModule(BaseSearchModule):
            pass

        with pytest.raises(TypeError):
            IncompleteModule("test", 10)  # type: ignore[abstract]
