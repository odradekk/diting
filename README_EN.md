# Diting

English | [中文](README.md)

[![Python](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](https://www.python.org/)
[![MCP](https://img.shields.io/badge/MCP-FastMCP-blue?logo=data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0Ij48cGF0aCBmaWxsPSJ3aGl0ZSIgZD0iTTEyIDJMMiA3bDEwIDUgMTAtNXoiLz48L3N2Zz4=)](https://github.com/jlowin/fastmcp)
[![Pydantic](https://img.shields.io/badge/Pydantic-v2-E92063?logo=pydantic&logoColor=white)](https://docs.pydantic.dev/)
[![Playwright](https://img.shields.io/badge/Playwright-green?logo=playwright&logoColor=white)](https://playwright.dev/python/)
[![License](https://img.shields.io/badge/License-GPL--3.0-blue)](https://www.gnu.org/licenses/gpl-3.0.html)

Deep aggregated search MCP service. Parallel multi-engine retrieval with LLM-based scoring and summarization, served via the Model Context Protocol.

## Features

- **Multi-engine aggregation** -- Baidu, Bing, DuckDuckGo, Brave, SerpAPI, X, Zhihu. Baidu / Bing / DuckDuckGo enabled by default, no API key required
- **Adaptive multi-round search** -- LLM generates an optimal initial query, then adaptively generates follow-up queries each round based on identified gaps in the accumulated results
- **Hybrid scoring backend** -- Supports `hybrid` (local BGE reranker + heuristic quality signals, default) and `llm` backends, configurable via settings
- **Dual-tier LLM config** -- Independent base_url / api_key / model for reasoning and fast tiers, enabling mixed providers
- **N-layer fetch fallback** -- local (curl_cffi + Playwright) → r.jina.ai → Wayback / Archive.today → Tavily, each layer with independent timeout
- **Prefetch interleaving** -- Search and fetch run in parallel; top-K results are prefetched in the background after each scoring round
- **Snippet aggregation fallback** -- When all fetch layers fail, aggregates multi-engine snippets as pseudo-content for summary generation
- **Stealth browser** -- Optional patchright replaces Playwright to strip automation fingerprints for anti-bot bypass
- **Thinking model support** -- Handles `reasoning_content` fields and `<think>` tags from DeepSeek, MiniMax M2.7, and similar reasoning models
- **Auto-blacklist** -- Domains with consistently low-quality results are automatically blacklisted
- **Content cache** -- SQLite read-through/write-through cache with automatic filtering of login walls, bot walls, and thin content
- **Structured logging** -- JSON log format with query_id context correlation
- **Summary generation** -- Fetches full text of top sources, generates Markdown analysis with inline citations
- **Response compression** -- MCP output retains only status / summary / sources (title, url, snippet), reducing token consumption by ~60-70%

## Installation

### Quick Install (Recommended)

One command to install and configure in Claude Code:

```bash
claude mcp add-json diting --scope user '{
  "type": "stdio",
  "command": "uvx",
  "args": ["--from", "git+https://github.com/s1n1996/diting", "diting"],
  "env": {
    "LLM_REASONING_BASE_URL": "https://your-api-endpoint.com/v1",
    "LLM_REASONING_MODEL": "your-reasoning-model",
    "LLM_REASONING_API_KEY": "your-key",
    "LLM_FAST_BASE_URL": "https://your-api-endpoint.com/v1",
    "LLM_FAST_MODEL": "your-fast-model",
    "LLM_FAST_API_KEY": "your-key"
  }
}'
```

Playwright Chromium is automatically installed on first launch — no manual setup required.

### Manual Installation

Requires Python >= 3.10.

```bash
# Install via Git
pip install git+https://github.com/s1n1996/diting.git

# Or using uv
uv pip install git+https://github.com/s1n1996/diting.git

# Local development
uv sync

# Enable local reranker (optional)
uv sync --extra rerank
```

## Configuration

Copy `.env.example` to `.env` and fill in the required values:

```bash
cp .env.example .env
```

### Required

| Variable | Description |
|----------|-------------|
| `LLM_REASONING_BASE_URL` | OpenAI v1-compatible API endpoint for reasoning tier |
| `LLM_REASONING_MODEL` | Reasoning model name (query generation, summarization, LLM scorer) |
| `LLM_REASONING_API_KEY` | API key for reasoning tier |
| `LLM_FAST_BASE_URL` | OpenAI v1-compatible API endpoint for fast tier |
| `LLM_FAST_MODEL` | Fast model name (evaluator) |
| `LLM_FAST_API_KEY` | API key for fast tier |

### Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `TAVILY_API_KEY` | empty | Tavily Extract API key for last-resort fetch fallback |
| `BRAVE_API_KEY` | empty | Brave Search API key |
| `SERP_API_KEY` | empty | SerpAPI key |
| `JINA_API_KEY` | empty | r.jina.ai Reader API key (optional, lifts rate limits) |
| `ENABLE_BAIDU` | `true` | Enable Baidu search module |
| `ENABLE_BING` | `true` | Enable Bing search module |
| `ENABLE_DUCKDUCKGO` | `true` | Enable DuckDuckGo search module |
| `ENABLE_BRAVE` | `false` | Enable Brave search (requires `BRAVE_API_KEY`) |
| `ENABLE_SERP` | `false` | Enable SerpAPI (requires `SERP_API_KEY`) |
| `ENABLE_X` | `false` | Enable X/Twitter module (requires Cookie or Storage State) |
| `ENABLE_ZHIHU` | `false` | Enable Zhihu module (requires Cookie or Storage State) |
| `ENABLE_JINA_READER` | `true` | Enable r.jina.ai fetch fallback layer |
| `ENABLE_ARCHIVE_FALLBACK` | `true` | Enable Wayback / Archive.today fetch fallback layer |
| `ENABLE_STEALTH_BROWSER` | `false` | Enable patchright stealth browser (requires `pip install diting[stealth]`) |
| `X_COOKIE` | empty | X/Twitter raw Cookie string |
| `ZHIHU_COOKIE` | empty | Zhihu raw Cookie string |
| `MAX_RESULTS` | `10` | Max results per search engine, with auto pagination/scrolling |
| `MAX_CONCURRENCY` | `5` | Max concurrent module searches |
| `LLM_MAX_TOKENS` | `8192` | Max output tokens per LLM call |
| `LLM_TIMEOUT` | `240` | Per-LLM-call timeout in seconds |
| `MODULE_TIMEOUT` | `30` | Per-module timeout in seconds |
| `GLOBAL_TIMEOUT` | `300` | Overall pipeline timeout in seconds |
| `MAX_SEARCH_ROUNDS` | `3` | Maximum iterative search rounds |
| `SCORE_THRESHOLD` | `0.6` | Minimum score to keep a result (0-1) |
| `RELEVANCE_WEIGHT` | `0.5` | Weight for relevance score |
| `QUALITY_WEIGHT` | `0.5` | Weight for quality score |
| `SCORER_BACKEND` | `hybrid` | `hybrid`, `llm`, or legacy alias `reranker`; `hybrid` uses local reranker + heuristic, falls back to LLM if reranker unavailable |
| `RERANKER_MODEL` | `BAAI/bge-reranker-base` | Local reranker model ID |
| `RERANKER_CACHE_DIR` | empty | Local reranker model cache directory; defaults to `~/.cache/diting/models/...` |
| `DITING_CACHE_ENABLED` | `true` | Enable SQLite content cache |
| `DITING_CACHE_PATH` | empty | Cache database path; defaults to `~/.cache/diting/content.db` |
| `AUTO_BLACKLIST` | `true` | Auto-blacklist low-quality domains |
| `AUTO_BLACKLIST_THRESHOLD` | `0.3` | Domains with all results below this score are auto-blacklisted |
| `MIN_SNIPPET_LENGTH` | `30` | Minimum snippet character count; shorter results are filtered |
| `BLACKLIST_FILE` | built-in | Path to blacklist rules file (defaults to bundled `diting/data/blacklist.txt`) |
| `PROMPTS_DIR` | empty | Custom prompts directory (overrides built-in defaults) |
| `LOG_LEVEL` | `INFO` | Logging level |
| `LOG_FORMAT` | `text` | Log format: `text` or `json` |

X and Zhihu modules support two authentication methods (in priority order):

1. **Storage State file** (recommended) -- Place a Playwright-exported JSON at `diting/data/x_storage_state.json` or `diting/data/zhihu_storage_state.json`; the module loads it automatically
2. **Cookie string** -- Set the `X_COOKIE` / `ZHIHU_COOKIE` environment variable; ignored when a Storage State file is present

## Quick Start

```bash
# Run after installation
diting

# Or using python -m
python -m diting

# Local development
uv run diting
```

### MCP Client Configuration

After installing via Git:

```json
{
  "mcpServers": {
    "diting": {
      "command": "diting"
    }
  }
}
```

Local development mode:

```json
{
  "mcpServers": {
    "diting": {
      "command": "uv",
      "args": ["run", "--directory", "/path/to/diting", "diting"]
    }
  }
}
```

## MCP Tools

### search

Deep aggregated search. Accepts a natural-language query and returns scored, structured results.

```
Parameter: query (string) -- Natural language search query
Returns:   { status, summary, sources: [{ title, url, snippet }] }
```

The response is compressed to include only the fields needed by the consuming LLM. Full pipeline data is logged internally.

### fetch

Fetch the full text content of a URL. Returns an error description string (not a structured error) on failure.

```
Parameter: url (string) -- Target URL
Returns:   string -- Extracted page content in Markdown format; error message string on failure
```

## Architecture

```
MCP Client --> FastMCP Server
                 |
                 |-- search tool --> Orchestrator
                 |     |-- Initial query generation (Reasoning LLM)
                 |     |-- Parallel module search (Semaphore-bounded + HealthTracker)
                 |     |-- Dedup + Prefilter + Blacklist (incl. auto-blacklist)
                 |     |-- Scoring (hybrid: reranker + heuristic / llm)
                 |     |-- Prefetch interleaving (background top-K fetch)
                 |     |-- Quality evaluation + next query generation (Fast LLM, adaptive)
                 |     +-- Summarization (reuse prefetch + snippet aggregation fallback + LLM)
                 |
                 +-- fetch tool --> LayeredFetcher (N-layer fallback)
                       |-- LocalFetcher (curl_cffi + Playwright/patchright)
                       |-- JinaReaderFetcher (r.jina.ai)
                       |-- ArchiveFetcher (Wayback + Archive.today)
                       |-- TavilyFetcher (last-resort fallback)
                       +-- CachedFetcher (SQLite read/write-through)
```

## Development

```bash
# Install dev dependencies
uv sync --extra dev

# Run tests
pytest

# Run specific tests
pytest tests/test_config.py -v
```

## Roadmap

- [ ] Tavily Search API support
- [ ] Exa Search API support
- [ ] Firecrawl Search API support
- [ ] Zhihu and X content fetching
- [ ] Yandex search module
- [ ] Reddit search and content fetching
- [ ] Google search module
- [ ] GitHub Issues/Discussions content fetching

## License

[GPL-3.0-or-later](https://www.gnu.org/licenses/gpl-3.0.html)
