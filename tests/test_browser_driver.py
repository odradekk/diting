"""Tests for diting.fetch.browser_driver — stealth driver selection."""

from __future__ import annotations

import logging
import sys
from types import ModuleType
from unittest.mock import MagicMock

import pytest

from diting.fetch.browser_driver import get_async_playwright


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def _enable_propagation():
    """Restore log propagation so ``caplog`` can see diting.* records.

    Other test files call ``setup_logging()`` which sets
    ``propagate=False`` on the "diting" logger to prevent double output.
    ``caplog`` attaches its capture handler at the root logger, so
    without propagation it never sees our warnings.  This fixture is
    autouse-scoped to each test in this module only.
    """
    diting_logger = logging.getLogger("diting")
    previous = diting_logger.propagate
    diting_logger.propagate = True
    yield
    diting_logger.propagate = previous


def _install_fake_module(monkeypatch, name: str, attrs: dict) -> ModuleType:
    """Install a fake module at *name* in sys.modules with *attrs* set."""
    mod = ModuleType(name)
    for key, val in attrs.items():
        setattr(mod, key, val)
    monkeypatch.setitem(sys.modules, name, mod)
    return mod


# ---------------------------------------------------------------------------
# prefer_stealth=False — always returns vanilla playwright
# ---------------------------------------------------------------------------


class TestStealthDisabled:
    """With stealth off, the function never tries to import patchright."""

    def test_returns_vanilla_playwright(self):
        factory = get_async_playwright(prefer_stealth=False)
        # Came from playwright.async_api, not patchright.async_api.
        assert factory.__module__ == "playwright.async_api"

    def test_does_not_touch_patchright_even_when_installed(self, monkeypatch):
        """If patchright happens to be on sys.path, we still skip it."""
        sentinel = MagicMock(name="patchright_stealth_pw")
        _install_fake_module(
            monkeypatch, "patchright", {},
        )
        _install_fake_module(
            monkeypatch, "patchright.async_api",
            {"async_playwright": sentinel},
        )

        factory = get_async_playwright(prefer_stealth=False)

        assert factory is not sentinel
        assert factory.__module__ == "playwright.async_api"


# ---------------------------------------------------------------------------
# prefer_stealth=True — patchright preferred, fallback on ImportError
# ---------------------------------------------------------------------------


class TestStealthEnabledWithPatchrightInstalled:
    def test_returns_patchright_factory(self, monkeypatch):
        sentinel = MagicMock(name="patchright_stealth_pw")
        _install_fake_module(monkeypatch, "patchright", {})
        _install_fake_module(
            monkeypatch, "patchright.async_api",
            {"async_playwright": sentinel},
        )

        factory = get_async_playwright(prefer_stealth=True)

        assert factory is sentinel

    def test_logs_selection(self, monkeypatch, caplog):
        sentinel = MagicMock(name="patchright_stealth_pw")
        _install_fake_module(monkeypatch, "patchright", {})
        _install_fake_module(
            monkeypatch, "patchright.async_api",
            {"async_playwright": sentinel},
        )

        with caplog.at_level(logging.INFO, logger="diting.fetch.browser_driver"):
            get_async_playwright(prefer_stealth=True)

        assert any("patchright" in r.message for r in caplog.records)


class TestStealthEnabledWithPatchrightMissing:
    """When patchright is not importable, fall back to vanilla playwright."""

    def test_falls_back_to_playwright(self, monkeypatch):
        # Remove patchright from sys.modules (if a prior test left it there)
        # and block re-import attempts.
        monkeypatch.setitem(sys.modules, "patchright", None)

        factory = get_async_playwright(prefer_stealth=True)

        assert factory.__module__ == "playwright.async_api"

    def test_logs_warning(self, monkeypatch, caplog):
        monkeypatch.setitem(sys.modules, "patchright", None)

        with caplog.at_level(logging.WARNING, logger="diting.fetch.browser_driver"):
            get_async_playwright(prefer_stealth=True)

        # The warning must reference the install hint so users can act.
        warnings = [r for r in caplog.records if r.levelno == logging.WARNING]
        assert any("diting[stealth]" in r.message for r in warnings)

    def test_returns_usable_factory_despite_missing_patchright(self, monkeypatch):
        monkeypatch.setitem(sys.modules, "patchright", None)

        factory = get_async_playwright(prefer_stealth=True)

        # The returned object must be callable — this is what server.py does.
        assert callable(factory)
