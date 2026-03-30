"""Search pipeline — deduplication, scoring, evaluation, and orchestration."""

from supersearch.pipeline.classifier import Classifier
from supersearch.pipeline.dedup import deduplicate, extract_domain, normalize_url
from supersearch.pipeline.evaluator import Evaluator, EvaluationResult
from supersearch.pipeline.orchestrator import Orchestrator
from supersearch.pipeline.scorer import Scorer
from supersearch.pipeline.summarizer import Summarizer, SummaryResult

__all__ = [
    "Classifier",
    "Evaluator",
    "EvaluationResult",
    "Orchestrator",
    "Scorer",
    "Summarizer",
    "SummaryResult",
    "deduplicate",
    "extract_domain",
    "normalize_url",
]
