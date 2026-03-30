"""Unified blacklist — regex-based URL filtering with auto-append support.

Rules are loaded from a text file (one regex per line).  Each pattern is
matched against the ``domain/path`` portion of a URL (no protocol, no
query string, no fragment, ``www.`` stripped).

Auto-blacklist entries are appended below a marker line so that manually
curated rules and automatically discovered rules coexist in the same file.
"""

from __future__ import annotations

import pathlib
import re
from urllib.parse import urlparse

from supersearch.log import get_logger
from supersearch.models import ScoredResult
from supersearch.pipeline.dedup import extract_domain

logger = get_logger("pipeline.blacklist")

AUTO_MARKER = "# === AUTO-BLACKLIST (managed automatically, do not edit below) ==="


# ------------------------------------------------------------------
# Loading
# ------------------------------------------------------------------


def load_blacklist(path: str) -> list[re.Pattern[str]]:
    """Load blacklist patterns from a text file.

    Ignores blank lines, comment lines (``#``), and the auto-blacklist
    marker line.  Invalid regex patterns are skipped with a warning.
    Returns an empty list if the file does not exist.
    """
    p = pathlib.Path(path)
    if not p.exists():
        logger.warning("Blacklist file not found: %s", p)
        return []

    patterns: list[re.Pattern[str]] = []
    for lineno, raw in enumerate(p.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        try:
            patterns.append(re.compile(line))
        except re.error as exc:
            logger.warning("Skipping invalid regex on line %d: %s (%s)", lineno, line, exc)

    logger.info("Loaded %d blacklist patterns from %s", len(patterns), p)
    return patterns


# ------------------------------------------------------------------
# Matching
# ------------------------------------------------------------------


def extract_match_target(url: str) -> str:
    """Extract ``domain/path`` from *url* for pattern matching.

    * Protocol, query string, and fragment are stripped.
    * ``www.`` prefix is removed from the domain.
    * Returns ``""`` for unparseable URLs.
    """
    if not url or not url.strip():
        return ""
    url = url.strip()
    if "://" not in url:
        url = f"https://{url}"
    parsed = urlparse(url)
    host = (parsed.hostname or "").lower()
    if host.startswith("www."):
        host = host[4:]
    if not host:
        return ""
    path = parsed.path or "/"
    return f"{host}{path}"


def is_blacklisted(url: str, patterns: list[re.Pattern[str]]) -> bool:
    """Return ``True`` if the URL matches any blacklist pattern."""
    if not patterns:
        return False
    target = extract_match_target(url)
    if not target:
        return False
    return any(p.search(target) for p in patterns)


# ------------------------------------------------------------------
# Auto-blacklist: collect + append
# ------------------------------------------------------------------


def collect_low_score_domains(
    scored: list[ScoredResult],
    threshold: float,
) -> set[str]:
    """Find domains where **all** results scored below *threshold*.

    A single good result protects the entire domain.
    """
    domain_scores: dict[str, list[float]] = {}
    for s in scored:
        domain = extract_domain(s.url).lower()
        if domain:
            domain_scores.setdefault(domain, []).append(s.final_score)

    bad: set[str] = set()
    for domain, scores in domain_scores.items():
        if all(sc < threshold for sc in scores):
            bad.add(domain)
            logger.debug("Auto-blacklist candidate: %s (scores: %s)",
                         domain, [round(s, 2) for s in scores])
    return bad


def append_auto_blacklist(domains: set[str], path: str) -> set[str]:
    """Append new domain patterns below the ``AUTO-BLACKLIST`` marker.

    Each domain is written as ``^domain\\.ext$`` (dots escaped, anchored).
    Duplicates are skipped by checking existing file content.

    Returns the set of domains that were actually added (may be empty).
    """
    if not domains:
        return set()

    p = pathlib.Path(path)

    # Read existing content (or empty if file doesn't exist).
    if p.exists():
        content = p.read_text(encoding="utf-8")
    else:
        p.parent.mkdir(parents=True, exist_ok=True)
        content = ""

    # Build the pattern string for each domain.
    def _domain_pattern(d: str) -> str:
        escaped = re.escape(d)
        return f"^{escaped}(/|$)"

    # Determine which domains are already present.
    existing_lines = set(content.splitlines())
    added: set[str] = set()
    new_lines: list[str] = []

    for domain in sorted(domains):
        pattern_line = _domain_pattern(domain)
        if pattern_line not in existing_lines:
            new_lines.append(pattern_line)
            added.add(domain)

    if not added:
        return set()

    # Ensure the marker exists.
    if AUTO_MARKER not in content:
        if content and not content.endswith("\n"):
            content += "\n"
        content += f"\n{AUTO_MARKER}\n"

    # Append new patterns after existing content.
    if not content.endswith("\n"):
        content += "\n"
    content += "\n".join(new_lines) + "\n"

    p.write_text(content, encoding="utf-8")
    logger.info("Auto-blacklist: added %d domains to %s: %s",
                len(added), p, sorted(added))

    return added
