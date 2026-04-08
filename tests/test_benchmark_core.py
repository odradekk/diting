"""Tests for benchmark.core.

The benchmark package lives outside the main package and is gitignored.
These tests are only runnable in a local dev checkout that has the
benchmark/ directory present.
"""

from __future__ import annotations

import pytest

benchmark_core = pytest.importorskip("benchmark.core", reason="benchmark/ package not present")
BenchmarkScoringModel = benchmark_core.BenchmarkScoringModel
load_llm_runtime_from_env = benchmark_core.load_llm_runtime_from_env


class TestBenchmarkScoringModel:
    def test_score_pairs_groups_by_query_and_preserves_order(self):
        model = BenchmarkScoringModel(backend="reranker")

        calls: list[tuple[str, list[str]]] = []

        def fake_rerank(query: str, docs: list[str]) -> list[float]:
            calls.append((query, docs))
            return [float(len(doc)) for doc in docs]

        model._reranker.rerank = fake_rerank  # type: ignore[method-assign]

        scores = model.score_pairs(
            ["q1", "q2", "q1"],
            ["aaa", "b", "cc"],
        )

        assert scores == [3.0, 1.0, 2.0]
        assert calls == [
            ("q1", ["aaa", "cc"]),
            ("q2", ["b"]),
        ]


class TestLoadLLMRuntimeFromEnv:
    def test_missing_env_raises(self, monkeypatch: pytest.MonkeyPatch):
        monkeypatch.delenv("LLM_REASONING_BASE_URL", raising=False)
        monkeypatch.delenv("LLM_REASONING_API_KEY", raising=False)
        monkeypatch.delenv("LLM_REASONING_MODEL", raising=False)

        with pytest.raises(ValueError):
            load_llm_runtime_from_env()

    def test_loads_from_env(self, monkeypatch: pytest.MonkeyPatch):
        monkeypatch.setenv("LLM_REASONING_BASE_URL", "https://api.example.com/v1")
        monkeypatch.setenv("LLM_REASONING_API_KEY", "sk-test")
        monkeypatch.setenv("LLM_REASONING_MODEL", "reasoning-model")
        monkeypatch.setenv("LLM_TIMEOUT", "120")
        monkeypatch.setenv("LLM_MAX_TOKENS", "4096")

        config = load_llm_runtime_from_env()

        assert config.base_url == "https://api.example.com/v1"
        assert config.api_key == "sk-test"
        assert config.model == "reasoning-model"
        assert config.timeout == 120
        assert config.max_tokens == 4096
