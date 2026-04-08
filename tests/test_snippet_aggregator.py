"""Tests for diting.pipeline.snippet_aggregator."""

from __future__ import annotations

import pytest

from diting.pipeline.snippet_aggregator import (
    AGGREGATED_SOURCE_TAG,
    aggregate_snippets,
)


# ---------------------------------------------------------------------------
# Threshold: at least 2 distinct engines
# ---------------------------------------------------------------------------


class TestEngineThreshold:
    def test_empty_input_returns_empty(self):
        assert aggregate_snippets([]) == ""

    def test_single_engine_returns_empty(self):
        result = aggregate_snippets([("google", "some snippet text")])
        assert result == ""

    def test_same_engine_repeated_returns_empty(self):
        """Same engine across rounds should not count twice."""
        snippets = [
            ("google", "first hit"),
            ("google", "second hit"),
        ]
        assert aggregate_snippets(snippets) == ""

    def test_two_distinct_engines_produces_content(self):
        snippets = [
            ("google", "google snippet"),
            ("bing", "bing snippet"),
        ]
        result = aggregate_snippets(snippets)
        assert result != ""
        assert AGGREGATED_SOURCE_TAG in result

    def test_three_engines_produces_content(self):
        snippets = [
            ("google", "g"),
            ("bing", "b"),
            ("brave", "r"),
        ]
        result = aggregate_snippets(snippets)
        assert "3 engines" in result


class TestCustomThreshold:
    def test_min_engines_one(self):
        """With threshold of 1, a single engine is enough."""
        result = aggregate_snippets(
            [("google", "alone")], min_engines=1,
        )
        assert "google" in result

    def test_min_engines_three_not_met(self):
        snippets = [("google", "g"), ("bing", "b")]
        assert aggregate_snippets(snippets, min_engines=3) == ""


# ---------------------------------------------------------------------------
# Filtering: empty/whitespace snippets dropped
# ---------------------------------------------------------------------------


class TestFiltering:
    def test_empty_snippet_ignored(self):
        snippets = [
            ("google", ""),
            ("bing", "real content"),
        ]
        assert aggregate_snippets(snippets) == ""  # only one usable engine

    def test_whitespace_only_snippet_ignored(self):
        snippets = [
            ("google", "   \n\t  "),
            ("bing", "real"),
            ("brave", "also real"),
        ]
        result = aggregate_snippets(snippets)
        assert "bing" in result and "brave" in result
        assert "google" not in result

    def test_empty_engine_name_ignored(self):
        snippets = [
            ("", "content"),
            ("bing", "real"),
        ]
        assert aggregate_snippets(snippets) == ""  # only bing survives

    def test_snippet_is_stripped(self):
        snippets = [
            ("google", "  hello  "),
            ("bing", "\n world \n"),
        ]
        result = aggregate_snippets(snippets)
        assert "hello" in result
        assert "world" in result
        # The inner snippet content should not have leading/trailing whitespace.
        assert "- [google] hello" in result
        assert "- [bing] world" in result


# ---------------------------------------------------------------------------
# Same-engine dedup: first snippet wins
# ---------------------------------------------------------------------------


class TestFirstWinsPerEngine:
    def test_first_snippet_per_engine_kept(self):
        snippets = [
            ("google", "first google snippet"),
            ("bing", "bing snippet"),
            ("google", "second google snippet"),
        ]
        result = aggregate_snippets(snippets)
        assert "first google snippet" in result
        assert "second google snippet" not in result
        assert "2 engines" in result


# ---------------------------------------------------------------------------
# Output format
# ---------------------------------------------------------------------------


class TestOutputFormat:
    def test_header_includes_tag_and_count(self):
        snippets = [("google", "g"), ("bing", "b")]
        result = aggregate_snippets(snippets)
        first_line = result.splitlines()[0]
        assert first_line.startswith("[source=aggregated_snippets]")
        assert "2 engines" in first_line

    def test_engines_sorted_alphabetically(self):
        """Output must be deterministic, independent of input order."""
        snippets = [
            ("zoogle", "z"),
            ("brave", "b"),
            ("alpha", "a"),
        ]
        result = aggregate_snippets(snippets)
        lines = result.splitlines()
        # Skip header, check per-engine lines.
        engine_order = [line.split("]")[0].split("[")[-1] for line in lines[1:]]
        assert engine_order == sorted(engine_order)

    def test_input_order_does_not_change_output(self):
        a = aggregate_snippets([("x", "1"), ("y", "2")])
        b = aggregate_snippets([("y", "2"), ("x", "1")])
        assert a == b

    def test_each_snippet_on_own_line(self):
        snippets = [("a", "one"), ("b", "two")]
        result = aggregate_snippets(snippets)
        # 1 header + 2 engine lines
        assert len(result.splitlines()) == 3

    @pytest.mark.parametrize("engine", ["google", "bing-lite", "brave_search", "X"])
    def test_engine_name_preserved_verbatim(self, engine):
        snippets = [(engine, "text"), ("other", "o")]
        result = aggregate_snippets(snippets)
        assert f"[{engine}]" in result
