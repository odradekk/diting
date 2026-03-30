"""Tests for diting.pipeline.classifier — LLM-based source classification."""

import json

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from diting.llm.client import LLMError
from diting.models import Category, Source
from diting.pipeline.classifier import Classifier


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

CATEGORIES_CONFIG = {
    "categories": [
        {"name": "Official Documentation", "description": "Official docs, API references, specifications"},
        {"name": "Tutorial & Guide", "description": "How-to articles, tutorials, step-by-step guides"},
        {"name": "Community & Forum", "description": "Stack Overflow, Reddit, GitHub discussions, forum posts"},
        {"name": "Other", "description": "Sources that do not fit the above categories"},
    ]
}

SOURCES = [
    Source(
        title="Python docs",
        url="https://docs.python.org/3/",
        normalized_url="docs.python.org/3",
        snippet="Welcome to Python 3 documentation",
        score=0.95,
        source_module="brave",
        domain="docs.python.org",
    ),
    Source(
        title="Real Python tutorial",
        url="https://realpython.com/python-basics/",
        normalized_url="realpython.com/python-basics",
        snippet="Learn Python step by step",
        score=0.85,
        source_module="serp",
        domain="realpython.com",
    ),
    Source(
        title="SO question on decorators",
        url="https://stackoverflow.com/q/739654",
        normalized_url="stackoverflow.com/q/739654",
        snippet="How to make function decorators",
        score=0.78,
        source_module="brave",
        domain="stackoverflow.com",
    ),
]

GOOD_LLM_RESPONSE = {
    "classifications": [
        {"url": "https://docs.python.org/3/", "category": "Official Documentation"},
        {"url": "https://realpython.com/python-basics/", "category": "Tutorial & Guide"},
        {"url": "https://stackoverflow.com/q/739654", "category": "Community & Forum"},
    ]
}


def _make_classifier(
    chat_json_return=None,
    chat_json_side_effect=None,
    categories=None,
) -> Classifier:
    llm = MagicMock()
    llm.chat_json = AsyncMock(
        return_value=chat_json_return,
        side_effect=chat_json_side_effect,
    )
    prompts = MagicMock()
    prompts.load.return_value = "You are a classifier."

    cats = categories if categories is not None else CATEGORIES_CONFIG

    with patch.object(Classifier, "_load_categories", return_value=cats["categories"]):
        return Classifier(llm, prompts)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestClassifyGroupsSources:
    async def test_classify_groups_sources_by_category(self):
        classifier = _make_classifier(chat_json_return=GOOD_LLM_RESPONSE)
        result = await classifier.classify(SOURCES)

        assert isinstance(result, list)
        assert all(isinstance(c, Category) for c in result)

        names = [c.name for c in result]
        assert "Official Documentation" in names
        assert "Tutorial & Guide" in names
        assert "Community & Forum" in names

        docs = next(c for c in result if c.name == "Official Documentation")
        assert len(docs.sources) == 1
        assert docs.sources[0].url == "https://docs.python.org/3/"

        tutorial = next(c for c in result if c.name == "Tutorial & Guide")
        assert len(tutorial.sources) == 1
        assert tutorial.sources[0].url == "https://realpython.com/python-basics/"


class TestClassifyEmpty:
    async def test_classify_empty_sources(self):
        classifier = _make_classifier()
        result = await classifier.classify([])
        assert result == []


class TestClassifyLLMError:
    async def test_classify_llm_error_degrades_to_other(self):
        classifier = _make_classifier(chat_json_side_effect=LLMError("timeout"))
        result = await classifier.classify(SOURCES)

        assert len(result) == 1
        assert result[0].name == "Other"
        assert len(result[0].sources) == len(SOURCES)


class TestClassifyInvalidJSON:
    async def test_classify_invalid_json_degrades_to_other(self):
        classifier = _make_classifier(chat_json_return={"wrong_key": "nope"})
        result = await classifier.classify(SOURCES)

        assert len(result) == 1
        assert result[0].name == "Other"
        assert len(result[0].sources) == len(SOURCES)


class TestClassifyUnknownCategory:
    async def test_classify_unknown_category_falls_to_other(self):
        response = {
            "classifications": [
                {"url": "https://docs.python.org/3/", "category": "Official Documentation"},
                {"url": "https://realpython.com/python-basics/", "category": "Nonexistent Category"},
                {"url": "https://stackoverflow.com/q/739654", "category": "Community & Forum"},
            ]
        }
        classifier = _make_classifier(chat_json_return=response)
        result = await classifier.classify(SOURCES)

        other = next(c for c in result if c.name == "Other")
        assert len(other.sources) == 1
        assert other.sources[0].url == "https://realpython.com/python-basics/"


class TestClassifyMissingURL:
    async def test_classify_missing_url_in_response_skipped(self):
        response = {
            "classifications": [
                {"url": "https://docs.python.org/3/", "category": "Official Documentation"},
                {"url": "https://unknown.example.com/page", "category": "Tutorial & Guide"},
                {"url": "https://stackoverflow.com/q/739654", "category": "Community & Forum"},
            ]
        }
        classifier = _make_classifier(chat_json_return=response)
        result = await classifier.classify(SOURCES)

        # realpython was not classified by LLM -> falls to "Other"
        other = next(c for c in result if c.name == "Other")
        assert len(other.sources) == 1
        assert other.sources[0].url == "https://realpython.com/python-basics/"

        # The unknown URL should not appear in any category
        all_urls = [s.url for c in result for s in c.sources]
        assert "https://unknown.example.com/page" not in all_urls


class TestClassifyOnlyNonemptyCategories:
    async def test_classify_only_nonempty_categories_in_output(self):
        response = {
            "classifications": [
                {"url": "https://docs.python.org/3/", "category": "Official Documentation"},
                {"url": "https://realpython.com/python-basics/", "category": "Official Documentation"},
                {"url": "https://stackoverflow.com/q/739654", "category": "Official Documentation"},
            ]
        }
        classifier = _make_classifier(chat_json_return=response)
        result = await classifier.classify(SOURCES)

        assert len(result) == 1
        assert result[0].name == "Official Documentation"
        assert len(result[0].sources) == 3


class TestClassifyCustomCategoriesPath:
    async def test_classify_custom_categories_path(self, tmp_path):
        import json

        custom_categories = {
            "categories": [
                {"name": "Alpha", "description": "First category"},
                {"name": "Beta", "description": "Second category"},
                {"name": "Other", "description": "Fallback"},
            ]
        }
        categories_file = tmp_path / "custom_categories.json"
        categories_file.write_text(json.dumps(custom_categories))

        response = {
            "classifications": [
                {"url": "https://docs.python.org/3/", "category": "Alpha"},
                {"url": "https://realpython.com/python-basics/", "category": "Beta"},
                {"url": "https://stackoverflow.com/q/739654", "category": "Alpha"},
            ]
        }

        llm = MagicMock()
        llm.chat_json = AsyncMock(return_value=response)
        prompts = MagicMock()
        prompts.load.return_value = "You are a classifier."

        classifier = Classifier(llm, prompts, categories_path=str(categories_file))
        result = await classifier.classify(SOURCES)

        names = [c.name for c in result]
        assert "Alpha" in names
        assert "Beta" in names
        alpha = next(c for c in result if c.name == "Alpha")
        assert len(alpha.sources) == 2


class TestClassifyPreservesSourceData:
    async def test_classify_preserves_source_data(self):
        classifier = _make_classifier(chat_json_return=GOOD_LLM_RESPONSE)
        result = await classifier.classify(SOURCES)

        all_sources = [s for c in result for s in c.sources]
        assert len(all_sources) == len(SOURCES)

        # Verify each original source appears with all fields intact.
        original_by_url = {s.url: s for s in SOURCES}
        for source in all_sources:
            orig = original_by_url[source.url]
            assert source.title == orig.title
            assert source.normalized_url == orig.normalized_url
            assert source.snippet == orig.snippet
            assert source.score == orig.score
            assert source.source_module == orig.source_module
            assert source.domain == orig.domain


class TestClassifyCategoryOrder:
    async def test_classify_preserves_category_order(self):
        """Categories in output follow the order from categories.json."""
        response = {
            "classifications": [
                {"url": "https://stackoverflow.com/q/739654", "category": "Community & Forum"},
                {"url": "https://docs.python.org/3/", "category": "Official Documentation"},
                {"url": "https://realpython.com/python-basics/", "category": "Tutorial & Guide"},
            ]
        }
        classifier = _make_classifier(chat_json_return=response)
        result = await classifier.classify(SOURCES)

        names = [c.name for c in result]
        # Config order: Official Documentation, Tutorial & Guide, Community & Forum, Other
        assert names == ["Official Documentation", "Tutorial & Guide", "Community & Forum"]


class TestBuildUserMessage:
    def test_message_contains_sources_and_categories(self):
        classifier = _make_classifier()
        msg = classifier._build_user_message(SOURCES)
        # Message is now structured JSON
        payload = json.loads(msg)
        assert len(payload["sources"]) == len(SOURCES)
        assert payload["sources"][0]["url"] == "https://docs.python.org/3/"
        assert payload["sources"][0]["title"] == "Python docs"
        assert payload["sources"][0]["domain"] == "docs.python.org"
        cat_names = [c["name"] for c in payload["categories"]]
        assert "Official Documentation" in cat_names
        assert "Tutorial & Guide" in cat_names


# ---------------------------------------------------------------------------
# _load_categories validation tests
# ---------------------------------------------------------------------------

def _write_categories(tmp_path, data):
    """Write a categories JSON file and return the path as a string."""
    path = tmp_path / "categories.json"
    path.write_text(json.dumps(data))
    return str(path)


def _make_llm_and_prompts():
    llm = MagicMock()
    llm.chat_json = AsyncMock()
    prompts = MagicMock()
    prompts.load.return_value = "You are a classifier."
    return llm, prompts


class TestLoadCategoriesValidation:
    def test_missing_other_gets_injected(self, tmp_path):
        data = {"categories": [{"name": "Alpha", "description": "First"}]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        classifier = Classifier(llm, prompts, categories_path=path)
        names = [c["name"] for c in classifier._categories]
        assert "Other" in names
        assert "Alpha" in names

    def test_duplicate_names_raises(self, tmp_path):
        data = {
            "categories": [
                {"name": "Dup", "description": "First"},
                {"name": "Dup", "description": "Second"},
            ]
        }
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="Duplicate category name"):
            Classifier(llm, prompts, categories_path=path)

    def test_missing_description_raises(self, tmp_path):
        data = {"categories": [{"name": "NoDesc"}]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="missing 'name' or 'description'"):
            Classifier(llm, prompts, categories_path=path)

    def test_non_string_description_raises(self, tmp_path):
        data = {"categories": [{"name": "Bad", "description": 123}]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="'description' must be a non-empty string"):
            Classifier(llm, prompts, categories_path=path)

    def test_empty_description_raises(self, tmp_path):
        data = {"categories": [{"name": "Bad", "description": "  "}]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="'description' must be a non-empty string"):
            Classifier(llm, prompts, categories_path=path)

    def test_empty_name_raises(self, tmp_path):
        data = {"categories": [{"name": "", "description": "Something"}]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="'name' must be a non-empty string"):
            Classifier(llm, prompts, categories_path=path)

    def test_empty_categories_list_raises(self, tmp_path):
        data = {"categories": []}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="non-empty 'categories' list"):
            Classifier(llm, prompts, categories_path=path)

    def test_non_list_categories_raises(self, tmp_path):
        data = {"categories": "not a list"}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="non-empty 'categories' list"):
            Classifier(llm, prompts, categories_path=path)

    def test_non_dict_entry_raises(self, tmp_path):
        data = {"categories": ["not a dict"]}
        path = _write_categories(tmp_path, data)
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="Each category must be an object"):
            Classifier(llm, prompts, categories_path=path)

    def test_non_dict_top_level_raises(self, tmp_path):
        path = tmp_path / "categories.json"
        path.write_text('["not", "an", "object"]')
        llm, prompts = _make_llm_and_prompts()

        with pytest.raises(ValueError, match="JSON object at the top level"):
            Classifier(llm, prompts, categories_path=str(path))
