"""Abstract base class for search modules with timeout and error handling."""

from __future__ import annotations

import asyncio
from abc import ABC, abstractmethod
from typing import ClassVar

from diting.log import get_logger
from diting.models import ModuleError, ModuleOutput, SearchResult
from diting.modules.manifest import ModuleManifest


class BaseSearchModule(ABC):
    """Base class that all search modules must extend.

    Subclasses implement :meth:`_execute` to call their specific API.
    The public :meth:`search` method wraps ``_execute`` with
    ``asyncio.wait_for`` timeout enforcement and structured error handling,
    so subclasses can focus purely on the API interaction.

    Concrete subclasses **must** assign a :class:`ModuleManifest` to the
    ``MANIFEST`` class attribute so the router can reason about their
    capabilities without instantiating them.  The base class declares
    ``MANIFEST`` as ``None`` to keep test stubs viable; the router raises
    a clear error when it encounters a production module missing a manifest.
    """

    MANIFEST: ClassVar[ModuleManifest | None] = None

    def __init__(self, name: str, timeout: int, max_results: int = 20) -> None:
        self._name = name
        self._timeout = timeout
        self._max_results = max_results
        self._logger = get_logger(f"modules.{name}")

    @property
    def name(self) -> str:
        """Module identifier (e.g. ``"brave"``, ``"serp"``)."""
        return self._name

    @property
    def timeout(self) -> int:
        """Per-module timeout in seconds."""
        return self._timeout

    @property
    def manifest(self) -> ModuleManifest | None:
        """Return the module's capability manifest, if declared.

        Returns the ``MANIFEST`` class attribute on the concrete subclass.
        Router code should check for ``None`` and handle the fallback
        (log a warning, include as generic source, or skip).
        """
        return type(self).MANIFEST

    @abstractmethod
    async def _execute(self, query: str) -> list[SearchResult]:
        """Run the search against the module's backing API.

        Subclasses should raise on failure; the base class converts
        exceptions into :class:`ModuleError` automatically.
        """

    async def search(self, query: str) -> ModuleOutput:
        """Execute the search with timeout enforcement and error handling.

        Returns a :class:`ModuleOutput` on both success and failure paths.
        """
        self._logger.debug("Starting search: query=%r", query)

        try:
            results = await asyncio.wait_for(
                self._execute(query),
                timeout=self._timeout,
            )
        except asyncio.TimeoutError:
            error = ModuleError(
                code="TIMEOUT",
                message=f"Module '{self._name}' timed out after {self._timeout}s",
                retryable=True,
            )
            self._logger.warning(
                "Search timed out: module=%s, query=%r", self._name, query
            )
            return ModuleOutput(module=self._name, results=[], error=error)
        except Exception as exc:
            error = ModuleError(
                code="ERROR",
                message=str(exc),
                retryable=False,
            )
            self._logger.warning(
                "Search failed: module=%s, query=%r, error=%s",
                self._name,
                query,
                exc,
            )
            return ModuleOutput(module=self._name, results=[], error=error)

        self._logger.info(
            "Search complete: module=%s, results=%d", self._name, len(results)
        )
        return ModuleOutput(module=self._name, results=results)
