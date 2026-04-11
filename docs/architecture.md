# diting v2 — Architecture

> **Status**: Design document for the Go rewrite. Supersedes the Python v1 architecture in `clever-wishing-lighthouse.md` Phase 1–5.
>
> **Audience**: Contributors implementing v2. Read this before writing any code.

---

## 1. Purpose and Scope

**diting** is a local, BYOK, deep-research CLI that answers a technical question by searching multiple heterogeneous sources, reading the highest-quality results, and producing a cited answer optimised for consumption by another LLM.

### In scope

- CLI binary (single Go executable) for Linux / macOS / Windows
- Multi-source search (general web / academic / code / community / docs)
- Multi-layer fetch fallback chain
- LLM-backed question answering with inline citations
- YAML configuration, BYOK only (no bundled keys)
- Structured JSON output as the default, human-readable formats optional
- Benchmark suite for architecture experimentation

### Out of scope for v2

- MCP server mode (dropped; CLI is the only interface)
- SaaS hosting
- Browser extension / GUI
- Long-lived background daemons
- Bundled API keys of any kind
- Python v1 compatibility layer

---

## 2. Design Principles

### 2.1 Metric priorities

```
Accuracy  >  Cost  >  Time
```

- **Accuracy** is the #1 metric. A fast cheap wrong answer is worthless to an LLM caller.
- **Cost** matters because each CLI invocation burns LLM tokens, and users pay from their own keys.
- **Time** can be traded away — but not infinitely. Target: **90 % of searches complete within 60 s**; p99 under 120 s. Beyond ~90 s, the caller's attention budget collapses.

### 2.2 Target user is an LLM, not a human

Every design decision assumes the caller is another LLM (Claude Code, Cursor, Aider, Roo, etc.) that received a question from a human. Implications:

- **Output is structured JSON by default.** Tables, emoji, and prose decorations waste the caller's context window.
- **Citations are inline and machine-parseable.** The caller must be able to verify any claim back to a source URL.
- **Token economy > wall-clock latency.** The caller's context window is precious; diting trades its own cheap tokens for caller-expensive tokens.
- **Idempotent, stateless invocations.** Every `diting search` call is self-contained. No session, no cross-call memory.
- **The caller can iterate.** If diting returns `confidence: low`, the caller can re-query with a refined prompt. diting does not need its own multi-round escape hatch built in.

### 2.3 BYOK (Bring Your Own Key) — non-negotiable

diting ships **zero** keys. Every external API (LLM provider, SerpAPI, Brave, Tavily, GitHub, …) is configured by the user in their local `config.yaml`. The project distributes software, not services.

### 2.4 Subtraction over addition

When two designs are viable, prefer the simpler one. v1 accumulated routing, safety nets, strategy presets, and an evaluator; v2 deliberately starts with less and earns every component by benchmark.

### 2.5 Submodule-first development

**Stabilise inputs before touching pipelines.** Search modules and the fetch layer determine the ceiling of answer quality. Pipeline tweaks cannot exceed the quality of their inputs. Therefore:

1. Build and harden every search module and every fetch layer in isolation.
2. Each submodule has its own unit + integration + smoke tests.
3. Only after inputs are stable do we experiment with pipeline orchestration.
4. Benchmark comparisons between pipeline variants are only meaningful when the input surface is a controlled variable.

---

## 3. High-Level Architecture

```
                ┌─────────────────────────────────────────────────┐
                │                    CLI (cobra)                   │
                │  diting search | fetch | config | doctor | bench │
                └───────────────────────┬─────────────────────────┘
                                        │
                                        ▼
                ┌─────────────────────────────────────────────────┐
                │                    Pipeline                       │
                │                                                   │
                │   Plan phase                                      │
                │    └─ LLM turn 1: question → {queries_by_type}   │
                │                                                   │
                │   Execute phase                                   │
                │    ├─ Parallel search (per source_type)          │
                │    │    └─ Search modules                        │
                │    ├─ Global dedup + heuristic scoring           │
                │    └─ Parallel fetch top-K (fallback chain)      │
                │                                                   │
                │   Answer phase                                    │
                │    └─ LLM turn 2 (same conversation):            │
                │         fetched content → {answer, citations}    │
                │                                                   │
                └──────────┬────────────────────────┬──────────────┘
                           │                        │
                           ▼                        ▼
                ┌──────────────────┐    ┌──────────────────────┐
                │  Search modules  │    │      Fetch layer      │
                │  ──────────────  │    │  ──────────────────  │
                │  baidu  bing     │    │  Chain:              │
                │  duckduckgo      │    │   1. utls (default)  │
                │  brave  serp     │    │   2. chromedp        │
                │  arxiv           │    │   3. r.jina.ai       │
                │  github          │    │   4. archive.org     │
                │  stackexchange   │    │   5. tavily (BYOK)   │
                │  wikipedia       │    │                      │
                │  (extensible)    │    │  Content cache       │
                │                  │    │   (SQLite, TTL)      │
                └──────────────────┘    └──────────────────────┘
                           │                        │
                           ▼                        ▼
                ┌─────────────────────────────────────────────────┐
                │              Infrastructure                      │
                │   slog (structured logs) | viper (config)        │
                │   cobra (CLI)            | resty + utls (HTTP)   │
                │   modernc sqlite (cache) | embedded prompts      │
                └─────────────────────────────────────────────────┘
```

---

## 4. Data Flow

A single `diting search "<question>"` invocation produces the following flow.

```
1. Parse CLI args, load YAML config, initialise logger.

2. Construct an LLM conversation.
   System prompt: "You are diting, a research assistant. ..."
   User turn:     <question>
   Expected LLM output (JSON):
     {
       "plan": {
         "rationale": "...",
         "queries_by_source_type": {
           "general_web": ["query A", "query B"],
           "academic":    [],
           "code":        ["query C"],
           "community":   ["query D"],
           "docs":        []
         },
         "expected_answer_shape": "..."
       }
     }

3. If --plan-only, print the plan and exit.

4. Execute the plan:
   - For each source_type with at least one query:
       For each module whose manifest.source_type matches:
         Fire each query in parallel.
   - Collect results into a flat pool, tagged with (source_type, module, query).
   - Dedup by URL (normalise first: strip tracking params, lowercase host, ...).
   - Score (heuristic only in v1: domain authority + snippet signals).
   - Select top-K by score, respecting per-source_type minimum retention.

5. Fetch the selected top-K URLs.
   - Run through the fetch fallback chain per URL.
   - Cache content in ~/.cache/diting/content.db.
   - Return (url, title, extracted_content, layer_used).

6. Continue the same LLM conversation:
   Assistant turn 1 (the plan) is in history.
   User turn 2:  "Here are the fetched results: <structured content>. Answer the original question."
   Expected LLM output (JSON):
     {
       "answer": "...<inline citations like [1] [2]>...",
       "citations": [
         {"id": 1, "url": "...", "title": "...", "source_type": "docs", "score": 0.92},
         ...
       ],
       "confidence": "high" | "medium" | "low" | "insufficient"
     }

7. Assemble final Result. If confidence == "insufficient",
   the answer must explicitly say so instead of hallucinating.

8. Emit output according to --format flag.
   Debug logs (cost, latency, layer_used) go to stderr when --debug is set.
```

**Why two turns of the same conversation instead of two separate calls?**

- Prompt caching (Anthropic / OpenAI) reuses the system prompt + user question across turn 1 and turn 2. Only the fetched content is billed as new input tokens.
- The LLM in turn 2 still "remembers" the plan it wrote, so it knows which queries it issued and why — enabling self-correction ("my `academic` query returned nothing, so I should rely more on `community`").
- Single conversation = single request context = cleaner error handling.

---

## 5. Search Module Layer

### 5.1 Interface

```go
// Package: internal/search

type SourceType string

const (
    SourceTypeGeneralWeb SourceType = "general_web"  // baidu, bing, duckduckgo, brave, serp
    SourceTypeAcademic   SourceType = "academic"     // arxiv, openalex, semantic_scholar
    SourceTypeCode       SourceType = "code"         // github, gitlab
    SourceTypeCommunity  SourceType = "community"    // stackexchange, zhihu, reddit, juejin
    SourceTypeDocs       SourceType = "docs"         // context7, devdocs, mdn
)

type CostTier string

const (
    CostTierFree      CostTier = "free"      // no API key, or free quota sufficient for personal use
    CostTierCheap     CostTier = "cheap"     // paid but low cost (e.g., Brave free tier, GitHub token)
    CostTierExpensive CostTier = "expensive" // cost per call matters (e.g., Tavily, Exa, SerpAPI)
)

type Manifest struct {
    Name       string      // stable identifier, matches registry key
    SourceType SourceType  // single primary type — no multi-type modules in v1
    CostTier   CostTier
    Languages  []string    // BCP 47 codes, e.g., "en", "zh-Hans"
    Scope      string      // human-readable description used by LLM for understanding
}

type SearchResult struct {
    Title   string
    URL     string
    Snippet string
    // Populated by the pipeline, not the module:
    Module     string     // module.Name() that produced this
    SourceType SourceType // copied from manifest
    Query      string     // the query string that produced it
}

type Module interface {
    Manifest() Manifest
    Search(ctx context.Context, query string) ([]SearchResult, error)
    Close() error
}
```

### 5.2 Module contract

Every module:

1. **Must** return within `ctx` deadline or respect cancellation.
2. **Must** return an error for HTTP failures, rate limits, parse failures; empty results is not an error.
3. **Must not** mutate package-level state (modules run concurrently).
4. **Must not** write to disk outside the content cache path.
5. **Must** be unit-testable with an HTTP mock; **should** have an integration test against the real endpoint behind a build tag.
6. **Should** expose a `Manifest()` whose `Scope` is short (≤ 200 chars) and written for another LLM to understand.

### 5.3 Module registry

Modules self-register at init time:

```go
// internal/search/registry.go
var registry = map[string]func(cfg ModuleConfig) (Module, error){}

func Register(name string, factory func(cfg ModuleConfig) (Module, error)) {
    registry[name] = factory
}

// internal/search/bing/bing.go
func init() {
    search.Register("bing", func(cfg search.ModuleConfig) (search.Module, error) {
        return New(cfg), nil
    })
}
```

The pipeline asks the registry for the modules listed in `config.yaml`, never for "all modules." Unknown module names in config produce a startup error.

### 5.4 Sources in v1

**General web** (all keyless or free tier):

- `baidu` (scraping, curl_cffi equivalent via utls)
- `bing` (scraping)
- `duckduckgo` (scraping)
- `brave` (API, BYOK, 2000 queries/month free)
- `serp` (API, BYOK, paid) — marked `expensive`

**Academic**:

- `arxiv` (keyless Atom API)
- `openalex` (keyless REST API)

**Code**:

- `github` (REST API, anonymous 10 req/min, BYOK PAT lifts to 30 req/min)

**Community**:

- `stackexchange` (keyless REST API, 300 req/day anonymous)

**Docs**:

- `context7` (MCP / HTTP interface, BYOK) — subject to feasibility check

Each source has a `Manifest` file checked into `internal/search/<name>/manifest.go`.

---

## 6. Fetch Layer

### 6.1 Interface

```go
// Package: internal/fetch

type Fetcher interface {
    Fetch(ctx context.Context, url string) (*Result, error)
    FetchMany(ctx context.Context, urls []string) ([]*Result, error)
    Close() error
}

type Result struct {
    URL         string
    FinalURL    string  // after redirects
    Title       string
    Content     string  // extracted main content (markdown)
    ContentType string  // "text/html", "application/pdf", ...
    LayerUsed   string  // "utls" | "chromedp" | "jina" | "archive" | "tavily" | "cache"
    LatencyMs   int
    FromCache   bool
    Err         error   // nil on success
}
```

### 6.2 Fallback chain

```
URL
 │
 ▼
┌──────────────┐   cache hit?   ┌───────────┐
│ Content Cache│─────Yes───────▶│  Return   │
│ (SQLite)     │                └───────────┘
└──────┬───────┘
       │ miss
       ▼
┌──────────────────────┐  success  ┌──────────┐
│ 1. utls HTTP fetcher │──────────▶│ Extract  │
│    (Chrome TLS)      │           │ (readab.)│
└──────┬───────────────┘           └────┬─────┘
       │ fail / blocked                 │
       ▼                                │
┌──────────────────────┐  success       │
│ 2. chromedp browser  │────────────────┤
│    (for JS-heavy)    │                │
└──────┬───────────────┘                │
       │ fail                           │
       ▼                                │
┌──────────────────────┐  success       │
│ 3. r.jina.ai reader  │────────────────┤
│    (BYOK optional)   │                │
└──────┬───────────────┘                │
       │ fail                           │
       ▼                                │
┌──────────────────────┐  success       │
│ 4. archive.org       │────────────────┤
│    wayback           │                │
└──────┬───────────────┘                │
       │ fail                           │
       ▼                                │
┌──────────────────────┐  success       │
│ 5. tavily (BYOK)     │────────────────┤
│    last-resort paid  │                │
└──────┬───────────────┘                │
       │ all layers failed              │
       ▼                                ▼
   Return error              Cache + return Result
```

Each layer:

- Has its own timeout (`utls: 15s`, `chromedp: 30s`, `jina: 20s`, `archive: 25s`, `tavily: 30s`).
- Is independently enabled / disabled via config.
- Logs its outcome with `layer_used` for observability.
- Never throws exceptions across layers; returns a structured error for the next layer to consider.

### 6.3 Critical risk: utls fingerprint validation — **RESOLVED**

**Status**: ✅ Gate PASSED (2026-04-11). See [ADR 0001](adr/0001-utls-fetch-layer.md).

**utls** is the Go replacement for curl_cffi's Chrome TLS fingerprinting. This was the **single highest risk** of the Go rewrite — if utls could not match curl_cffi's success rate, the whole Go path would have been abandoned.

**Spike**: `test/spike/tls_fingerprint/main.go` — 14 URLs × 4 techniques (`net/http`, `utls+chrome120`, `utls+chrome_auto`, `utls+roller`) × 8 runs.

**Result**:

| Technique | Mean success | Median | StdDev |
|---|---|---|---|
| `net/http` (baseline) | 58.9 % | 57.1 % | 4.8 |
| `utls+chrome120` | 79.5 % | 78.6 % | 2.5 |
| **`utls+chrome_auto`** | **83.9 %** | **85.7 %** | 5.0 |
| `utls+roller` | 74.1 % | 71.4 % | 6.6 |

**Gate criterion**: best utls technique must reach ≥ 80 % success → **Passed at 83.9 % mean / 85.7 % median**.

**Production choice**: `utls.HelloChrome_Auto` (moving-target alias that tracks upstream's current Chrome spec). See ADR 0001 §6 for the version-selection rationale and §7 for the multi-fingerprint roadmap.

**Spike-discovered bug** (preserved in ADR 0001 §4): `utls.Config.NextProtos` does NOT override the ALPN extension baked into `HelloChrome_*` specs. The production fetch layer must always handle ALPN-negotiated `h2` via `golang.org/x/net/http2.Transport.NewClientConn`, otherwise every server that picks h2 silently returns EOF.

### 6.4 Content cache

- Backend: SQLite (`modernc.org/sqlite`, pure Go, no CGo).
- Path: `~/.cache/diting/content.db` (configurable).
- Schema:

```sql
CREATE TABLE content (
    url TEXT PRIMARY KEY,
    final_url TEXT,
    title TEXT,
    content TEXT NOT NULL,
    content_type TEXT,
    layer_used TEXT,
    fetched_at INTEGER NOT NULL,
    ttl_seconds INTEGER NOT NULL
);
CREATE INDEX idx_fetched_at ON content(fetched_at);
```

- TTL policy (configurable, defaults):
  - News / time-sensitive domains: 1 day
  - Tech articles / blogs: 30 days
  - Academic papers (arxiv / openalex): infinite
  - Docs sites: 7 days
  - Fallback: 3 days

- Eviction: size-based (LRU by `fetched_at`) when `db` exceeds `cache.max_mb`.

---

## 7. LLM Layer

### 7.1 Client abstraction

```go
// Package: internal/llm

type Client interface {
    // Complete sends a conversation and returns the next assistant message.
    // The returned Response must include token counts for cost reporting.
    Complete(ctx context.Context, req Request) (*Response, error)
}

type Request struct {
    System      string
    Messages    []Message          // conversation so far
    JSONSchema  *JSONSchema        // when set, LLM is instructed to emit matching JSON
    MaxTokens   int
    Temperature float32
}

type Message struct {
    Role    string // "user" | "assistant"
    Content string
}

type Response struct {
    Content     string
    InputTokens int
    OutputTokens int
    CacheReadTokens int  // prompt caching hit
}
```

### 7.2 Provider implementations

- `internal/llm/anthropic`: Messages API, supports prompt caching
- `internal/llm/openai`: Chat Completions (responses API once stable), supports prompt caching
- `internal/llm/minimax`: MiniMax M2.7 HighSpeed (v1 default), OpenAI-compatible endpoint

All three implement `Client`. A provider is selected via `config.llm.provider`.

### 7.3 Default model

```yaml
llm:
  provider: minimax
  base_url: https://api.minimaxi.com/v1
  model: MiniMax-M2.7-highspeed
  api_key: ${MINIMAX_API_KEY}  # env var interpolation
```

MiniMax M2.7 HighSpeed is chosen for the reference default because of its cost/capability balance. Users override via config.

### 7.4 Two-turn single conversation

The pipeline always sends exactly one conversation, with two LLM turns interleaved around the search/fetch phase.

**Turn 1 — Plan**

```
System: <diting system prompt, see prompts/system.md>
User:   <user's question>

Expected assistant output (JSON, enforced via JSON mode or grammar):
{
  "plan": {
    "rationale": "string — why these source_types and queries",
    "queries_by_source_type": {
      "general_web": ["..."],
      "academic":    ["..."],
      "code":        [],
      "community":   [],
      "docs":        []
    },
    "expected_answer_shape": "string — what a good answer looks like"
  }
}
```

**Search + Fetch happens here.**

**Turn 2 — Answer**

```
... (turn 1 messages preserved) ...
Assistant: <the plan JSON from turn 1>
User: Here are the fetched results:

<structured content block>
SOURCE 1 [docs / score 0.92]
URL: https://docs.python.org/...
Title: asyncio — Coroutines and Tasks
Content:
  ...extracted markdown...

SOURCE 2 [community / score 0.85]
URL: https://stackoverflow.com/...
Title: ...
Content:
  ...
</structured content block>

Answer the original question using these sources. Every factual claim
must carry an inline citation like [1] or [2][3]. If the sources are
insufficient, set confidence to "insufficient" and say so explicitly
in the answer rather than guessing.

Expected assistant output (JSON):
{
  "answer": "...",
  "citations": [
    {"id": 1, "url": "...", "title": "...", "source_type": "docs"},
    ...
  ],
  "confidence": "high" | "medium" | "low" | "insufficient"
}
```

### 7.5 Prompt files

Prompts live in `prompts/` and are `//go:embed`-ed into the binary. No runtime filesystem lookup.

- `prompts/system.md` — the shared system prompt
- `prompts/plan.md` — turn 1 instructions
- `prompts/answer.md` — turn 2 instructions

Prompts are Markdown with `{{.Var}}` placeholders rendered via `text/template`.

---

## 8. Pipeline

### 8.1 Interface

```go
// Package: internal/pipeline

type Pipeline struct {
    modules  []search.Module
    fetcher  fetch.Fetcher
    llm      llm.Client
    scorer   Scorer
    config   Config
    logger   *slog.Logger
}

type Config struct {
    MaxSourcesPerType int      // per-source_type cap for fetched sources
    MaxFetchedTotal   int      // global cap across all types
    FetchTimeout      time.Duration
    AllowPlanOnly     bool     // honour --plan-only
    PlanMode          PlanMode // auto | confirm | show
}

type Result struct {
    Question    string
    Plan        Plan
    Sources     []FetchedSource  // what actually got fetched
    Answer      Answer
    Debug       DebugInfo        // populated only when debug=true
}

type Plan struct {
    Rationale              string
    QueriesBySourceType    map[search.SourceType][]string
    ExpectedAnswerShape    string
}

type Answer struct {
    Text       string
    Citations  []Citation
    Confidence string
}
```

### 8.2 Execution algorithm (v1, deliberately simple)

```
func (p *Pipeline) Run(ctx, question) (*Result, error) {
    // 1. Plan phase
    conv := llm.NewConversation(systemPrompt)
    conv.AddUser(question)
    planResp, err := p.llm.Complete(ctx, conv.AsRequest(PlanSchema))
    if err != nil { return nil, err }
    plan := parsePlan(planResp)

    // Optional: --plan-only short-circuit
    if p.config.PlanMode == PlanModeShow {
        return &Result{Question: question, Plan: plan}, nil
    }

    // 2. Execute phase — parallel search across source_types
    rawResults, err := p.parallelSearch(ctx, plan)
    if err != nil { return nil, err }

    // 3. Dedup + heuristic score
    dedupped := dedupByURL(rawResults)
    scored := p.scorer.Score(question, dedupped)

    // 4. Select top sources with per-source_type guarantee
    selected := selectTopSources(scored, p.config)

    // 5. Parallel fetch with fallback chain
    fetched, err := p.fetcher.FetchMany(ctx, urlsFrom(selected))
    if err != nil { return nil, err }

    // 6. Answer phase — same conversation, append fetched content
    conv.AddAssistant(planResp.Content)
    conv.AddUser(formatFetchedContent(fetched))
    answerResp, err := p.llm.Complete(ctx, conv.AsRequest(AnswerSchema))
    if err != nil { return nil, err }
    answer := parseAnswer(answerResp)

    return &Result{
        Question: question,
        Plan:     plan,
        Sources:  fetched,
        Answer:   answer,
        Debug:    buildDebugInfo(planResp, answerResp, ...),
    }, nil
}
```

**What is deliberately absent**:

- No routing. All modules matching the plan's source_types always fire.
- No embedding router, no LLM router, no progressive routing.
- No evaluator. No multi-round loop (v1 is strictly single-round).
- No semantic deduplication.
- No strategy presets.
- No safety nets beyond "all layers failed ⇒ return structured error".

These are **not forgotten** — they are **deferred** until the benchmark shows they pay for their complexity. See `docs/benchmark.md` (to be written) for the experiment plan.

### 8.3 Scorer (v1)

Pure heuristic, no LLM, no reranker model.

```go
type Scorer interface {
    Score(question string, results []SearchResult) []ScoredResult
}
```

Features:

- Domain authority table (loaded from `internal/data/domain_authority.json`):
  - docs.*, *.python.org, developer.mozilla.org, kubernetes.io, rust-lang.org: 1.0
  - arxiv.org, openalex.org, scholar.google.com: 0.95
  - github.com, gitlab.com: 0.90
  - stackoverflow.com, stackexchange.com: 0.85
  - Known low-quality (baijiahao.baidu.com, jb51.net, csdn.net low-tier): 0.2
  - Default: 0.5
- Title / snippet keyword overlap with the question
- Snippet length (penalise very thin snippets)
- Language match (question language vs snippet language)

Final score is a weighted sum, coefficients in `config.yaml` so they are tunable without recompiling.

BGE reranker is **not** in v1. It is an optional future feature, gated behind `--rerank` once it exists and can be compiled into the binary or loaded on demand.

---

## 9. Configuration

### 9.1 Location

- Default: `~/.config/diting/config.yaml`
- Override: `--config <path>` or `$DITING_CONFIG`
- Initial generation: `diting init` walks the user through LLM provider + enabled modules and writes the file.

### 9.2 Schema

```yaml
# ~/.config/diting/config.yaml
# diting v2 configuration. All API keys are BYOK.

llm:
  provider: minimax                # anthropic | openai | minimax | custom
  base_url: https://api.minimaxi.com/v1
  model: MiniMax-M2.7-highspeed
  api_key: ${MINIMAX_API_KEY}      # env var interpolation (required)
  timeout: 120s
  max_tokens: 4096

search:
  enabled:
    - bing
    - duckduckgo
    - arxiv
    - github
    - stackexchange

  modules:
    bing:
      timeout: 20s
      max_results: 15
    duckduckgo:
      timeout: 20s
      max_results: 15
    brave:
      api_key: ${BRAVE_API_KEY}
      timeout: 15s
      max_results: 20
    github:
      token: ${GITHUB_TOKEN}        # optional, lifts rate limit
      timeout: 15s
      max_results: 10
    serp:
      api_key: ${SERP_API_KEY}
      timeout: 20s
      max_results: 10

fetch:
  layers:
    - utls
    - chromedp
    - jina
    - archive
  # tavily is disabled unless explicitly added
  jina:
    api_key: ${JINA_API_KEY}        # optional, lifts rate limit
  chromedp:
    headless: true
    user_data_dir: ""               # empty = ephemeral
  cache:
    enabled: true
    path: ~/.cache/diting/content.db
    max_mb: 500
    default_ttl_days: 3

pipeline:
  max_sources_per_type: 3
  max_fetched_total: 8
  fetch_timeout: 40s

scoring:
  weights:
    domain_authority: 0.40
    keyword_overlap:  0.30
    snippet_quality:  0.20
    language_match:   0.10

logging:
  level: info                       # debug | info | warn | error
  format: text                      # text | json
  file: ""                          # empty = stderr
```

### 9.3 BYOK principles

- Keys are **never** embedded in the binary.
- Keys are **never** written to logs, even at debug level.
- Keys support env var interpolation (`${VAR_NAME}`) so users can keep them out of the YAML file.
- `diting doctor` checks presence of required keys for enabled modules and reports missing ones — without printing the key values.

---

## 10. CLI Commands

```
diting search "<question>"             # default: JSON answer output
diting search "<question>" --raw       # return sources without LLM synthesis
diting search "<question>" --plan-only # run plan phase, print plan, exit
diting search "<question>" --format markdown    # human-readable
diting search "<question>" --format text        # plain text answer, strip JSON
diting search "<question>" --debug              # stderr detailed logs + cost report
diting search "<question>" --no-cache           # bypass content cache
diting search "<question>" --max-cost 0.10      # abort if estimated cost > $0.10
diting search "<question>" --config /path/to/config.yaml

diting fetch "<url>"                   # single-URL fetch, prints extracted content
diting fetch "<url>" --layer utls      # force a specific fetch layer

diting config show                     # print effective configuration (keys redacted)
diting config path                     # print config file path
diting config validate                 # validate config.yaml structure
diting init                            # interactive config generator

diting doctor                          # environment health check

diting bench run                       # run benchmark suite
diting bench run --variant v2-single   # run a specific variant
diting bench report                    # show latest benchmark report

diting version
diting --help
```

All commands return exit code 0 on success, non-zero on failure. Output that matters goes to stdout, diagnostics to stderr.

---

## 11. Output Formats

### 11.1 Default (JSON)

```json
{
  "question": "Why does asyncio.gather swallow exceptions?",
  "answer": "asyncio.gather does not exactly swallow exceptions; by default, when any task raises, gather immediately propagates that first exception to the awaiter [1]. The remaining tasks continue running in the background unless you explicitly cancel them [1][2]. Pass return_exceptions=True to have gather collect exceptions alongside results instead of propagating them [1][3].",
  "citations": [
    {
      "id": 1,
      "url": "https://docs.python.org/3/library/asyncio-task.html#asyncio.gather",
      "title": "asyncio — Tasks and coroutines",
      "source_type": "docs"
    },
    {
      "id": 2,
      "url": "https://stackoverflow.com/questions/61528013/...",
      "title": "How do I handle exceptions in asyncio.gather?",
      "source_type": "community"
    },
    {
      "id": 3,
      "url": "https://github.com/python/cpython/blob/main/Lib/asyncio/tasks.py",
      "title": "cpython/Lib/asyncio/tasks.py",
      "source_type": "code"
    }
  ],
  "confidence": "high"
}
```

- All text compact, no pretty-printing by default.
- Citations ordered by `id` (ascending), matching the `[N]` markers in the answer.
- `confidence` values: `high` / `medium` / `low` / `insufficient`.
- When `confidence == "insufficient"`, the `answer` field **must** explicitly state that authoritative sources were not found. The caller can then decide whether to retry with a different query or fall back to the LLM caller's own knowledge.

### 11.2 `--raw` (no LLM synthesis)

```json
{
  "question": "...",
  "sources": [
    {
      "source_type": "docs",
      "url": "...",
      "title": "...",
      "snippet": "...",
      "content": "...full extracted markdown...",
      "score": 0.92,
      "module": "bing",
      "fetch_layer": "utls"
    },
    ...
  ]
}
```

- Zero LLM calls after the plan phase.
- Caller is responsible for synthesis.
- Still runs the plan phase to produce specialised queries per source_type.

### 11.3 `--plan-only`

```json
{
  "question": "...",
  "plan": {
    "rationale": "...",
    "queries_by_source_type": {
      "general_web": ["..."],
      "academic":    [],
      ...
    },
    "expected_answer_shape": "..."
  }
}
```

One LLM call, no search, no fetch. Useful for debugging and for callers that want to inspect the plan before committing budget.

### 11.4 `--format markdown` / `--format text`

Human-readable rendering of the default JSON. Markdown wraps citations as footnote-style links; text strips citations to plain `[N]`.

### 11.5 Debug output (stderr, `--debug`)

Cost and latency never appear in stdout output. They go to stderr as structured logs when `--debug` is set:

```
time=2026-04-11T10:30:15Z level=INFO msg="pipeline.plan" tokens_in=320 tokens_out=180 cache_read=0 latency_ms=1250
time=2026-04-11T10:30:18Z level=INFO msg="pipeline.search" source_type=general_web query="..." results=12 latency_ms=2300
time=2026-04-11T10:30:20Z level=INFO msg="pipeline.fetch" url="..." layer=utls success=true latency_ms=1800
time=2026-04-11T10:30:25Z level=INFO msg="pipeline.answer" tokens_in=8400 tokens_out=410 cache_read=320 latency_ms=3100
time=2026-04-11T10:30:25Z level=INFO msg="pipeline.done" total_cost_usd=0.047 wall_ms=10200 fetched=6 confidence=high
```

Production invocations (no `--debug`) are silent on stderr unless an error occurs.

---

## 12. Benchmarking

The benchmark suite is the **only** accepted evidence for adding complexity back to the pipeline. If a new component does not improve a benchmark metric by a meaningful margin, it is rejected.

### 12.1 Query set

`test/bench/queries.yaml` contains **50 queries** distributed per the target usage profile:

| Bucket | Count | Rationale |
|---|---|---|
| Error troubleshooting ("Why does X fail") | 15 | Codex-predicted 50 %+ of real traffic |
| API usage ("How do I use Y") | 10 | Second-most common |
| Version / compatibility ("Is Z in version V") | 8 | Common for developers tracking ecosystem |
| Concept explanation ("What is X") | 5 | Less common because callers often know it |
| Comparison ("A vs B") | 5 | Harder queries that stress synthesis |
| Fuzzy / tip-of-tongue | 5 | Includes the S4 / Mamba case as a golden test |
| Time-sensitive | 2 | Latest releases, breaking changes |

### 12.2 Ground-truth pipeline

Three-stage labelling:

1. **Draft**: GPT-5.4 generates the initial `must_contain_domains`, `must_contain_terms`, `forbidden_domains`, and `expected_source_types` per query.
2. **Audit**: Claude Opus reviews GPT-5.4's drafts, flags disagreements, proposes edits.
3. **Final**: Human (project maintainer) reviews both outputs and commits the final ground-truth YAML.

Each query's ground truth is versioned. Changes to ground truth require bumping `bench_version`.

### 12.3 Metrics

| Metric | Weight | Definition |
|---|---|---|
| Domain hit rate | 30 % | `must_contain_domains` ∩ top-5 citations |
| Term coverage | 25 % | `must_contain_terms` ∩ answer text |
| Pollution suppression | 15 % | `forbidden_domains` ∩ top-5 (inverse) |
| Source-type diversity | 10 % | distinct source_types in fetched top-K / expected |
| Latency (p95) | 10 % | normalised to `1 - min(latency/90s, 1)` |
| Token cost (mean) | 10 % | normalised to `1 - min(usd/budget, 1)` |

Composite score = weighted sum, 0–100.

### 12.4 Variants

| Variant | Description |
|---|---|
| `v0-baseline` | Single search module (bing only), no LLM answer — just top-3 snippets |
| `v1-python` | Python v1 architecture via subprocess wrapper |
| `v2-single` | Go v2 default: plan + execute + answer, single round |
| `v2-raw` | Go v2 with `--raw`, no LLM synthesis |
| `v2-plus-refine` | Go v2 with hypothetical RefinementController (built only if v2-single underperforms) |

**Decision rule (from Codex):** If `v2-plus-refine` improves composite score by **≥ 5 percentage points** over `v2-single` with token cost increase **< 50 %**, RefinementController ships. Otherwise it does not.

### 12.5 Running the benchmark

```bash
diting bench run                     # runs all variants currently compiled
diting bench run --variant v2-single # single variant
diting bench report                  # Markdown report from last run
```

Benchmark reports are committed to `test/bench/reports/YYYY-MM-DD-<commit>.md` so history is reviewable.

---

## 13. Go Project Structure

```
diting/                                   # new repo, separate from Python v1
├── cmd/
│   └── diting/
│       └── main.go                       # cobra setup, command wiring
├── internal/
│   ├── search/
│   │   ├── module.go                     # Module interface + Manifest type
│   │   ├── registry.go                   # self-registration
│   │   ├── result.go                     # SearchResult, scoring types
│   │   ├── baidu/
│   │   ├── bing/
│   │   ├── duckduckgo/
│   │   ├── brave/
│   │   ├── serp/
│   │   ├── arxiv/
│   │   ├── github/
│   │   ├── stackexchange/
│   │   └── (future: openalex, wikipedia, context7, ...)
│   ├── fetch/
│   │   ├── fetcher.go                    # Fetcher interface, chain orchestrator
│   │   ├── chain.go                      # multi-layer chain implementation
│   │   ├── utls/                         # utls-based HTTP fetcher
│   │   ├── chromedp/                     # browser fallback
│   │   ├── jina/                         # r.jina.ai reader
│   │   ├── archive/                      # wayback / archive.today
│   │   ├── tavily/                       # Tavily extract API (BYOK)
│   │   ├── cache/                        # SQLite content cache
│   │   └── extract/                      # go-readability / goquery extraction
│   ├── llm/
│   │   ├── client.go                     # Client interface, Request/Response types
│   │   ├── conversation.go               # conversation builder
│   │   ├── schema.go                     # JSONSchema helpers
│   │   ├── anthropic/
│   │   ├── openai/
│   │   └── minimax/
│   ├── pipeline/
│   │   ├── pipeline.go                   # Pipeline struct, Run method
│   │   ├── plan.go                       # turn 1 logic, Plan type
│   │   ├── execute.go                    # parallel search + dedup + score + fetch
│   │   ├── answer.go                     # turn 2 logic, Answer type
│   │   └── select.go                     # top-K selection with source_type guarantee
│   ├── scorer/
│   │   └── heuristic.go                  # domain authority + keyword overlap
│   ├── config/
│   │   ├── config.go                     # viper-backed config loader
│   │   └── env.go                        # env var interpolation
│   ├── log/
│   │   └── log.go                        # slog setup
│   └── bench/
│       ├── runner.go                     # benchmark execution
│       ├── scoring.go                    # metric computation
│       └── report.go                     # Markdown report generation
├── prompts/                              # embedded via //go:embed
│   ├── system.md
│   ├── plan.md
│   └── answer.md
├── internal/data/                        # embedded data files
│   ├── domain_authority.json
│   └── blacklist.txt
├── test/
│   ├── bench/
│   │   ├── queries.yaml
│   │   ├── reports/
│   │   └── testdata/
│   ├── spike/
│   │   └── tls_fingerprint/              # utls validation spike
│   └── fixtures/                         # HTTP response fixtures
├── docs/
│   ├── architecture.md                   # this file
│   ├── benchmark.md                      # benchmark methodology details
│   ├── modules.md                        # per-module implementation notes
│   └── adr/                              # architecture decision records
├── scripts/
│   ├── install.sh                        # one-line installer
│   └── release.sh                        # release packaging
├── .github/
│   └── workflows/
│       ├── test.yml
│       ├── release.yml                   # cross-compile for Linux / macOS / Windows
│       └── bench.yml                     # optional CI benchmark
├── go.mod
├── go.sum
├── Makefile
├── README.md
└── LICENSE
```

### 13.1 Dependency policy

- Prefer stdlib when possible (`slog`, `log/slog`, `errors`, `net/http`, `encoding/json`).
- Approved external dependencies listed in `docs/adr/0001-dependencies.md`:
  - `github.com/spf13/cobra` — CLI
  - `github.com/spf13/viper` — config
  - `github.com/refraction-networking/utls` — TLS fingerprinting
  - `github.com/chromedp/chromedp` — browser automation
  - `github.com/go-shiori/go-readability` — content extraction
  - `github.com/PuerkitoBio/goquery` — HTML parsing
  - `modernc.org/sqlite` — SQLite without CGo
  - `github.com/stretchr/testify` — assertions
  - `github.com/anthropics/anthropic-sdk-go` — Anthropic LLM
  - `github.com/sashabaranov/go-openai` — OpenAI-compatible LLM
- Adding a new external dependency requires an ADR.

### 13.2 Build and release

```
make build          # build for current platform
make test           # run unit tests
make test-integration  # run integration tests (hits real APIs, BYOK required)
make bench          # run benchmark suite
make release        # cross-compile for linux-amd64/arm64, darwin-amd64/arm64, windows-amd64
```

Releases publish static binaries via GitHub Releases. Installation:

```bash
curl -fsSL https://raw.githubusercontent.com/odradekk/diting/main/scripts/install.sh | sh
```

The script detects OS / arch, downloads the matching binary to `~/.local/bin/diting`, and runs `diting doctor` to verify setup.

---

## 14. Development Phases

The v2 rewrite follows a **submodule-first** order. Each phase produces tested, independently useful artefacts.

### Phase 0 — Spike and validation — **✅ GATE CLEARED**

- [~] **0.1** Repo bootstrap, Go module init, cobra skeleton, CI running — **partial**
  - ✅ `go` orphan branch created with clean-slate layout
  - ✅ `go.mod` initialised (`github.com/odradekk/diting`)
  - ✅ Core deps pinned: `utls v1.8.2`, `golang.org/x/net v0.38.0`
  - ✅ `.gitignore` for Go artefacts
  - ⏭ cobra skeleton — deferred to Phase 4.x (CLI surface)
  - ⏭ CI workflow — deferred to Phase 6.2 (cross-compile workflow)
- [x] **0.2** utls TLS fingerprint spike — **complete, gate PASSED**
  - 8 runs × 14 URLs × 4 techniques
  - Best technique `utls+chrome_auto`: 83.9 % mean / 85.7 % median
  - External review (GPT-5.4) surfaced 2 gaps → spike re-run with expanded techniques → ADR 0001 revised
  - Production choice: `utls.HelloChrome_Auto`
  - See [ADR 0001](adr/0001-utls-fetch-layer.md) for full results and version-upgrade policy
- [→] **0.3** chromedp minimal integration — **absorbed into Phase 1.3**
  - Rationale: chromedp is mature Go tooling; no existential risk to de-risk in isolation. Integration validation happens during Phase 1.3 (`chromedp` layer). The `g2.com` and `quora.com` URLs from the 0.2 spike already identify targets the chromedp layer must handle.
- [→] **0.4** LLM client stub — **absorbed into Phase 3.1**
  - Rationale: `go-openai` and `anthropic-sdk-go` are mature. No existential risk. First real integration happens in Phase 3.1 (`Client` interface + provider implementations).
- [x] **0.5** Decision: continue Go path or fall back to Python CLI — **continue Go**
  - Trigger: 0.2 gate PASSED → Go rewrite is viable → proceed.

**Gate**: 0.2 was the hard blocker. **Cleared** on 2026-04-11.

**Phase 0 artefacts committed to `go` branch**:

| Commit | Contents |
|---|---|
| `fc1c1bf` | Initial v2 architecture (`docs/architecture.md`, `docs/bench/generate_queries_prompt.md`) |
| `21804aa` | utls smoke test + ADR 0001 first draft + `go.mod` + `.gitignore` |
| `f4d52e7` | Audit self-prompt (`docs/bench/audit_queries_prompt.md`) |
| `0e60080` | ADR 0001 revised per external review (chrome_auto + Roller analysis) |
| `faed425` | ADR writing guide (`docs/adr/README.md`) |

**Phase 0 → Phase 1 handoff**: no open blockers. Proceed to Phase 1.1 (`Fetcher` interface + chain orchestrator) at will.

### Phase 1 — Fetch layer (5–7 days)

- [ ] **1.1** `Fetcher` interface and `chain` orchestrator
- [ ] **1.2** `utls` layer with Chrome fingerprint
- [ ] **1.3** `chromedp` layer with stealth options
- [ ] **1.4** `jina` layer (r.jina.ai reader)
- [ ] **1.5** `archive` layer (Wayback + archive.today)
- [ ] **1.6** `tavily` layer (BYOK, disabled by default)
- [ ] **1.7** Content extraction pipeline (go-readability + custom post-processing for docs sites)
- [ ] **1.8** SQLite content cache with TTL policy
- [ ] **1.9** Unit tests per layer + integration test for the full chain against representative URLs
- [ ] **1.10** `diting fetch <url>` CLI command working end-to-end

**Gate**: Fetch layer matches or exceeds Python v1 fetch success rate on a 100-URL probe set.

### Phase 2 — Search modules (7–10 days)

- [ ] **2.1** `Module` interface, `Manifest` type, registry
- [ ] **2.2** `bing` scraping module
- [ ] **2.3** `duckduckgo` scraping module
- [ ] **2.4** `baidu` scraping module (if utls handles reCAPTCHA at all)
- [ ] **2.5** `brave` API module
- [ ] **2.6** `serp` API module
- [ ] **2.7** `arxiv` Atom API module
- [ ] **2.8** `github` REST API module (with optional PAT)
- [ ] **2.9** `stackexchange` REST API module
- [ ] **2.10** Per-module unit tests with mocked HTTP
- [ ] **2.11** Per-module integration tests behind `//go:build integration` tag

**Gate**: Each module passes integration tests and produces results for its 5 canonical smoke-test queries.

### Phase 3 — LLM and pipeline (5–7 days)

- [ ] **3.1** `Client` interface + Anthropic + OpenAI + MiniMax implementations
- [ ] **3.2** Conversation builder with prompt caching hints
- [ ] **3.3** Plan phase: prompt, JSON schema enforcement, parser
- [ ] **3.4** Execute phase: parallel search, dedup, scoring, top-K selection
- [ ] **3.5** Answer phase: content formatting, turn-2 prompt, citation parser
- [ ] **3.6** End-to-end pipeline test against at least 5 real queries
- [ ] **3.7** `diting search <question>` CLI working for default output

**Gate**: `diting search` returns a correct, cited answer for the canonical "asyncio.gather swallows exceptions" question on first run with a cold cache.

### Phase 4 — CLI surface (3–5 days)

- [ ] **4.1** `--format` (json / markdown / text)
- [ ] **4.2** `--raw` (skip answer phase)
- [ ] **4.3** `--plan-only` (skip execute/answer)
- [ ] **4.4** `--debug` + structured slog output on stderr
- [ ] **4.5** `diting config show|path|validate`
- [ ] **4.6** `diting init` (interactive config generator)
- [ ] **4.7** `diting doctor`
- [ ] **4.8** `--max-cost` guard
- [ ] **4.9** `--config` override

**Gate**: All commands covered by CLI tests, `--help` output is accurate.

### Phase 5 — Benchmark (4–6 days)

- [ ] **5.1** Author `test/bench/queries.yaml` (50 queries)
- [ ] **5.2** Three-stage ground-truth labelling (GPT-5.4 → Opus → human)
- [ ] **5.3** `bench.Runner` execution harness
- [ ] **5.4** Metric computation (6 metrics)
- [ ] **5.5** Report generator (Markdown)
- [ ] **5.6** Run `v0-baseline`, `v2-single`, `v2-raw` variants
- [ ] **5.7** First benchmark report committed to `test/bench/reports/`

**Gate**: `v2-single` composite score ≥ Python v1 on the same queries.

### Phase 6 — Release (2–3 days)

- [ ] **6.1** `install.sh` one-line installer
- [ ] **6.2** Cross-compile workflow (linux amd64/arm64, darwin amd64/arm64, windows amd64)
- [ ] **6.3** GitHub Release automation
- [ ] **6.4** README with quick start
- [ ] **6.5** `docs/benchmark.md`, `docs/modules.md`
- [ ] **6.6** Version tagged `v2.0.0`

### Deferred (Phase 7+)

- RefinementController (only if benchmark shows ≥ 5 pp improvement)
- BGE reranker integration (only if heuristic scoring proves insufficient)
- Query rewriting as a separate LLM call (only if single-call merged approach underperforms)
- Additional sources: openalex, wikipedia, context7, zhihu, juejin
- Skill markdown for code-agent distribution
- `diting bench diff` to compare two reports
- `diting search --stream` for streamed answer output

---

## 15. Non-Goals for v2

Explicitly **not** part of v2:

- **MCP server mode.** Python v1 provided MCP; v2 is CLI-only. Callers invoke via shell tool use.
- **Multi-round search by default.** Single round is the baseline. Refinement is deferred until benchmark justifies it.
- **LLM-based scoring.** Heuristic only in v1. BGE reranker is deferred.
- **Semantic deduplication.** URL dedup is the only dedup in v1.
- **Embedding-based routing.** Plan phase output directly drives search; no secondary router.
- **Strategy presets.** A single default pipeline path. `--raw` is the only variant.
- **Auto-blacklist.** Static blacklist only. Dynamic domain reputation is future work.
- **Circuit breakers.** Simple retry-with-backoff on transient failures; no health tracker state machine.
- **Bundled credentials.** Every external API is BYOK without exception.
- **Backwards compatibility with Python v1.** v2 is a clean rewrite; users are expected to migrate.

---

## 16. Open Questions

Tracked here until resolved with an ADR or benchmark result.

### Resolved

| Question | Resolution |
|---|---|
| Will utls reach ≥ 80 % of curl_cffi success rate on hard URLs? | ✅ **Resolved by [ADR 0001](adr/0001-utls-fetch-layer.md)** — `utls.HelloChrome_Auto` reached 83.9 % mean / 85.7 % median on 8-run × 14-URL spike. Phase 0 gate CLEARED. |

### Still open

| Question | Blocks | Decision owner |
|---|---|---|
| Is `context7` feasible as a Go HTTP client without the Node runtime? | Phase 2 | Module feasibility check |
| Does MiniMax M2.7 HighSpeed support OpenAI-compatible prompt caching? | Phase 3 | API docs review |
| What is the cold-start time for `diting search` (Go binary without BGE)? | Phase 3 | Measure after Phase 3 |
| Should `--raw` still run the plan phase, or skip it too? | Phase 4 | Benchmark `v2-raw` with both |
| What TTL should academic sources default to? | Phase 1 | Review during Phase 1.8 |
| How much does the larger 50-URL re-test (ADR 0001 §9 policy) shift the utls success rate estimate? | Before v2.0.0 release | Phase 1 extended re-test |

---

## 17. Glossary

- **BYOK**: Bring Your Own Key. diting never ships API credentials.
- **Caller**: The LLM (Claude Code, Cursor, etc.) that invokes `diting search`.
- **Source type**: The manifest-declared category of a search module (`general_web`, `academic`, `code`, `community`, `docs`).
- **Plan phase**: The first LLM turn; produces per-source-type queries.
- **Execute phase**: Parallel search + dedup + score + fetch.
- **Answer phase**: The second LLM turn; produces the cited answer.
- **Fallback chain**: The ordered list of fetch layers tried in sequence until one succeeds.
- **Composite score**: The weighted benchmark metric used for variant comparison.
- **Spike**: A throwaway prototype built to de-risk a specific technical question.

---

*Last updated: 2026-04-11. Status: draft — Phase 0 complete, Phase 1 ready to start. See `docs/adr/` for committed decisions and `docs/adr/README.md` for the ADR writing guide.*

## Progress tracker

- **Phase 0**: ✅ **Gate cleared** (2026-04-11). utls viability confirmed. 0.3 (chromedp) and 0.4 (LLM stub) absorbed into Phase 1 and Phase 3 respectively.
- **Phase 1**: ⏳ Not started. Ready to begin at 1.1.
- **Phase 2**: ⏳ Blocked on Phase 1.
- **Phase 3**: ⏳ Blocked on Phase 2.
- **Phase 4**: ⏳ Can start in parallel with Phase 3.
- **Phase 5**: ⏳ Can start benchmark-query curation in parallel with any phase. Query generation prompt ready at `docs/bench/generate_queries_prompt.md`, audit prompt ready at `docs/bench/audit_queries_prompt.md`.
- **Phase 6**: ⏳ Blocked on Phase 5.
