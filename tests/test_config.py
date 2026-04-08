"""Tests for diting.config — Settings class."""

import pytest
from pydantic import ValidationError

from diting.config import Settings


class TestDefaultValues:
    """Settings created with only required fields should have correct defaults."""

    def test_default_values(self):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",
            _env_file=None,
        )
        # Optional API keys default to empty string
        assert s.TAVILY_API_KEY == ""
        assert s.BRAVE_API_KEY == ""
        assert s.SERP_API_KEY == ""

        # Timeout defaults
        assert s.LLM_TIMEOUT == 240
        assert s.MODULE_TIMEOUT == 30
        assert s.GLOBAL_TIMEOUT == 300

        # Search control defaults — only Baidu, Bing, DuckDuckGo on by default
        assert s.MAX_SEARCH_ROUNDS == 3
        assert s.ENABLE_BAIDU is True
        assert s.ENABLE_BING is True
        assert s.ENABLE_DUCKDUCKGO is True
        assert s.ENABLE_BRAVE is False
        assert s.ENABLE_SERP is False
        assert s.ENABLE_X is False
        assert s.ENABLE_ZHIHU is False

        # Filtering / scoring defaults
        assert s.SCORE_THRESHOLD == 0.6
        assert s.MIN_SNIPPET_LENGTH == 30
        assert s.SCORER_BACKEND == "hybrid"
        assert s.RERANKER_MODEL == "BAAI/bge-reranker-base"
        assert s.RERANKER_CACHE_DIR == ""

        # Blacklist defaults — path points to bundled file inside the package
        assert s.BLACKLIST_FILE.endswith("diting/data/blacklist.txt")
        assert s.AUTO_BLACKLIST is True
        assert s.AUTO_BLACKLIST_THRESHOLD == 0.3

        # Misc defaults
        assert s.LOG_LEVEL == "INFO"
        assert s.PROMPTS_DIR == ""


class TestRequiredFields:
    """Omitting any required field must raise ValidationError."""

    REQUIRED_KWARGS = dict(
        LLM_REASONING_BASE_URL="https://api.example.com/v1",
        LLM_REASONING_MODEL="gpt-4o",
        LLM_REASONING_API_KEY="sk-test",
        LLM_FAST_BASE_URL="https://api.example.com/v1",
        LLM_FAST_MODEL="gpt-4o-mini",
        LLM_FAST_API_KEY="sk-test",
    )

    @pytest.mark.parametrize("field", [
        "LLM_REASONING_BASE_URL", "LLM_REASONING_MODEL", "LLM_REASONING_API_KEY",
        "LLM_FAST_BASE_URL", "LLM_FAST_MODEL", "LLM_FAST_API_KEY",
    ])
    def test_required_fields_missing(self, field: str):
        kwargs = {**self.REQUIRED_KWARGS}
        del kwargs[field]
        with pytest.raises(ValidationError):
            Settings(**kwargs, _env_file=None)


class TestBlacklistSettings:
    """Blacklist settings must have correct types and defaults."""

    def test_blacklist_file_configurable(self):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",

            BLACKLIST_FILE="/custom/blacklist.txt",
            _env_file=None,
        )
        assert s.BLACKLIST_FILE == "/custom/blacklist.txt"

    def test_auto_blacklist_toggle(self):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",

            AUTO_BLACKLIST=False,
            _env_file=None,
        )
        assert s.AUTO_BLACKLIST is False

    def test_auto_blacklist_threshold(self):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",

            AUTO_BLACKLIST_THRESHOLD=0.5,
            _env_file=None,
        )
        assert s.AUTO_BLACKLIST_THRESHOLD == 0.5


class TestAllFieldsConfigurable:
    """Every optional field can be overridden via constructor kwargs."""

    def test_all_fields_configurable(self):
        s = Settings(
            LLM_REASONING_BASE_URL="https://custom.api/v1",
            LLM_REASONING_MODEL="reasoning-model",
            LLM_REASONING_API_KEY="sk-custom",
            LLM_FAST_BASE_URL="https://fast.api/v1",
            LLM_FAST_MODEL="fast-model",
            LLM_FAST_API_KEY="sk-fast",
            TAVILY_API_KEY="tvly-custom",
            BRAVE_API_KEY="bk-789",
            SERP_API_KEY="sk-serp-123",
            LLM_TIMEOUT=120,
            MAX_SEARCH_ROUNDS=5,
            MODULE_TIMEOUT=45,
            GLOBAL_TIMEOUT=300,
            ENABLE_SERP=False,
            ENABLE_BRAVE=False,
            SCORE_THRESHOLD=0.5,
            SCORER_BACKEND="hybrid",
            RERANKER_MODEL="custom/reranker",
            RERANKER_CACHE_DIR="/tmp/reranker-cache",
            BLACKLIST_FILE="/custom/bl.txt",
            AUTO_BLACKLIST=False,
            LOG_LEVEL="DEBUG",
            PROMPTS_DIR="/custom/prompts",
            _env_file=None,
        )
        assert s.LLM_REASONING_BASE_URL == "https://custom.api/v1"
        assert s.LLM_REASONING_MODEL == "reasoning-model"
        assert s.LLM_REASONING_API_KEY == "sk-custom"
        assert s.LLM_FAST_BASE_URL == "https://fast.api/v1"
        assert s.LLM_FAST_MODEL == "fast-model"
        assert s.LLM_FAST_API_KEY == "sk-fast"
        assert s.TAVILY_API_KEY == "tvly-custom"
        assert s.BRAVE_API_KEY == "bk-789"
        assert s.SERP_API_KEY == "sk-serp-123"
        assert s.LLM_TIMEOUT == 120
        assert s.MAX_SEARCH_ROUNDS == 5
        assert s.MODULE_TIMEOUT == 45
        assert s.GLOBAL_TIMEOUT == 300
        assert s.ENABLE_SERP is False
        assert s.ENABLE_BRAVE is False
        assert s.SCORE_THRESHOLD == 0.5
        assert s.SCORER_BACKEND == "hybrid"
        assert s.RERANKER_MODEL == "custom/reranker"
        assert s.RERANKER_CACHE_DIR == "/tmp/reranker-cache"
        assert s.BLACKLIST_FILE == "/custom/bl.txt"
        assert s.AUTO_BLACKLIST is False
        assert s.LOG_LEVEL == "DEBUG"
        assert s.PROMPTS_DIR == "/custom/prompts"


class TestBoolParsing:
    """Boolean fields must accept various truthy/falsy string representations."""

    @pytest.mark.parametrize("value,expected", [
        ("true", True),
        ("True", True),
        ("TRUE", True),
        ("1", True),
        ("false", False),
        ("False", False),
        ("FALSE", False),
        ("0", False),
    ])
    def test_bool_parsing_enable_serp(self, value: str, expected: bool):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",

            ENABLE_SERP=value,
            _env_file=None,
        )
        assert s.ENABLE_SERP is expected

    @pytest.mark.parametrize("value,expected", [
        ("true", True),
        ("1", True),
        ("false", False),
        ("0", False),
    ])
    def test_bool_parsing_enable_brave(self, value: str, expected: bool):
        s = Settings(
            LLM_REASONING_BASE_URL="https://api.example.com/v1",
            LLM_REASONING_MODEL="gpt-4o",
            LLM_REASONING_API_KEY="sk-test",
            LLM_FAST_BASE_URL="https://api.example.com/v1",
            LLM_FAST_MODEL="gpt-4o-mini",
            LLM_FAST_API_KEY="sk-test",

            ENABLE_BRAVE=value,
            _env_file=None,
        )
        assert s.ENABLE_BRAVE is expected


class TestEnvVarLoading:
    """Settings must load values from environment variables."""

    def test_loads_from_env(self, monkeypatch: pytest.MonkeyPatch):
        monkeypatch.setenv("LLM_REASONING_BASE_URL", "https://env.api/v1")
        monkeypatch.setenv("LLM_REASONING_MODEL", "env-reasoning")
        monkeypatch.setenv("LLM_REASONING_API_KEY", "sk-env")
        monkeypatch.setenv("LLM_FAST_BASE_URL", "https://fast.api/v1")
        monkeypatch.setenv("LLM_FAST_MODEL", "env-fast")
        monkeypatch.setenv("LLM_FAST_API_KEY", "sk-fast")
        monkeypatch.setenv("TAVILY_API_KEY", "tvly-env")
        monkeypatch.setenv("BLACKLIST_FILE", "/env/bl.txt")
        monkeypatch.setenv("ENABLE_SERP", "false")
        monkeypatch.setenv("SCORE_THRESHOLD", "0.7")

        s = Settings(_env_file=None)

        assert s.LLM_REASONING_BASE_URL == "https://env.api/v1"
        assert s.LLM_REASONING_MODEL == "env-reasoning"
        assert s.LLM_REASONING_API_KEY == "sk-env"
        assert s.LLM_FAST_BASE_URL == "https://fast.api/v1"
        assert s.LLM_FAST_MODEL == "env-fast"
        assert s.LLM_FAST_API_KEY == "sk-fast"
        assert s.TAVILY_API_KEY == "tvly-env"
        assert s.BLACKLIST_FILE == "/env/bl.txt"
        assert s.ENABLE_SERP is False
        assert s.SCORE_THRESHOLD == 0.7
