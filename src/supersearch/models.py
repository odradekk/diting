"""Pydantic v2 data models for Super Search MCP."""

from __future__ import annotations

from pydantic import BaseModel, Field


class SearchResult(BaseModel):
    """Basic search result returned by a search module."""

    title: str
    url: str
    snippet: str


class ModuleError(BaseModel):
    """Structured error from a search module."""

    code: str
    message: str
    retryable: bool


class ModuleOutput(BaseModel):
    """Output from a single search module execution."""

    module: str
    results: list[SearchResult]
    error: ModuleError | None = None


class ScoredResult(BaseModel):
    """Search result after LLM relevance/quality scoring."""

    url: str
    relevance: float = Field(ge=0, le=1)
    quality: float = Field(ge=0, le=1)
    final_score: float = Field(ge=0, le=1)
    reason: str


class Source(BaseModel):
    """Enriched source in the final search output."""

    title: str
    url: str
    normalized_url: str
    snippet: str
    score: float
    source_module: str
    domain: str


class Category(BaseModel):
    """Classification category containing scored sources."""

    name: str
    sources: list[Source]


class SearchMetadata(BaseModel):
    """Metadata about the search execution."""

    request_id: str
    query: str
    rounds: int
    total_sources_found: int
    sources_after_dedup: int
    sources_after_filter: int
    elapsed_ms: int


class SearchResponse(BaseModel):
    """Top-level structured output of a search request."""

    status: str
    summary: str = ""
    categories: list[Category] = []
    metadata: SearchMetadata
    warnings: list[str] = []
    errors: list[str] = []
