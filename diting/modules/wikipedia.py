"""Wikipedia search module — English and Chinese Wikipedia."""

from __future__ import annotations

import asyncio
import re
import urllib.parse

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule
from diting.modules.manifest import ModuleManifest

_WIKI_API: dict[str, str] = {
    "en": "https://en.wikipedia.org/w/api.php",
    "zh": "https://zh.wikipedia.org/w/api.php",
}


class WikipediaSearchModule(BaseSearchModule):
    """Search module backed by the MediaWiki Action API.

    Queries both English and Chinese Wikipedia concurrently and
    interleaves results to balance cross-language coverage.
    """

    MANIFEST = ModuleManifest(
        domains=["encyclopedia", "general"],
        languages=["en", "zh", "*"],
        cost_tier="free",
        latency_tier="fast",
        result_type="entity",
        scope=(
            "Encyclopedia articles from English and Chinese Wikipedia. "
            "Strong for factual lookups, entity definitions, historical "
            "events, and scientific concepts. Not suitable for recent "
            "news or opinion content."
        ),
    )

    def __init__(self, timeout: int = 15, max_results: int = 20) -> None:
        super().__init__(name="wikipedia", timeout=timeout, max_results=max_results)
        self._http = httpx.AsyncClient(timeout=None)

    async def _fetch_wiki(self, lang: str, query: str) -> list[SearchResult]:
        """Fetch search results from a single Wikipedia language edition."""
        url = _WIKI_API[lang]
        params: dict[str, str | int] = {
            "action": "query",
            "list": "search",
            "srsearch": query,
            "format": "json",
            "utf8": 1,
            "srlimit": self._max_results,
        }

        response = await self._http.get(url, params=params)
        response.raise_for_status()

        data = response.json()
        raw_results = data.get("query", {}).get("search", [])

        results: list[SearchResult] = []
        for item in raw_results:
            title: str = item.get("title", "")
            snippet: str = re.sub(r"<[^>]+>", "", item.get("snippet", ""))
            if title:
                encoded = urllib.parse.quote(title.replace(" ", "_"), safe="/:()")
                page_url = f"https://{lang}.wikipedia.org/wiki/{encoded}"
                results.append(
                    SearchResult(title=title, url=page_url, snippet=snippet),
                )
        return results

    async def _execute(self, query: str) -> list[SearchResult]:
        """Query both English and Chinese Wikipedia, interleave results."""
        self._logger.debug(
            "Querying Wikipedia: query=%r, max_results=%d", query, self._max_results
        )

        tasks = {
            lang: self._fetch_wiki(lang, query)
            for lang in _WIKI_API
        }

        settled = await asyncio.gather(*tasks.values(), return_exceptions=True)
        wiki_results: dict[str, list[SearchResult]] = {}
        errors: dict[str, Exception] = {}
        for lang, result in zip(tasks, settled):
            if isinstance(result, Exception):
                self._logger.debug("Wikipedia %s failed: %s", lang, result)
                errors[lang] = result
                wiki_results[lang] = []
            else:
                wiki_results[lang] = result

        # If all backends failed, raise so BaseSearchModule emits ModuleError.
        if len(errors) == len(tasks):
            raise RuntimeError(
                "All Wikipedia backends failed: "
                + "; ".join(f"{lang}: {exc}" for lang, exc in errors.items())
            )

        en_results = wiki_results.get("en", [])
        zh_results = wiki_results.get("zh", [])

        # Interleave: en[0], zh[0], en[1], zh[1], ...
        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()
        max_len = max(len(en_results), len(zh_results))

        for i in range(max_len):
            for batch in (en_results, zh_results):
                if i < len(batch):
                    item = batch[i]
                    if item.url not in seen_urls:
                        seen_urls.add(item.url)
                        all_results.append(item)

        self._logger.debug("Wikipedia returned %d results", len(all_results))
        return all_results[: self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
