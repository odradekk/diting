# Diting (谛听)

[English](README_EN.md) | 中文

[![Python](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](https://www.python.org/)
[![MCP](https://img.shields.io/badge/MCP-FastMCP-blue?logo=data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0Ij48cGF0aCBmaWxsPSJ3aGl0ZSIgZD0iTTEyIDJMMiA3bDEwIDUgMTAtNXoiLz48L3N2Zz4=)](https://github.com/jlowin/fastmcp)
[![Pydantic](https://img.shields.io/badge/Pydantic-v2-E92063?logo=pydantic&logoColor=white)](https://docs.pydantic.dev/)
[![Playwright](https://img.shields.io/badge/Playwright-green?logo=playwright&logoColor=white)](https://playwright.dev/python/)
[![License](https://img.shields.io/badge/License-GPL--3.0-blue)](https://www.gnu.org/licenses/gpl-3.0.html)

深度聚合搜索 MCP 服务。跨多引擎并行检索，由 LLM 评分、摘要，通过 MCP 协议返回结构化结果。

## 特性

- **多引擎聚合** -- Baidu、Bing、DuckDuckGo、Brave、SerpAPI、X、知乎，其中 Baidu / Bing / DuckDuckGo 默认启用，无需 API Key
- **自适应多轮搜索** -- 首轮由 LLM 生成最优搜索词，每轮结束后根据已有结果智能分析信息缺口，自适应生成下一轮搜索词
- **LLM 评分** -- 对每条结果进行相关性 + 质量双维评分，权重可配置
- **思考模型兼容** -- 自动处理 DeepSeek、MiniMax M2.7 等思考模型的 `reasoning_content` 字段和 `<think>` 标签
- **内容抓取** -- 本地抓取（curl_cffi HTTP + Playwright 浏览器升级）为主，Tavily API 作为降级后备
- **自动黑名单** -- 低质量域名自动加入黑名单，后续搜索直接过滤
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
    "LLM_BASE_URL": "https://your-api-endpoint.com/v1",
    "LLM_MODEL": "your-model",
    "LLM_API_KEY": "your-key"
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
```

## 配置

复制 `.env.example` 为 `.env`，填入所需配置：

```bash
cp .env.example .env
```

### 必填项

| 变量 | 说明 |
|------|------|
| `LLM_BASE_URL` | OpenAI v1 兼容 API 端点 |
| `LLM_MODEL` | 模型名称，如 `gpt-4o-mini` |
| `LLM_API_KEY` | API 密钥 |

### 可选项

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `TAVILY_API_KEY` | 空 | Tavily Extract API 密钥，用于内容抓取降级 |
| `BRAVE_API_KEY` | 空 | Brave Search API 密钥 |
| `SERP_API_KEY` | 空 | SerpAPI 密钥 |
| `ENABLE_BAIDU` | `true` | 启用百度搜索模块 |
| `ENABLE_BING` | `true` | 启用 Bing 搜索模块 |
| `ENABLE_DUCKDUCKGO` | `true` | 启用 DuckDuckGo 搜索模块 |
| `ENABLE_BRAVE` | `false` | 启用 Brave 搜索模块（需要 `BRAVE_API_KEY`） |
| `ENABLE_SERP` | `false` | 启用 SerpAPI 模块（需要 `SERP_API_KEY`） |
| `ENABLE_X` | `false` | 启用 X/Twitter 模块（需要 Cookie 或 Storage State） |
| `ENABLE_ZHIHU` | `false` | 启用知乎模块（需要 Cookie 或 Storage State） |
| `X_COOKIE` | 空 | X/Twitter 原始 Cookie 字符串 |
| `ZHIHU_COOKIE` | 空 | 知乎原始 Cookie 字符串 |
| `MAX_RESULTS` | `10` | 每个搜索引擎返回的最大结果数，支持分页/滚动自动获取 |
| `MAX_CONCURRENCY` | `5` | 模块并行搜索最大并发数 |
| `LLM_MAX_TOKENS` | `8192` | LLM 最大输出 token 数 |
| `LLM_TIMEOUT` | `120` | 单次 LLM 调用超时（秒） |
| `MODULE_TIMEOUT` | `30` | 单个搜索模块超时（秒） |
| `GLOBAL_TIMEOUT` | `150` | 整体搜索管线超时（秒） |
| `MAX_SEARCH_ROUNDS` | `3` | 最大迭代搜索轮数 |
| `SCORE_THRESHOLD` | `0.6` | 结果最低保留分数（0-1） |
| `RELEVANCE_WEIGHT` | `0.5` | 相关性评分权重 |
| `QUALITY_WEIGHT` | `0.5` | 质量评分权重 |
| `AUTO_BLACKLIST` | `true` | 自动黑名单低质量域名 |
| `AUTO_BLACKLIST_THRESHOLD` | `0.3` | 域名所有结果低于此分数时自动加入黑名单 |
| `MIN_SNIPPET_LENGTH` | `30` | 结果最短摘要字符数，低于则过滤 |
| `BLACKLIST_FILE` | 内置 | 黑名单规则文件路径（默认使用包内 `diting/data/blacklist.txt`） |
| `PROMPTS_DIR` | 空 | 自定义提示词目录（覆盖内置默认） |
| `LOG_LEVEL` | `INFO` | 日志级别 |

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
                 |     |-- 初始查询生成 (LLM)
                 |     |-- 并行模块搜索 (Semaphore 限流)
                 |     |-- 去重 + 预过滤 + 黑名单
                 |     |-- LLM 评分 (relevance * w1 + quality * w2)
                 |     |-- 质量评估 + 下轮查询生成（自适应）
                 |     +-- 摘要生成（抓取全文 + LLM）
                 |
                 +-- fetch tool --> CompositeFetcher
                       |-- LocalFetcher (HTTP + Browser)
                       +-- TavilyFetcher (降级后备)
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

## 许可证

[GPL-3.0-or-later](https://www.gnu.org/licenses/gpl-3.0.html)
