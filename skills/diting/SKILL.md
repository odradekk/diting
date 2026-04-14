---
name: diting
description: Invoke the diting CLI to answer a technical question with cited sources synthesised from up to eight live web search engines. TRIGGER when the user asks to search the web, look up current or version-specific technical information, research a topic across multiple authoritative sources, compare tools, or troubleshoot errors that need up-to-date documentation. DO NOT TRIGGER when the built-in WebSearch tool is clearly sufficient, when the question is about code in the current repository, when the user has already provided the answer in their message, or for chit-chat and purely logical questions.
argument-hint: "[search query]"
---

# diting

Invoke the `diting` CLI via `Bash` to run a multi-source web search, fetch the top results, and return a cited answer synthesised by a configured LLM.

## Critical

- diting is a **single binary** already on PATH. Invoke via the `Bash` tool. Never attempt to import or call it as a library.
- All searches hit the network and cost LLM tokens. Typical wall-clock is **30-300 seconds** per query. Never invoke for questions answerable from the current repo or training knowledge.
- diting is strictly BYOK. The user's environment must have `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`. If a call fails with `no LLM provider configured`, run `diting doctor` and surface the missing variable to the user — do not attempt to set it.
- Always pass the question in double quotes as a single positional argument. Do not inject shell metacharacters.
- Prefer `--format json` when the answer will be parsed programmatically. Prefer `--format markdown` only when surfacing the answer directly to the user without further processing.

## Primary command

```sh
diting search "<question>" --format json
```

Returns a JSON object with:

- `answer.text` — the synthesised answer with inline `[N]` citation markers
- `answer.confidence` — `high` | `medium` | `low` | `insufficient`
- `answer.citations[]` — each entry has `id`, `url`, `title`, `source_type`
- `sources[]` — full list of fetched sources, including ones the LLM did not cite
- `plan` — the per-source-type search plan that was executed
- `debug` — token counts and timing (present only with `--debug`)

### Reading the output

1. Read `answer.confidence`.
   - `high` or `medium`: trust and surface `answer.text`.
   - `low`: surface `answer.text` with a caveat that the sources were thin.
   - `insufficient`: do NOT surface the answer text. Report to the user that diting could not find enough authoritative sources, and optionally show `sources[]` for manual inspection.
2. Surface the top three entries from `answer.citations[]` (sorted by `id` ascending) as supporting links.
3. If the user asked a comparison or decision question, preserve any tables or enumerations from `answer.text` verbatim.

## Alternative commands

| Command | Use when |
|---|---|
| `diting search "<q>" --raw --format json` | Only the raw fetched sources are needed (no synthesis). Faster, cheaper, but `answer.text` is empty. Parse `sources[]` directly. |
| `diting search "<q>" --plan-only --format json` | Preview the search plan (per-source-type query list) without executing any searches. Zero network cost beyond the plan LLM call. |
| `diting fetch "<url>" --format json` | Extract the main content of a single URL via the fetch chain (utls → chromedp → jina → archive → tavily). No search, no LLM. |
| `diting doctor` | Run the environment health check. Use only when a previous invocation failed with an env or connectivity error. |

## Common flags

| Flag | Purpose |
|---|---|
| `--format json\|markdown\|plain` | Output format. Default is `json` for `search` and `fetch`. |
| `--max-cost <usd>` | Abort before the answer phase if the estimated LLM cost exceeds the cap (e.g. `--max-cost 0.50`). |
| `--timeout <dur>` | Per-query deadline in Go duration syntax (e.g. `5m`, `10m`). Default is 300s. |
| `--debug` | Emit structured slog events to stderr. Use only when diagnosing a failure. |
| `--config <path>` | Override the config file path. Default is `~/.config/diting/config.yaml`. |

## Error handling

Non-zero exit codes print a classified error on stderr. Common cases:

| Error substring | Cause | Action |
|---|---|---|
| `no LLM provider configured` | Missing `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` | Run `diting doctor`. Report the missing variable. Do not retry until the user has set it. |
| `context deadline exceeded` | Query slower than `--timeout` | Retry once with `--timeout 10m`. If it still fails, fall back to `--plan-only` and surface the plan to the user. |
| `pipeline: plan: parse: ...` | LLM returned unparseable plan shape | Retry once. diting has built-in retry; a second CLI-level retry is the last resort. |
| `pipeline: answer: parse: ...` | LLM returned unparseable answer JSON | Retry once. If it fails again, fall back to `--raw` and return the source list. |
| `command not found: diting` | Binary not on PATH | Tell the user to install via the one-liner at https://github.com/odradekk/diting — do not attempt to install it automatically. |

## Examples

**User**: "What's the recommended way to share database state across Axum handlers?"
→ `diting search "Axum share database state handlers idiomatic" --format json`. Parse `answer.text`, surface with the top three citations.

**User**: "Compare DuckDB vs SQLite for analytics over 20 GB Parquet on a laptop."
→ `diting search "DuckDB vs SQLite analytics 20GB parquet laptop" --format json`. Preserve any comparison table from `answer.text` verbatim.

**User**: "Fetch https://go.dev/doc/effective_go and summarise it."
→ `diting fetch "https://go.dev/doc/effective_go" --format json`, then summarise the extracted content.

**User**: "What's 2 + 2?"
→ Do NOT invoke diting. Purely logical.

**User**: "Where is the scoring config defined in this repo?"
→ Do NOT invoke diting. This is about the current repo. Use `Grep` or `Read` instead.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Stale cached content | The content cache lives at `~/.cache/diting/content.db`. Delete it to force a full re-fetch. |
| Unexpectedly low `answer.confidence` | Inspect `sources[]`. If many entries have `fetched: false`, the fetch chain is failing — rerun with `--debug` and surface the layer errors. |
| Answer cites the wrong domain | Rerun with `--raw` to see the full unfiltered source set and pick citations manually. |
| Cost cap triggered | The estimate exceeded `--max-cost`. Raise the cap or switch to a cheaper provider via `OPENAI_BASE_URL` + `OPENAI_MODEL` (for example DeepSeek via `https://api.deepseek.com`). |
