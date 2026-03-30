"""LLM-based quality evaluation to decide whether more search rounds are needed."""

from __future__ import annotations

from dataclasses import dataclass

from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import ScoredResult

logger = get_logger("pipeline.evaluator")


@dataclass
class EvaluationResult:
    """Outcome of a quality evaluation."""

    sufficient: bool
    reason: str
    supplementary_queries: list[str]


class Evaluator:
    """Decide whether current search results meet quality requirements.

    Uses the LLM to analyse round statistics and determine if another
    search round is beneficial.
    """

    def __init__(self, llm: LLMClient, prompts: PromptLoader) -> None:
        self._llm = llm
        self._system_prompt = prompts.load("quality_evaluation")

    async def evaluate(
        self,
        query: str,
        scored: list[ScoredResult],
        current_round: int,
        max_rounds: int,
    ) -> EvaluationResult:
        """Evaluate whether *scored* results are sufficient.

        On LLM failure, returns ``sufficient=True`` to avoid unnecessary
        extra rounds (conservative degradation).
        """
        stats = self._compute_stats(scored)
        user_message = self._build_user_message(
            query, stats, current_round, max_rounds,
        )

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM evaluation failed: %s — treating as sufficient", exc)
            return EvaluationResult(
                sufficient=True,
                reason=f"Evaluation failed: {exc}",
                supplementary_queries=[],
            )

        return self._parse_response(data)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    @staticmethod
    def _compute_stats(scored: list[ScoredResult]) -> dict:
        if not scored:
            return {
                "total_results": 0,
                "average_score": 0.0,
                "max_score": 0.0,
                "min_score": 0.0,
                "above_0_7": 0,
                "unique_domains": 0,
            }

        scores = [s.final_score for s in scored]
        domains = set()
        for s in scored:
            parts = s.url.split("/")
            if len(parts) >= 3:
                domains.add(parts[2])

        return {
            "total_results": len(scored),
            "average_score": round(sum(scores) / len(scores), 3),
            "max_score": round(max(scores), 3),
            "min_score": round(min(scores), 3),
            "above_0_7": sum(1 for sc in scores if sc > 0.7),
            "unique_domains": len(domains),
        }

    @staticmethod
    def _build_user_message(
        query: str,
        stats: dict,
        current_round: int,
        max_rounds: int,
    ) -> str:
        lines = [
            f"Query: {query}",
            f"Round: {current_round}/{max_rounds}",
            "",
            "Statistics:",
            f"  Total results: {stats['total_results']}",
            f"  Average score: {stats['average_score']}",
            f"  Max score: {stats['max_score']}",
            f"  Min score: {stats['min_score']}",
            f"  Results above 0.7: {stats['above_0_7']}",
            f"  Unique domains: {stats['unique_domains']}",
        ]
        return "\n".join(lines)

    @staticmethod
    def _parse_response(data: dict) -> EvaluationResult:
        sufficient = bool(data.get("sufficient", True))
        reason = str(data.get("reason", ""))
        raw_queries = data.get("supplementary_queries", [])

        queries: list[str] = []
        if isinstance(raw_queries, list):
            for q in raw_queries:
                if isinstance(q, str) and q.strip():
                    queries.append(q.strip())

        return EvaluationResult(
            sufficient=sufficient,
            reason=reason,
            supplementary_queries=queries,
        )
