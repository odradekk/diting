"""One-shot e2e test — runs a real search and writes logs to test.log."""

import asyncio
import json
import logging
import sys

from supersearch.config import Settings
from supersearch.fetch.tavily import TavilyFetcher
from supersearch.llm.client import LLMClient
from supersearch.llm.prompts import PromptLoader
from supersearch.log import setup_logging
from supersearch.modules.brave import BraveSearchModule
from supersearch.modules.serp import SerpSearchModule
from supersearch.pipeline.orchestrator import Orchestrator


def configure_file_logging(path: str = "test.log") -> None:
    """Add a file handler to the supersearch root logger."""
    logger = logging.getLogger("supersearch")
    fh = logging.FileHandler(path, mode="w", encoding="utf-8")
    fh.setFormatter(
        logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s")
    )
    logger.addHandler(fh)


async def main() -> None:
    settings = Settings()
    setup_logging("DEBUG")
    configure_file_logging("test.log")

    logger = logging.getLogger("supersearch.run_test")
    logger.info("=== E2E Test Start ===")

    llm = LLMClient(
        base_url=settings.LLM_BASE_URL,
        api_key=settings.LLM_API_KEY,
        model=settings.LLM_MODEL,
        timeout=settings.LLM_TIMEOUT,
    )
    prompts = PromptLoader(prompts_dir=settings.PROMPTS_DIR)

    fetcher = None
    if settings.TAVILY_API_KEY and settings.TAVILY_API_KEY != "tvly-xxx":
        fetcher = TavilyFetcher(api_key=settings.TAVILY_API_KEY)

    modules = []
    if settings.ENABLE_BRAVE and settings.BRAVE_API_KEY:
        modules.append(
            BraveSearchModule(
                api_key=settings.BRAVE_API_KEY, timeout=settings.MODULE_TIMEOUT
            )
        )
    if settings.ENABLE_SERP and settings.SERP_API_KEY:
        modules.append(
            SerpSearchModule(
                api_key=settings.SERP_API_KEY, timeout=settings.MODULE_TIMEOUT
            )
        )

    logger.info("Modules: %s", [m.name for m in modules])
    logger.info("Fetcher: %s", "enabled" if fetcher else "disabled (no Tavily key)")

    orchestrator = Orchestrator(
        llm=llm,
        prompts=prompts,
        modules=modules,
        max_rounds=settings.MAX_SEARCH_ROUNDS,
        global_timeout=settings.GLOBAL_TIMEOUT,
        score_threshold=settings.SCORE_THRESHOLD,
        blacklist=settings.blacklist_domains,
        fetcher=fetcher,
        filter_video_domains=settings.filter_video_domains,
        min_snippet_length=settings.MIN_SNIPPET_LENGTH,
        filter_search_pages=settings.FILTER_SEARCH_PAGES,
        auto_blacklist_threshold=settings.AUTO_BLACKLIST_THRESHOLD,
        auto_blacklist_file=settings.AUTO_BLACKLIST_FILE,
    )

    query = "什么是牛爷爷"
    logger.info("Query: %s", query)

    result = await orchestrator.search(query)

    # Print result to stdout as formatted JSON.
    print(json.dumps(result.model_dump(), indent=2, ensure_ascii=False))

    logger.info("=== E2E Test End ===")

    await llm.close()
    if fetcher:
        await fetcher.close()


if __name__ == "__main__":
    asyncio.run(main())
