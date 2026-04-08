"""Composite / layered fetchers — multi-level fallback orchestration.

This module provides two related abstractions:

* :class:`LayeredFetcher` — an N-layer fallback chain where each layer
  carries a human-readable name and an optional independent timeout.
  A URL walks down the layers until one succeeds; every attempt is
  logged with the layer name, URL, and outcome so the path taken by
  each request is fully visible in the logs.
* :class:`CompositeFetcher` — a thin 2-layer wrapper kept for
  backward compatibility with earlier code.  New callers should use
  :func:`chain_fetchers` to build a labelled layered chain.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING

from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

if TYPE_CHECKING:
    from diting.fetch.base import Fetcher

logger = get_logger("fetch.composite")


@dataclass(frozen=True)
class FetchLayer:
    """A labelled layer in a fallback chain.

    Attributes:
        fetcher: The underlying :class:`~diting.fetch.base.Fetcher`.
        name: Short, human-readable identifier shown in logs
            (e.g. ``"local"``, ``"jina"``, ``"archive"``).
        timeout: Optional per-call timeout in seconds.  When set, a
            layer that does not produce a result within this window is
            treated as a failure and the next layer is tried.  ``None``
            defers to the underlying fetcher's own timeout handling.
    """

    fetcher: Fetcher
    name: str
    timeout: float | None = None


class LayeredFetcher:
    """N-layer fallback orchestrator with per-layer timeouts + naming.

    Layers are tried left-to-right.  A layer is considered to have
    failed when it raises :class:`FetchError`, times out, returns
    :class:`FetchResult` with ``success=False``, or raises any other
    exception.  The next layer then gets a turn.  Every outcome is
    logged so the full path each URL walks is visible.

    Raises:
        ValueError: If *layers* is empty.
    """

    def __init__(self, layers: list[FetchLayer]) -> None:
        if not layers:
            raise ValueError("LayeredFetcher requires at least one layer")
        self._layers = list(layers)

    # ------------------------------------------------------------------
    # Single-URL fetch
    # ------------------------------------------------------------------

    async def fetch(self, url: str) -> str:
        """Try each layer for *url*.  Raise the last error if all fail.

        Only :class:`FetchError` and :class:`asyncio.TimeoutError` are
        treated as layer failures.  Any other exception surfaces
        immediately so real bugs in a fetcher are never masked.
        """
        path: list[str] = []
        last_error: FetchError | None = None

        for layer in self._layers:
            path.append(layer.name)
            try:
                content = await self._call_fetch(layer, url)
            except FetchError as exc:
                last_error = exc
                logger.info(
                    "fetch layer=%s url=%s outcome=fail error=%s",
                    layer.name, url, exc,
                )
                continue
            except asyncio.TimeoutError:
                timeout_desc = (
                    f"{layer.timeout}s" if layer.timeout is not None
                    else "inner timeout"
                )
                last_error = FetchError(
                    f"layer {layer.name} timed out ({timeout_desc})"
                )
                logger.info(
                    "fetch layer=%s url=%s outcome=timeout after=%s",
                    layer.name, url, timeout_desc,
                )
                continue

            logger.info(
                "fetch success url=%s served_by=%s path=%s",
                url, layer.name, "->".join(path),
            )
            return content

        logger.warning(
            "fetch exhausted url=%s path=%s last_error=%s",
            url, "->".join(path), last_error,
        )
        if last_error is None:
            raise FetchError(f"no fetch layers available for {url}")
        raise last_error

    async def _call_fetch(self, layer: FetchLayer, url: str) -> str:
        if layer.timeout is None:
            return await layer.fetcher.fetch(url)
        return await asyncio.wait_for(
            layer.fetcher.fetch(url), timeout=layer.timeout,
        )

    # ------------------------------------------------------------------
    # Batch fetch
    # ------------------------------------------------------------------

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Try each layer on the URLs still failing; preserve order.

        A slot is populated by the first layer that returns
        ``success=True`` for it.  Failed URLs carry the *most recent*
        failure's :class:`FetchResult` so callers can inspect the last
        error they encountered.
        """
        if not urls:
            return []

        # Initial placeholders — overwritten by every layer's attempt.
        results: list[FetchResult] = [
            FetchResult(
                url=url, content="", success=False,
                error="no layer attempted",
            )
            for url in urls
        ]
        pending: list[int] = list(range(len(urls)))

        for layer in self._layers:
            if not pending:
                break
            layer_urls = [urls[i] for i in pending]
            try:
                layer_results = await self._call_fetch_many(layer, layer_urls)
            except asyncio.TimeoutError:
                logger.info(
                    "fetch_many layer=%s outcome=timeout urls=%d after=%.1fs",
                    layer.name, len(layer_urls), layer.timeout or -1.0,
                )
                for idx in pending:
                    results[idx] = FetchResult(
                        url=urls[idx], content="", success=False,
                        error=f"layer {layer.name} timeout",
                    )
                continue
            except Exception as exc:
                logger.warning(
                    "fetch_many layer=%s outcome=crash urls=%d error=%s",
                    layer.name, len(layer_urls), exc,
                )
                for idx in pending:
                    results[idx] = FetchResult(
                        url=urls[idx], content="", success=False,
                        error=f"layer {layer.name} error: {exc}",
                    )
                continue

            # Guard against layers returning fewer results than requested.
            if len(layer_results) != len(layer_urls):
                logger.warning(
                    "fetch_many layer=%s returned %d results for %d URLs — "
                    "treating entire batch as failure",
                    layer.name, len(layer_results), len(layer_urls),
                )
                for idx in pending:
                    results[idx] = FetchResult(
                        url=urls[idx], content="", success=False,
                        error=f"layer {layer.name} result count mismatch",
                    )
                continue

            new_pending: list[int] = []
            resolved = 0
            for idx, layer_result in zip(pending, layer_results):
                results[idx] = layer_result
                if layer_result.success:
                    resolved += 1
                else:
                    new_pending.append(idx)
            logger.info(
                "fetch_many layer=%s resolved=%d/%d remaining=%d",
                layer.name, resolved, len(pending), len(new_pending),
            )
            pending = new_pending

        return results

    async def _call_fetch_many(
        self, layer: FetchLayer, urls: list[str],
    ) -> list[FetchResult]:
        if layer.timeout is None:
            return await layer.fetcher.fetch_many(urls)
        return await asyncio.wait_for(
            layer.fetcher.fetch_many(urls), timeout=layer.timeout,
        )

    # ------------------------------------------------------------------
    # Resource cleanup
    # ------------------------------------------------------------------

    async def close(self) -> None:
        """Close every layer.  All layers are attempted even if some raise;
        the first exception encountered is re-raised at the end."""
        first_error: BaseException | None = None
        for layer in self._layers:
            try:
                await layer.fetcher.close()
            except BaseException as exc:
                if first_error is None:
                    first_error = exc
        if first_error is not None:
            raise first_error


class CompositeFetcher:
    """2-layer fallback wrapper kept for backward compatibility.

    Delegates to a :class:`LayeredFetcher` internally.  New callers
    should prefer :func:`chain_fetchers` with labelled
    :class:`FetchLayer` instances.
    """

    def __init__(self, primary: Fetcher, fallback: Fetcher) -> None:
        self._primary = primary
        self._fallback = fallback
        self._inner = LayeredFetcher([
            FetchLayer(fetcher=primary, name=type(primary).__name__),
            FetchLayer(fetcher=fallback, name=type(fallback).__name__),
        ])

    async def fetch(self, url: str) -> str:
        return await self._inner.fetch(url)

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        return await self._inner.fetch_many(urls)

    async def close(self) -> None:
        await self._inner.close()


def chain_fetchers(layers: list) -> Fetcher:
    """Build a left-to-right fallback chain from *layers*.

    Each entry may be either a bare :class:`~diting.fetch.base.Fetcher`
    (wrapped with a default name and no timeout) or a
    :class:`FetchLayer` (for explicit naming and per-layer timeout).

    ``layers[0]`` is tried first; ``layers[-1]`` is the deepest fallback.
    A single-layer list returns that layer's fetcher unwrapped — no
    redundant :class:`LayeredFetcher` is created.

    Raises:
        ValueError: If *layers* is empty.
    """
    if not layers:
        raise ValueError("chain_fetchers requires at least one layer")

    wrapped: list[FetchLayer] = []
    for item in layers:
        if isinstance(item, FetchLayer):
            wrapped.append(item)
        else:
            wrapped.append(
                FetchLayer(fetcher=item, name=type(item).__name__),
            )

    if len(wrapped) == 1:
        return wrapped[0].fetcher
    return LayeredFetcher(wrapped)
