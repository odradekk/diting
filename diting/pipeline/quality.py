"""Heuristic quality signals for search results.

The goal is not to perfectly judge content quality from a snippet — that would
be too strong a claim. The goal is narrower: assign a useful prior based on
signals we can compute cheaply and locally before any LLM call.
"""

from __future__ import annotations

import json
import pathlib
import re
from collections import Counter
from dataclasses import dataclass
from functools import lru_cache

from diting.models import SearchResult
from diting.pipeline.dedup import extract_domain

_DATA_DIR = pathlib.Path(__file__).resolve().parent.parent / "data"
DEFAULT_DOMAIN_AUTHORITY_PATH = _DATA_DIR / "domain_authority.json"

_MARKETING_PATTERNS = (
    "buy now",
    "click here",
    "sponsored",
    "advertisement",
    "limited time",
    "free download",
    "best price",
    "立即下载",
    "点击查看",
    "广告",
)

_TOKEN_RE = re.compile(r"\w+", re.UNICODE)
_NON_WORD_RE = re.compile(r"[^\w\u4e00-\u9fff]+", re.UNICODE)


def _clamp(value: float) -> float:
    return max(0.0, min(1.0, value))


@dataclass(frozen=True)
class DomainAuthorityTable:
    """Lookup table for domain authority priors."""

    default: float
    exact: dict[str, float]
    suffix: dict[str, float]
    low_exact: dict[str, float]

    def score(self, domain_or_url: str) -> float:
        domain = extract_domain(domain_or_url) or domain_or_url.strip().lower()
        if not domain:
            return self.default
        if domain.startswith("www."):
            domain = domain[4:]

        for candidate in _domain_candidates(domain):
            if candidate in self.low_exact:
                return _clamp(self.low_exact[candidate])
            if candidate in self.exact:
                return _clamp(self.exact[candidate])

        suffix_score: float | None = None
        for suffix, score in self.suffix.items():
            if domain.endswith(suffix):
                suffix_score = score if suffix_score is None else max(suffix_score, score)

        return _clamp(self.default if suffix_score is None else suffix_score)


@lru_cache(maxsize=8)
def load_domain_authority(path: str | pathlib.Path = DEFAULT_DOMAIN_AUTHORITY_PATH) -> DomainAuthorityTable:
    """Load the domain authority table from JSON and cache the result.

    Falls back to a minimal built-in table when the file does not exist.
    """
    file_path = pathlib.Path(path)
    if not file_path.is_file():
        return DomainAuthorityTable(default=0.5, exact={}, suffix={}, low_exact={})
    data = json.loads(file_path.read_text(encoding="utf-8"))
    return DomainAuthorityTable(
        default=float(data.get("default", 0.5)),
        exact={str(k).lower(): float(v) for k, v in data.get("exact", {}).items()},
        suffix={str(k).lower(): float(v) for k, v in data.get("suffix", {}).items()},
        low_exact={str(k).lower(): float(v) for k, v in data.get("low_exact", {}).items()},
    )


class HeuristicQualityScorer:
    """Assign a cheap local quality prior to each search result.

    Signals used:
    - domain authority prior
    - snippet length
    - originality / marketing-noise heuristics
    - duplicate snippet penalty
    """

    def __init__(
        self,
        authority: DomainAuthorityTable | None = None,
        authority_path: str | pathlib.Path = DEFAULT_DOMAIN_AUTHORITY_PATH,
    ) -> None:
        resolved_path = authority_path or DEFAULT_DOMAIN_AUTHORITY_PATH
        self._authority = authority or load_domain_authority(resolved_path)

    def score_results(self, results: list[SearchResult]) -> dict[str, float]:
        """Return a per-URL quality score in the range [0, 1]."""
        fingerprints = [self._fingerprint(result.snippet) for result in results]
        counts = Counter(fp for fp in fingerprints if fp)

        scored: dict[str, float] = {}
        for result, fingerprint in zip(results, fingerprints):
            duplicate_count = counts.get(fingerprint, 1) if fingerprint else 1
            scored[result.url] = self.score_result(result, duplicate_count=duplicate_count)
        return scored

    def score_result(
        self,
        result: SearchResult,
        *,
        duplicate_count: int = 1,
    ) -> float:
        authority = self._authority.score(result.url)
        length = self._length_score(result.snippet)
        originality = self._originality_score(result.title, result.snippet)
        duplicate = self._duplicate_score(duplicate_count)

        final = (
            0.45 * authority
            + 0.25 * length
            + 0.15 * originality
            + 0.15 * duplicate
        )
        return round(_clamp(final), 4)

    @staticmethod
    def _length_score(snippet: str) -> float:
        text = snippet.strip()
        if not text:
            return 0.0
        return _clamp(len(text) / 240.0)

    @staticmethod
    def _originality_score(title: str, snippet: str) -> float:
        text = snippet.strip()
        if not text:
            return 0.0

        lowered = text.lower()
        score = 0.7

        if any(pattern in lowered for pattern in _MARKETING_PATTERNS):
            score -= 0.25
        if "..." in text or "…" in text:
            score -= 0.08
        if re.search(r"[。！？.!?]", text):
            score += 0.05
        if len(_TOKEN_RE.findall(text)) >= 12:
            score += 0.05

        title_tokens = {token.lower() for token in _TOKEN_RE.findall(title)}
        snippet_tokens = {token.lower() for token in _TOKEN_RE.findall(text)}
        if title_tokens:
            overlap = len(title_tokens & snippet_tokens) / len(title_tokens)
            if overlap > 0.8:
                score -= 0.12
            elif overlap < 0.35:
                score += 0.05

        unique_tokens = len(snippet_tokens)
        if unique_tokens < 6:
            score -= 0.15

        return _clamp(score)

    @staticmethod
    def _duplicate_score(duplicate_count: int) -> float:
        if duplicate_count <= 1:
            return 1.0
        return _clamp(1.0 / (1.0 + 0.35 * (duplicate_count - 1)))

    @staticmethod
    def _fingerprint(snippet: str) -> str:
        text = snippet.strip().lower()
        if not text:
            return ""
        text = _NON_WORD_RE.sub(" ", text)
        text = re.sub(r"\s+", " ", text).strip()
        return text[:200]


def _domain_candidates(domain: str) -> list[str]:
    parts = domain.split(".")
    return [".".join(parts[i:]) for i in range(len(parts))]
