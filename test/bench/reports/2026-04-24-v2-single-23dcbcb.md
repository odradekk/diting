# Bench Report — v2-single

- Generated: 2026-04-23T16:45:48Z
- Started: 2026-04-23T16:34:08Z
- Duration: 11m39.937s
- Commit: 23dcbcb

## Composite

Composite: 74.1/100 across 50 queries

## Per-category

| Category | N | Composite | Domain hit | Term cov | Pollution | Source div |
|---|---|---|---|---|---|---|
| error_troubleshooting | 15 | 69.8 | 0.48 | 0.81 | 1.00 | 0.39 |
| api_usage | 10 | 78.6 | 0.72 | 0.95 | 1.00 | 0.23 |
| version_compatibility | 8 | 77.9 | 0.69 | 0.86 | 1.00 | 0.40 |
| concept_explanation | 5 | 74.1 | 0.53 | 0.96 | 1.00 | 0.27 |
| comparison | 5 | 61.8 | 0.25 | 0.88 | 1.00 | 0.07 |
| fuzzy_recall | 5 | 88.0 | 0.90 | 1.00 | 1.00 | 0.47 |
| time_sensitive | 2 | 65.3 | 0.50 | 0.70 | 1.00 | 0.25 |

## Per-metric drill-down

| Metric | Mean |
|---|---|
| Domain hit rate | 0.59 |
| Term coverage | 0.88 |
| Pollution suppression | 1.00 |
| Source-type diversity | 0.32 |
| Latency | 0.65 |
| Cost | 0.99 |

## Top-10 best queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
| fr_005 | 92.7 | 1.00 | 1.00 | The tool you're describing is **direnv**. It hooks into your shell and automati… |
| fr_001 | 91.3 | 1.00 | 1.00 | The paper/model you're thinking of is **Mamba: Linear-Time Sequence Modeling wi… |
| fr_004 | 90.8 | 1.00 | 1.00 | The tool you're remembering is **Litestream**, created by Ben Johnson [3]. It i… |
| vc_008 | 90.0 | 1.00 | 0.75 | Spring Boot 3.4.x requires Spring Framework 6.2.x. The Spring Boot 3.4 release … |
| fr_003 | 89.8 | 1.00 | 1.00 | The open-source private CA from Smallstep is called **step-ca** (also referred … |
| vc_005 | 89.8 | 1.00 | 1.00 | In Next.js 15, `cookies()` and `headers()` are now asynchronous functions that … |
| vc_006 | 89.4 | 1.00 | 0.80 | Based on available search results, there is no direct evidence that FastAPI 0.1… |
| au_009 | 89.4 | 1.00 | 1.00 | To launch the Android system photo picker from Jetpack Compose and let the user… |
| vc_002 | 86.3 | 1.00 | 1.00 | No, in ESLint 9 a project **cannot** still rely on `.eslintrc.*` as the normal … |
| au_006 | 85.9 | 1.00 | 1.00 | The idiomatic way to share state across Axum handlers and nested routers is to … |


## Top-10 worst queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
| ts_002 | 49.8 | 0.00 | 0.60 | Moving a production PostgreSQL cluster from 17 to 18 requires attention to seve… |
| cp_004 | 52.1 | 0.00 | 0.80 | Based on the available search results, direct comparisons between Loki, OpenSea… |
| et_001 | 52.1 | 0.00 | 0.67 | The `pip install pandas==2.2.3` command fails on Python 3.8 because pandas 2.2.… |
| et_004 | 56.9 | 0.25 | 0.50 | The `ERESOLVE unable to resolve dependency tree` error occurs because npm v7+ e… |
| cp_005 | 57.5 | 0.00 | 1.00 | For a TypeScript monorepo with workspaces and CI, pnpm offers the best default … |
| cp_002 | 59.8 | 0.33 | 0.60 | I cannot answer your question based on the retrieved sources. The search result… |
| vc_007 | 61.3 | 0.00 | 1.00 | Yes, `engine.execute()` is deprecated in SQLAlchemy 2.0 and will be removed in … |
| et_002 | 61.4 | 0.25 | 0.75 | The ‘Error loading ASGI app. Could not import module “app”’ error when … |
| et_014 | 63.6 | 0.33 | 0.75 | The build failure occurs because Android 12 (API 31) requires that any `<activi… |
| cp_001 | 64.8 | 0.25 | 1.00 | For analytical queries over Parquet files under ~20 GB on a laptop, **DuckDB is… |

