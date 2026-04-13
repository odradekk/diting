# Bench Report — v2-single

- Generated: 2026-04-13T15:23:52Z
- Started: 2026-04-13T14:59:49Z
- Duration: 24m2.749s
- Commit: 9bdbbf7

## Composite

Composite: 62.4/100 across 50 queries

## Per-category

| Category | N | Composite | Domain hit | Term cov | Pollution | Source div |
|---|---|---|---|---|---|---|
| error_troubleshooting | 15 | 66.4 | 0.51 | 0.81 | 1.00 | 0.51 |
| api_usage | 10 | 59.9 | 0.55 | 0.65 | 1.00 | 0.10 |
| version_compatibility | 8 | 66.6 | 0.56 | 0.76 | 1.00 | 0.31 |
| concept_explanation | 5 | 64.8 | 0.40 | 0.96 | 1.00 | 0.33 |
| comparison | 5 | 56.3 | 0.20 | 0.92 | 1.00 | 0.10 |
| fuzzy_recall | 5 | 55.5 | 0.40 | 0.58 | 1.00 | 0.27 |
| time_sensitive | 2 | 55.3 | 0.50 | 0.50 | 1.00 | 0.25 |

## Per-metric drill-down

| Metric | Mean |
|---|---|
| Domain hit rate | 0.47 |
| Term coverage | 0.76 |
| Pollution suppression | 1.00 |
| Source-type diversity | 0.30 |
| Latency | 0.14 |
| Cost | 0.98 |

## Top-10 best queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
| et_007 | 89.8 | 1.00 | 1.00 | A CrashLoopBackOff occurs when a container keeps restarting, and in a Deploymen… |
| vc_008 | 89.1 | 1.00 | 1.00 | Spring Boot 3.4.0 was initially released with Spring Framework 6.1.0 [12]. Howe… |
| au_003 | 86.4 | 1.00 | 1.00 | In current Kubernetes (1.22+) the recommended way to give a Pod a service‑acc… |
| vc_002 | 85.1 | 1.00 | 1.00 | In ESLint 9, flat config with `eslint.config.js` is the default, and legacy `.e… |
| et_003 | 83.6 | 1.00 | 0.75 | In SQLAlchemy 2.0 the connection’s execute() method was made strictly typed… |
| et_009 | 79.9 | 0.67 | 1.00 | The `Runtime.ImportModuleError` occurs because macOS zips contain Darwin-binary… |
| au_008 | 79.8 | 1.00 | 1.00 | ## Instrumenting Go net/http with OpenTelemetry and OTLP  To instrument a Go `n… |
| au_007 | 76.3 | 1.00 | 0.75 | Use `pl.scan_parquet()` with an S3 path pattern and `storage_options` for authe… |
| et_006 | 74.7 | 0.67 | 1.00 | PostgreSQL's SERIALIZABLE isolation uses Serializable Snapshot Isolation (SSI),… |
| au_006 | 74.3 | 1.00 | 0.75 | The idiomatic way to share a database pool or application state across Axum han… |


## Top-10 worst queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
| fr_005 | 24.8 | 0.00 | 0.00 | (no answer) |
| au_001 | 24.9 | 0.00 | 0.00 | (no answer) |
| au_010 | 25.6 | 0.00 | 0.00 | (no answer) |
| au_002 | 32.4 | 0.00 | 0.00 | (no answer) |
| ts_002 | 39.8 | 0.00 | 0.40 | PostgreSQL 18 was officially released on September 25, 2025, and is production-… |
| vc_006 | 40.0 | 0.00 | 0.60 | The fetched search results did not contain relevant information about FastAPI 0… |
| et_004 | 47.4 | 0.25 | 0.50 | The `ERESOLVE unable to resolve dependency tree` error occurs because npm v7+ s… |
| fr_004 | 49.4 | 0.50 | 0.25 | Litestream [1][2][3] |
| cp_003 | 49.8 | 0.00 | 1.00 | **Playwright is the recommended default choice** for E2E testing modern React/N… |
| cp_001 | 49.9 | 0.00 | 1.00 | For analytical queries over Parquet files under ~20 GB on a laptop, DuckDB is t… |


## Failed queries

4 of 50 queries reported an error during the
run. The full error metadata + per-query Result is in the sibling `.json` dump.

| ID | Error |
|---|---|
| au_001 | pipeline: answer: answer: llm: openai: request: Post "https://api.minimaxi.com/v1/chat/completions": context deadline exceeded |
| au_002 | pipeline: plan: plan: parse: plan missing queries_by_source_type |
| au_010 | pipeline: answer: answer: parse: invalid answer JSON: invalid character '\\' looking for beginning of object key string |
| fr_005 | pipeline: answer: answer: llm: openai: request: Post "https://api.minimaxi.com/v1/chat/completions": context deadline exceeded |
