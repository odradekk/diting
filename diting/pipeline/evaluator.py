"""LLM-based quality evaluation to decide whether more search rounds are needed."""

from __future__ import annotations

from dataclasses import dataclass

from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import ScoredResult, SearchResult

logger = get_logger("pipeline.evaluator")


@dataclass
class EvaluationResult:
    """Outcome of a quality evaluation."""

    sufficient: bool
    reason: str
    next_query: str


class Evaluator:
    """Decide whether current search results meet quality requirements.

    Uses the LLM to analyse round statistics and determine if another
    search round is beneficial.  When not sufficient, generates a targeted
    next query based on gaps in the current results.
    """

    def __init__(self, llm: LLMClient, prompts: PromptLoader) -> None:
        self._llm = llm
        self._system_prompt = prompts.load("quality_evaluation")

    async def evaluate(
        self,
        query: str,
        scored: list[ScoredResult],
        all_results: list[SearchResult],
        current_round: int,
        max_rounds: int,
    ) -> EvaluationResult:
        """Evaluate whether *scored* results are sufficient.

        On LLM failure, returns ``sufficient=True`` to avoid unnecessary
        extra rounds (conservative degradation).
        """
        stats = self._compute_stats(scored)
        user_message = self._build_user_message(
            query, stats, scored, all_results, current_round, max_rounds,
        )

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM evaluation failed: %s — treating as sufficient", exc)
            return EvaluationResult(
                sufficient=True,
                reason=f"Evaluation failed: {exc}",
                next_query="",
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
        scored: list[ScoredResult],
        all_results: list[SearchResult],
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

        # Append current results context so the LLM can identify gaps.
        if all_results:
            score_map = {s.url: s.final_score for s in scored}
            lines.append("")
            lines.append("Current results:")
            for i, r in enumerate(all_results[:20], 1):
                score = score_map.get(r.url)
                score_str = f" (score: {score:.2f})" if score is not None else ""
                lines.append(f"  {i}. [{r.title}] — {r.url}{score_str}")

        return "\n".join(lines)

    @staticmethod
    def _parse_response(data: dict) -> EvaluationResult:
        sufficient = bool(data.get("sufficient", True))
        reason = str(data.get("reason", ""))
        raw_next_query = data.get("next_query", "")
        if raw_next_query is None:
            next_query = ""
        else:
            next_query = str(raw_next_query).strip()

        return EvaluationResult(
            sufficient=sufficient,
            reason=reason,
            next_query=next_query,
        )
