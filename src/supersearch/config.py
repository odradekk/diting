"""Application configuration loaded from environment variables and .env file."""

from pydantic_settings import BaseSettings


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
    TAVILY_API_KEY: str

    # --- Search module keys (optional) ------------------------------------
    BRAVE_API_KEY: str = ""
    SERP_API_KEY: str = ""

    # --- Timeouts ---------------------------------------------------------
    LLM_TIMEOUT: int = 60
    MODULE_TIMEOUT: int = 30
    GLOBAL_TIMEOUT: int = 120

    # --- Search control ---------------------------------------------------
    MAX_SEARCH_ROUNDS: int = 3
    ENABLE_BRAVE: bool = True
    ENABLE_SERP: bool = True

    # --- Filtering --------------------------------------------------------
    BLACKLIST_DOMAINS: str = ""
    SCORE_THRESHOLD: float = 0.3

    # --- Misc -------------------------------------------------------------
    LOG_LEVEL: str = "INFO"
    PROMPTS_DIR: str = ""

    @property
    def blacklist_domains(self) -> list[str]:
        """Parse comma-separated BLACKLIST_DOMAINS into a list."""
        if not self.BLACKLIST_DOMAINS:
            return []
        return [d.strip() for d in self.BLACKLIST_DOMAINS.split(",") if d.strip()]
