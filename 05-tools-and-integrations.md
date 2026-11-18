# 05 · 工具与集成

## 1. 工具的三个来源

Agent 看到的永远只是普通函数调用，但这些调用来自三处：

| 来源 | 运行方式 | 注册位置 |
| --- | --- | --- |
| **内置工具（in-process）** | 在 Hive 进程内直接执行，无子进程/无工具服务器 | `core/framework/tools/harness_tools.py` |
| **外部 MCP 服务器** | 通过 MCP 协议连接；可「工具模式」或「CLI 模式」 | `loader/mcp_registry.py`、`loader/mcp_client.py` |
| **浏览器桥** | 驱动用户自己的 Chrome | `tools/src/gcu/`、`tools/browser-extension/` |

此外，`tools/src/aden_tools/tools/` 下的 104 个集成构成「集成目录」，既可作为内置工具的子集运行，也可通过独立的 MCP server（`tools/mcp_server.py`）暴露给 IDE。

## 2. 内置工具分组

`harness_tools.py` 把 Hive 自己的工具按组注册：

| 组 | 内容 |
| --- | --- |
| `terminal-tools` | shell 命令、后台 job、ripgrep 搜索、glob |
| `files-tools` | 读/写/编辑文件、搜索 |
| `chart-tools` | ECharts、Mermaid（Playwright 渲染） |
| `memory-tools` | 会话搜索、recall |
| `hive_tools` | 附件、PDF、网页抓取、CSV、图片生成等 |

这些工具「启动时不需要任何子进程」，是 Hive 相比「每个工具一个 MCP server」的显著简化。

## 3. 104 个集成目录

`tools/src/aden_tools/tools/` 下的集成按域大致分为：

| 域 | 代表 |
| --- | --- |
| 协作/沟通 | Slack、Discord、Telegram、Mattermost、Zoom、Intercom、Zendesk、Freshdesk |
| CRM/销售 | HubSpot、Salesforce、Pipedrive、Attio、Apollo、Lusha、Zoho CRM |
| 营销/内容 | Gmail、Email、Calendly、Cal.com、Brevo、Twitter、Reddit、YouTube、Wikipedia |
| 工程/DevOps | GitHub、GitLab、Jira、Linear、Asana、Trello、PagerDuty、Docker Hub、Vercel、Cloudflare、Terraform、n8n |
| 数据/数据库 | PostgreSQL、Azure SQL、Snowflake、BigQuery、Redshift、Databricks、Supabase、MongoDB、Redis、Pinecone、Kafka |
| 财务/电商 | Stripe、QuickBooks、Razorpay、Plaid、Shopify |
| Google/Microsoft | Google Sheets、Google Docs、Google Analytics、Search Console、Microsoft Graph、Confluence、Notion、Obsidian |
| 搜索/研究 | Exa、SerpAPI、DuckDuckGo、arXiv、HuggingFace、Similarweb、news |
| 安全/网络 | DNS 安全扫描、HTTP headers 扫描、端口扫描、SSL/TLS 扫描、子域枚举、技术栈探测、风险评分 |
| 文件/媒体 | PDF 读取、CSV、Excel、图片生成、视觉、附件 |
| 可观测性 | Langfuse、Prometheus、WandB |

> 数量统计：`tools/src/aden_tools/tools/*/` 共 **104** 个目录。

这些工具大多需要凭证，由 `framework/credentials/` 与 `aden_tools/credentials/` 统一管理，并支持 OAuth2 刷新。

## 4. MCP：两种接入模式

### 4.1 工具模式（默认）

`hive mcp add` 注册的服务器，其工具加入与内置工具相同的 allowlist，schema 随每次模型调用发送。

优点：模型直接可见、调用简单。
缺点：工具多时 prompt 膨胀。文档给出的例子是 comfy-mcp 有 39 个工具、约 15,600 tokens。

### 4.2 CLI 模式（`--as-cli`）

服务器不在工具列表里出现，只留一行说明；Agent 通过 `terminal_exec` 调用 `hive mcp run <server> ...`，由 `mcp2cli` 把 MCP 工具转成命令行。

```
hive mcp add --name comfy --transport stdio --as-cli \
  --description "ComfyUI on this PC: run workflows, generate images" \
  --env COMFYUI_URL=http://127.0.0.1:8000 \
  -- uvx --from comfy-mcp --with "comfy-cli>=1.14.0" comfy-mcp
```

环境变量与 headers 通过环境传递，不出现在命令行。工具列表缓存在 `$HIVE_HOME/cache/mcp2cli`。

### 4.3 注册表

- 安装记录：`~/.hive/mcp_registry/installed.json`
- 客户端：`loader/mcp_client.py`
- 工具注册与筛选：`loader/tool_registry.py`（1,720 行）

## 5. 工具分层（eager / searchable）

这是控制上下文体积的关键机制，实现在 `tools/tool_tiers.py` 与 `agents/queen/queen_tools_defaults.py`。

```
允许的工具集合（allowlist）
  ├─ eager 层：完整 schema 随每次请求发送
  └─ searchable 层：只在 <searchable_tools> 清单里出现一行
        ↑ Agent 调用 search_tools("select:a,b") 或自由文本查询后提升为 eager
```

设计要点：

- **空 eager 集合 = 关闭分层**，所有允许工具都 eager（fail-open，避免启动期展开失败导致工具消失）。
- **`get_current_tools()` 在无变化时返回同一个列表对象**，保证发给模型的 tools JSON 逐字节稳定，从而 prompt cache 不失效。
- **提升顺序持久化**，恢复会话时重建相同的 eager schema 顺序。

### 5.1 类别（categories）

`queen_tools_defaults.py` 定义 `_TOOL_CATEGORIES`，每个 Queen 的 YAML 档案声明 `default_tool_categories`。一个工具可以属于多个类别；`ALWAYS_ENABLED_CATEGORIES` 是与角色无关的常开集合。

Worker 有独立的 keep-set：`WORKER_ALWAYS_ENABLED_CATEGORIES`，可通过 `configuration.json` 的 `worker_tools.always_enabled_categories` 覆盖。

### 5.2 搜索解析

`_match_searchable_tools()` 是确定性的（无模型参与）：

- `select:a,b,c`：精确加载。
- 自由文本：按工具名 + 描述做 token 重叠打分，返回最多 5 个。

这避免了「用模型选工具」的不确定性和额外成本。

## 6. 浏览器桥

- 前端扩展：`tools/browser-extension/`（Chrome 扩展，用户自己的浏览器、自己的登录态）。
- 桥进程：`tools/src/gcu/bridge_host.py`，默认端口 `14829`，状态端口 `14830`（兼容旧版 `9230`）。
- Agent 通过 `hive-browser` CLI 驱动。
- 每个 Worker 获得独立的 tab group，避免互相干扰。
- `browser_setup` 工具让浏览器可发现、可门控。

价值：Agent 可以使用用户已登录的 SaaS，而不需要为每个系统配置 API key。代价：依赖用户的 Chrome、需要扩展安装、调试链路长。

## 7. 图片/视觉

- 支持视觉的模型：截图与附件直接进入上下文。
- API 不支持在 tool result 里带图：图片被搬到下一条 user message。
- 纯文本模型：由 `vision_fallback` 模型生成 caption，并做 crop 映射。
- 相关：`llm/capabilities.py`、`agent_loop/internals/vision_fallback.py`。

## 8. 凭证与安全边界

| 机制 | 说明 |
| --- | --- |
| 加密凭证库 | `~/.hive/credentials/`，Fernet 加密；`HIVE_CREDENTIAL_KEY` 控制 |
| 运行时取用 | `hive-cred token <provider>` 让脚本按需取短期令牌，避免明文落盘 |
| 工具级校验 | 每个工具在运行时校验自己的凭证 |
| 工具 allowlist | 类别 + 每 Queen/每 Colony 白名单，在 Skills Library 的 MCP Tools 标签页编辑 |
| 审批点 | `suggest_colony` 需确认；`write_skill(scope="global")` 需确认；Sentinel 升级 |

需要注意：`write_skill(scope="global")` 的「审批」是**行为规则**而非硬墙——Queen 如果有 terminal 工具，仍可自己写技能目录（release notes 明确说明）。这是有意的取舍，但采用时应知情。

## 9. 工具设计的优点与问题

**优点**

- in-process 工具避免了 MCP 进程管理的大量运维负担。
- eager/searchable 分层 + 确定性搜索，兼顾上下文成本与可发现性。
- 类别 + 白名单的双层门控，适合多角色（13 个 Queen）场景。
- 凭证集中管理，支持 OAuth 刷新与短期令牌。

**问题**

- 104 个集成的质量、测试覆盖、凭证路径不可能完全一致；使用时需要逐个验证。
- 工具名/描述是 prompt 的一部分，任何改动都可能影响模型行为，但缺少「工具描述变更」的回归测试。
- CLI 模式依赖 `mcp2cli` 与终端工具，链路比工具模式长，错误信息也更难定位。
- 浏览器桥是单点：扩展未连接、端口占用、Chrome 升级都可能让依赖浏览器的 Worker 失败。

**建议**

- 为工具描述建立快照测试（名称 + 首行描述），把 prompt 影响纳入 CI。
- 为 104 个集成建立「最小可用矩阵」（无凭证 mock 测试 + live 测试分层），并在文档中标注成熟度。
- 给浏览器桥增加明确的健康检查与降级路径（例如自动回退到无浏览器工具）。
