"""Browser driver selection — prefers patchright when installed.

Patchright is a drop-in-compatible fork of Playwright that removes the
automation fingerprints most anti-bot services use (Cloudflare,
DataDome, PerimeterX, etc.).  We keep it behind an opt-in extra so the
default install stays light:

    pip install diting[stealth]

At runtime the choice is driven by ``ENABLE_STEALTH_BROWSER``.  When
stealth is requested but ``patchright`` is not importable, we log a
clear warning and fall back to vanilla Playwright rather than refusing
to start — the server remains functional, just less stealthy.
"""

from __future__ import annotations

from typing import Any, Callable

from diting.log import get_logger

logger = get_logger("fetch.browser_driver")


def get_async_playwright(prefer_stealth: bool = False) -> Callable[[], Any]:
    """Return an ``async_playwright`` factory.

    Args:
        prefer_stealth: When ``True``, try to import ``patchright`` first.
            On ``ImportError`` we log a warning and fall back to the
            stock ``playwright`` package.

    Returns:
        A callable that, when invoked, returns a context manager yielding
        an ``AsyncPlaywright`` instance — exactly the API callers already
        use with ``playwright.async_api.async_playwright``.
    """
    if prefer_stealth:
        try:
            from patchright.async_api import async_playwright as stealth_pw
            logger.info("Browser driver: patchright (stealth)")
            return stealth_pw
        except ImportError:
            logger.warning(
                "ENABLE_STEALTH_BROWSER=true but patchright is not installed. "
                "Install with: pip install diting[stealth]. "
                "Falling back to vanilla playwright."
            )

    from playwright.async_api import async_playwright
    logger.info("Browser driver: playwright")
    return async_playwright


__all__ = ["get_async_playwright"]
