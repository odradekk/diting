"""Diting MCP server — exposes search and fetch tools via FastMCP."""

from __future__ import annotations

import json
import logging
import subprocess

from fastmcp import FastMCP, Context
from fastmcp.server.lifespan import lifespan

from diting.config import Settings
from diting.fetch.archive import ArchiveFetcher
from diting.fetch.base import Fetcher
from diting.fetch.browser_driver import get_async_playwright
from diting.fetch.cache import ContentCache, default_cache_path
from diting.fetch.cached import CachedFetcher
from diting.fetch.composite import FetchLayer, chain_fetchers
from diting.fetch.jina_reader import JinaReaderFetcher
from diting.fetch.local import LocalFetcher
from diting.fetch.tavily import FetchError, TavilyFetcher
from diting.llm.client import LLMClient
from diting.llm.prompts import PromptLoader
from diting.log import setup_logging
from diting.modules.baidu import BaiduSearchModule
from diting.modules.bing import BingSearchModule
from diting.modules.brave import BraveSearchModule
from diting.modules.duckduckgo import DuckDuckGoSearchModule
from diting.modules.serp import SerpSearchModule
from diting.modules.x import XSearchModule
from diting.modules.zhihu import ZhihuSearchModule
from diting.pipeline.orchestrator import Orchestrator

logger = logging.getLogger("diting.server")


@lifespan
async def app_lifespan(server: FastMCP):
    """Initialise shared resources on startup and clean them up on shutdown."""
    settings = Settings()
    setup_logging(settings.LOG_LEVEL, fmt=settings.LOG_FORMAT)

    llm = LLMClient(
        base_url=settings.LLM_BASE_URL,
        api_key=settings.LLM_API_KEY,
        model=settings.LLM_MODEL,
        timeout=settings.LLM_TIMEOUT,
        max_tokens=settings.LLM_MAX_TOKENS,
    )
    prompts = PromptLoader(prompts_dir=settings.PROMPTS_DIR)

    # Browser for local fetcher (persistent across requests).  Uses
    # patchright when ENABLE_STEALTH_BROWSER=true and the [stealth] extra
    # is installed; otherwise falls back to vanilla Playwright.
    async_playwright = get_async_playwright(
        prefer_stealth=settings.ENABLE_STEALTH_BROWSER,
    )
    pw = await async_playwright().start()
    try:
        browser = await pw.chromium.launch(headless=True)
    except Exception:
        logger.info("Chromium not found, installing via Playwright...")
        subprocess.run(["playwright", "install", "chromium"], check=True)
        browser = await pw.chromium.launch(headless=True)

    # Build the fetch fallback chain.  Layers are added in order of
    # preference: local (curl_cffi + browser + stealth + cookies) →
    # r.jina.ai → archive snapshots → Tavily.  Each non-local layer
    # gets an independent timeout so a stalled service can never hold
    # the whole chain hostage.  Disabled / unconfigured layers are
    # skipped.
    chain_layers: list[FetchLayer] = [
        FetchLayer(
            fetcher=LocalFetcher(browser=browser),
            name="local",
            # LocalFetcher orchestrates its own per-URL timeouts inside
            # curl_cffi + Playwright; a wrapping timeout here would just
            # cut one strategy short without letting the next try.
            timeout=None,
        ),
    ]
    if settings.ENABLE_JINA_READER:
        chain_layers.append(FetchLayer(
            fetcher=JinaReaderFetcher(api_key=settings.JINA_API_KEY),
            name="jina",
            timeout=20.0,
        ))
    if settings.ENABLE_ARCHIVE_FALLBACK:
        chain_layers.append(FetchLayer(
            fetcher=ArchiveFetcher(),
            name="archive",
            timeout=25.0,
        ))
    if settings.TAVILY_API_KEY:
        chain_layers.append(FetchLayer(
            fetcher=TavilyFetcher(api_key=settings.TAVILY_API_KEY),
            name="tavily",
            timeout=30.0,
        ))
    fetcher: Fetcher = chain_fetchers(chain_layers)
    logger.info(
        "Fetch fallback chain: %s",
        " -> ".join(
            f"{layer.name}({layer.timeout}s)" if layer.timeout
            else layer.name
            for layer in chain_layers
        ),
    )

    # Wrap with a read-through / write-through content cache.  The cache
    # itself is owned by the lifespan so we can close it on shutdown.
    content_cache: ContentCache | None = None
    if settings.DITING_CACHE_ENABLED:
        cache_path = (
            settings.DITING_CACHE_PATH if settings.DITING_CACHE_PATH
            else str(default_cache_path())
        )
        content_cache = ContentCache(cache_path)
        fetcher = CachedFetcher(fetcher, content_cache)
        logger.info("Content cache enabled at %s", cache_path)

    # Wrap with a read-through / write-through content cache.  The cache
    # itself is owned by the lifespan so we can close it on shutdown.
    content_cache: ContentCache | None = None
    if settings.DITING_CACHE_ENABLED:
        cache_path = (
            settings.DITING_CACHE_PATH if settings.DITING_CACHE_PATH
            else str(default_cache_path())
        )
        content_cache = ContentCache(cache_path)
        fetcher = CachedFetcher(fetcher, content_cache)
        logger.info("Content cache enabled at %s", cache_path)

    modules = []
    mr = settings.MAX_RESULTS
    if settings.ENABLE_BAIDU:
        modules.append(BaiduSearchModule(timeout=settings.MODULE_TIMEOUT, max_results=mr))
    if settings.ENABLE_BING:
        modules.append(BingSearchModule(timeout=settings.MODULE_TIMEOUT, max_results=mr))
    if settings.ENABLE_BRAVE and settings.BRAVE_API_KEY:
        modules.append(
            BraveSearchModule(
                api_key=settings.BRAVE_API_KEY, timeout=settings.MODULE_TIMEOUT, max_results=mr,
            )
        )
    if settings.ENABLE_DUCKDUCKGO:
        modules.append(DuckDuckGoSearchModule(timeout=settings.MODULE_TIMEOUT, max_results=mr))
    if settings.ENABLE_SERP and settings.SERP_API_KEY:
        modules.append(
            SerpSearchModule(
                api_key=settings.SERP_API_KEY, timeout=settings.MODULE_TIMEOUT, max_results=mr,
            )
        )
    if settings.ENABLE_X:
        modules.append(XSearchModule(
            cookie=settings.X_COOKIE, max_results=mr,
            stealth=settings.ENABLE_STEALTH_BROWSER,
        ))
    if settings.ENABLE_ZHIHU:
        modules.append(ZhihuSearchModule(
            cookie=settings.ZHIHU_COOKIE, max_results=mr,
            stealth=settings.ENABLE_STEALTH_BROWSER,
        ))

    if not modules:
        logger.warning("No search modules enabled — check API key settings")

    orchestrator = Orchestrator(
        llm=llm,
        prompts=prompts,
        modules=modules,
        max_rounds=settings.MAX_SEARCH_ROUNDS,
        global_timeout=settings.GLOBAL_TIMEOUT,
        score_threshold=settings.SCORE_THRESHOLD,
        fetcher=fetcher,
        min_snippet_length=settings.MIN_SNIPPET_LENGTH,
        blacklist_file=settings.BLACKLIST_FILE,
        auto_blacklist=settings.AUTO_BLACKLIST,
        auto_blacklist_threshold=settings.AUTO_BLACKLIST_THRESHOLD,
        relevance_weight=settings.RELEVANCE_WEIGHT,
        quality_weight=settings.QUALITY_WEIGHT,
        max_concurrency=settings.MAX_CONCURRENCY,
    )

    yield {"orchestrator": orchestrator, "fetcher": fetcher}

    for m in modules:
        await m.close()
    await fetcher.close()
    if content_cache is not None:
        content_cache.close()
    await browser.close()
    await pw.stop()
    await llm.close()


mcp = FastMCP(
    name="Diting",
    instructions=(
        "Deep aggregated search service. Use the 'search' tool for"
        " natural-language queries and the 'fetch' tool to retrieve the full"
        " text content of a URL."
    ),
    lifespan=app_lifespan,
)


@mcp.tool
async def search(query: str, ctx: Context) -> dict:
    """Run a deep aggregated search across multiple engines.

    Args:
        query: Natural language search query.

    Returns:
        Compressed dict with status, summary, and sources (title/url/snippet).
    """
    orchestrator: Orchestrator = ctx.lifespan_context["orchestrator"]
    response = await orchestrator.search(query)

    # Compress output for the consuming LLM — drop internal pipeline
    # fields (metadata, warnings, errors, normalized_url, score, domain)
    # to reduce token usage by ~60-70%.
    compressed = {
        "status": response.status,
        "summary": response.summary,
        "sources": [
            {
                "title": s.title,
                "url": s.url,
                "snippet": s.snippet,
            }
            for s in response.sources
        ],
    }

    if logger.isEnabledFor(logging.INFO):
        compact_json = json.dumps(compressed, ensure_ascii=False)
        full_len = len(json.dumps(response.model_dump(), ensure_ascii=False))
        compact_len = len(compact_json)
        logger.info(
            "Response compression: %d → %d chars (%.0f%% reduction)",
            full_len, compact_len, (1 - compact_len / full_len) * 100 if full_len else 0,
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug("Compressed response:\n%s", compact_json)

    return compressed


@mcp.tool
async def fetch(url: str, ctx: Context) -> str:
    """Fetch the full text content of a URL.

    Args:
        url: The URL to fetch content from.

    Returns:
        The extracted text content of the page.
    """
    fetcher = ctx.lifespan_context["fetcher"]
    try:
        return await fetcher.fetch(url)
    except FetchError as exc:
        return str(exc)


def main() -> None:
    """Entry point for the ``diting`` console script."""
    mcp.run()


if __name__ == "__main__":
    main()
