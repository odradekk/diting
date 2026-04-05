"""Declarative capability manifest for search modules.

A :class:`ModuleManifest` describes what a search module is good at — its
topical coverage, language support, cost and latency tiers, and representative
example queries.  The router (Phase 5) consults manifests to decide which
modules to invoke for a given user query, avoiding the waste of firing every
enabled module on every request.

Manifests are **class-level declarations**: they must be inspectable without
instantiating the module (since some modules require API keys or cookies to
construct).  Concrete subclasses of :class:`BaseSearchModule` must assign
``MANIFEST`` as a :class:`typing.ClassVar`.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, Field

# ---------------------------------------------------------------------------
# Enum types
# ---------------------------------------------------------------------------

CostTier = Literal["free", "cheap", "expensive"]
"""Per-call cost category.

- ``free``   — No API key required, no per-call charge (scraping, keyless APIs).
- ``cheap``  — Paid API with generous free tier (e.g. Brave 2000/month).
- ``expensive`` — Metered paid API where every call costs money (SerpAPI, Tavily).

The router uses this to gate expensive modules behind explicit signals:
expensive modules are never invoked in Round 1 without user override.
"""

LatencyTier = Literal["fast", "medium", "slow"]
"""Typical end-to-end response latency.

- ``fast``   — <2s (REST APIs, cached endpoints).
- ``medium`` — 2-10s (HTML scraping with parsing).
- ``slow``   — >10s (Playwright browser automation).
"""

ResultType = Literal[
    "general",
    "papers",
    "code",
    "qa",
    "news",
    "social",
    "entity",
]
"""Primary type of result this module returns.

- ``general`` — Heterogeneous web results (Bing, DuckDuckGo, Baidu).
- ``papers``  — Academic papers (arxiv, OpenAlex, Crossref).
- ``code``    — Source code and repositories (GitHub).
- ``qa``      — Question-and-answer content (Stack Exchange, Zhihu).
- ``news``    — Time-sensitive news articles.
- ``social``  — Social media posts (X, Reddit).
- ``entity``  — Structured entity data (Wikipedia, Wikidata).
"""


# ---------------------------------------------------------------------------
# Manifest model
# ---------------------------------------------------------------------------


class ModuleManifest(BaseModel):
    """Declarative capability manifest for a search module.

    Routers consume manifests to match queries to appropriate modules.
    The ``scope`` field is the primary signal for LLM and embedding routing:
    a concise natural-language description of what this module excels at,
    kept focused on search semantics (no operational noise like pricing or
    auth requirements).
    """

    domains: list[str] = Field(
        description=(
            "Topical domains this module covers, e.g. ['academic', 'science'] "
            "or ['general']. Free-form tags consumed by the router."
        ),
    )
    languages: list[str] = Field(
        description=(
            "ISO 639-1 language codes supported, e.g. ['en', 'zh']. "
            "Use ['*'] for language-agnostic modules."
        ),
    )
    cost_tier: CostTier = Field(
        description="Per-call cost category. See :data:`CostTier`.",
    )
    latency_tier: LatencyTier = Field(
        description="Typical response latency. See :data:`LatencyTier`.",
    )
    result_type: ResultType = Field(
        description="Primary result type. See :data:`ResultType`.",
    )
    scope: str = Field(
        description=(
            "Concise natural-language description of what this module is good "
            "at searching for. Used by the LLM router and embedding router to "
            "match queries to modules. Keep focused on search semantics — "
            "avoid operational noise (pricing, auth, anti-crawling, etc.)."
        ),
    )

    model_config = {"frozen": True}
