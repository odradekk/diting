"""Tests for diting.routing.decision_log."""

from __future__ import annotations

import json

import pytest

from diting.routing.decision_log import RoutingDecision, RoutingDecisionLog


class TestRoutingDecisionLog:
    """Core append / read functionality."""

    def test_record_and_last_query(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        log = RoutingDecisionLog(log_path=log_file)

        log.record(RoutingDecision(
            query_id="abc123",
            round=1,
            query="test query",
            routing_source="llm",
            included_modules=["bing", "arxiv"],
            excluded_modules=["github"],
        ))
        log.record(RoutingDecision(
            query_id="abc123",
            round=2,
            query="deeper query",
            routing_source="evaluator",
            included_modules=["bing", "arxiv", "github"],
            excluded_modules=[],
        ))

        decisions = log.last_query("abc123")

        assert len(decisions) == 2
        assert decisions[0].round == 1
        assert decisions[1].round == 2
        assert decisions[0].included_modules == ["bing", "arxiv"]
        assert decisions[1].routing_source == "evaluator"

    def test_last_query_none_returns_most_recent(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        log = RoutingDecisionLog(log_path=log_file)

        log.record(RoutingDecision(
            query_id="old", round=1, query="old",
            routing_source="all", included_modules=["bing"],
            excluded_modules=[],
        ))
        log.record(RoutingDecision(
            query_id="new", round=1, query="new",
            routing_source="llm", included_modules=["arxiv"],
            excluded_modules=[],
        ))

        decisions = log.last_query()

        assert len(decisions) == 1
        assert decisions[0].query_id == "new"

    def test_empty_log_returns_empty(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        log = RoutingDecisionLog(log_path=log_file)

        assert log.last_query() == []
        assert log.last_query("nonexistent") == []

    def test_disabled_log_does_not_write(self, tmp_path):
        """Empty string path disables file persistence."""
        log = RoutingDecisionLog(log_path="")

        log.record(RoutingDecision(
            query_id="abc", round=1, query="test",
            routing_source="all", included_modules=["bing"],
            excluded_modules=[],
        ))

        assert log.path is None
        assert log.last_query() == []

    def test_cost_gated_field_preserved(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        log = RoutingDecisionLog(log_path=log_file)

        log.record(RoutingDecision(
            query_id="abc", round=1, query="test",
            routing_source="llm",
            included_modules=["bing"],
            excluded_modules=["serp"],
            cost_gated=["serp"],
        ))

        decisions = log.last_query("abc")
        assert decisions[0].cost_gated == ["serp"]

    def test_jsonl_format_valid(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        log = RoutingDecisionLog(log_path=log_file)

        log.record(RoutingDecision(
            query_id="abc", round=1, query="test",
            routing_source="all", included_modules=["bing"],
            excluded_modules=[],
        ))

        lines = log_file.read_text().strip().splitlines()
        assert len(lines) == 1
        data = json.loads(lines[0])
        assert data["query_id"] == "abc"
        assert data["round"] == 1
        assert isinstance(data["timestamp"], float)

    def test_rotation_keeps_recent(self, tmp_path):
        log_file = tmp_path / "decisions.jsonl"
        # Very small limit to trigger rotation.
        log = RoutingDecisionLog(log_path=log_file, max_file_mb=0.001)

        for i in range(50):
            log.record(RoutingDecision(
                query_id=f"q{i}", round=1, query=f"query {i}",
                routing_source="all",
                included_modules=["bing", "arxiv"] * 10,  # make entries big
                excluded_modules=[],
            ))

        # File should have been rotated.
        size = log_file.stat().st_size
        # Original unrotated size would be ~50 * ~500 = ~25KB.
        # With 1KB limit, rotation should have happened.
        assert size < 25000
        # Recent entries should still be readable.
        lines = log_file.read_text().strip().splitlines()
        assert len(lines) > 0
        # All lines should be valid JSON.
        for line in lines:
            json.loads(line)
