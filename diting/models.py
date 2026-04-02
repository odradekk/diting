"""
Pydantic v2 data models for Super Search MCP.
{
  "status": "success",
  "summary": "",
  "sources": [
    {
      "title": "Pydantic V2 Migration Guide",
      "url": "https://docs.pydantic.dev/latest/migration/",
      "normalized_url": "docs.pydantic.dev/latest/migration",
      "snippet": "Learn how to migrate your code from Pydantic V1 to V2...",
      "score": 0.98,
      "source_module": "google_search",
      "domain": "docs.pydantic.dev"
    }
  ],
  "metadata": {
    "request_id": "req-987654321",
    "query": "Pydantic v2 data models",
    "rounds": 2,
    "total_sources_found": 15,
    "sources_after_dedup": 12,
    "sources_after_filter": 5,
    "elapsed_ms": 1450
  },
  "warnings": [],
  "errors": []
}
"""

from __future__ import annotations
from pydantic import BaseModel, Field

class SearchResult(BaseModel):
    """Raw search result returned by individual search modules (Baidu, Bing, Brave, etc.)."""

    title: str = Field(description="Page title extracted from the search engine result")
    url: str = Field(description="Original URL of the search result")
    snippet: str = Field(description="Text snippet or description shown by the search engine")
    source_module: str = Field(default="", description="Name of the search module that produced this result")


class ModuleError(BaseModel):
    """Structured error captured when a search module fails during execution."""

    code: str = Field(description="Machine-readable error code, e.g. 'TIMEOUT', 'HTTP_403'")
    message: str = Field(description="Human-readable error description")
    retryable: bool = Field(description="Whether the orchestrator should retry this module in a subsequent round")


class ModuleOutput(BaseModel):
    """Output from a single search module execution.

    Each module (e.g. ``BaiduSearchModule``) returns one ``ModuleOutput`` per round,
    containing zero or more results and an optional error if the module failed.
    """

    module: str = Field(description="Name of the search module that produced this output, e.g. 'brave'")
    results: list[SearchResult] = Field(description="Search results collected in this round")
    error: ModuleError | None = Field(default=None, description="Non-None when the module encountered an error")


class ScoredResult(BaseModel):
    """Search result after LLM relevance/quality scoring.

    The orchestrator sends deduplicated results to the LLM for evaluation.
    ``final_score`` is used to filter results against ``SCORE_THRESHOLD``.
    """

    url: str = Field(description="URL being scored")
    relevance: float = Field(ge=0, le=1, description="How relevant the result is to the query (0–1)")
    quality: float = Field(ge=0, le=1, description="Content quality assessment (0–1)")
    final_score: float = Field(ge=0, le=1, description="Combined score used for filtering and ranking")
    reason: str = Field(description="LLM-generated explanation for the assigned scores")


class Source(BaseModel):
    """Enriched source in the final search response, ready for client consumption.

    Combines the original ``SearchResult`` data with scoring and normalization
    performed by the orchestrator pipeline.
    """

    title: str = Field(description="Page title")
    url: str = Field(description="Original URL")
    normalized_url: str = Field(description="URL after scheme/trailing-slash normalization, used for deduplication")
    snippet: str = Field(description="Content snippet, fetched or extracted from the search engine")
    score: float = Field(description="Final relevance score assigned by the LLM scorer")
    source_module: str = Field(description="Name of the search module that originally found this result")
    domain: str = Field(description="Domain extracted from the URL, e.g. 'docs.pydantic.dev'")


class SearchMetadata(BaseModel):
    """Metadata tracking the search execution pipeline for observability."""

    request_id: str = Field(description="Unique identifier for this search request")
    query: str = Field(description="Original user query")
    rounds: int = Field(description="Number of search rounds actually executed (≤ MAX_SEARCH_ROUNDS)")
    total_sources_found: int = Field(description="Total raw results collected across all modules and rounds")
    sources_after_dedup: int = Field(description="Results remaining after URL normalization and deduplication")
    sources_after_filter: int = Field(description="Results remaining after LLM scoring and SCORE_THRESHOLD filtering")
    elapsed_ms: int = Field(description="Wall-clock time for the entire search pipeline in milliseconds")


class SearchResponse(BaseModel):
    """Top-level structured output returned by the ``search`` MCP tool.

    See the module-level JSON example for the complete response shape.
    """

    status: str = Field(description="'success' or 'error'")
    summary: str = Field(default="", description="LLM-generated natural-language summary of the search results")
    sources: list[Source] = Field(default_factory=list, description="Scored and ranked sources matching the query")
    metadata: SearchMetadata = Field(description="Pipeline execution statistics")
    warnings: list[str] = Field(default_factory=list, description="Non-fatal issues encountered during the search")
    errors: list[str] = Field(default_factory=list, description="Fatal errors that prevented some modules from returning results")
