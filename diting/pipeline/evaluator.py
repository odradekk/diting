"""LLM-based quality evaluation to decide whether more search rounds are needed."""

from __future__ import annotations

import re
from dataclasses import dataclass, field

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
    next_modules: list[str] = field(default_factory=list)


class Evaluator:
    """Decide whether current search results meet quality requirements.

    Uses the LLM to analyse round statistics and determine if another
    search round is beneficial.  When not sufficient, generates a targeted
    next query based on gaps in the current results.
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        *,
        module_catalog: str = "",
    ) -> None:
        self._llm = llm
        base_prompt = prompts.load("quality_evaluation")
        self._system_prompt = (
            f"{base_prompt}\n\n## Available Search Modules\n\n{module_catalog}"
            if module_catalog else base_prompt
        )

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
            data = await self._llm.chat_json(
                self._system_prompt,
                user_message,
            )
        except LLMError as exc:
            logger.warning("LLM evaluation failed: %s — treating as sufficient", exc)
            return EvaluationResult(
                sufficient=True,
                reason=f"Evaluation failed: {exc}",
                next_query="",
                next_modules=[],
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

        # Per-module breakdown so the evaluator can make routing decisions.
        score_map = {s.url: s.final_score for s in scored}
        module_stats = Evaluator._compute_module_stats(all_results, score_map)
        if module_stats:
            lines.append("")
            lines.append("Per-module breakdown:")
            for mod_name, ms in sorted(module_stats.items()):
                avg_str = f", avg_score={ms['avg_score']:.2f}" if ms["avg_score"] is not None else ""
                lines.append(f"  {mod_name}: {ms['count']} results{avg_str}")

        if all_results:
            lines.append("")
            lines.append("Current results:")
            for i, r in enumerate(all_results[:20], 1):
                score = score_map.get(r.url)
                score_str = f" (score: {score:.2f})" if score is not None else ""
                mod_str = f" [{r.source_module}]" if r.source_module else ""
                lines.append(f"  {i}. [{r.title}] — {r.url}{score_str}{mod_str}")

        return "\n".join(lines)

    @staticmethod
    def _compute_module_stats(
        results: list[SearchResult],
        score_map: dict[str, float],
    ) -> dict[str, dict]:
        """Aggregate result counts and average scores per source module."""
        modules: dict[str, list[float | None]] = {}
        for r in results:
            mod = r.source_module or "unknown"
            modules.setdefault(mod, []).append(score_map.get(r.url))

        out: dict[str, dict] = {}
        for mod, scores in modules.items():
            valid = [s for s in scores if s is not None]
            out[mod] = {
                "count": len(scores),
                "avg_score": sum(valid) / len(valid) if valid else None,
            }
        return out

    @staticmethod
    def _parse_response(data: dict) -> EvaluationResult:
        sufficient = bool(data.get("sufficient", True))
        reason = str(data.get("reason", ""))
        raw_next_query = data.get("next_query", "")
        if raw_next_query is None:
            next_query = ""
        else:
            next_query = str(raw_next_query).strip()

        next_query = re.sub(
            r'\b(site|intitle|filetype|inurl):\S*', '', next_query,
        ).strip()
        next_query = re.sub(r'\b(AND|OR)\b', '', next_query).strip()
        next_query = next_query.replace('"', '')

        raw_next_modules = data.get("next_modules", [])
        next_modules: list[str] = []
        if isinstance(raw_next_modules, list):
            seen: set[str] = set()
            for item in raw_next_modules:
                if not isinstance(item, str):
                    continue
                module = item.strip().lower()
                if not module or module in seen:
                    continue
                seen.add(module)
                next_modules.append(module)

        return EvaluationResult(
            sufficient=sufficient,
            reason=reason,
            next_query=next_query,
            next_modules=next_modules,
        )
