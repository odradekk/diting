"""Search pipeline — deduplication, scoring, evaluation, and orchestration."""

from supersearch.pipeline.dedup import deduplicate, extract_domain, normalize_url
from supersearch.pipeline.evaluator import Evaluator, EvaluationResult
from supersearch.pipeline.orchestrator import Orchestrator
from supersearch.pipeline.scorer import Scorer

__all__ = [
    "Evaluator",
    "EvaluationResult",
    "Orchestrator",
    "Scorer",
    "deduplicate",
    "extract_domain",
    "normalize_url",
]
