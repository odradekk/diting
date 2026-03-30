"""Tests for supersearch.models — Pydantic v2 data models."""

from __future__ import annotations

import json

import pytest
from pydantic import ValidationError

from supersearch.models import (
    Category,
    ModuleError,
    ModuleOutput,
    ScoredResult,
    SearchMetadata,
    SearchResponse,
    SearchResult,
    Source,
)


# ---------------------------------------------------------------------------
# SearchResult
# ---------------------------------------------------------------------------

class TestSearchResult:
    def test_search_result_creation(self) -> None:
        result = SearchResult(
            title="Example Page",
            url="https://example.com",
            snippet="A short description of the page.",
        )
        assert result.title == "Example Page"
        assert result.url == "https://example.com"
        assert result.snippet == "A short description of the page."


# ---------------------------------------------------------------------------
# ModuleError
# ---------------------------------------------------------------------------

class TestModuleError:
    def test_module_error_fields(self) -> None:
        err = ModuleError(code="TIMEOUT", message="Request timed out", retryable=True)
        assert err.code == "TIMEOUT"
        assert err.message == "Request timed out"
        assert err.retryable is True

    def test_module_error_non_retryable(self) -> None:
        err = ModuleError(code="API_ERROR", message="Invalid key", retryable=False)
        assert err.retryable is False


# ---------------------------------------------------------------------------
# ModuleOutput
# ---------------------------------------------------------------------------

class TestModuleOutput:
    def test_module_output_success(self) -> None:
        result = SearchResult(
            title="Page",
            url="https://example.com",
            snippet="snippet",
        )
        output = ModuleOutput(module="google", results=[result])
        assert output.module == "google"
        assert len(output.results) == 1
        assert output.error is None

    def test_module_output_error(self) -> None:
        err = ModuleError(code="RATE_LIMITED", message="Too many requests", retryable=True)
        output = ModuleOutput(module="bing", results=[], error=err)
        assert output.module == "bing"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "RATE_LIMITED"


# ---------------------------------------------------------------------------
# ScoredResult
# ---------------------------------------------------------------------------

class TestScoredResult:
    def test_scored_result_score_range(self) -> None:
        scored = ScoredResult(
            url="https://example.com",
            relevance=0.9,
            quality=0.8,
            final_score=0.85,
            reason="Authoritative source with direct answer.",
        )
        assert scored.relevance == 0.9
        assert scored.quality == 0.8
        assert scored.final_score == 0.85
        assert scored.reason == "Authoritative source with direct answer."

    def test_scored_result_boundary_zero(self) -> None:
        scored = ScoredResult(
            url="https://example.com",
            relevance=0.0,
            quality=0.0,
            final_score=0.0,
            reason="Irrelevant.",
        )
        assert scored.relevance == 0.0

    def test_scored_result_boundary_one(self) -> None:
        scored = ScoredResult(
            url="https://example.com",
            relevance=1.0,
            quality=1.0,
            final_score=1.0,
            reason="Perfect match.",
        )
        assert scored.final_score == 1.0

    def test_scored_result_invalid_score_too_high(self) -> None:
        with pytest.raises(ValidationError):
            ScoredResult(
                url="https://example.com",
                relevance=1.5,
                quality=0.8,
                final_score=0.85,
                reason="Bad relevance.",
            )

    def test_scored_result_invalid_score_too_low(self) -> None:
        with pytest.raises(ValidationError):
            ScoredResult(
                url="https://example.com",
                relevance=0.5,
                quality=-0.1,
                final_score=0.3,
                reason="Bad quality.",
            )

    def test_scored_result_invalid_final_score(self) -> None:
        with pytest.raises(ValidationError):
            ScoredResult(
                url="https://example.com",
                relevance=0.5,
                quality=0.5,
                final_score=2.0,
                reason="Bad final_score.",
            )


# ---------------------------------------------------------------------------
# Source
# ---------------------------------------------------------------------------

class TestSource:
    def test_source_creation(self) -> None:
        source = Source(
            title="Example",
            url="https://example.com/page?ref=abc",
            normalized_url="https://example.com/page",
            snippet="A relevant snippet.",
            score=0.85,
            source_module="google",
            domain="example.com",
        )
        assert source.title == "Example"
        assert source.url == "https://example.com/page?ref=abc"
        assert source.normalized_url == "https://example.com/page"
        assert source.snippet == "A relevant snippet."
        assert source.score == 0.85
        assert source.source_module == "google"
        assert source.domain == "example.com"


# ---------------------------------------------------------------------------
# Category
# ---------------------------------------------------------------------------

class TestCategory:
    def test_category_creation(self) -> None:
        source = Source(
            title="Doc",
            url="https://docs.example.com",
            normalized_url="https://docs.example.com",
            snippet="Official documentation.",
            score=0.9,
            source_module="bing",
            domain="docs.example.com",
        )
        cat = Category(name="Documentation", sources=[source])
        assert cat.name == "Documentation"
        assert len(cat.sources) == 1
        assert cat.sources[0].title == "Doc"

    def test_category_empty_sources(self) -> None:
        cat = Category(name="Empty", sources=[])
        assert cat.sources == []


# ---------------------------------------------------------------------------
# SearchMetadata
# ---------------------------------------------------------------------------

class TestSearchMetadata:
    def test_search_metadata(self) -> None:
        meta = SearchMetadata(
            request_id="abc-123",
            query="python pydantic v2 guide",
            rounds=2,
            total_sources_found=45,
            sources_after_dedup=30,
            sources_after_filter=15,
            elapsed_ms=8500,
        )
        assert meta.request_id == "abc-123"
        assert meta.query == "python pydantic v2 guide"
        assert meta.rounds == 2
        assert meta.total_sources_found == 45
        assert meta.sources_after_dedup == 30
        assert meta.sources_after_filter == 15
        assert meta.elapsed_ms == 8500


# ---------------------------------------------------------------------------
# SearchResponse
# ---------------------------------------------------------------------------

class TestSearchResponse:
    @pytest.fixture()
    def full_response(self) -> SearchResponse:
        source = Source(
            title="Example",
            url="https://example.com",
            normalized_url="https://example.com",
            snippet="Snippet.",
            score=0.85,
            source_module="google",
            domain="example.com",
        )
        meta = SearchMetadata(
            request_id="req-001",
            query="test query",
            rounds=1,
            total_sources_found=10,
            sources_after_dedup=8,
            sources_after_filter=5,
            elapsed_ms=3000,
        )
        return SearchResponse(
            status="success",
            summary="A concise summary of results.",
            categories=[Category(name="General", sources=[source])],
            metadata=meta,
            warnings=["Fetch failed for 1 URL"],
            errors=[],
        )

    def test_search_response_success(self, full_response: SearchResponse) -> None:
        assert full_response.status == "success"
        assert full_response.summary == "A concise summary of results."
        assert len(full_response.categories) == 1
        assert full_response.categories[0].name == "General"
        assert full_response.metadata.request_id == "req-001"
        assert full_response.warnings == ["Fetch failed for 1 URL"]
        assert full_response.errors == []

    def test_search_response_defaults(self) -> None:
        meta = SearchMetadata(
            request_id="req-002",
            query="defaults test",
            rounds=1,
            total_sources_found=0,
            sources_after_dedup=0,
            sources_after_filter=0,
            elapsed_ms=100,
        )
        resp = SearchResponse(status="no_results", metadata=meta)
        assert resp.summary == ""
        assert resp.categories == []
        assert resp.warnings == []
        assert resp.errors == []

    def test_search_response_json_serialization(
        self, full_response: SearchResponse
    ) -> None:
        raw = full_response.model_dump_json()
        data = json.loads(raw)

        # Top-level keys match Design.md schema
        assert set(data.keys()) == {
            "status",
            "summary",
            "categories",
            "metadata",
            "warnings",
            "errors",
        }

        # Nested category / source structure
        cat = data["categories"][0]
        assert "name" in cat
        assert "sources" in cat
        src = cat["sources"][0]
        for key in (
            "title",
            "url",
            "normalized_url",
            "snippet",
            "score",
            "source_module",
            "domain",
        ):
            assert key in src, f"Missing key '{key}' in serialized source"

        # Metadata structure
        meta = data["metadata"]
        for key in (
            "request_id",
            "query",
            "rounds",
            "total_sources_found",
            "sources_after_dedup",
            "sources_after_filter",
            "elapsed_ms",
        ):
            assert key in meta, f"Missing key '{key}' in serialized metadata"
