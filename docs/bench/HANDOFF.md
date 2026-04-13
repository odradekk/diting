# Phase 5.7 Benchmark Handoff

**Status**: 🟡 Ready to run — awaiting operator go-ahead
**Objective**: Produce and commit the first real benchmark report for diting v2
**Authoritative spec**: `docs/architecture.md` §14 Phase 5 + §12 Benchmark methodology
**Last updated**: 2026-04-13

---

## 1. What this document is

This is a self-contained runbook for the person (or LLM agent) picking up
Phase 5.7 after the variants have been implemented. Everything you need
to run the first benchmark is in this file — you should not need to read
`architecture.md` end-to-end unless you want deeper context. Cross-
references are provided where they help.

Phase 5.6 landed 2026-04-13: three variant packages (`v0-baseline`,
`v2-single`, `v2-raw`) are implemented, tested, and wired into the
`diting bench` CLI. 5.7 is the only remaining Phase 5 task: **run the
variants against real infrastructure, commit the rendered markdown
reports, note the composite score against the gate criterion**.

The project deliberately pauses here because the first real run consumes
LLM tokens and network time, and the author wants a human-in-the-loop
moment to sanity-check before burning a real API budget on a pipeline
that has never been exercised end-to-end against live traffic at this
scale.

---

## 2. The gate you are trying to clear

From `docs/architecture.md` §14 Phase 5:

> **Gate**: `v2-single` composite score ≥ Python v1 on the same queries.

**Important**: the gate compares `v2-single` against a `v1-python`
variant that **does not yet exist in this repo**. Per §12.4, `v1-python`
is described as "Python v1 architecture via subprocess wrapper" but
the wrapper has not been implemented and no concrete composite number
has been recorded for Python v1 on the 50-query set. That means the
gate cannot be literally evaluated today — there is no baseline
number to compare against.

### What to actually do for Phase 5.7

Given the above, the pragmatic interpretation for Phase 5.7 is:

1. **Run `v2-single` on the full 50-query set under current live
   conditions.** Capture the rendered report.
2. **Record the Overall composite score** as the diting v2 reference
   number in the commit message and in the updated architecture.md
   progress tracker.
3. **Sanity-check the number against the Phase 5 fixture bands**:
   - The fixture harness test (see `internal/bench/harness_e2e_test.go`)
     hits 94.6 / 79.7 / 66.3 on hand-constructed perfect / partial /
     polluted inputs. A real v2-single run should land *somewhere in
     the middle* — likely 65–85 — since the real query set includes
     deliberately-hard edges (time-sensitive, fuzzy-recall).
   - A composite **below 50** is a red flag that something is broken
     systemically (scorer misconfigured, LLM returning garbage, most
     queries hitting an error path). Stop and diagnose.
   - A composite **above 90** is also a red flag — it would mean the
     queries are too easy or the scorer is too lax.
4. **Do NOT claim the gate is cleared** unless and until a `v1-python`
   variant exists and produces a comparable number. Phase 5 stays 🟡
   after this run; it only goes 🟢 when there's a real head-to-head
   comparison recorded.
5. **Report back to the user** with:
   - The v2-single composite number
   - The per-category breakdown
   - Any obviously broken queries (many errors, very low scores)
   - A recommendation on whether to continue with v1-python (if they
     want to close the gate) or accept the v2-single number as the
     working baseline and defer the v1 comparison to later

v0-baseline is the floor comparison. v2-raw isolates how much the LLM
answer-synthesis step contributes. Neither blocks the gate — they are
diagnostic baselines to contextualize v2-single's number.

---

## 3. Environment preconditions

Run `diting doctor` from the repo root before anything else. It will
categorize every prerequisite as `[ OK ]` / `[WARN]` / `[FAIL]`. You
must see zero `[FAIL]` lines before continuing.

```bash
diting doctor
```

### Required

| Check | Source | Notes |
|---|---|---|
| `ANTHROPIC_API_KEY` **or** `OPENAI_API_KEY` | shell env | At least one. v2-single and v2-raw need LLM access; v0-baseline does not |
| chromium or google-chrome binary on PATH | system package | Required by the chromedp fetch layer; `doctor` will point at the exact path it found |
| Internet access | n/a | 8 search modules + the fetch chain all hit live endpoints |
| Working directory = repo root | `pwd` | Default paths (`test/bench/queries.yaml`, `test/bench/reports/`) are relative |

### Optional (recommended)

| Env var | Effect | Without it |
|---|---|---|
| `OPENAI_BASE_URL=https://api.minimaxi.com/v1` | Route OpenAI client at MiniMax (cheapest option) | Falls through to api.openai.com |
| `OPENAI_MODEL=MiniMax-M2.7-highspeed` | Pin MiniMax reasoning model | OpenAI client default (gpt-4.1-mini) |
| `ANTHROPIC_MODEL=claude-sonnet-4-20250514` | Pin a specific Sonnet version | Anthropic client default |
| `BRAVE_API_KEY` | Enables brave search module (otherwise skipped) | 7 modules instead of 8 |
| `SERP_API_KEY` | Enables serp module (otherwise skipped) | 7 modules instead of 8 |
| `GITHUB_TOKEN` | Lifts GitHub's 10 req/min anonymous limit | Anonymous rate limit, may throttle on code-heavy queries |
| `JINA_API_KEY` | Raises jina fetch layer's rate limit | Anonymous rate limit |

### Secrets hygiene

- **Never** pass a literal API key on the command line — use env vars.
- The diting config schema supports `${VAR}` references but for the
  first run, env-var-only is simpler. Skip `--config` unless you
  already have a validated `config.yaml`.
- `diting doctor` is guaranteed not to print key values — it only
  reports presence/absence. Sharing its output is safe.

---

## 4. Pre-flight smoke tests

Before burning a real run, verify the binary, registry, and query set
are all wired correctly:

```bash
# Rebuild from source against the current tree
go build -o ./diting ./cmd/diting

# 1. Registry should list all three variants
./diting bench
# expect: "Registered variants: v0-baseline, v2-raw, v2-single"

# 2. Query set loads and parses cleanly
./diting bench run --variant nonexistent 2>&1 | head -3
# expect: "error: bench: unknown variant ..." (NOT a parse error)

# 3. Environment health
./diting doctor
# expect: zero [FAIL] lines; LLM key present if running v2 variants

# 4. Test suite still passes on HEAD
go test ./... -race -count=1
# expect: all 32+ packages green
```

If any of the above fails, **stop and fix** before running the real
benchmark. A failing smoke test during a live run would corrupt cost
data and waste LLM tokens.

---

## 5. Execution plan

Run the three variants in this order:

### Step 1: `v0-baseline` (free, fast, safety net)

```bash
./diting bench run \
  --variant v0-baseline \
  --concurrency 4 \
  --per-query-timeout 30s
```

- **Cost**: $0 (no LLM)
- **Wall-clock**: ~2–5 minutes (50 queries × 1–3 s bing latency ÷ 4
  parallel workers)
- **What to verify**: report file is written to
  `test/bench/reports/YYYY-MM-DD-<commit>.md`, Overall composite is
  **low** (expect 30–50 range — the floor), Summary line shows
  50 queries scored, zero timeouts

If v0-baseline fails or hangs, there's a problem with the bing module
or the test/bench/queries.yaml file — do NOT continue to v2 variants.
Diagnose with `diting search "test query" --debug` and inspect the
bing module output.

### Step 2: `v2-single` (the reference number)

```bash
./diting bench run \
  --variant v2-single \
  --concurrency 4 \
  --per-query-timeout 180s
```

- **Cost estimate** (50 queries × typical token mix):
  - MiniMax M2.7 HighSpeed: ~$0.15 total
  - GPT-4.1-mini: ~$0.20 total
  - Claude Sonnet: ~$2.50 total
  - Claude Opus: ~$12 total (do NOT use opus for the first run)
- **Wall-clock**: 30–90 minutes depending on LLM latency and
  concurrency. Reasoning models (MiniMax, DeepSeek-R1) are slower
  per query due to `<think>` token generation but cheaper per token.
- **Per-query timeout**: bump to 180s (reasoning models regularly hit
  100–150 s on hard queries; 120 s default cuts it too fine)

This is the run that matters for the gate. Watch `stdout` for the
summary line:

```
diting bench: wrote report to test/bench/reports/YYYY-MM-DD-<commit>.md
  variant:   v2-single
  duration:  XmXXs
  composite: XX.X (p50=XX.X p95=XX.X, n=50)
  commit:    abc1234
```

The composite number is what you're capturing. Record it, then read
§2 above on how to interpret it — there is no hard gate threshold to
compare against today, only the "plausible 50–90 range" sanity check
and the "note the number + commit + report back" workflow.

### Step 3: `v2-raw` (diagnostic)

```bash
./diting bench run \
  --variant v2-raw \
  --concurrency 4 \
  --per-query-timeout 180s
```

- **Cost**: roughly half of v2-single (plan phase LLM call only; no
  answer synthesis call)
- **Wall-clock**: ~50–70 % of v2-single (answer phase skipped)
- **Purpose**: measure how much the LLM answer-synthesis step
  contributes to the composite score. A large v2-single − v2-raw gap
  means the answer phase is doing real work (stitching citations,
  filtering noise, declaring gaps). A small gap means the scorer is
  already picking up everything useful from raw fetched sources, and
  the answer prompt could be simplified.

### Concurrency tuning

Default is 4 parallel queries. Knobs:

- **Lower (2)**: if live rate limits are biting (Bing starts returning
  429s, GitHub anonymous rate limit hits, MiniMax tokens/min cap
  reached). Slower total wall-clock but gentler on endpoints.
- **Higher (8+)**: if you have paid API quotas on every provider and
  want the full 50 to finish faster. May trip transient rate limits
  on free modules (DuckDuckGo, Baidu) — errors are captured per-query
  in `Metadata["error"]` and the run continues, so this is safe to
  try.

Start at 4. Only adjust if the first run shows systematic failures.

---

## 6. Interpreting the report

Each run produces `test/bench/reports/YYYY-MM-DD-<commit>.md` with the
following structure (template: `internal/bench/report.go`):

- **Header**: variant name, timestamp, duration, commit hash
- **Overall**: composite Mean / P50 / P95, sample size, per-metric means
- **Per-category**: same aggregations grouped by query category
  (error_troubleshooting, api_usage, version_compatibility, …)
- **Top best / top worst**: individual queries ranked by composite
- **Per-query details**: one row per query with the six metrics

### The six metrics (from §12.3)

1. **domain_hit** — fraction of `must_contain_domains` that appear in
   citations. High means the scorer's authoritative sources were found.
2. **term_coverage** — fraction of `must_contain_terms` that appear in
   the answer text. High means the answer actually used the expected
   vocabulary.
3. **pollution_suppression** — penalized when `forbidden_domains`
   appear. `1.0 - (pollution_incidents / max_incidents)`, so 1.0 means
   no polluted sources were cited.
4. **source_type_diversity** — how many of `expected_source_types` were
   actually represented. 1.0 means all expected types appear in
   citations.
5. **latency** — normalized, 1.0 for instant, approaches 0 for slow
   runs (exact curve in `scoring.go`).
6. **cost** — normalized, 1.0 for free, approaches 0 for expensive.

Composite is a weighted sum per §12.3 (domain 0.30 + term 0.25 +
pollution 0.15 + diversity 0.15 + latency 0.10 + cost 0.05).

### Red flags to look for

- **Overall composite < 60**: something is systemically wrong. Check
  per-category table for a single category dragging the average down.
- **Many `error` metadata entries**: inspect a few. Common causes:
  LLM timeout (bump `--per-query-timeout`), rate-limited module
  (reduce `--concurrency`), or a specific query that consistently
  fails (may need to flag it in queries.yaml).
- **Pollution score near 0**: Baidu or a content farm is leaking into
  citations. Verify the scorer config's `default_domain_score` and
  low-quality domain list in `scorer_config.yaml`.
- **Cost score near 0**: you're using an expensive model. Cross-check
  against the `--debug` output of a single `diting search` query if
  the number seems off.

---

## 7. Known gotchas

These all come from Phase 3's manual verification and are worth
watching for:

- **MiniMax M2.7 reasoning tokens inflate output latency but NOT final
  output size.** A 3-second "real" answer can take 60+ s because of
  thinking-token generation. The per-query timeout of 180 s is deliberately
  generous; don't reduce it just because real answers look short.
- **chromedp stderr noise** was silenced in Phase 3 via `WithErrorf` /
  `WithLogf`. If you see `ERROR: unhandled node event ...` lines on
  stderr during the run, something regressed in
  `internal/fetch/chromedp/fetcher.go` — investigate before trusting
  the report.
- **Baidu CAPTCHA**: Baidu intermittently serves `百度安全验证`
  captcha pages. The module detects this and returns zero results
  rather than bogus ones — expect some Baidu queries to produce empty
  source lists under contentious conditions. This is not a variant
  bug.
- **Cold cache first run**: the fetch layer caches fetched content in
  BoltDB at `~/.cache/diting/content.db`. First run is slower; reruns
  of the same queries against the same URLs hit cache and are much
  faster. For a clean gate measurement, delete the cache before the
  real run: `rm -f ~/.cache/diting/content.db`.
- **Per-module rate limits**: if you run v2-single and v2-raw
  back-to-back, the second run may hit the same endpoints' rate
  limits. Sleep 60 s between variants, or reduce concurrency to 2 for
  the second run.
- **GitHub anonymous rate limit is 10 req/min**: without `GITHUB_TOKEN`,
  a 50-query benchmark easily exhausts this. Set `GITHUB_TOKEN` if
  you have one, or expect several github module queries to return
  empty.

---

## 8. Failure playbook

### The CLI crashes mid-run

The runner has panic recovery per-query (see
`internal/bench/runner.go`), but a panic in the variant factory itself
will exit non-zero before any query runs. Check:

```bash
# Re-run with verbose output
./diting bench run --variant v2-single 2>&1 | tee run.log
```

If the factory fails (e.g. `v2-single: no LLM provider configured`),
fix the environment and retry. Factory failures waste zero tokens.

### Many queries return `Metadata["error"]`

- **Pattern: LLM timeout** — bump `--per-query-timeout` to 300 s.
- **Pattern: 401 / invalid key** — re-source your `.env` file. Do
  NOT trust an in-shell `export` from a previous session if the
  error suggests authentication failure.
- **Pattern: connection refused / DNS** — check internet, check
  proxy env vars, retry.
- **Pattern: "fixture: no canned result for query"** — this is the
  harness e2e test's fixture variant leaking into production somehow.
  Should be impossible; investigate `cmd/diting/main.go` blank imports.

### Composite score is in the "red flag" ranges

**Below 50** (systemically broken): do NOT commit the report as a
reference baseline. Instead:

1. Save it under a diagnostic name:
   `test/bench/reports/diagnostic/v2-single-YYYY-MM-DD-LOW.md`
2. Create `docs/bench/notes/YYYY-MM-DD-v2-single-low-score.md` with:
   - Observed composite + per-category breakdown
   - Top 5 worst queries with a one-line diagnosis each (compare to
     their ground truth in `test/bench/queries.yaml`)
   - Error-metadata rate (how many queries produced `Metadata["error"]`)
   - Proposed fixes (scorer re-weighting, prompt tweaks, module
     addition, etc.) ranked by expected impact vs. effort
3. Stop. Do not continue to v2-raw. Report to the user with the
   notes file and wait for guidance.

**Above 90** (suspiciously high): the fixture-harness perfect band is
94.6, and the real query set contains time-sensitive / fuzzy-recall
queries that should suppress the overall number. If v2-single is
landing above 90, sanity-check the per-category table for a category
with only 1–2 queries dominating the average, or a scorer
misconfiguration that silently awards full marks. Commit the report
but flag the anomaly in the notes.

**Between 50 and 90** (plausible range): treat this as the working
v2-single baseline. Commit the report, update architecture.md with
the number, and report back to the user.

---

## 9. Commit workflow

Assuming v2-single produced a composite in the plausible 50–90 range
(or the user has explicitly accepted a number outside that range):

```bash
# Verify no secrets leaked into the report
grep -i 'sk-\|api.key\|token' test/bench/reports/YYYY-MM-DD-*.md
# expect: no matches

# Stage the report(s)
git add test/bench/reports/YYYY-MM-DD-*.md

# Also update architecture.md Phase 5 status
# (unmark the [ ] on 5.7, add Result line with the composite number,
#  add "Gate cleared" to the header, add handoff paragraph pointing
#  at Phase 6)
git add docs/architecture.md

# Commit with a message capturing the number
git commit -m "phase 5.7: v2-single benchmark reference (composite XX.X)"
```

Do NOT commit `v0-baseline` alone as the "first report" — the Phase 5
reference number is v2-single's. Commit v0-baseline and v2-raw
reports in the same commit (or follow-up commits) as diagnostic
evidence framing v2-single's score.

The user's instruction at the end of Phase 5.6 was explicit: produce
the infrastructure, then **notify and wait for the next command**. Do
not auto-commit; always check in before the final `git commit`.

---

## 10. Cross-references

- `docs/architecture.md` §12 — full benchmark methodology
- `docs/architecture.md` §14 Phase 5 — official Phase 5 checklist and
  gate criteria
- `internal/bench/doc.go` — library-side package doc
- `internal/bench/harness_e2e_test.go` — reference e2e test showing
  how a fixture variant is constructed and scored (good to read
  before touching any variant code)
- `test/bench/queries.yaml` — the 50-query set with ground truth
- `test/bench/testdata/fixtures/` — canned Results covering the
  perfect / partial / polluted composite bands

---

## 11. TL;DR for the impatient agent

```bash
# Setup
source .env   # or export ANTHROPIC_API_KEY / OPENAI_API_KEY yourself
go build -o ./diting ./cmd/diting
./diting doctor   # verify environment

# Floor measurement
./diting bench run --variant v0-baseline

# The reference number (record it, don't claim gate)
./diting bench run --variant v2-single --per-query-timeout 180s

# Diagnostic
./diting bench run --variant v2-raw --per-query-timeout 180s

# Composite interpretation:
#   < 50  → systemically broken, diagnose and do NOT commit as baseline
#   50–90 → plausible, commit as v2-single reference number
#   > 90  → suspiciously high, commit but flag for sanity-check
```

### About "the gate"

§14 Phase 5 literally states `v2-single ≥ v1-python`, but `v1-python`
is not implemented yet — see §2 above. Phase 5.7 as practiced today
is **"capture v2-single's reference composite number and commit it"**;
the v1 head-to-head comparison is deferred until someone implements
the `v1-python` subprocess variant. After Phase 5.7 commits a
v2-single number, Phase 5 stays 🟡 with 5.8 "v1-python comparison
recorded" as implicit follow-up work, unless the user explicitly
marks the phase green.

### Critical: wait for the user

If you are an LLM agent reading this: **do not run the benchmark
without confirming with the user first**. The instruction at Phase 5.6
was to prepare infrastructure and wait. Always check in before spending
tokens. "The user told me to read HANDOFF.md" is not the same as
"the user told me to run the benchmark". Ask.
