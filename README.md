# Diting (谛听)

[English](README_EN.md) | 中文

[![Python](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](https://www.python.org/)
[![MCP](https://img.shields.io/badge/MCP-FastMCP-blue?logo=data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0Ij48cGF0aCBmaWxsPSJ3aGl0ZSIgZD0iTTEyIDJMMiA3bDEwIDUgMTAtNXoiLz48L3N2Zz4=)](https://github.com/jlowin/fastmcp)
[![Pydantic](https://img.shields.io/badge/Pydantic-v2-E92063?logo=pydantic&logoColor=white)](https://docs.pydantic.dev/)
[![Playwright](https://img.shields.io/badge/Playwright-green?logo=playwright&logoColor=white)](https://playwright.dev/python/)
[![License](https://img.shields.io/badge/License-GPL--3.0-blue)](https://www.gnu.org/licenses/gpl-3.0.html)

深度聚合搜索 MCP 服务。跨多引擎并行检索，由 LLM 评分、分类、摘要，通过 MCP 协议返回结构化结果。

## 特性

- **多引擎聚合** -- Baidu、Bing、DuckDuckGo、Brave、SerpAPI、X、知乎，其中 Baidu / Bing / DuckDuckGo 默认启用，无需 API Key
- **多轮迭代搜索** -- LLM 自动生成排序查询词，评估结果质量，质量不足时自动发起下一轮搜索
- **LLM 评分** -- 对每条结果进行相关性 + 质量双维评分，权重可配置
- **内容抓取** -- 本地抓取（curl_cffi HTTP + Playwright 浏览器升级）为主，Tavily API 作为降级后备
- **自动黑名单** -- 低质量域名自动加入黑名单，后续搜索直接过滤
- **结果分类** -- LLM 自动将结果按类别归组（如"官方文档"、"博客文章"、"GitHub 仓库"等）
- **摘要生成** -- 抓取高分来源页面全文，生成带引用的 Markdown 分析摘要

## 安装

需要 Python >= 3.10。

```bash
# 使用 uv（推荐）
uv sync

# 或使用 pip
pip install .
```

安装 Playwright 浏览器（本地抓取的浏览器升级及 X、知乎模块需要）：

```bash
playwright install chromium
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
| `MAX_CONCURRENCY` | `5` | 模块并行搜索最大并发数 |
| `LLM_TIMEOUT` | `60` | 单次 LLM 调用超时（秒） |
| `MODULE_TIMEOUT` | `30` | 单个搜索模块超时（秒） |
| `GLOBAL_TIMEOUT` | `120` | 整体搜索管线超时（秒） |
| `MAX_SEARCH_ROUNDS` | `3` | 最大迭代搜索轮数 |
| `SCORE_THRESHOLD` | `0.3` | 结果最低保留分数（0-1） |
| `RELEVANCE_WEIGHT` | `0.5` | 相关性评分权重 |
| `QUALITY_WEIGHT` | `0.5` | 质量评分权重 |
| `AUTO_BLACKLIST` | `true` | 自动黑名单低质量域名 |
| `AUTO_BLACKLIST_THRESHOLD` | `0.3` | 域名所有结果低于此分数时自动加入黑名单 |
| `MIN_SNIPPET_LENGTH` | `30` | 结果最短摘要字符数，低于则过滤 |
| `BLACKLIST_FILE` | `config/blacklist.txt` | 黑名单规则文件路径 |
| `PROMPTS_DIR` | 空 | 自定义提示词目录（覆盖内置默认） |
| `LOG_LEVEL` | `INFO` | 日志级别 |

X 和知乎模块支持两种认证方式，优先级从高到低：

1. **Storage State 文件**（推荐）-- 将 Playwright 导出的 JSON 放在 `config/x_storage_state.json` 或 `config/zhihu_storage_state.json`，模块会自动加载
2. **Cookie 字符串** -- 设置 `X_COOKIE` / `ZHIHU_COOKIE` 环境变量，存在 Storage State 文件时会被忽略

## 快速开始

```bash
# 直接运行（stdio 传输）
python server.py

# 或使用 fastmcp CLI
fastmcp run server.py
```

### 在 MCP 客户端中配置

以 Claude Desktop 为例：

```json
{
  "mcpServers": {
    "diting": {
      "command": "uv",
      "args": ["run", "--directory", "/path/to/diting", "python", "server.py"]
    }
  }
}
```

## MCP 工具

### search

深度聚合搜索。接受自然语言查询，返回评分后的结构化结果。

```
参数: query (string) -- 自然语言搜索查询
返回: SearchResponse -- 包含评分来源、分类和摘要的结构化响应
```

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
                 |     |-- 查询生成 (LLM)
                 |     |-- 并行模块搜索 (Semaphore 限流)
                 |     |-- 去重 + 预过滤 + 黑名单
                 |     |-- LLM 评分 (relevance * w1 + quality * w2)
                 |     |-- 质量评估（不足则继续搜索）
                 |     |-- 分类
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

## 许可证

[GPL-3.0-or-later](https://www.gnu.org/licenses/gpl-3.0.html)
