"""Tests for diting.pipeline.quality."""

from diting.models import SearchResult
from diting.pipeline.quality import HeuristicQualityScorer, load_domain_authority


class TestDomainAuthority:
    def test_high_authority_domain_scores_higher_than_low_authority(self):
        authority = load_domain_authority()

        assert authority.score("https://arxiv.org/abs/1234.5678") > authority.score(
            "https://wenku.csdn.net/view/abc"
        )


class TestHeuristicQualityScorer:
    def test_longer_snippet_scores_higher(self):
        scorer = HeuristicQualityScorer()
        short = SearchResult(title="Short", url="https://example.com/1", snippet="tiny")
        long = SearchResult(
            title="Long",
            url="https://example.com/2",
            snippet=(
                "This is a longer, descriptive snippet with enough detail to act as a useful preview "
                "for ranking and heuristic quality estimation."
            ),
        )

        assert scorer.score_result(long) > scorer.score_result(short)

    def test_duplicate_snippets_are_penalized(self):
        scorer = HeuristicQualityScorer()
        snippet = "Python frameworks comparison with Django Flask and FastAPI in practical production use."
        results = [
            SearchResult(title="A", url="https://example.com/a", snippet=snippet),
            SearchResult(title="B", url="https://example.com/b", snippet=snippet),
            SearchResult(
                title="C",
                url="https://example.com/c",
                snippet="A distinct snippet discussing trade-offs, performance, and deployment constraints.",
            ),
        ]

        scores = scorer.score_results(results)

        assert scores[results[0].url] < scores[results[2].url]
        assert scores[results[1].url] < scores[results[2].url]

    def test_scores_are_clamped_to_unit_interval(self):
        scorer = HeuristicQualityScorer()
        result = SearchResult(
            title="Docs",
            url="https://docs.python.org/3/tutorial/index.html",
            snippet="Official tutorial documentation for Python, including examples and precise explanations.",
        )

        score = scorer.score_result(result)
        assert 0.0 <= score <= 1.0
