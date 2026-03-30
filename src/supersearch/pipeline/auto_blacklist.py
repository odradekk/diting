"""Automatic domain blacklist based on low-scoring search results.

Domains where all results score below a configurable threshold are
persisted to a JSON file and merged with the manual blacklist on
subsequent searches.
"""

from __future__ import annotations

import json
import pathlib

from supersearch.log import get_logger
from supersearch.models import ScoredResult
from supersearch.pipeline.dedup import extract_domain

logger = get_logger("pipeline.auto_blacklist")

_DEFAULT_PATH = "data/auto_blacklist.json"


def load_auto_blacklist(path: str | None = None) -> set[str]:
    """Load the persisted auto-blacklist from disk.

    Returns an empty set if the file does not exist or is malformed.
    """
    p = pathlib.Path(path or _DEFAULT_PATH)
    if not p.exists():
        return set()
    try:
        data = json.loads(p.read_text(encoding="utf-8"))
        if isinstance(data, list):
            domains = {d for d in data if isinstance(d, str)}
            logger.info("Loaded auto-blacklist: %d domains", len(domains))
            return domains
    except (json.JSONDecodeError, OSError) as exc:
        logger.warning("Failed to load auto-blacklist from %s: %s", p, exc)
    return set()


def save_auto_blacklist(domains: set[str], path: str | None = None) -> None:
    """Persist the auto-blacklist to disk."""
    p = pathlib.Path(path or _DEFAULT_PATH)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(
        json.dumps(sorted(domains), indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    logger.info("Saved auto-blacklist: %d domains → %s", len(domains), p)


def collect_low_score_domains(
    scored: list[ScoredResult],
    threshold: float,
) -> set[str]:
    """Find domains where ALL results scored below *threshold*.

    A domain is only blacklisted when every result from it is below
    the threshold — a single good result protects the domain.
    """
    domain_scores: dict[str, list[float]] = {}
    for s in scored:
        domain = extract_domain(s.url).lower()
        if domain:
            domain_scores.setdefault(domain, []).append(s.final_score)

    bad_domains: set[str] = set()
    for domain, scores in domain_scores.items():
        if all(sc < threshold for sc in scores):
            bad_domains.add(domain)
            logger.debug(
                "Auto-blacklist candidate: %s (scores: %s)",
                domain, [round(s, 2) for s in scores],
            )

    return bad_domains


def update_auto_blacklist(
    scored: list[ScoredResult],
    threshold: float,
    path: str | None = None,
) -> set[str]:
    """Load existing blacklist, add newly detected low-score domains, save.

    Returns the full updated blacklist set.
    """
    existing = load_auto_blacklist(path)
    new_domains = collect_low_score_domains(scored, threshold)
    added = new_domains - existing

    if added:
        logger.info("Auto-blacklist: adding %d new domains: %s", len(added), sorted(added))
        updated = existing | new_domains
        save_auto_blacklist(updated, path)
        return updated

    return existing
