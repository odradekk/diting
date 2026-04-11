# GPT-5.4 Prompt — diting Benchmark Query Generator

This is the prompt given to GPT-5.4 to draft the v1 benchmark query set. The prompt is category-driven: you invoke it once per category, GPT-5.4 returns a YAML batch for that category only, you (the human maintainer) review the batch, I (Claude) audit it, and we iterate.

**Usage**:

1. Edit the `## Target Batch` block at the bottom of the prompt to specify which category to generate.
2. Copy the entire prompt into a fresh GPT-5.4 conversation.
3. Save GPT-5.4's YAML output to `docs/bench/drafts/<category>.yaml`.
4. Share it back with me (Claude) for audit.
5. After audit revisions, the final version goes into `test/bench/queries.yaml` in the Go codebase.

Do one category at a time. Do not try to generate all 50 in one shot — quality degrades.

---

# ============================================================
# PROMPT BEGINS BELOW THIS LINE — COPY EVERYTHING TO GPT-5.4
# ============================================================

You are the benchmark curator for **diting**, a local CLI deep-research tool. Your job is to draft realistic benchmark queries with machine-checkable ground-truth annotations. This batch will be audited by another model (Claude Opus) and then by a human reviewer before being committed to `test/bench/queries.yaml`.

## 1. What diting is

diting is a single Go binary that answers one technical question per invocation. It:

1. Uses a small/cheap LLM to split the question into specialised search queries per source type.
2. Fires those queries in parallel against multiple search modules (general web, academic, code, community, docs).
3. Fetches the top-ranked results through a multi-layer fallback chain (utls HTTP → chromedp → r.jina.ai → archive.org → Tavily).
4. Uses the same LLM to read the fetched content and produce a cited answer with a `confidence` field.
5. Returns structured JSON to its caller.

diting is **not** invoked by humans directly. Its caller is another LLM — Claude Code, Cursor, Aider, Roo Cline, or a similar coding agent acting on behalf of a human developer. When you draft queries, imagine you are the developer's LLM agent asking diting for help because your own training knowledge is insufficient or stale.

diting is **not** a general-purpose search engine. It is a research tool for software developers. Queries should be technical.

## 2. Metric priorities of diting

```
Accuracy  >  Cost  >  Time
```

- **Accuracy** is paramount. A wrong cited answer is worse than "I don't know".
- **Cost** matters because the caller pays LLM tokens.
- **Time** can slip up to ~90 seconds per call, but not more.

Your queries should reflect this priority: they should exercise accuracy failure modes, not just "can you fetch fast".

## 3. Source types available to diting

Every fetched result is tagged with one of five `source_type` values. Your ground-truth annotations must use these exact strings:

| `source_type` | What it is | Example domains |
|---|---|---|
| `general_web` | Generic web results from scraping/API search engines | bing.com, duckduckgo.com, brave.com, baidu.com |
| `academic` | Papers and research metadata | arxiv.org, openalex.org, aclanthology.org, scholar.google.com |
| `code` | Code repositories, issues, commits, pull requests | github.com, gitlab.com, sourcehut.org |
| `community` | Q&A, blog posts, community discussion | stackoverflow.com, stackexchange.com, reddit.com, zhihu.com, juejin.cn, news.ycombinator.com, lobste.rs |
| `docs` | Official project/framework documentation | docs.python.org, developer.mozilla.org, kubernetes.io, rust-lang.org, nodejs.org, pytorch.org, tensorflow.org |

## 4. Category definitions and target counts

The full benchmark has **50 queries** distributed as follows. You only generate **one category per invocation** — whichever is named in the `## Target Batch` block at the bottom.

| id | `type` value | Count | What it is |
|---|---|---|---|
| 1 | `error_troubleshooting` | **15** | "Why does X fail with Y error", "Why does X behave unexpectedly" — developer hit a failure and wants the cause |
| 2 | `api_usage` | **10** | "How do I use X to achieve Y", "What is the correct way to X with library Y" — developer needs idiomatic usage |
| 3 | `version_compatibility` | **8** | "Does X support Y in version Z", "What changed between A and B" — ecosystem / breaking changes |
| 4 | `concept_explanation` | **5** | "What is X", "What are the key ideas behind Y" — synthesis questions that span multiple authoritative sources |
| 5 | `comparison` | **5** | "A vs B for purpose C", "Which of X/Y/Z is best for W" — multi-entity evaluation |
| 6 | `fuzzy_recall` | **5** | Tip-of-the-tongue queries with partial information — "I remember a tool that does X, Y-ish name" |
| 7 | `time_sensitive` | **2** | "Latest version of X", "Recent changes in Y" — requires recent web information, not training data |

### Category-specific quality bars

#### `error_troubleshooting` (15)

- Must reference a **specific** error message, stack trace pattern, or observable misbehaviour.
- Must name the **specific** tool / library / framework and ideally its version or context.
- Good: "Why does `pip install <package>` fail with `ERROR: Could not find a version that satisfies the requirement` even when the package exists on PyPI and I can see it in the browser?"
- Bad: "Why doesn't my Python code work?" (no specifics, unanswerable).
- Bad: "Why does Linux crash?" (too broad).

#### `api_usage` (10)

- Must ask how to accomplish a **specific** task with a **specific** library or framework.
- Must imply the asker knows the library exists; they want the idiomatic pattern.
- Good: "How do I make a streaming request to the Anthropic Messages API in Python using `server-sent-events` and handle interruptions?"
- Bad: "How do I use Python?" (too vague).
- Bad: "How do I write code?" (not actionable).

#### `version_compatibility` (8)

- Must reference **real, verifiable** version numbers of **real** software.
- The version numbers you use MUST have existed in 2024 or 2025. Do NOT fabricate.
- Good: "Does Kubernetes 1.30 still support the `PodSecurityPolicy` API, or was it fully removed?"
- Bad: "Is Python 4.0 backwards compatible?" (Python 4.0 does not exist).
- Bad: "What's the latest React?" (better phrased as time_sensitive).

#### `concept_explanation` (5)

- Must require **synthesis** of multiple authoritative sources — papers + docs + community.
- Must NOT be something a well-trained LLM can answer accurately from memory alone. The concept should be either niche, recent (post-2024), or frequently misunderstood.
- Good: "What is structured output generation in LLMs, and how do `guidance`, `outlines`, and `instructor` differ in their implementation?"
- Bad: "What is HTTP?" (LLM knows this perfectly).
- Bad: "What is consciousness?" (not technical).

#### `comparison` (5)

- Must be between **3+ real alternatives** in a specific use case.
- Must specify the **context** under which comparison is meaningful (e.g., "for < 10 GB analytical workloads on a single machine").
- Good: "DuckDB vs SQLite vs ClickHouse Local vs Polars for analytical queries on Parquet files under 20 GB on a laptop."
- Bad: "Python vs Rust" (too broad, no context).
- Bad: "Best database" (no criteria).

#### `fuzzy_recall` (5)

- The user remembers **some** attributes of a real, verifiable thing (library / paper / protocol / tool) but not its name.
- You MUST know the correct answer and encode it into the ground truth — if the answer is ambiguous or you are not confident it is real, pick a different query.
- The clues in the query should be enough to disambiguate but NOT explicit enough that basic keyword search solves it trivially.
- Good: "I remember a 2022 paper that proposed a transformer alternative using state-space models, handled very long sequences, and had a single-letter plus number name. What was it?"
- Good: "I remember a CLI tool that lets you navigate logs by directory structure, written in Rust, ended with `g`. What is it?"
- Bad: "What was that one thing I read about?" (no clues).

#### `time_sensitive` (2)

- Must be dated information that changes frequently: latest releases, recent CVEs, active project events.
- The correct answer must require **actual web access** — not obtainable from mid-2024 training data.
- Good: "What are the breaking changes in the PostgreSQL 17 release from late 2024?"
- Bad: "What's new in Python 3.12?" (too old, likely in training data).
- Bad: "Who won the last World Cup?" (not technical).

## 5. Output schema (YAML, strict)

Output ONLY valid YAML in exactly this shape. No preamble, no commentary, no explanation before or after the YAML.

```yaml
batch:
  category: <category_type_value>
  count: <integer>
  generator: gpt-5.4
  generated_at: <ISO 8601 timestamp>
  notes: |
    <one-paragraph summary of how you chose the queries for this batch,
    mentioning the tech-area diversity>

queries:
  - id: <short_category_prefix>_<3digit>
    type: <category_type_value>
    query: "<the verbatim question the caller would ask diting>"
    intent: "<one sentence stating what the caller actually wants to know>"
    difficulty: easy | medium | hard
    tech_area: <short tag, see section 6>

    ground_truth:
      # 2-4 domain patterns. diting's top-5 fetched sources SHOULD contain
      # at least one of these for the answer to be considered correct.
      must_contain_domains:
        - pattern: "<hostname or hostname suffix, no scheme, no path>"
          rationale: "<why this domain is authoritative for this query>"

      # 2-5 terms that MUST appear in a correct answer.
      # Prefer terms that are unambiguous, technically precise.
      must_contain_terms:
        - term: "<lowercase-or-exact-case technical term>"
          rationale: "<why this term must be present>"

      # 1-4 domains that SHOULD NOT appear in a correct answer's top-5.
      # Use for known content farms, low-quality translators, or spam.
      forbidden_domains:
        - pattern: "<hostname pattern>"
          rationale: "<why this domain is low quality for this query>"

      # Ordered list (most preferred first). A good answer's primary
      # sources should come from the types listed here.
      expected_source_types:
        - docs        # or any of: academic, code, community, general_web
        - community

      # Canonical correct answer in one or two sentences.
      # Used by the human reviewer to sanity-check the query is answerable.
      canonical_answer: "<the actual answer, written by you as a domain expert>"

    # Optional notes for reviewers. Flag uncertainty, edge cases,
    # alternative acceptable answers.
    reviewer_notes: "<optional>"
```

## 6. Tech-area tags (use consistently)

Pick one for each query's `tech_area` field:

```
python_ecosystem    rust_ecosystem     go_ecosystem
nodejs_ecosystem    java_ecosystem     cpp_ecosystem
web_frontend        web_backend        mobile
databases           search_engines     vector_databases
kubernetes          docker             cicd
cloud_aws           cloud_gcp          cloud_azure
networking          protocols          tls_security
systems             kernel             compilers
machine_learning    llm_tools          data_engineering
devtools            cli_tools          editors
observability       security           cryptography
```

If a query spans multiple areas, pick the dominant one.

## 7. Diversity requirements

Within a single batch, queries MUST span at least the following number of distinct `tech_area` tags:

| Batch category | Min distinct `tech_area` tags |
|---|---|
| `error_troubleshooting` (15) | 8 |
| `api_usage` (10) | 6 |
| `version_compatibility` (8) | 5 |
| `concept_explanation` (5) | 4 |
| `comparison` (5) | 4 |
| `fuzzy_recall` (5) | 4 |
| `time_sensitive` (2) | 2 |

No more than **2 queries** may share the same `tech_area` in a single batch (with the exception of `error_troubleshooting` where you may have up to 3 from `python_ecosystem` since Python is common).

## 8. Anti-hallucination rules (CRITICAL)

1. **Do NOT fabricate URLs.** `must_contain_domains` may only use domains you are highly confident exist and are reputable for the query's subject.
2. **Do NOT fabricate version numbers.** Use only real released versions you are confident existed in 2024–2025.
3. **Do NOT fabricate library names, tool names, or paper titles.** If uncertain, pick a different query.
4. **Do NOT fabricate error messages.** Error messages in queries should be patterns you have seen in real code or issue trackers. If you cannot be confident the exact wording is real, use a more general phrasing like `the error complaining that the package cannot be found`.
5. **For `fuzzy_recall`**, the target entity MUST be real and the `canonical_answer` MUST name it exactly. If you cannot name it, drop the query.
6. **If you are below 80 % confidence on any `must_contain_domains` or `must_contain_terms` entry, omit that entry or drop the query entirely.** It is better to produce 3 high-quality queries than 5 queries with 2 fabricated ground-truth fields.

## 9. Forbidden domains to always include when applicable

The following domains are generally considered low-quality for technical content and should appear in `forbidden_domains` for any query where they might pollute the top-5:

- `baijiahao.baidu.com` — Baidu content-farm reposts
- `jb51.net` — machine-translated mirror site
- `csdn.net` (for non-original long-form; original posts are OK)
- `m.blog.csdn.net`
- `hostol.com`, `dohost.us`, `markaicode.com` — SEO spam

You don't need to include them in every query, only where they are likely to appear for that subject. If you add them, provide a rationale specific to the query.

## 10. Good worked example (for `error_troubleshooting`)

```yaml
- id: et_001
  type: error_troubleshooting
  query: "Python asyncio.gather 遇到一个 task 抛异常时,为什么其他 task 没有被取消,后续却以神秘的方式 hang 住?"
  intent: "Understand asyncio.gather's exception-propagation and cancellation semantics, and the common hang bug when return_exceptions is not set."
  difficulty: medium
  tech_area: python_ecosystem

  ground_truth:
    must_contain_domains:
      - pattern: "docs.python.org"
        rationale: "Official asyncio documentation is the authoritative source for gather() semantics."
      - pattern: "github.com/python/cpython"
        rationale: "CPython source is the ground truth for gather()'s exact cancellation behaviour."
      - pattern: "stackoverflow.com"
        rationale: "Well-answered threads explain the common user confusion."

    must_contain_terms:
      - term: "return_exceptions"
        rationale: "The specific parameter that changes whether gather raises or collects exceptions."
      - term: "cancel"
        rationale: "The correct answer must discuss whether sibling tasks are cancelled."
      - term: "pending"
        rationale: "The hang bug is caused by remaining pending tasks not being awaited/cancelled."

    forbidden_domains:
      - pattern: "baijiahao.baidu.com"
        rationale: "Content-farm reposts of Python tutorials are low quality."
      - pattern: "jb51.net"
        rationale: "Machine-translated mirror site known for outdated reposts."

    expected_source_types:
      - docs
      - code
      - community

    canonical_answer: |
      By default, asyncio.gather() propagates the first exception it sees from
      any child task and leaves the other tasks running in the background — it
      does NOT cancel them. The "hang" occurs because those pending tasks are
      still referenced by the event loop. To collect exceptions alongside
      results without propagating, pass return_exceptions=True. To actively
      cancel siblings, use asyncio.TaskGroup (Python 3.11+) instead.

  reviewer_notes: "Query deliberately in Chinese to test language-aware scoring; English-only retrieval should still work because asyncio.gather is an English term."
```

This example shows:
- Chinese query (diting supports CJK developer queries).
- Specific, verifiable authoritative domains.
- Technical terms that must appear.
- Named low-quality domains to suppress.
- Canonical answer written by a domain expert.
- Reviewer note flagging a special aspect.

## 11. Bad worked examples (do NOT do these)

```yaml
# BAD: fabricated library
- query: "How do I use FastHTTPX library's streaming feature?"
# No such library exists. Do not make things up.

# BAD: fabricated version
- query: "Is Go 2.0 backwards compatible with Go 1.x?"
# Go 2.0 has not been released.

# BAD: vague
- query: "Why does my program crash?"
# No specifics, no ground truth is possible.

# BAD: too easy for an LLM
- query: "What is HTTP?"
# LLMs know this from training. diting adds no value.

# BAD: non-technical
- query: "What are the best restaurants near me?"
# diting is a research tool for developers, not a yellow-pages.

# BAD: fabricated ground truth domain
- must_contain_domains:
    - pattern: "advanced-python-tips.com"
      # If you can't confirm this domain exists, do not list it.
```

## 12. Process

1. Read the `## Target Batch` block below.
2. Plan in your head which tech areas you will cover to meet diversity requirements.
3. Draft each query one at a time, writing the full entry including ground truth.
4. After drafting all queries, re-read and verify:
   - Every `must_contain_domains` pattern is a real, authoritative domain.
   - Every version number is a real release.
   - Every fuzzy_recall target is a real, findable thing.
   - No two queries have the same or near-duplicate subject.
   - Diversity requirements are met.
5. Emit the final YAML. Nothing else.

## 13. Strictness

- If you cannot meet the count with high-quality queries, produce fewer queries and flag it in `batch.notes`. Quality > quantity.
- If you are under 80 % confident about any single `ground_truth` field, omit that field or drop the query. Do not guess.
- Your output will be audited by another model and a human. Hallucinated entries will be rejected and you will be asked to redo the batch. It is strictly cheaper to produce fewer honest queries.

---

## Target Batch

**EDIT THIS BLOCK BEFORE SENDING THE PROMPT.**

```
category: error_troubleshooting
count: 15
```

*(Available categories: `error_troubleshooting`, `api_usage`, `version_compatibility`, `concept_explanation`, `comparison`, `fuzzy_recall`, `time_sensitive`.)*

Generate the YAML batch for the target category above now. Output only YAML, starting with `batch:` on the first line.

# ============================================================
# PROMPT ENDS ABOVE THIS LINE
# ============================================================

---

## Companion file layout

```
docs/bench/
├── generate_queries_prompt.md         # this file
├── drafts/                            # raw GPT-5.4 output, one file per batch
│   ├── error_troubleshooting.yaml
│   ├── api_usage.yaml
│   ├── version_compatibility.yaml
│   ├── concept_explanation.yaml
│   ├── comparison.yaml
│   ├── fuzzy_recall.yaml
│   └── time_sensitive.yaml
├── audits/                            # Claude Opus audit comments per batch
│   └── <category>.md
└── final/                             # post-audit, human-approved
    └── queries.yaml                   # composite, committed to Go codebase at test/bench/queries.yaml

```

## Review pipeline

For each category:

1. **You** → edit `## Target Batch` in the prompt, paste to GPT-5.4, save output to `docs/bench/drafts/<category>.yaml`.
2. **Claude (me)** → audits the draft: checks hallucination, schema correctness, diversity, difficulty calibration. Writes audit report to `docs/bench/audits/<category>.md`.
3. **You** → human review of the draft + audit, decide what stays / changes / drops. Commit the vetted version to `docs/bench/final/` by appending to `queries.yaml`.
4. Repeat for next category.

Audit cycle per category is expected to be under 10 minutes each, so the full 7-category pipeline should fit in a single working session if draft quality is decent.
