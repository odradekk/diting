"""Tests for diting.config — Settings class."""

import pytest
from pydantic import ValidationError

from diting.config import Settings


class TestDefaultValues:
    """Settings created with only required fields should have correct defaults."""

    def test_default_values(self):
        s = Settings(
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
            _env_file=None,
        )
        # Search module keys default to empty string
        assert s.BRAVE_API_KEY == ""
        assert s.SERP_API_KEY == ""

        # Timeout defaults
        assert s.LLM_TIMEOUT == 60
        assert s.MODULE_TIMEOUT == 30
        assert s.GLOBAL_TIMEOUT == 120

        # Search control defaults
        assert s.MAX_SEARCH_ROUNDS == 3
        assert s.ENABLE_SERP is True
        assert s.ENABLE_BRAVE is True

        # Filtering defaults
        assert s.SCORE_THRESHOLD == 0.3
        assert s.MIN_SNIPPET_LENGTH == 30

        # Blacklist defaults
        assert s.BLACKLIST_FILE == "blacklist.txt"
        assert s.AUTO_BLACKLIST is True
        assert s.AUTO_BLACKLIST_THRESHOLD == 0.3

        # Misc defaults
        assert s.LOG_LEVEL == "INFO"
        assert s.PROMPTS_DIR == ""


class TestRequiredFields:
    """Omitting any required field must raise ValidationError."""

    REQUIRED_KWARGS = dict(
        LLM_BASE_URL="https://api.example.com/v1",
        LLM_MODEL="gpt-4o-mini",
        LLM_API_KEY="sk-test",
        TAVILY_API_KEY="tvly-test",
    )

    @pytest.mark.parametrize("field", ["LLM_BASE_URL", "LLM_MODEL", "LLM_API_KEY", "TAVILY_API_KEY"])
    def test_required_fields_missing(self, field: str):
        kwargs = {**self.REQUIRED_KWARGS}
        del kwargs[field]
        with pytest.raises(ValidationError):
            Settings(**kwargs, _env_file=None)


class TestBlacklistSettings:
    """Blacklist settings must have correct types and defaults."""

    def test_blacklist_file_configurable(self):
        s = Settings(
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
            BLACKLIST_FILE="/custom/blacklist.txt",
            _env_file=None,
        )
        assert s.BLACKLIST_FILE == "/custom/blacklist.txt"

    def test_auto_blacklist_toggle(self):
        s = Settings(
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
            AUTO_BLACKLIST=False,
            _env_file=None,
        )
        assert s.AUTO_BLACKLIST is False

    def test_auto_blacklist_threshold(self):
        s = Settings(
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
            AUTO_BLACKLIST_THRESHOLD=0.5,
            _env_file=None,
        )
        assert s.AUTO_BLACKLIST_THRESHOLD == 0.5


class TestAllFieldsConfigurable:
    """Every optional field can be overridden via constructor kwargs."""

    def test_all_fields_configurable(self):
        s = Settings(
            LLM_BASE_URL="https://custom.api/v1",
            LLM_MODEL="custom-model",
            LLM_API_KEY="sk-custom",
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
            BLACKLIST_FILE="/custom/bl.txt",
            AUTO_BLACKLIST=False,
            LOG_LEVEL="DEBUG",
            PROMPTS_DIR="/custom/prompts",
            _env_file=None,
        )
        assert s.LLM_BASE_URL == "https://custom.api/v1"
        assert s.LLM_MODEL == "custom-model"
        assert s.LLM_API_KEY == "sk-custom"
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
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
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
            LLM_BASE_URL="https://api.example.com/v1",
            LLM_MODEL="gpt-4o-mini",
            LLM_API_KEY="sk-test",
            TAVILY_API_KEY="tvly-test",
            ENABLE_BRAVE=value,
            _env_file=None,
        )
        assert s.ENABLE_BRAVE is expected


class TestEnvVarLoading:
    """Settings must load values from environment variables."""

    def test_loads_from_env(self, monkeypatch: pytest.MonkeyPatch):
        monkeypatch.setenv("LLM_BASE_URL", "https://env.api/v1")
        monkeypatch.setenv("LLM_MODEL", "env-model")
        monkeypatch.setenv("LLM_API_KEY", "sk-env")
        monkeypatch.setenv("TAVILY_API_KEY", "tvly-env")
        monkeypatch.setenv("BLACKLIST_FILE", "/env/bl.txt")
        monkeypatch.setenv("ENABLE_SERP", "false")
        monkeypatch.setenv("SCORE_THRESHOLD", "0.7")

        s = Settings(_env_file=None)

        assert s.LLM_BASE_URL == "https://env.api/v1"
        assert s.LLM_MODEL == "env-model"
        assert s.LLM_API_KEY == "sk-env"
        assert s.TAVILY_API_KEY == "tvly-env"
        assert s.BLACKLIST_FILE == "/env/bl.txt"
        assert s.ENABLE_SERP is False
        assert s.SCORE_THRESHOLD == 0.7
