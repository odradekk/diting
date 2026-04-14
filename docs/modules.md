# Modules Reference

Reference for all pluggable components in diting v2: search modules, fetch layers, LLM providers, and cache.

---

## Search modules

Eight modules are registered at startup. The pipeline queries only the modules listed in `search.enabled` in `config.yaml` (or all of them if no config is present).

Source manifest for each module lives in the module's `Manifest()` method. Registry pattern: `internal/search/registry.go`.

| Module | Source type | Auth | Rate limit / quota | Free quota | Package |
|---|---|---|---|---|---|
| bing | general_web | Keyless (scraping) | Unspecified; aggressive scraping may trigger blocks | Unlimited | `internal/search/bing` |
| duckduckgo | general_web | Keyless (scraping) | Unspecified; first page only, no pagination | Unlimited | `internal/search/duckduckgo` |
| baidu | general_web | Keyless (scraping) | Intermittent CAPTCHA at high volume | Unlimited | `internal/search/baidu` |
| brave | general_web | BYOK (`BRAVE_API_KEY`, `X-Subscription-Token`) | 1 req/s | 2,000 queries/month | `internal/search/brave` |
| serp | general_web | BYOK (`SERP_API_KEY`) | Paid; no hard per-second limit published | ~100 queries/month | `internal/search/serp` |
| arxiv | academic | Keyless (Atom API) | Unspecified; standard academic usage fine | Unlimited | `internal/search/arxiv` |
| github | code | Optional BYOK PAT (`GITHUB_TOKEN`) | 10 req/min anonymous; 30 req/min with PAT | Unlimited (anonymous) | `internal/search/github` |
| stackexchange | community | Keyless (REST API v2.3) | — | 300 req/day (anonymous) | `internal/search/stackexchange` |

Notes:
- `openalex` appears in `docs/architecture.md` §14 Deferred list; it is not shipped in v2.0.0.
- `serp` is marked `CostTierExpensive` in its manifest; the LLM planner deprioritises it.
- `brave` is `CostTierCheap`; all others are `CostTierFree`.
- `baidu` may return zero results when a CAPTCHA is triggered; this is surfaced as empty results, not an error.

---

## Fetch layers

Five layers form the fetch chain (`internal/fetch/chain.go`). Layers are tried in order; the first successful response wins. A layer falls through on error or timeout.

| Layer | Transport | Auth | Preferred when | Falls through when |
|---|---|---|---|---|
| utls | Direct HTTP/1.1 or HTTP/2 with Chrome TLS fingerprint | None | Default for all URLs | TLS handshake blocked, bot detection, EOF |
| chromedp | Headless Chrome via Chrome DevTools Protocol | None (requires system Chrome/Chromium binary) | DataDome, advanced Cloudflare, JS-rendered SPAs | Chrome not installed, timeout, JS execution error |
| jina | External API (`r.jina.ai`) | Optional BYOK (`JINA_API_KEY`) | Server-side rendering required, utls and chromedp both failed | API error, rate limit, timeout |
| archive | Wayback Machine availability API + snapshot fetch | None | Live site down, geo-blocked, extreme bot protection | No snapshot found, archive.org unreachable |
| tavily | External API (`api.tavily.com/extract`) | BYOK (`TAVILY_API_KEY`, required) | All other layers failed; last resort | Not configured (no API key), quota exhausted |

Layer order in config is fixed; users cannot reorder. The `tavily` layer is disabled unless `TAVILY_API_KEY` is set.

---

## LLM providers

diting uses one LLM provider per invocation, auto-detected from environment variables. Provider resolution order: Anthropic first, then OpenAI-compatible.

| Provider | Env vars | Notes |
|---|---|---|
| Anthropic | `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL` (optional) | Native Anthropic SDK. Default model: latest Claude Sonnet. |
| OpenAI | `OPENAI_API_KEY`, `OPENAI_MODEL` (optional) | Native OpenAI SDK. Default model: gpt-4.1-mini. |
| MiniMax M2.7 HighSpeed | `OPENAI_API_KEY` + `OPENAI_BASE_URL=https://api.minimaxi.com/v1` + `OPENAI_MODEL=MiniMax-M2.7-highspeed` | OpenAI-compatible endpoint. Cheapest option for the benchmark. |
| DeepSeek Chat | `OPENAI_API_KEY` + `OPENAI_BASE_URL=https://api.deepseek.com` + `OPENAI_MODEL=deepseek-chat` | OpenAI-compatible endpoint. `DEEPSEEK_API_KEY/BASE_URL/MODEL` are supported by bench variants only, not the main CLI. |

Pricing for `--max-cost` budget guard: static table at `internal/pricing/pricing.go`. Covers Anthropic Claude 4.6 series, OpenAI GPT-4.1/GPT-5 series, MiniMax M2.7, DeepSeek. Unknown models fall back to a Sonnet-equivalent estimate.

Override provider or model per-invocation: `diting search --provider anthropic --model claude-haiku-4`.

---

## Cache

The content cache stores post-extraction page content to avoid redundant fetches. It is a SQLite database using WAL mode.

| Property | Value |
|---|---|
| Location | `~/.cache/diting/content.db` (fixed; path is not configurable via config.yaml in v2.0.0) |
| Max size | 256 MB (oldest entries evicted when exceeded) |
| Fallback TTL | 3 days (for domains not matched by any rule) |

Per-domain TTL rules (first match wins):

| Pattern | TTL |
|---|---|
| `arxiv.org`, `openalex.org`, `pubmed`, `jmlr.org`, `aclanthology.org` | 365 days |
| `stackoverflow.com`, `github.com`, `wikipedia.org` | 30 days |
| `docs.*`, `developer.*`, `go.dev`, `python.org`, `postgresql.org`, `react.dev`, `nextjs.org/docs` | 7 days |
| `news.*`, `blog.*`, `*/blog/*` | 24 hours |
| all other domains | 3 days (fallback) |

Manual cache purge:

```sh
rm ~/.cache/diting/content.db
```

The cache is bypassed per-URL with `diting fetch --no-cache <url>`. It is bypassed for the entire invocation with the `noCache` option in `buildChain`.

---

## Adding a module

All 8 modules follow the same pattern:

1. Create a subpackage under `internal/search/<name>/`.
2. Implement `search.Module` (`Manifest()`, `Search()`, `Close()`).
3. Register via `search.Register(ModuleName, factory)` in an `init()` function.
4. Blank-import the package in `cmd/diting/main.go`.

The registry pattern (`internal/search/registry.go`) panics on duplicate names so wiring mistakes surface at program start. Each module's `Manifest()` must return an accurate `SourceType` and `CostTier` — the LLM planner uses these fields to decide which modules to invoke for a given query.
