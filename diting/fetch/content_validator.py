"""Content validation — gate garbage responses from entering the cache.

A fetcher can return HTTP 200 with completely useless content: login walls,
Cloudflare challenges, CAPTCHAs, rate-limit pages.  Caching any of that is
actively harmful — the bad content wins every cache lookup for the URL's
TTL and blocks the fetch-chain fallback layers from ever trying again.

This module offers a single conservative predicate :func:`is_cacheable`
which returns ``(ok, reason)``.  The *reason* is cheap to log and makes
diagnostics actionable.  When in doubt, reject — re-fetching is cheaper
than being wrong about what we store.
"""

from __future__ import annotations

# Content shorter than this is rarely worth caching, regardless of markers —
# summarisers ignore it and the cost of a re-fetch is negligible.
_MIN_VALID_LENGTH = 300

# Phrases that are never part of legitimate article content.  Each signals
# an anti-bot or authentication wall that the fetcher failed to bypass.
_BOT_WALL_MARKERS: tuple[str, ...] = (
    "Checking your browser before accessing",
    "Just a moment...",
    "cf-chl-bypass",
    "Attention Required! | Cloudflare",
    "DDoS protection by Cloudflare",
    "Please enable JavaScript and cookies",
    "Please verify you are human",
    "Verifying you are human. This may take a few seconds",
    'class="g-recaptcha"',
    "hCaptcha solve",
    "Access to this page has been denied",
    "Access Denied - Sucuri Website Firewall",
)

# Login-wall signatures.  A login wall is effectively zero content for our
# purposes — we cannot extract the real page, so do not cache what we got.
_LOGIN_WALL_MARKERS: tuple[str, ...] = (
    "Log in to X",
    "Sign in to X",
    "登录 X",
    'href="/i/flow/login"',
    "Sign in to continue reading",
    "Subscribe to continue reading",
    "You need to sign in to",
    "登录后查看",
    "请先登录",
)

# Hard rate-limit / error pages that sometimes return 200 from CDNs.
_RATE_LIMIT_MARKERS: tuple[str, ...] = (
    "Too Many Requests",
    "Rate limit exceeded",
    "请求过于频繁",
)


def is_cacheable(content: str) -> tuple[bool, str]:
    """Decide whether *content* is worth caching.

    Returns ``(ok, reason)`` where *reason* is a short tag suitable for log
    output.  The call is a pure function: no I/O, no mutation.
    """
    if not content or not content.strip():
        return False, "empty"

    length = len(content)
    if length < _MIN_VALID_LENGTH:
        return False, f"too_short({length})"

    for marker in _BOT_WALL_MARKERS:
        if marker in content:
            return False, f"bot_wall:{marker!r}"

    for marker in _LOGIN_WALL_MARKERS:
        if marker in content:
            return False, f"login_wall:{marker!r}"

    for marker in _RATE_LIMIT_MARKERS:
        if marker in content:
            return False, f"rate_limit:{marker!r}"

    return True, "ok"
