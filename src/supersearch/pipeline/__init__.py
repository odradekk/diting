"""Search pipeline — deduplication, scoring, and orchestration."""

from supersearch.pipeline.dedup import deduplicate, extract_domain, normalize_url

__all__ = ["deduplicate", "extract_domain", "normalize_url"]
