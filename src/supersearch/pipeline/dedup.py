"""URL normalization and deduplication utilities for the search pipeline.

Only ``http`` and ``https`` URLs are supported.  Non-web schemes (e.g.
``mailto:``, ``ftp:``) are treated as invalid and produce empty strings from
:func:`normalize_url` and :func:`extract_domain`.

.. note::

   URL path parameters (the ``;key=val`` segment) are **not** preserved
   during normalization.  This is intentional — path parameters are
   exceedingly rare in web search result URLs and dropping them yields
   more aggressive deduplication.
"""

from __future__ import annotations

import re
from urllib.parse import parse_qs, urlencode, urlparse, urlunparse

from supersearch.log import get_logger
from supersearch.models import SearchResult

logger = get_logger("pipeline.dedup")

# Tracking / noise parameters stripped during normalization.
_TRACKING_PARAMS: frozenset[str] = frozenset({
    "utm_source",
    "utm_medium",
    "utm_campaign",
    "utm_term",
    "utm_content",
    "utm_id",
    "fbclid",
    "gclid",
    "ref",
    "source",
})

# Default ports that can be safely removed.
_DEFAULT_PORTS: dict[str, int] = {
    "http": 80,
    "https": 443,
}

# Only these schemes are considered valid web URLs.
_VALID_SCHEMES: frozenset[str] = frozenset({"http", "https"})

# RFC 3986 scheme: ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )
_SCHEME_RE = re.compile(r"^[a-zA-Z][a-zA-Z0-9+\-.]*:")


def normalize_url(url: str) -> str:
    """Normalize a URL for consistent comparison.

    * Lowercases scheme and host
    * Removes common tracking query parameters
    * Removes trailing slashes from the path (preserves root ``/``)
    * Removes default ports (80/http, 443/https)
    * Removes URL fragments and path parameters
    * Sorts remaining query parameters alphabetically
    * Upgrades ``http`` to ``https``
    * Gracefully handles missing schemes and empty strings
    * Returns ``""`` for non-http/https schemes or unparseable URLs
    """
    if not url or not url.strip():
        return ""

    url = url.strip()

    # Detect explicit scheme.  URLs like "example.com:8080/page" match the
    # regex but the "scheme" (example.com) contains dots so it is really a
    # host:port pair.  Only reject when a clean scheme is present AND it is
    # not a web scheme.
    scheme_match = _SCHEME_RE.match(url)
    if scheme_match:
        detected = scheme_match.group(0).rstrip(":").lower()
        if "." not in detected and detected not in _VALID_SCHEMES:
            # Genuine non-web scheme (mailto:, ftp:, javascript:, etc.).
            return ""

    # Add scheme when missing so urlparse can handle it.
    if "://" not in url:
        url = f"https://{url}"

    parsed = urlparse(url)

    scheme = parsed.scheme.lower()
    if scheme not in _VALID_SCHEMES:
        return ""

    hostname = (parsed.hostname or "").lower()
    if not hostname:
        return ""

    # Remove default port (check against original scheme before upgrade).
    # Reject URLs with malformed port numbers.
    try:
        port = parsed.port
    except ValueError:
        return ""

    if port and port == _DEFAULT_PORTS.get(scheme):
        port = None

    # Upgrade http -> https.
    if scheme == "http":
        scheme = "https"

    # Reconstruct netloc: hostname + optional port.
    netloc = hostname
    if port:
        netloc = f"{hostname}:{port}"

    # Strip trailing slashes from path but keep root "/".
    path = parsed.path.rstrip("/") or "/"

    # Filter tracking params and sort remaining ones.
    query_params = parse_qs(parsed.query, keep_blank_values=True)
    filtered = {
        k: v
        for k, v in sorted(query_params.items())
        if k.lower() not in _TRACKING_PARAMS
    }
    query = urlencode(filtered, doseq=True)

    # Drop fragment and path params entirely.
    return urlunparse((scheme, netloc, path, "", query, ""))


def extract_domain(url: str) -> str:
    """Extract the domain (hostname) from *url*, stripping ``www.`` prefix.

    Returns a lowercased domain string suitable for blacklist comparison.
    Only ``http`` and ``https`` URLs are processed; other schemes return ``""``.
    """
    if not url or not url.strip():
        return ""

    url = url.strip()

    # Detect and reject non-web schemes (same logic as normalize_url).
    scheme_match = _SCHEME_RE.match(url)
    if scheme_match:
        detected = scheme_match.group(0).rstrip(":").lower()
        if "." not in detected and detected not in _VALID_SCHEMES:
            return ""

    if "://" not in url:
        url = f"https://{url}"

    parsed = urlparse(url)

    if parsed.scheme.lower() not in _VALID_SCHEMES:
        return ""

    hostname = (parsed.hostname or "").lower()
    if hostname.startswith("www."):
        hostname = hostname[4:]
    return hostname


def deduplicate(
    results: list[SearchResult],
    seen_urls: set[str] | None = None,
) -> tuple[list[SearchResult], set[str]]:
    """Remove duplicate results by normalized URL.

    Args:
        results: Incoming search results to filter.
        seen_urls: Previously seen normalized URLs (enables cross-round
            deduplication).  Mutated in-place **and** returned.

    Returns:
        A tuple of ``(unique_results, updated_seen_urls)``.
    """
    if seen_urls is None:
        seen_urls = set()

    unique: list[SearchResult] = []

    for result in results:
        normalized = normalize_url(result.url)

        # Skip results with unparseable or non-web URLs.
        if not normalized:
            logger.debug("Invalid or non-web URL skipped: %s", result.url)
            continue

        if normalized in seen_urls:
            logger.debug("Duplicate URL removed: %s", result.url)
            continue

        seen_urls.add(normalized)
        unique.append(result)

    return unique, seen_urls
