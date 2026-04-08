# Diting (谛听)

[English](README_EN.md) | 中文

[![Python](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](https://www.python.org/)
[![MCP](https://img.shields.io/badge/MCP-FastMCP-blue?logo=data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0Ij48cGF0aCBmaWxsPSJ3aGl0ZSIgZD0iTTEyIDJMMiA3bDEwIDUgMTAtNXoiLz48L3N2Zz4=)](https://github.com/jlowin/fastmcp)
[![Pydantic](https://img.shields.io/badge/Pydantic-v2-E92063?logo=pydantic&logoColor=white)](https://docs.pydantic.dev/)
[![Playwright](https://img.shields.io/badge/Playwright-green?logo=playwright&logoColor=white)](https://playwright.dev/python/)
[![License](https://img.shields.io/badge/License-GPL--3.0-blue)](https://www.gnu.org/licenses/gpl-3.0.html)

深度聚合搜索 MCP 服务。跨多引擎并行检索，由 LLM 评分、摘要，通过 MCP 协议返回结构化结果。

## 特性

- **多引擎聚合** -- Baidu、Bing、DuckDuckGo、Brave、SerpAPI、X、知乎 + 垂直源（arXiv、Wikipedia、StackExchange、GitHub），通用引擎默认启用，无需 API Key
- **自适应多轮搜索** -- 首轮由 LLM 生成最优搜索词，每轮结束后根据已有结果智能分析信息缺口，自适应生成下一轮搜索词
- **混合评分后端** -- 支持 `hybrid`（本地 BGE reranker + 启发式质量信号，默认）与 `llm` 两种后端，可通过配置切换
- **双层 LLM 配置** -- reasoning 和 fast 模型独立配置 base_url / api_key / model，支持不同服务商混用
- **N 层抓取回退** -- local（curl_cffi + Playwright）→ r.jina.ai → Wayback / Archive.today → Tavily，每层独立超时
- **预抓取交错** -- 搜索与抓取并行，每轮评分后立即后台预抓取 top-K 结果，缩短摘要生成延迟
- **Snippet 聚合降级** -- 抓取全部失败时，聚合多引擎 snippet 作为伪内容生成摘要
- **隐身浏览器** -- 可选 patchright 替代 Playwright，去除自动化指纹绕过反爬
- **思考模型兼容** -- 自动处理 DeepSeek、MiniMax M2.7 等思考模型的 `reasoning_content` 字段和 `<think>` 标签
- **语义去重** -- 可选 BGE 嵌入向量余弦相似度去重，消除跨引擎近义重复结果（需 `pip install diting[rerank]`）
- **自动黑名单** -- 低质量域名自动加入黑名单，后续搜索直接过滤
- **内容缓存** -- SQLite 读写穿透缓存，自动过滤登录墙 / 反爬页 / 薄内容
- **结构化日志** -- 支持 JSON 格式日志输出，带 query_id 上下文关联
- **摘要生成** -- 抓取高分来源页面全文，生成带引用的 Markdown 分析摘要
- **响应压缩** -- MCP 输出仅保留 status / summary / sources（title、url、snippet），减少约 60-70% 的 token 消耗

## 安装

### 快速安装（推荐）

在 Claude Code 中一条命令完成安装和配置：

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

Playwright Chromium 会在首次启动时自动安装，无需手动操作。

### 手动安装

需要 Python >= 3.10。

```bash
# 通过 Git 安装
pip install git+https://github.com/s1n1996/diting.git

# 或使用 uv
uv pip install git+https://github.com/s1n1996/diting.git

# 本地开发
uv sync

# 启用本地 reranker（可选）
uv sync --extra rerank
```

## 配置

复制 `.env.example` 为 `.env`，填入所需配置：

```bash
cp .env.example .env
```

### 必填项

| 变量 | 说明 |
|------|------|
| `LLM_REASONING_BASE_URL` | Reasoning 模型 OpenAI v1 兼容 API 端点 |
| `LLM_REASONING_API_KEY` | Reasoning 模型 API 密钥 |
| `LLM_REASONING_MODEL` | Reasoning 模型名称（查询生成 / 摘要 / LLM scorer） |
| `LLM_FAST_BASE_URL` | Fast 模型 OpenAI v1 兼容 API 端点 |
| `LLM_FAST_API_KEY` | Fast 模型 API 密钥 |
| `LLM_FAST_MODEL` | Fast 模型名称（evaluator） |

### 可选项

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `TAVILY_API_KEY` | 空 | Tavily Extract API 密钥，用于内容抓取末层降级 |
| `BRAVE_API_KEY` | 空 | Brave Search API 密钥 |
| `SERP_API_KEY` | 空 | SerpAPI 密钥 |
| `JINA_API_KEY` | 空 | r.jina.ai Reader API 密钥（可选，解除速率限制） |
| `ENABLE_BAIDU` | `true` | 启用百度搜索模块 |
| `ENABLE_BING` | `true` | 启用 Bing 搜索模块 |
| `ENABLE_DUCKDUCKGO` | `true` | 启用 DuckDuckGo 搜索模块 |
| `ENABLE_BRAVE` | `false` | 启用 Brave 搜索模块（需要 `BRAVE_API_KEY`） |
| `ENABLE_SERP` | `false` | 启用 SerpAPI 模块（需要 `SERP_API_KEY`） |
| `ENABLE_X` | `false` | 启用 X/Twitter 模块（需要 Cookie 或 Storage State） |
| `ENABLE_ZHIHU` | `false` | 启用知乎模块（需要 Cookie 或 Storage State） |
| `ENABLE_ARXIV` | `false` | 启用 arXiv 学术论文搜索（免费，无需 Key） |
| `ENABLE_WIKIPEDIA` | `false` | 启用 Wikipedia 搜索，en + zh 双语（免费，无需 Key） |
| `ENABLE_STACKEXCHANGE` | `false` | 启用 StackExchange Q&A 搜索（免费，无需 Key） |
| `ENABLE_GITHUB` | `false` | 启用 GitHub 仓库搜索（免费，可选 Token 提升限额） |
| `GITHUB_TOKEN` | 空 | GitHub 个人访问令牌（可选，将速率限制从 10/min 提升到 30/min） |
| `SEMANTIC_DEDUP` | `false` | 启用 BGE 嵌入向量语义去重（需要 `pip install diting[rerank]`） |
| `SEMANTIC_DEDUP_THRESHOLD` | `0.9` | 余弦相似度超过此阈值视为重复 |
| `ENABLE_JINA_READER` | `true` | 启用 r.jina.ai 抓取回退层 |
| `ENABLE_ARCHIVE_FALLBACK` | `true` | 启用 Wayback / Archive.today 抓取回退层 |
| `ENABLE_STEALTH_BROWSER` | `false` | 启用 patchright 隐身浏览器（需要 `pip install diting[stealth]`） |
| `X_COOKIE` | 空 | X/Twitter 原始 Cookie 字符串 |
| `ZHIHU_COOKIE` | 空 | 知乎原始 Cookie 字符串 |
| `MAX_RESULTS` | `10` | 每个搜索引擎返回的最大结果数，支持分页/滚动自动获取 |
| `MAX_CONCURRENCY` | `5` | 模块并行搜索最大并发数 |
| `LLM_MAX_TOKENS` | `8192` | LLM 最大输出 token 数 |
| `LLM_TIMEOUT` | `240` | 单次 LLM 调用超时（秒） |
| `MODULE_TIMEOUT` | `30` | 单个搜索模块超时（秒） |
| `GLOBAL_TIMEOUT` | `300` | 整体搜索管线超时（秒） |
| `MAX_SEARCH_ROUNDS` | `3` | 最大迭代搜索轮数 |
| `SCORE_THRESHOLD` | `0.6` | 结果最低保留分数（0-1） |
| `RELEVANCE_WEIGHT` | `0.5` | LLM scorer 的相关性评分权重 |
| `QUALITY_WEIGHT` | `0.5` | LLM scorer 的质量评分权重 |
| `SCORER_BACKEND` | `hybrid` | `hybrid`、`llm` 或兼容旧值 `reranker`；`hybrid` 使用本地 reranker + heuristic，本地 reranker 不可用时自动回退到 LLM |
| `RERANKER_MODEL` | `BAAI/bge-reranker-base` | 本地 reranker 模型 ID |
| `RERANKER_CACHE_DIR` | 空 | 本地 reranker 模型缓存目录；为空时使用 `~/.cache/diting/models/...` |
| `DITING_CACHE_ENABLED` | `true` | 启用 SQLite 内容缓存 |
| `DITING_CACHE_PATH` | 空 | 缓存数据库路径；为空时使用 `~/.cache/diting/content.db` |
| `AUTO_BLACKLIST` | `true` | 自动黑名单低质量域名 |
| `AUTO_BLACKLIST_THRESHOLD` | `0.3` | 域名所有结果低于此分数时自动加入黑名单 |
| `MIN_SNIPPET_LENGTH` | `30` | 结果最短摘要字符数，低于则过滤 |
| `BLACKLIST_FILE` | 内置 | 黑名单规则文件路径（默认使用包内 `diting/data/blacklist.txt`） |
| `PROMPTS_DIR` | 空 | 自定义提示词目录（覆盖内置默认） |
| `LOG_LEVEL` | `INFO` | 日志级别 |
| `LOG_FORMAT` | `text` | 日志格式：`text` 或 `json` |

X 和知乎模块支持两种认证方式，优先级从高到低：

1. **Storage State 文件**（推荐）-- 将 Playwright 导出的 JSON 放在 `diting/data/x_storage_state.json` 或 `diting/data/zhihu_storage_state.json`，模块会自动加载
2. **Cookie 字符串** -- 设置 `X_COOKIE` / `ZHIHU_COOKIE` 环境变量，存在 Storage State 文件时会被忽略

## 快速开始

```bash
# 安装后直接运行
diting

# 或使用 python -m
python -m diting

# 本地开发运行
uv run diting
```

### 在 MCP 客户端中配置

通过 Git 安装后：

```json
{
  "mcpServers": {
    "diting": {
      "command": "diting"
    }
  }
}
```

本地开发模式：

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

## MCP 工具

### search

深度聚合搜索。接受自然语言查询，返回评分后的结构化结果。

```
参数: query (string) -- 自然语言搜索查询
返回: { status, summary, sources: [{ title, url, snippet }] }
```

响应已压缩，仅包含消费端 LLM 所需的核心字段，完整管线数据记录在日志中。

### fetch

抓取指定 URL 的全文内容。抓取失败时返回错误描述文本而非结构化错误。

```
参数: url (string) -- 目标 URL
返回: string -- 提取的 Markdown 格式页面内容；失败时返回错误信息字符串
```

## 架构

```
MCP 客户端 --> FastMCP Server
                 |
                 |-- search tool --> Orchestrator
                 |     |-- 初始查询生成 (Reasoning LLM)
                 |     |-- 并行模块搜索 (Semaphore 限流 + HealthTracker)
                 |     |-- 去重 + 预过滤 + 黑名单（含自动黑名单）
                 |     |-- 语义去重（可选，BGE 嵌入余弦相似度）
                 |     |-- 评分 (hybrid: reranker + heuristic / llm)
                 |     |-- 预抓取交错（后台并行抓取 top-K）
                 |     |-- 质量评估 + 下轮查询生成（Fast LLM，自适应）
                 |     +-- 摘要生成（复用预抓取 + snippet 聚合降级 + LLM）
                 |
                 +-- fetch tool --> LayeredFetcher (N 层回退)
                       |-- LocalFetcher (curl_cffi + Playwright/patchright)
                       |-- JinaReaderFetcher (r.jina.ai)
                       |-- ArchiveFetcher (Wayback + Archive.today)
                       |-- TavilyFetcher (末层降级)
                       +-- CachedFetcher (SQLite 读写穿透)
```

## 开发

```bash
# 安装开发依赖
uv sync --extra dev

# 运行测试
pytest

# 运行指定测试
pytest tests/test_config.py -v
```

## Roadmap

- [ ] 支持 Tavily Search API
- [ ] 支持 Exa Search API
- [ ] 支持 Firecrawl Search API
- [ ] 支持知乎、X 的内容抓取
- [ ] 支持 Yandex 搜索模块
- [ ] 支持 Reddit 搜索与内容抓取
- [ ] 支持 Google 搜索模块
- [ ] GitHub Issues/Discussions 内容抓取
- [ ] 搜索查询路由（按查询类型自动选择垂直源）

## 许可证

[GPL-3.0-or-later](https://www.gnu.org/licenses/gpl-3.0.html)
