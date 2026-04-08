"""Application configuration loaded from environment variables and .env file."""

import pathlib

from pydantic_settings import BaseSettings

_PACKAGE_DIR = pathlib.Path(__file__).resolve().parent


class Settings(BaseSettings):
    """Super Search MCP configuration.

    Required fields raise a validation error if not provided.
    Optional fields carry sensible defaults aligned with Design.md.
    """

    model_config = {"env_file": ".env", "env_file_encoding": "utf-8"}

    # --- Required: LLM tiers ---------------------------------------------
    LLM_REASONING_BASE_URL: str
    LLM_REASONING_API_KEY: str
    LLM_REASONING_MODEL: str
    LLM_FAST_BASE_URL: str
    LLM_FAST_API_KEY: str
    LLM_FAST_MODEL: str

    # --- Search module keys (optional) ------------------------------------
    TAVILY_API_KEY: str = ""
    BRAVE_API_KEY: str = ""
    SERP_API_KEY: str = ""
    JINA_API_KEY: str = ""  # r.jina.ai reader — optional, lifts rate limits
    GITHUB_TOKEN: str = ""  # GitHub API — optional, lifts rate limit to 30/min

    # --- Timeouts ---------------------------------------------------------
    LLM_MAX_TOKENS: int = 8192
    LLM_TIMEOUT: int = 240
    MODULE_TIMEOUT: int = 30
    GLOBAL_TIMEOUT: int = 300

    # --- Search control ---------------------------------------------------
    MAX_RESULTS: int = 10
    MAX_SEARCH_ROUNDS: int = 3
    MAX_CONCURRENCY: int = 5
    ENABLE_BAIDU: bool = True
    ENABLE_BING: bool = True
    ENABLE_BRAVE: bool = False
    ENABLE_DUCKDUCKGO: bool = True
    ENABLE_SERP: bool = False
    ENABLE_X: bool = False
    ENABLE_ZHIHU: bool = False
    ENABLE_ARXIV: bool = False
    ENABLE_WIKIPEDIA: bool = False
    ENABLE_STACKEXCHANGE: bool = False
    ENABLE_GITHUB: bool = False

    # --- Cookies (for Playwright modules) ---------------------------------
    X_COOKIE: str = ""
    ZHIHU_COOKIE: str = ""

    # --- Filtering --------------------------------------------------------
    SCORE_THRESHOLD: float = 0.6
    MIN_SNIPPET_LENGTH: int = 30
    RELEVANCE_WEIGHT: float = 0.5
    QUALITY_WEIGHT: float = 0.5
    SCORER_BACKEND: str = "hybrid"  # "hybrid" | "llm" | legacy alias: "reranker"
    RERANKER_MODEL: str = "BAAI/bge-reranker-base"
    RERANKER_CACHE_DIR: str = ""  # empty -> ~/.cache/diting/models/bge-reranker-base
    SEMANTIC_DEDUP: bool = False
    SEMANTIC_DEDUP_THRESHOLD: float = 0.9

    # --- Blacklist --------------------------------------------------------
    BLACKLIST_FILE: str = str(_PACKAGE_DIR / "data" / "blacklist.txt")
    AUTO_BLACKLIST: bool = True
    AUTO_BLACKLIST_THRESHOLD: float = 0.3

    # --- Content cache ----------------------------------------------------
    DITING_CACHE_ENABLED: bool = True
    DITING_CACHE_PATH: str = ""  # empty → ~/.cache/diting/content.db

    # --- Fetch fallbacks --------------------------------------------------
    ENABLE_JINA_READER: bool = True  # r.jina.ai second-layer fallback
    ENABLE_ARCHIVE_FALLBACK: bool = True  # Wayback + Archive.today snapshots
    ENABLE_STEALTH_BROWSER: bool = False  # requires: pip install diting[stealth]

    # --- Routing ----------------------------------------------------------
    ROUTING_STRATEGY: str = "funnel"  # "funnel" | "cheap_first" | "fire_all"

    # --- Misc -------------------------------------------------------------
    LOG_LEVEL: str = "INFO"
    LOG_FORMAT: str = "text"  # "text" | "json"
    PROMPTS_DIR: str = ""

