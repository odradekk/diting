"""Tests for diting.pipeline.semantic_dedup."""

from __future__ import annotations

from unittest.mock import MagicMock

import numpy as np
import pytest

from diting.models import SearchResult
from diting.pipeline.semantic_dedup import SemanticDeduplicator


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _result(url: str, snippet: str = "some text", title: str = "Title") -> SearchResult:
    return SearchResult(title=title, url=url, snippet=snippet)


def _mock_embedder(embeddings: np.ndarray) -> MagicMock:
    """Build a mock embedder that returns pre-computed embeddings."""
    embedder = MagicMock()
    embedder.embed.return_value = embeddings
    return embedder


def _normalized(vectors: np.ndarray) -> np.ndarray:
    """L2-normalize rows."""
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    return (vectors / np.clip(norms, 1e-12, None)).astype(np.float32)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestBasicDedup:
    def test_no_duplicates_all_kept(self):
        # 3 orthogonal vectors — no duplicates.
        vecs = _normalized(np.array([
            [1, 0, 0],
            [0, 1, 0],
            [0, 0, 1],
        ], dtype=np.float32))
        embedder = _mock_embedder(vecs)
        dedup = SemanticDeduplicator(embedder, threshold=0.9)

        results = [_result(f"https://example.com/{i}") for i in range(3)]
        unique, stats = dedup.deduplicate(results)

        assert len(unique) == 3
        assert stats["semantic_removed"] == 0

    def test_identical_vectors_deduped(self):
        # 3 identical vectors — 2 removed.
        vecs = _normalized(np.array([
            [1, 0, 0],
            [1, 0, 0],
            [1, 0, 0],
        ], dtype=np.float32))
        embedder = _mock_embedder(vecs)
        dedup = SemanticDeduplicator(embedder, threshold=0.9)

        results = [_result(f"https://example.com/{i}") for i in range(3)]
        unique, stats = dedup.deduplicate(results)

        assert len(unique) == 1
        assert stats["semantic_removed"] == 2
        assert unique[0].url == "https://example.com/0"  # first is kept

    def test_threshold_boundary(self):
        # Two vectors with cosine ~0.95 — above 0.9 threshold.
        v1 = np.array([1.0, 0.0, 0.0])
        v2 = np.array([0.95, 0.31, 0.0])  # cos(v1, v2) ≈ 0.95
        vecs = _normalized(np.array([v1, v2], dtype=np.float32))
        embedder = _mock_embedder(vecs)

        dedup_strict = SemanticDeduplicator(embedder, threshold=0.9)
        unique, stats = dedup_strict.deduplicate([_result("a"), _result("b")])
        assert len(unique) == 1
        assert stats["semantic_removed"] == 1

    def test_threshold_below_keeps_both(self):
        # Two vectors with cosine ~0.7 — below 0.9 threshold.
        v1 = np.array([1.0, 0.0])
        v2 = np.array([0.7, 0.71])  # cos ≈ 0.7
        vecs = _normalized(np.array([v1, v2], dtype=np.float32))
        embedder = _mock_embedder(vecs)

        dedup = SemanticDeduplicator(embedder, threshold=0.9)
        unique, stats = dedup.deduplicate([_result("a"), _result("b")])
        assert len(unique) == 2
        assert stats["semantic_removed"] == 0


class TestEdgeCases:
    def test_single_result(self):
        embedder = _mock_embedder(np.array([[1, 0]], dtype=np.float32))
        dedup = SemanticDeduplicator(embedder, threshold=0.9)
        results = [_result("https://example.com/1")]
        unique, stats = dedup.deduplicate(results)
        assert len(unique) == 1
        assert stats["semantic_removed"] == 0
        embedder.embed.assert_not_called()

    def test_empty_results(self):
        embedder = _mock_embedder(np.array([], dtype=np.float32))
        dedup = SemanticDeduplicator(embedder, threshold=0.9)
        unique, stats = dedup.deduplicate([])
        assert unique == []
        assert stats["semantic_removed"] == 0
        embedder.embed.assert_not_called()


class TestGracefulDegradation:
    def test_embedder_error_returns_all(self):
        embedder = MagicMock()
        embedder.embed.side_effect = RuntimeError("model failed")
        dedup = SemanticDeduplicator(embedder, threshold=0.9)

        results = [_result("a"), _result("b")]
        unique, stats = dedup.deduplicate(results)

        assert len(unique) == 2
        assert stats["semantic_removed"] == 0

    def test_shape_mismatch_returns_all(self):
        # Embedder returns wrong number of rows.
        bad_emb = np.array([[1, 0], [0, 1], [1, 1]], dtype=np.float32)
        embedder = _mock_embedder(bad_emb)
        dedup = SemanticDeduplicator(embedder, threshold=0.9)

        results = [_result("a"), _result("b")]  # 2 results, 3 embeddings
        unique, stats = dedup.deduplicate(results)

        assert len(unique) == 2
        assert stats["semantic_removed"] == 0


class TestSnippetFallback:
    def test_empty_snippet_uses_title(self):
        """When snippet is empty, title should be used for embedding."""
        vecs = _normalized(np.array([
            [1, 0],
            [0, 1],
        ], dtype=np.float32))
        embedder = _mock_embedder(vecs)
        dedup = SemanticDeduplicator(embedder, threshold=0.9)

        results = [
            SearchResult(title="Title A", url="a", snippet=""),
            SearchResult(title="Title B", url="b", snippet="has snippet"),
        ]
        unique, stats = dedup.deduplicate(results)

        # Check that embed was called with title fallback for first result.
        call_args = embedder.embed.call_args[0][0]
        assert call_args[0] == "Title A"  # empty snippet → title
        assert call_args[1] == "has snippet"  # non-empty snippet used
