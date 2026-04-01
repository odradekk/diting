# Changelog

## 1.0.0

### 搜索引擎模块
- Baidu 网页搜索（4 级 URL 提取）
- Bing HTML 抓取搜索
- DuckDuckGo HTML 抓取搜索
- Brave Search API
- SerpAPI
- X (Twitter) Playwright 搜索（cookie 认证 + 滚动加载）
- 知乎 Playwright 搜索（cookie 认证）
- 默认启用 Baidu、Bing、DuckDuckGo；其余需手动开启并提供 API key

### 搜索管道
- 多轮搜索：LLM 生成排序查询队列，每轮使用一个查询
- LLM 评分：独立的相关性和质量评分，可配置权重计算最终分数
- 质量评估：每轮结束后 LLM 判断结果是否充分，不足时追加补充查询
- URL 去重：标准化 URL 后基于集合去重
- 预过滤：移除短摘要、视频页面、搜索聚合器等低质量结果
- 黑名单过滤：正则匹配，支持手动规则和自动拉黑（基于质量分低于阈值的域名）
- LLM 分类：将结果按类别分组（官方文档、博客、GitHub 等）
- 摘要生成：抓取 top N 源页面内容，LLM 生成带引用的 Markdown 分析

### 内容抓取
- LocalFetcher：curl_cffi HTTP 抓取 + Playwright 浏览器自动升级
  - 双提取器：trafilatura（主）+ readability/markdownify（备）
  - 阻断检测：captcha、登录墙、JS shell 识别
  - 常驻浏览器实例，每次请求创建独立 context
- TavilyFetcher：Tavily Extract API（可选 fallback）
- CompositeFetcher：本地优先，Tavily 兜底

### MCP 服务
- FastMCP 集成，暴露 `search` 和 `fetch` 两个工具
- 生命周期管理：启动时初始化所有资源，关闭时按序清理
- 并发控制：可配置 Semaphore 限制模块并行数

### 基础设施
- Pydantic Settings 配置管理，支持 .env 文件和环境变量
- 统一日志系统，请求级别 ID 追踪
- 全链路降级策略：LLM 失败返回未评分结果，模块失败跳过继续，全局超时返回部分结果
- 459+ 单元测试覆盖所有主要模块
