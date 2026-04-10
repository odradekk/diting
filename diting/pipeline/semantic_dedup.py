"""Semantic deduplication using BGE embeddings.

Computes cosine similarity between snippet embeddings and removes
near-duplicates above a configurable threshold.  Runs between the
URL-level dedup / prefilter and the scoring stage.
"""

from __future__ import annotations

from typing import Any

from diting.log import get_logger
from diting.models import SearchResult

logger = get_logger("pipeline.semantic_dedup")

try:
    import numpy as np
except ImportError:  # pragma: no cover - optional dependency
    np = None  # type: ignore[assignment]


class SemanticDeduplicator:
    """Remove near-duplicate search results by snippet embedding similarity.

    Parameters
    ----------
    embedder:
        A :class:`~diting.rerank.embedder.BGEEmbedder` instance (or any
        object with an ``embed(texts) -> ndarray`` method).
    threshold:
        Cosine similarity above which two results are considered
        duplicates.  The later result is dropped.
    """

    def __init__(self, embedder: Any, threshold: float = 0.9) -> None:
        self._embedder: Any = embedder
        self._threshold = threshold

    @property
    def embedder(self) -> Any:
        """The underlying embedder — exposed so callers can share instances.

        Typed as ``Any`` because callers (e.g. ``Orchestrator``) pass the
        returned object to APIs that require a concrete ``BGEEmbedder``,
        and this class accepts any duck-typed embedder at construction.
        """
        return self._embedder

    def deduplicate(
        self,
        results: list[SearchResult],
    ) -> tuple[list[SearchResult], dict[str, int]]:
        """Return ``(unique_results, stats)``."""
        if len(results) <= 1:
            return results, {"semantic_removed": 0}

        if np is None:
            logger.warning("numpy unavailable — skipping semantic dedup")
            return results, {"semantic_removed": 0}

        texts = [r.snippet or r.title for r in results]

        try:
            embeddings = self._embedder.embed(texts)  # type: ignore[union-attr]
        except Exception as exc:
            logger.warning("Embedding failed — skipping semantic dedup: %s", exc)
            return results, {"semantic_removed": 0}

        if embeddings.shape[0] != len(results):
            logger.warning(
                "Embedding count mismatch (%d vs %d) — skipping semantic dedup",
                embeddings.shape[0], len(results),
            )
            return results, {"semantic_removed": 0}

        # Cosine similarity matrix (embeddings are already L2-normalized).
        sim_matrix = embeddings @ embeddings.T  # (N, N)

        kept_indices: list[int] = []
        removed = 0

        for i in range(len(results)):
            is_dup = False
            for j in kept_indices:
                if sim_matrix[i, j] > self._threshold:
                    is_dup = True
                    logger.debug(
                        "Semantic dup: %.3f — %s vs %s",
                        sim_matrix[i, j], results[i].url, results[j].url,
                    )
                    break
            if not is_dup:
                kept_indices.append(i)
            else:
                removed += 1

        unique = [results[i] for i in kept_indices]
        return unique, {"semantic_removed": removed}
