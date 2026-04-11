# Audit Prompt — diting Benchmark Query Batch Review

> **Audience**: Claude (future self). Read this file before auditing any benchmark-query draft produced by GPT-5.4. Follow it step by step. Do not skip the checklists.
>
> **Context**: The companion file `generate_queries_prompt.md` is the prompt GPT-5.4 used to draft the batch. Your audit is the middle stage of a three-stage pipeline:
>
> ```
> GPT-5.4 (draft) → YOU AUDIT HERE → human final review → test/bench/queries.yaml
> ```
>
> You are not the final authority. You are a high-quality filter. Catch everything suspicious so the human review stage is fast.

---

## Step 0 — Before you start auditing

Read these files in order. Do NOT skip any of them. If any are missing, stop and report the missing file to the user.

1. **`docs/bench/generate_queries_prompt.md`** — the original GPT-5.4 prompt. This is the ground truth for schema, category definitions, quality bars, anti-hallucination rules, and diversity requirements. Every audit criterion below flows from this file.
2. **`docs/architecture.md` sections 3, 5, and 12** — refresh your memory on what diting is, what `source_type` enum values are legal, and what the benchmark is for.
3. **`docs/bench/drafts/<category>.yaml`** — the draft to audit. This is what GPT-5.4 produced.
4. **`docs/bench/final/queries.yaml`** (if it exists) — previously vetted queries. Used for cross-batch deduplication.
5. **Any previous audit files** under `docs/bench/audits/` — to pick up on patterns from earlier batches (if GPT-5.4 repeatedly makes the same mistake, call it out).

If the user asks you to "audit X" without specifying a file, infer the category from context and look under `docs/bench/drafts/` first. If you cannot find it, ask.

---

## Step 1 — Schema compliance check

This is a fast mechanical pass. Run through it first so you reject malformed drafts before spending time on content review.

### Required top-level fields

```yaml
batch:
  category:       # must match one of the 7 categories
  count:          # integer
  generator:      # "gpt-5.4"
  generated_at:   # ISO 8601 timestamp
  notes: |        # multi-line string
queries:          # list
```

If `batch` or `queries` is missing at the top level → **REJECT with message "schema: missing top-level field"**.

### Required per-query fields

For every item in `queries`:

```yaml
- id:                           # "<prefix>_<3digit>" e.g. "et_001"
  type:                         # must equal batch.category
  query:                        # string, the question text
  intent:                       # single-sentence string
  difficulty:                   # easy | medium | hard
  tech_area:                    # from the tech_area enum
  ground_truth:
    must_contain_domains:       # 2-4 items
      - pattern:                # hostname or suffix
        rationale:              # why authoritative
    must_contain_terms:         # 2-5 items
      - term:                   # string
        rationale:              # why essential
    forbidden_domains:          # 1-4 items (may be empty if none apply)
      - pattern:
        rationale:
    expected_source_types:      # ordered list of source_type values
    canonical_answer: |         # the actual answer, written by GPT-5.4
  reviewer_notes:               # optional
```

### Enum validation

- `type` must be one of: `error_troubleshooting`, `api_usage`, `version_compatibility`, `concept_explanation`, `comparison`, `fuzzy_recall`, `time_sensitive`.
- `difficulty` must be one of: `easy`, `medium`, `hard`.
- `tech_area` must come from the enum in `generate_queries_prompt.md` section 6.
- Each `expected_source_types` entry must be one of: `general_web`, `academic`, `code`, `community`, `docs`.

### Count check

- `batch.count` must equal `len(queries)`.
- `batch.count` must equal the target for the category (see table below). If fewer, the draft is incomplete — report it but still audit what is present.

| Category | Target count |
|---|---|
| `error_troubleshooting` | 15 |
| `api_usage` | 10 |
| `version_compatibility` | 8 |
| `concept_explanation` | 5 |
| `comparison` | 5 |
| `fuzzy_recall` | 5 |
| `time_sensitive` | 2 |

### ID uniqueness

- All `id` values must be unique within the batch.
- All `id` values must be globally unique when combined with any already-final queries in `docs/bench/final/queries.yaml`.

---

## Step 2 — Anti-hallucination audit (the expensive, important one)

This is where GPT-5.4 is most likely to fail. Be paranoid. **When in doubt, flag it.**

For each query, run these checks:

### 2.1 — Do `must_contain_domains` actually exist and serve technical content?

Use your own knowledge to rate each domain:

- ✅ **Confident it exists and is authoritative**: e.g. `docs.python.org`, `kubernetes.io`, `arxiv.org`, `github.com/<known-org>`, `stackoverflow.com`.
- ⚠ **Uncertain** — domain plausibly exists but you cannot confirm its authority for the specific topic: flag for human review.
- ❌ **Confident it does not exist or is not authoritative**: flag as hallucination, query must be revised.

Common GPT-5.4 failure patterns to check for:
- **Fabricated subdomains**: e.g., `advanced-python-tips.com`, `rustlang-docs.com`. Real official docs always live under well-known domains.
- **Wrong owners of real names**: e.g., `typescript.dev` instead of `typescriptlang.org`.
- **Dead sites**: e.g., `planet.gnome.org` still exists but is not authoritative for modern topics.
- **Low-authority aggregators listed as authoritative**: e.g., `dev.to`, `hackernoon.com` — these are fine as community sources but should not be in `must_contain_domains` for an authoritative answer.

### 2.2 — Do `must_contain_terms` actually belong in the canonical answer?

- Read the `canonical_answer` carefully.
- For each term in `must_contain_terms`, verify that the term appears in (or is plainly implied by) the canonical answer.
- If the canonical answer itself does not contain a term, GPT-5.4 is forcing a keyword that is not actually required → flag for revision.
- Watch for made-up acronyms or API names that look plausible but do not exist. Example: `asyncio.TaskGroup` is real (Python 3.11+), but `asyncio.ConcurrentPool` is not.

### 2.3 — Is the `canonical_answer` factually correct?

This is the hardest check. Apply your best knowledge of the subject.

- For **concept_explanation** and **api_usage**: verify the answer is not subtly wrong (e.g., swapping semantics of two similar APIs).
- For **version_compatibility**: verify the version number and the claim. GPT-5.4 has a known tendency to confuse what was added in which version.
- For **error_troubleshooting**: verify the named cause matches the observed symptom. A common failure is attributing a symptom to a plausible-but-wrong root cause.
- For **fuzzy_recall**: verify the named entity is real AND matches the clues in the query. If the query says "S-prefixed 2022 paper on transformer alternative" and the canonical answer says "Reformer", that is a hallucinated mismatch (Reformer is from 2020 and starts with R).
- For **comparison**: verify all named alternatives exist. Verify they are genuine alternatives for the stated use case.
- For **time_sensitive**: accept that your knowledge may be stale. Flag these for human review rather than approving or rejecting outright.

When you are under **70 % confident** in a canonical answer, flag it for human verification. Do not silently approve.

### 2.4 — Are `forbidden_domains` actually likely to pollute this query?

- `baijiahao.baidu.com` is only relevant for queries that Baidu would pick up (Chinese or general developer topics).
- `jb51.net` is only relevant if the subject is popular enough to have Chinese mirror spam.
- If GPT-5.4 added a forbidden domain that has no reason to appear for this topic, it is noise — flag to remove.

### 2.5 — For `fuzzy_recall` queries specifically

Run one extra check: can *you* answer the query from the clues alone (before reading the canonical answer)? If yes, the query is valid and the canonical answer should match your answer. If the two diverge, one is wrong — flag it and state your own best guess so the human can disambiguate.

If you cannot answer the query from the clues alone, the clues are either too vague or the target does not exist. Flag for human review.

---

## Step 3 — Query-quality audit

Independent of hallucination, each query must actually be *useful* for benchmarking diting.

### 3.1 — Is the query too easy?

A query is too easy if any of:

- A well-trained LLM can answer accurately from memory alone (e.g., "What is HTTP?" — diting adds nothing).
- The answer is in the first paragraph of any single doc page.
- The query can be answered from the title of a single Stack Overflow question.

Flag easy queries for removal unless they are explicitly in the `easy` difficulty slot AND the category definition allows them (most categories do not).

### 3.2 — Is the query too hard or ambiguous?

A query is too hard if any of:

- No single right answer exists (opinion, taste).
- The correct answer depends on unstated context (platform, version, region).
- The query is so specific that no public source would plausibly document it.

Flag ambiguous queries for rewording.

### 3.3 — Is the query non-technical?

diting is a developer research tool. Reject any query that is not technical. "What are good Italian restaurants in Shanghai" is an automatic reject regardless of how well-formed it is.

### 3.4 — Is the query honest to its category?

Re-read the category definition in `generate_queries_prompt.md` section 4 for this batch's category. Verify each query actually belongs in this category and not a different one. Example: a query worded as "Why does X fail" is actually `api_usage` if the "failure" is really "I don't know how to use it".

### 3.5 — Is the canonical answer verifiable by web search?

For each query, mentally simulate: if a perfect diting were to fetch the top-5 `must_contain_domains` URLs, could it answer correctly? If the answer requires information that is not written down publicly (e.g., a private Slack message, a paywalled paper, a verbal statement at a conference), the query is not a benchmark query — flag for removal.

---

## Step 4 — Diversity audit

Check against the diversity requirements from `generate_queries_prompt.md` section 7.

| Batch category | Min distinct `tech_area` |
|---|---|
| `error_troubleshooting` (15) | 8 |
| `api_usage` (10) | 6 |
| `version_compatibility` (8) | 5 |
| `concept_explanation` (5) | 4 |
| `comparison` (5) | 4 |
| `fuzzy_recall` (5) | 4 |
| `time_sensitive` (2) | 2 |

### Per-area cap

No more than **2 queries** may share the same `tech_area` in a single batch. **Exception**: `error_troubleshooting` allows up to **3** from `python_ecosystem`.

### Difficulty distribution sanity

- A batch should not be 100 % `easy` or 100 % `hard`. A reasonable mix is ~20 % easy, ~60 % medium, ~20 % hard.
- If the distribution is skewed, flag it for rebalancing. Not a hard reject.

### Near-duplicate detection within the batch

Compare every pair of queries. A pair is a near-duplicate if any of:

- Same library/tool + same type of problem (e.g., two queries both about "why does `asyncio.gather` swallow exceptions").
- Same canonical answer.
- Same `tech_area` + overlapping `must_contain_domains` + similar phrasing.

Flag one of the pair for removal (prefer keeping the higher-difficulty one).

---

## Step 5 — Cross-batch deduplication

If `docs/bench/final/queries.yaml` exists, load it and check every new query against every finalised query. A cross-batch duplicate is worse than an intra-batch duplicate — it wastes two batches of work.

Cross-batch duplication criteria (stricter than intra-batch):

- Same `tech_area` + same target concept/tool + same difficulty → duplicate, drop the new one.
- Different phrasing but equivalent `canonical_answer` → duplicate, drop the new one.

---

## Step 6 — Decision matrix

For each query, assign one of four verdicts:

| Verdict | Meaning | Examples |
|---|---|---|
| **ACCEPT** | Ship as-is. No changes needed. | Schema valid, canonical answer correct, domains verified, no duplicates. |
| **REVISE-MINOR** | Small fix required; can be auto-applied by human. | Typo in query, wrong `difficulty` label, one `forbidden_domain` irrelevant. |
| **REVISE-MAJOR** | Significant change required; GPT-5.4 should rewrite. | Canonical answer has subtle factual error; ≥ 2 `must_contain_domains` unverified; query ambiguous. |
| **REJECT** | Drop entirely. | Hallucinated tool/paper; non-technical; duplicate of finalised query; query is too easy. |

### Batch-level recommendation

Based on per-query verdicts, recommend one of:

- **SHIP** — ≥ 80 % ACCEPT, no REJECTs on critical items, diversity OK. Human reviewer will do a fast scan and commit to `final/`.
- **PARTIAL-SHIP** — Accept the valid subset, send the REVISE/REJECT ones back to GPT-5.4 for a supplemental draft.
- **REDO** — < 50 % ACCEPT or systematic hallucination pattern. Whole batch back to GPT-5.4 with stronger prompt guard rails.

---

## Step 7 — Audit report format

Write your output to `docs/bench/audits/<category>.md`. Use this exact structure. Do not skip sections.

```markdown
# Audit — <category>

- **Draft**: `docs/bench/drafts/<category>.yaml`
- **Generator**: gpt-5.4
- **Auditor**: Claude (<model name from environment if available>)
- **Audit date**: <YYYY-MM-DD>
- **Target count**: <N>
- **Actual count**: <N>

## Summary

| Verdict | Count | Percentage |
|---|---|---|
| ACCEPT       | X | XX% |
| REVISE-MINOR | Y | YY% |
| REVISE-MAJOR | Z | ZZ% |
| REJECT       | W | WW% |

**Batch recommendation**: SHIP | PARTIAL-SHIP | REDO

**One-line rationale**: <why this recommendation>

## Batch-level findings

- **Schema compliance**: pass | fail (<reason>)
- **Diversity**: <X> distinct `tech_area` values (requirement: ≥ <Y>) — pass | fail
- **Intra-batch duplicates**: <N> pair(s) — list them
- **Cross-batch duplicates** (vs `final/queries.yaml`): <N> — list them
- **Hallucination incidents**: <N> — list query IDs
- **Difficulty distribution**: easy=X, medium=Y, hard=Z

## Per-query findings

### <id> — <verdict>

**Query**: <verbatim first line of the query>

**Findings**:
- <bullet points of issues found>
- <or "clean" if ACCEPT>

**Suggested fix** (if REVISE-*):
- <concrete actionable fix>

<repeat for each query>

## Notes for the human reviewer

- <any patterns observed across the batch>
- <things I was uncertain about that need a human call>
- <anything GPT-5.4 did well that we want to encourage in future prompts>
```

Keep the per-query section under 50 words per query when ACCEPT. Longer is fine for REVISE/REJECT.

---

## Step 8 — Critical failure patterns to always watch for

These are known GPT-5.4 failure modes from past batches. Always check.

### Pattern A — "Plausible but fictional libraries"

GPT-5.4 sometimes invents library names that sound real. Example: "FastHTTPX", "AsyncRedisPool", "pytest-rich-report". If you have not heard of a library named in a query, **do not assume it exists**. Treat it as a hallucination until proven otherwise.

### Pattern B — "Off-by-one version claims"

"`PodSecurityPolicy` was removed in Kubernetes 1.25" — is that right? (It was deprecated in 1.21 and removed in 1.25. GPT-5.4 might say 1.24 or 1.26.) For every version_compatibility query, cross-reference the exact version.

### Pattern C — "Subtle API semantics swap"

GPT-5.4 sometimes swaps the semantics of two near-neighbour APIs. Example: confusing `pandas.DataFrame.at` and `pandas.DataFrame.iat`, or `asyncio.gather(return_exceptions=True)` with `asyncio.wait`. Read the canonical_answer carefully when it covers similar-named APIs.

### Pattern D — "Fabricated Chinese translations"

For Chinese queries, GPT-5.4 may use Chinese technical terms that do not match standard Chinese technical usage. If a Chinese query uses a term you have not seen used this way in Chinese technical writing, flag for native-speaker review.

### Pattern E — "Canonical answer too vague"

The canonical answer must be specific enough to verify. "It depends on the configuration" is not a canonical answer. Reject.

### Pattern F — "Query that is just a title lookup"

"What is the paper that introduced X" is fine. "What is paper XYZ-2022 about" is just a title lookup and any search engine can answer it. Reject unless the target entity is genuinely obscure.

### Pattern G — "Query whose answer is trivially in training data"

"What is the syntax of a Python for-loop" — LLMs know this. These queries measure nothing about diting. Reject.

### Pattern H — "Ground truth domains are the answer itself"

If `must_contain_domains` includes the exact URL that literally spells out the answer in its title tag (e.g., `docs.python.org/3/library/functools.html#functools.lru_cache` for a query about `functools.lru_cache`), that is too easy. It should be there, but the canonical answer must require actually reading the page, not just reading the URL. Flag for difficulty re-rating.

---

## Step 9 — When to push back to the user

Escalate to the user (do not silently decide) when:

- ≥ 30 % of queries fail hallucination checks → prompt may need strengthening, not just another draft.
- A category consistently fails: the category definition in `generate_queries_prompt.md` may need to be tightened or split.
- You are unsure whether a specific technical claim is true: defer to the human reviewer.
- A query raises a safety concern (e.g., asks about exploiting a specific CVE in a way that reads as "help me hack something"): flag and stop, do not audit further for that query.

---

## Step 10 — After the audit

1. Save the audit report to `docs/bench/audits/<category>.md`.
2. If your recommendation is SHIP or PARTIAL-SHIP, wait for human review.
3. If REDO, suggest specific prompt-strengthening edits for `generate_queries_prompt.md` so the next attempt does better.
4. Do not commit anything to git. The human handles commits to `final/queries.yaml` after their own review.
5. Report back to the user with:
   - A one-paragraph summary of what you found.
   - The verdict counts.
   - The batch recommendation.
   - The top 3 items that most need human attention.

---

## Self-check: you are ready to audit when you can answer all of these

Before touching a draft, verify you can answer each question. If you cannot, re-read the relevant file.

- What are the 7 categories and their target counts?
- What are the 5 legal `source_type` values?
- What are the diversity requirements per category?
- What does the schema look like for a single query entry?
- What are the 8 critical failure patterns (A–H) to watch for?
- What is the decision matrix (ACCEPT / REVISE-MINOR / REVISE-MAJOR / REJECT)?
- When do you escalate to the user instead of deciding yourself?

If you can answer all of these from memory, you are ready. Begin at Step 0.
