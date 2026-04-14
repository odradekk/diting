# Benchmark

This document describes the diting v2 benchmark methodology, query set, scoring, and reference results. For the full specification see `docs/architecture.md` §12 and `docs/bench/HANDOFF.md`.

---

## Overview

The benchmark measures end-to-end search quality: given a technical question, how well does a diting variant retrieve authoritative sources and produce a correct, well-cited answer? It is not a microbenchmark of individual modules or fetch layers — it tests the complete pipeline under realistic conditions.

The primary purpose is to detect regressions after tuning passes and to provide a reproducible reference score that can be compared across variants and commits.

---

## Query set

- **50 queries** across 7 categories
- Stored at `docs/bench/final/queries.yaml` (symlinked as `test/bench/queries.yaml`)
- Each query carries ground-truth labels: `must_contain_domains`, `must_contain_terms`, `forbidden_domains`, `expected_source_types`
- Labels were produced through a three-stage pipeline: automated generation, LLM-assisted refinement, and manual review

| Category | N | Notes |
|---|---|---|
| error_troubleshooting | 15 | Stack traces, runtime errors, build failures |
| api_usage | 10 | Library/SDK usage questions |
| version_compatibility | 8 | Breaking changes, migration paths |
| concept_explanation | 5 | Definitions, architecture concepts |
| comparison | 5 | Tool comparisons, trade-off questions |
| fuzzy_recall | 5 | Partial names, approximate descriptions |
| time_sensitive | 2 | Questions where recency matters |

For query authoring conventions and the labelling pipeline see `docs/architecture.md` §12.1–12.2.

---

## Variants

Three variants are registered at `internal/bench/variants/`:

| Variant | Description | Cost |
|---|---|---|
| `v0-baseline` | Bing-only search, no LLM, top-3 result snippets as citations. Floor comparison. | $0 |
| `v2-raw` | Full 8-module search + fetch chain, no LLM answer synthesis. Measures retrieval quality before the answer phase. | ~half of v2-single |
| `v2-single` | Full pipeline: plan + multi-source search + fetch + LLM answer synthesis. The primary reference variant. | ~$0.15–$2.50 depending on model |

Use `v2-single` as the reference. `v0-baseline` and `v2-raw` are diagnostic baselines.

---

## Metrics

Six metrics are computed per query and aggregated into a weighted composite score (0–100). Defined in `docs/architecture.md` §12.3.

| Metric | Weight | Description |
|---|---|---|
| domain_hit | 0.30 | Fraction of `must_contain_domains` that appear in answer citations |
| term_coverage | 0.25 | Fraction of `must_contain_terms` that appear in the answer text |
| pollution_suppression | 0.15 | Penalty for `forbidden_domains` appearing in citations. 1.0 = none cited |
| source_type_diversity | 0.10 | Fraction of `expected_source_types` represented in citations |
| latency | 0.10 | Normalised wall-clock; 1.0 for fast queries, approaches 0 for slow |
| cost | 0.10 | Normalised estimated LLM cost; 1.0 for free, approaches 0 for expensive |

Composite = sum(metric * weight) * 100. Implementation: `internal/bench/scoring.go`.

---

## Results

Reference run at commit `59f1bc9`, 2026-04-13. Report: `test/bench/reports/2026-04-14-v2-single-59f1bc9.md`.

### Composite summary

| Variant | Composite | N |
|---|---|---|
| v2-single | **73.0** | 50 |
| v2-raw | 46.9 | 50 |
| v0-baseline | 35.4 | 50 |

### v2-single per-category

| Category | N | Composite | Domain hit | Term cov | Pollution | Source div |
|---|---|---|---|---|---|---|
| error_troubleshooting | 15 | 71.2 | 0.62 | 0.83 | 1.00 | 0.51 |
| api_usage | 10 | 77.3 | 0.72 | 0.97 | 1.00 | 0.37 |
| version_compatibility | 8 | 76.2 | 0.75 | 0.85 | 1.00 | 0.35 |
| concept_explanation | 5 | 71.0 | 0.40 | 1.00 | 1.00 | 0.53 |
| comparison | 5 | 60.4 | 0.20 | 0.92 | 0.98 | 0.30 |
| fuzzy_recall | 5 | 78.9 | 0.80 | 1.00 | 1.00 | 0.13 |
| time_sensitive | 2 | 74.2 | 0.75 | 0.70 | 1.00 | 0.75 |

### v2-single per-metric (mean across 50 queries)

| Metric | Mean |
|---|---|
| Domain hit rate | 0.62 |
| Term coverage | 0.90 |
| Pollution suppression | 1.00 |
| Source-type diversity | 0.41 |
| Latency | 0.30 |
| Cost | 0.98 |

Top single-query composite: 95.0 (`au_009`). Failures: 0 of 50.

---

## Reproducing

Required environment variables (at minimum one LLM key):

```sh
export ANTHROPIC_API_KEY=...      # or OPENAI_API_KEY + OPENAI_BASE_URL
export BRAVE_API_KEY=...          # optional: enables Brave module
export GITHUB_TOKEN=...           # optional: lifts GitHub rate limit
```

Run the v2-single variant:

```sh
diting bench run --variant v2-single --concurrency 4 --per-query-timeout 180s
```

Expected wall-clock: ~22 minutes at 4-worker concurrency (model-dependent). Reports are written to `test/bench/reports/YYYY-MM-DD-<variant>-<commit>.md` (with a sibling `<variant>-<commit>.json` for machine consumption).

For v0-baseline (free, no LLM key needed):

```sh
diting bench run --variant v0-baseline --concurrency 4 --per-query-timeout 30s
```

See `docs/bench/HANDOFF.md` §3–5 for the full environment checklist, pre-flight steps, and concurrency tuning guidance.

---

## Limitations

- **LLM stochasticity**: answer quality varies between runs on the same query. The reference numbers above are single-run snapshots, not averages over multiple runs.
- **Cost ceiling**: the benchmark does not enforce a hard per-query cost limit. Running with Claude Opus or other expensive models may cost 10-50x more than the reference run (DeepSeek Chat for both plan and answer at commit `59f1bc9`).
- **No v1-python head-to-head**: the `v1-python` subprocess variant is not implemented. The Phase 5 gate (`v2-single >= v1-python composite`) cannot be evaluated until that variant exists. The 73.0 number is the v2 working baseline; the comparison is deferred.
- **Query set coverage**: 50 queries across 7 categories is sufficient for regression detection but does not cover all technical domains evenly. The `comparison` category shows the lowest composite (60.4), suggesting that multi-tool trade-off questions are a systematic weak point.
