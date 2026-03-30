"""LLM-based classification of search sources into configurable categories."""

from __future__ import annotations

import json
import pathlib

from supersearch.llm.client import LLMClient, LLMError
from supersearch.llm.prompts import PromptLoader
from supersearch.log import get_logger
from supersearch.models import Category, Source

logger = get_logger("pipeline.classifier")


class Classifier:
    """Classify scored sources into categories using an LLM.

    Categories are loaded from a JSON configuration file.  On LLM or
    parse failure, all sources fall back to a single ``"Other"`` category
    (graceful degradation).
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        categories_path: str | None = None,
    ) -> None:
        self._llm = llm
        self._system_prompt = prompts.load("classification")
        self._categories = self._load_categories(categories_path)

    async def classify(self, sources: list[Source]) -> list[Category]:
        """Classify *sources* into categories.

        On LLM failure, returns all sources under a single ``"Other"``
        category.  On empty sources, returns an empty list.
        """
        if not sources:
            return []

        user_message = self._build_user_message(sources)

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM classification failed: %s", exc)
            return [Category(name="Other", sources=list(sources))]

        return self._parse_response(data, sources)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _load_categories(self, categories_path: str | None) -> list[dict]:
        """Load and validate category definitions from a JSON file.

        Raises :class:`ValueError` when the file content is malformed
        (missing keys, duplicate names, etc.).  Ensures an ``"Other"``
        fallback category always exists.
        """
        if categories_path is not None:
            path = pathlib.Path(categories_path)
        else:
            path = self._detect_project_root() / "config" / "categories.json"

        with path.open(encoding="utf-8") as fh:
            data = json.load(fh)

        if not isinstance(data, dict):
            raise ValueError("categories.json must contain a JSON object at the top level")

        raw = data.get("categories")
        if not isinstance(raw, list) or not raw:
            raise ValueError("categories.json must contain a non-empty 'categories' list")

        seen_names: set[str] = set()
        for entry in raw:
            if not isinstance(entry, dict):
                raise ValueError(f"Each category must be an object, got {type(entry).__name__}")
            if "name" not in entry or "description" not in entry:
                raise ValueError(f"Category entry missing 'name' or 'description': {entry!r}")
            name = entry["name"]
            if not isinstance(name, str) or not name.strip():
                raise ValueError(f"Category 'name' must be a non-empty string: {name!r}")
            description = entry["description"]
            if not isinstance(description, str) or not description.strip():
                raise ValueError(f"Category 'description' must be a non-empty string: {description!r}")
            if name in seen_names:
                raise ValueError(f"Duplicate category name: {name!r}")
            seen_names.add(name)

        # Ensure an "Other" fallback category exists.
        if "Other" not in seen_names:
            raw.append({"name": "Other", "description": "Sources that do not fit the above categories"})

        return raw

    @staticmethod
    def _detect_project_root() -> pathlib.Path:
        """Walk up from this file to find the project root.

        The project root is identified by the presence of ``pyproject.toml``.
        Falls back to the grandparent of the ``src/`` package directory.
        """
        current = pathlib.Path(__file__).resolve().parent
        for parent in (current, *current.parents):
            if (parent / "pyproject.toml").is_file():
                return parent
        # Fallback: src/supersearch/pipeline -> three levels up is project root
        return pathlib.Path(__file__).resolve().parent.parent.parent.parent

    def _build_user_message(self, sources: list[Source]) -> str:
        sources_data = [
            {
                "title": s.title,
                "url": s.url,
                "snippet": s.snippet,
                "domain": s.domain,
            }
            for s in sources
        ]
        categories_data = [
            {"name": cat["name"], "description": cat["description"]}
            for cat in self._categories
        ]
        payload = {
            "sources": sources_data,
            "categories": categories_data,
        }
        return json.dumps(payload, indent=2)

    def _parse_response(
        self,
        data: dict,
        original: list[Source],
    ) -> list[Category]:
        """Parse the LLM JSON response and group sources by category."""
        raw_list = data.get("classifications")
        if not isinstance(raw_list, list):
            logger.warning("LLM response missing 'classifications' list")
            return [Category(name="Other", sources=list(original))]

        url_to_source = {s.url: s for s in original}
        valid_names = {cat["name"] for cat in self._categories}

        # Map each source URL to its assigned category name.
        url_to_category: dict[str, str] = {}
        for item in raw_list:
            if not isinstance(item, dict):
                continue
            url = item.get("url")
            category = item.get("category")
            if not isinstance(url, str) or not isinstance(category, str):
                continue
            if url not in url_to_source:
                logger.debug("Classified URL not in original sources: %s", url)
                continue
            if url in url_to_category:
                logger.debug("Duplicate classification for %s — keeping first", url)
                continue
            if category not in valid_names:
                logger.debug(
                    "Unknown category %r for %s — falling back to Other",
                    category, url,
                )
                category = "Other"
            url_to_category[url] = category

        # Sources not mentioned in the LLM response fall back to "Other".
        for source in original:
            if source.url not in url_to_category:
                url_to_category[source.url] = "Other"

        # Group sources by category, preserving category order from config.
        category_sources: dict[str, list[Source]] = {}
        for cat in self._categories:
            category_sources[cat["name"]] = []

        for source in original:
            cat_name = url_to_category[source.url]
            category_sources[cat_name].append(source)

        # Build output — only include categories that have at least one source.
        return [
            Category(name=name, sources=srcs)
            for name, srcs in category_sources.items()
            if srcs
        ]
