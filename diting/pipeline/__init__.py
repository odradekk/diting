"""Search pipeline — deduplication, scoring, evaluation, and orchestration."""

from diting.pipeline.dedup import deduplicate, extract_domain, normalize_url
from diting.pipeline.evaluator import Evaluator, EvaluationResult
from diting.pipeline.health import HealthTracker
from diting.pipeline.orchestrator import Orchestrator
from diting.pipeline.scorer import Scorer
from diting.pipeline.summarizer import Summarizer, SummaryResult

__all__ = [
    "Evaluator",
    "EvaluationResult",
    "HealthTracker",
    "Orchestrator",
    "Scorer",
    "Summarizer",
    "SummaryResult",
    "deduplicate",
    "extract_domain",
    "normalize_url",
]
