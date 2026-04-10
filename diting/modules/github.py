"""GitHub repository search module."""

from __future__ import annotations

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule
from diting.modules.manifest import ModuleManifest

_BASE_URL = "https://api.github.com/search/repositories"

# GitHub Search API caps per_page at 100.
_MAX_PER_PAGE = 100


class GitHubSearchModule(BaseSearchModule):
    """Search module backed by the GitHub Search API v3.

    Queries GitHub for repositories sorted by stars and converts the
    response into a list of :class:`SearchResult` objects.  Supports
    optional token-based authentication to raise the rate limit from
    10 req/min (unauthenticated) to 30 req/min (authenticated).
    """

    MANIFEST = ModuleManifest(
        domains=["code", "open-source"],
        languages=["en", "*"],
        cost_tier="free",
        latency_tier="fast",
        result_type="code",
        scope=(
            "GitHub repository search sorted by stars. Strong for finding "
            "open-source projects, libraries, frameworks, and developer "
            "tools. Weak for general knowledge, documentation content, "
            "or non-code resources."
        ),
    )

    def __init__(
        self, token: str = "", timeout: int = 15, max_results: int = 20
    ) -> None:
        super().__init__(name="github", timeout=timeout, max_results=max_results)
        self._token = token
        headers: dict[str, str] = {
            "Accept": "application/vnd.github+json",
            "User-Agent": "diting-search",
            "X-GitHub-Api-Version": "2022-11-28",
        }
        if token:
            headers["Authorization"] = f"Bearer {token}"
        # Let BaseSearchModule.search() own timeout via asyncio.wait_for;
        # disable httpx's own timeout so the two don't race.
        self._http = httpx.AsyncClient(headers=headers, timeout=None)

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call the GitHub Search API and return parsed results."""
        self._logger.debug(
            "Querying GitHub API: query=%r, max_results=%d",
            query,
            self._max_results,
        )

        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()

        params: dict[str, str | int] = {
            "q": query,
            "sort": "stars",
            "order": "desc",
            "per_page": min(self._max_results, _MAX_PER_PAGE),
            "page": 1,
        }

        response = await self._http.get(_BASE_URL, params=params)

        if response.status_code == 403:
            remaining = response.headers.get("X-RateLimit-Remaining", "")
            if remaining == "0":
                raise httpx.HTTPStatusError(
                    "GitHub API rate limit exceeded (X-RateLimit-Remaining: 0)",
                    request=response.request,
                    response=response,
                )
            response.raise_for_status()

        if response.status_code == 422:
            raise httpx.HTTPStatusError(
                "GitHub API validation error",
                request=response.request,
                response=response,
            )

        response.raise_for_status()

        data = response.json()
        items = data.get("items")
        if not items:
            self._logger.debug("GitHub API returned 0 results")
            return []

        for item in items:
            title = item.get("full_name", "")
            url = item.get("html_url", "")
            snippet = item.get("description") or ""
            if title and url and url not in seen_urls:
                seen_urls.add(url)
                all_results.append(
                    SearchResult(title=title, url=url, snippet=snippet)
                )

        self._logger.debug("GitHub API returned %d results", len(all_results))
        return all_results[: self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
