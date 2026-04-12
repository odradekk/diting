# Prompt: Generate 50 URLs for Fetch Layer Testing

> **Role**: You are a QA engineer designing a test suite for a web content fetch pipeline. The pipeline fetches URLs, extracts article content from the raw HTML, and returns clean text suitable for LLM input. You need to produce a diverse, representative set of 50 real URLs that stress-test both **fetch success rate** and **content extraction quality**.

## Context

The fetch pipeline under test has 5 layers tried in order:

1. **utls** — Go HTTP client with Chrome TLS fingerprint (handles most sites)
2. **chromedp** — headless Chrome browser (handles JS-heavy sites, DataDome)
3. **jina** — r.jina.ai reader API (server-side rendering + extraction)
4. **archive** — Wayback Machine cached snapshots
5. **tavily** — Tavily Extract API (paid, last resort)

After fetching, content extraction runs:
- HTML → go-readability (Mozilla Readability.js port) extracts article text
- Markdown/text → light sanitization + truncation

## What we measure

### 1. Fetch success rate
A fetch **succeeds** if any layer returns HTTP 200 with a non-empty body. A fetch **fails** if all layers return errors (blocked, timeout, DNS failure, etc.).

### 2. Content extraction quality
For each successfully fetched page, we measure:

| Metric | Definition | Scale |
|---|---|---|
| **Readability** | Is the extracted text coherent and well-structured? No garbled encoding, no broken sentences, no interleaved navigation text. | 1-5 (5 = perfect) |
| **Completeness** | Does the extracted text contain the main article/page content? Key sections, code blocks, tables, lists — are they present? | 1-5 (5 = nothing important missing) |
| **Noise filtering** | Is non-article content removed? Navigation links, sidebar text, footer, cookie banners, ads, related-article links. | 1-5 (5 = zero noise) |

## URL selection criteria

### Distribution by difficulty (for fetch)

| Difficulty | Count | Description |
|---|---|---|
| **Easy** | 15 | No bot protection. Static HTML. Should succeed on utls layer every time. |
| **Medium** | 20 | Cloudflare or similar WAF, but tolerant of well-fingerprinted clients. May require chromedp fallback occasionally. |
| **Hard** | 15 | Aggressive bot detection (DataDome, PerimeterX, Akamai Bot Manager, Kasada, Shape). JS-rendered SPAs. May require chromedp or jina fallback. |

### Distribution by content type (for extraction quality)

Each URL must be tagged with its **primary content type** — the kind of content the page contains. The distribution should cover:

| Content type | Min count | Examples |
|---|---|---|
| **Article/blog** | 10 | News articles, blog posts, opinion pieces — long-form prose with headings |
| **Documentation** | 10 | API docs, library references, tutorials with code blocks |
| **Forum/Q&A** | 8 | StackOverflow questions, GitHub discussions, Reddit threads |
| **Academic** | 5 | arXiv abstracts, PubMed articles, university course pages |
| **Landing/product** | 5 | Product pages, SaaS landing pages — heavy on marketing copy + images |
| **Wiki/reference** | 5 | Wikipedia, MDN, encyclopedia pages — structured reference content |
| **Code-heavy** | 4 | GitHub READMEs, Gist pages, code playground outputs |
| **Mixed/other** | 3 | Pages with unusual layouts, single-page apps, infographics |

### Diversity requirements

- **No more than 2 URLs from the same domain** (e.g., max 2 from stackoverflow.com).
- **At least 15 distinct domains** across all 50 URLs.
- **At least 3 non-English pages** (Chinese, Japanese, German, or other) to test encoding handling.
- **At least 3 pages with significant code blocks** (inline `<code>` or `<pre>` elements).
- **At least 2 pages with tables** that contain important data.
- **At least 2 pages that are primarily JS-rendered** (React/Vue/Angular SPAs where the initial HTML has minimal content).

### Freshness

- All URLs must be accessible as of **April 2026**. Do not include URLs that are likely to 404 by then (e.g., time-limited promotions, temporary event pages).
- Prefer evergreen content: documentation, reference articles, well-established blog posts.

### Anti-patterns to avoid

- No URLs behind login walls (LinkedIn articles are OK if publicly accessible, but not LinkedIn feed posts).
- No URLs that are primarily images or video (YouTube, Instagram).
- No URL shorteners (bit.ly, t.co).
- No URLs that redirect to a completely different domain.
- No paywalled content (NYT, WSJ) unless there's a free-access path.

## Output schema

Return a YAML array. Each entry must have exactly these fields:

```yaml
- url: "https://..."
  domain: "example.com"
  difficulty: "easy"          # easy | medium | hard
  content_type: "article"     # article | documentation | forum | academic | landing | wiki | code | mixed
  language: "en"              # ISO 639-1
  bot_protection: "none"      # none | cloudflare | datadome | akamai | perimeterx | kasada | shape | other
  js_rendered: false          # true if the page is primarily JS-rendered (SPA)
  has_code_blocks: false      # true if page contains significant <pre>/<code> content
  has_tables: false           # true if page contains data tables
  expected_title: "..."       # the expected <title> or main heading of the page
  notes: "..."                # one-line note about why this URL is in the set
```

## Validation rules (self-check before output)

Before outputting, verify:

1. **Exactly 50 entries**.
2. **Difficulty distribution**: 15 easy + 20 medium + 15 hard.
3. **Content type distribution**: meets all minimum counts above.
4. **Domain diversity**: ≤ 2 URLs per domain, ≥ 15 distinct domains.
5. **Language diversity**: ≥ 3 non-English URLs.
6. **Feature coverage**: ≥ 3 with code blocks, ≥ 2 with tables, ≥ 2 JS-rendered.
7. **No duplicates**: every URL is unique.
8. **No known-broken URLs**: every URL is expected to be accessible in April 2026.

If any rule is violated, fix it before outputting. State the validation result at the end:

```
Validation:
- Total: 50 ✓
- Difficulty: 15/20/15 ✓
- Content types: [counts] ✓
- Domains: [count] distinct, max [N] per domain ✓
- Languages: [count] non-English ✓
- Code blocks: [count] ✓
- Tables: [count] ✓
- JS-rendered: [count] ✓
- Duplicates: 0 ✓
```

## Output

Output **only** the YAML array and the validation summary. No prose, no explanation, no commentary.
