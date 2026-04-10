"""arXiv academic paper search module."""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule
from diting.modules.manifest import ModuleManifest

_BASE_URL = "https://export.arxiv.org/api/query"
_ATOM_NS = "{http://www.w3.org/2005/Atom}"
_SNIPPET_MAX_LEN = 500


class ArxivSearchModule(BaseSearchModule):
    """Search module backed by the arXiv API.

    Sends search queries to the arXiv Atom API and converts
    the response entries into a list of :class:`SearchResult` objects.
    """

    MANIFEST = ModuleManifest(
        domains=["academic", "papers"],
        languages=["en", "*"],
        cost_tier="free",
        latency_tier="medium",
        result_type="papers",
        scope=(
            "Academic preprints from arXiv covering physics, mathematics, "
            "computer science, quantitative biology, statistics, and more. "
            "Strong for cutting-edge research papers and technical reports. "
            "Weak for general knowledge or non-academic content."
        ),
    )

    def __init__(self, timeout: int = 15, max_results: int = 20) -> None:
        super().__init__(name="arxiv", timeout=timeout, max_results=max_results)
        self._http = httpx.AsyncClient(timeout=None)

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call the arXiv API and return parsed results."""
        self._logger.debug("Querying arXiv API: query=%r, max_results=%d", query, self._max_results)

        params: dict[str, str | int] = {
            "search_query": f"all:{query}",
            "start": 0,
            "max_results": self._max_results,
            "sortBy": "relevance",
            "sortOrder": "descending",
        }

        response = await self._http.get(_BASE_URL, params=params)
        response.raise_for_status()

        root = ET.fromstring(response.text)

        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()

        for entry in root.findall(f"{_ATOM_NS}entry"):
            title = entry.findtext(f"{_ATOM_NS}title", "").strip()
            # Collapse newlines and multiple spaces into single spaces
            title = re.sub(r"\s+", " ", title)

            url = entry.findtext(f"{_ATOM_NS}id", "").strip()

            summary = entry.findtext(f"{_ATOM_NS}summary", "").strip()
            summary = re.sub(r"\s+", " ", summary)
            snippet = summary[:_SNIPPET_MAX_LEN]

            if title and url and url not in seen_urls:
                seen_urls.add(url)
                all_results.append(
                    SearchResult(title=title, url=url, snippet=snippet)
                )

        self._logger.debug("arXiv API returned %d results", len(all_results))
        return all_results[:self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
