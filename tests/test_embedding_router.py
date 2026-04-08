"""Tests for diting.routing.embedding_router."""

from __future__ import annotations

from unittest.mock import MagicMock

import numpy as np
import pytest

from diting.modules.manifest import ModuleManifest
from diting.routing.embedding_router import EmbeddingRouter


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _normalized(vectors: np.ndarray) -> np.ndarray:
    """L2-normalize rows."""
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    return (vectors / np.clip(norms, 1e-12, None)).astype(np.float32)


def _mock_module(
    name: str,
    result_type: str = "general",
    scope: str = "General web search",
) -> MagicMock:
    """Build a mock module with a manifest."""
    manifest = ModuleManifest(
        domains=["general"],
        languages=["en"],
        cost_tier="free",
        latency_tier="fast",
        result_type=result_type,
        scope=scope,
    )
    mod = MagicMock()
    mod.name = name
    mod.manifest = manifest
    return mod


def _mock_module_no_manifest(name: str) -> MagicMock:
    mod = MagicMock()
    mod.name = name
    mod.manifest = None
    return mod


def _mock_embedder(
    scope_embeddings: np.ndarray,
    query_embedding: np.ndarray,
) -> MagicMock:
    """Build a mock embedder that returns scope embeddings first, then query."""
    embedder = MagicMock()
    call_count = 0

    def side_effect(texts):
        nonlocal call_count
        call_count += 1
        if call_count == 1:
            return scope_embeddings
        return query_embedding

    embedder.embed.side_effect = side_effect
    return embedder


def _mock_embedder_simple(return_value: np.ndarray) -> MagicMock:
    """Build a mock embedder that always returns the same thing."""
    embedder = MagicMock()
    embedder.embed.return_value = return_value
    return embedder


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestBasicRouting:
    """Core routing behavior."""

    def test_routes_to_most_similar_modules(self):
        """Top-K routable modules selected by cosine similarity."""
        # 3 routable modules + 1 baseline
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module(
            "arxiv", result_type="papers",
            scope="Academic preprints from arXiv",
        )
        github = _mock_module(
            "github", result_type="code",
            scope="GitHub repository search",
        )
        stackexchange = _mock_module(
            "stackexchange", result_type="qa",
            scope="Programming Q&A from Stack Overflow",
        )

        # Embeddings: arxiv closest to query, github second, stackexchange third
        scope_vecs = _normalized(np.array([
            [0.9, 0.1, 0.0],  # arxiv
            [0.1, 0.8, 0.1],  # github
            [0.1, 0.1, 0.8],  # stackexchange
        ], dtype=np.float32))

        query_vec = _normalized(np.array([[0.85, 0.15, 0.0]], dtype=np.float32))

        embedder = _mock_embedder(scope_vecs, query_vec)

        router = EmbeddingRouter(
            modules=[bing, arxiv, github, stackexchange],
            embedder=embedder,
            top_k=2,
        )
        result = router.route("quantum computing papers")

        # Baseline (bing) always included + top-2 routable (arxiv, github)
        assert "bing" in result
        assert "arxiv" in result
        assert "github" in result
        assert "stackexchange" not in result

    def test_baseline_always_included(self):
        """General modules are always in the result regardless of similarity."""
        bing = _mock_module("bing", result_type="general")
        baidu = _mock_module("baidu", result_type="general")
        arxiv = _mock_module(
            "arxiv", result_type="papers", scope="Academic papers",
        )

        # Even though arxiv has zero similarity, baseline stays
        scope_vecs = _normalized(np.array([[0.0, 0.0, 1.0]], dtype=np.float32))
        query_vec = _normalized(np.array([[1.0, 0.0, 0.0]], dtype=np.float32))
        embedder = _mock_embedder(scope_vecs, query_vec)

        router = EmbeddingRouter(
            modules=[bing, baidu, arxiv], embedder=embedder, top_k=1,
        )
        result = router.route("restaurant near me")

        assert "bing" in result
        assert "baidu" in result
        assert "arxiv" in result  # top_k=1 and only 1 routable, so included

    def test_top_k_larger_than_routable_returns_all(self):
        """When top_k >= routable count, all modules returned without embedding."""
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")

        embedder = MagicMock()  # Should NOT be called
        router = EmbeddingRouter(
            modules=[bing, arxiv], embedder=embedder, top_k=5,
        )
        result = router.route("anything")

        assert set(result) == {"bing", "arxiv"}
        embedder.embed.assert_not_called()


class TestUnclassifiedModules:
    """Modules without manifests are always included."""

    def test_no_manifest_always_included(self):
        bing = _mock_module("bing", result_type="general")
        custom = _mock_module_no_manifest("custom_engine")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")

        scope_vecs = _normalized(np.array([[1.0, 0.0]], dtype=np.float32))
        query_vec = _normalized(np.array([[1.0, 0.0]], dtype=np.float32))
        embedder = _mock_embedder(scope_vecs, query_vec)

        router = EmbeddingRouter(
            modules=[bing, custom, arxiv], embedder=embedder, top_k=1,
        )
        result = router.route("test query")

        assert "custom_engine" in result
        assert "bing" in result
        assert "arxiv" in result


class TestGracefulDegradation:
    """Router falls back to all modules on failure."""

    def test_embedder_scope_failure_returns_all(self):
        """When scope embedding fails, return all modules."""
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")
        github = _mock_module("github", result_type="code", scope="Code")

        embedder = MagicMock()
        embedder.embed.side_effect = RuntimeError("ONNX not available")

        router = EmbeddingRouter(
            modules=[bing, arxiv, github], embedder=embedder, top_k=1,
        )
        result = router.route("test query")

        assert set(result) == {"bing", "arxiv", "github"}

    def test_embedder_query_failure_returns_all(self):
        """When query embedding fails (after scope succeeds), return all."""
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")
        github = _mock_module("github", result_type="code", scope="Code")

        call_count = 0

        def embed_side_effect(texts):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                # Scope embeddings succeed
                return _normalized(np.array([
                    [1.0, 0.0], [0.0, 1.0],
                ], dtype=np.float32))
            # Query embedding fails
            raise RuntimeError("GPU OOM")

        embedder = MagicMock()
        embedder.embed.side_effect = embed_side_effect

        router = EmbeddingRouter(
            modules=[bing, arxiv, github], embedder=embedder, top_k=1,
        )
        result = router.route("test query")

        assert set(result) == {"bing", "arxiv", "github"}


class TestEdgeCases:
    """Edge cases and boundary conditions."""

    def test_all_general_modules(self):
        """When all modules are general (baseline), return all."""
        bing = _mock_module("bing", result_type="general")
        baidu = _mock_module("baidu", result_type="general")
        ddg = _mock_module("duckduckgo", result_type="general")

        embedder = MagicMock()
        router = EmbeddingRouter(
            modules=[bing, baidu, ddg], embedder=embedder, top_k=2,
        )
        result = router.route("anything")

        assert set(result) == {"bing", "baidu", "duckduckgo"}
        embedder.embed.assert_not_called()

    def test_empty_modules(self):
        """Router with no modules returns empty list."""
        embedder = MagicMock()
        router = EmbeddingRouter(modules=[], embedder=embedder, top_k=3)
        result = router.route("test")

        assert result == []

    def test_single_routable_top_k_1(self):
        """Single routable module with top_k=1."""
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")

        # top_k=1 >= 1 routable, so all returned without embedding
        embedder = MagicMock()
        router = EmbeddingRouter(
            modules=[bing, arxiv], embedder=embedder, top_k=1,
        )
        result = router.route("test")

        assert set(result) == {"bing", "arxiv"}
        embedder.embed.assert_not_called()

    def test_scope_embeddings_cached_across_calls(self):
        """Scope embeddings computed only once, reused across route() calls."""
        bing = _mock_module("bing", result_type="general")
        arxiv = _mock_module("arxiv", result_type="papers", scope="Papers")
        github = _mock_module("github", result_type="code", scope="Code")

        # Fixed embeddings for deterministic test
        scope_vecs = _normalized(np.array([
            [1.0, 0.0], [0.0, 1.0],
        ], dtype=np.float32))
        query_vec = _normalized(np.array([[0.9, 0.1]], dtype=np.float32))

        embedder = _mock_embedder_simple(scope_vecs)
        # Override: first call returns scope, subsequent return query
        call_count = 0

        def embed_side_effect(texts):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return scope_vecs
            return query_vec

        embedder.embed.side_effect = embed_side_effect

        router = EmbeddingRouter(
            modules=[bing, arxiv, github], embedder=embedder, top_k=1,
        )

        # Call route() twice
        router.route("first query")
        router.route("second query")

        # embed() called 3 times: 1 scope + 2 queries (not 2 scope + 2 query)
        assert embedder.embed.call_count == 3
