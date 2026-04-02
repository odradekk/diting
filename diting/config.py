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

    # --- Required ---------------------------------------------------------
    LLM_BASE_URL: str
    LLM_MODEL: str
    LLM_API_KEY: str

    # --- Search module keys (optional) ------------------------------------
    TAVILY_API_KEY: str = ""
    BRAVE_API_KEY: str = ""
    SERP_API_KEY: str = ""

    # --- Timeouts ---------------------------------------------------------
    LLM_TIMEOUT: int = 120
    MODULE_TIMEOUT: int = 30
    GLOBAL_TIMEOUT: int = 150

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

    # --- Cookies (for Playwright modules) ---------------------------------
    X_COOKIE: str = ""
    ZHIHU_COOKIE: str = ""

    # --- Filtering --------------------------------------------------------
    SCORE_THRESHOLD: float = 0.6
    MIN_SNIPPET_LENGTH: int = 30
    RELEVANCE_WEIGHT: float = 0.5
    QUALITY_WEIGHT: float = 0.5

    # --- Blacklist --------------------------------------------------------
    BLACKLIST_FILE: str = str(_PACKAGE_DIR / "data" / "blacklist.txt")
    AUTO_BLACKLIST: bool = True
    AUTO_BLACKLIST_THRESHOLD: float = 0.3

    # --- Misc -------------------------------------------------------------
    LOG_LEVEL: str = "INFO"
    PROMPTS_DIR: str = ""

