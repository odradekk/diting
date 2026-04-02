"""Diting MCP server — exposes search and fetch tools via FastMCP."""

from __future__ import annotations

import logging
import subprocess

from fastmcp import FastMCP, Context
from fastmcp.server.lifespan import lifespan

from playwright.async_api import async_playwright

from diting.config import Settings
from diting.fetch.composite import CompositeFetcher
from diting.fetch.local import LocalFetcher
from diting.fetch.tavily import FetchError, TavilyFetcher
from diting.llm.client import LLMClient
from diting.llm.prompts import PromptLoader
from diting.log import setup_logging
from diting.models import SearchResponse
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
    setup_logging(settings.LOG_LEVEL)

    llm = LLMClient(
        base_url=settings.LLM_BASE_URL,
        api_key=settings.LLM_API_KEY,
        model=settings.LLM_MODEL,
        timeout=settings.LLM_TIMEOUT,
    )
    prompts = PromptLoader(prompts_dir=settings.PROMPTS_DIR)

    # Browser for local fetcher (persistent across requests).
    pw = await async_playwright().start()
    try:
        browser = await pw.chromium.launch(headless=True)
    except Exception:
        logger.info("Chromium not found, installing via Playwright...")
        subprocess.run(["playwright", "install", "chromium"], check=True)
        browser = await pw.chromium.launch(headless=True)

    local_fetcher = LocalFetcher(browser=browser)
    if settings.TAVILY_API_KEY:
        tavily_fetcher = TavilyFetcher(api_key=settings.TAVILY_API_KEY)
        fetcher = CompositeFetcher(primary=local_fetcher, fallback=tavily_fetcher)
    else:
        fetcher = local_fetcher

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
        modules.append(XSearchModule(cookie=settings.X_COOKIE, max_results=mr))
    if settings.ENABLE_ZHIHU:
        modules.append(ZhihuSearchModule(cookie=settings.ZHIHU_COOKIE, max_results=mr))

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
async def search(query: str, ctx: Context) -> SearchResponse:
    """Run a deep aggregated search across multiple engines.

    Args:
        query: Natural language search query.

    Returns:
        Structured search response with scored sources and summary.
    """
    orchestrator: Orchestrator = ctx.lifespan_context["orchestrator"]
    return await orchestrator.search(query)


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
