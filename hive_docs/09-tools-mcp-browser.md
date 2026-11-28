# 09 · 工具、MCP 与浏览器

> Agent 只看见“普通函数调用”，但这些函数来自三个地方：
> **进程内 harness 工具 × 外部 MCP 服务器 × 用户 Chrome 的浏览器桥**。

## 1. 工具的三个来源

| 来源 | 运行位置 | 注册方式 | 典型工具 |
|---|---|---|---|
| **内置 harness** | Hive 进程内 | `tools/harness_tools.py` 的 5 个 group builder | 终端、文件、图表、记忆、PDF/CSV/图片 |
| **外部 MCP** | 独立进程/HTTP 服务 | `hive mcp add/install`，存 `mcp_registry/installed.json` | 104 个 `aden_tools` 集成、用户自己的 server |
| **浏览器** | 用户 Chrome + Bridge 进程 | `browser_setup` 发现 + `hive-browser` CLI | 导航、点击、截图、JS、标签组 |

三者最终都进入 `ToolRegistry`，对模型呈现为同一种 `Tool`（name/description/JSON Schema）。

## 2. 内置 harness 工具

`harness_tools.py` 定义 5 组，每个组名可以像 MCP server 名一样用于允许列表与 `@server:` 引用：

| 组 | 工具 |
|---|---|
| `terminal-tools` | 10 个：`terminal_exec`、`terminal_job_start/logs/manage`、`terminal_pty_open/run/close`（POSIX）、`terminal_rg`、`terminal_glob`、`terminal_output_get` |
| `files-tools` | `read_file`、`write_file`、`edit_file`、`search_files` |
| `chart-tools` | `chart_render`（ECharts / Mermaid → PNG，嵌入聊天并可下载） |
| `memory-tools` | `search_messages`；`search_timeline`（`memory_timeline` 开启时） |
| `hive_tools` | `attach_file`、`pdf_read`、`web_scrape`、`get_current_time`、`get_account_info`、`image_generate`、`csv_*`；可选 `excel_*`（安装 openpyxl）；可选 email senders（`HIVE_EMAIL_SENDERS=1`）；`read_file`/`edit_file` 也在此组注册 |

### 2.1 终端能力

`terminal_exec` 是核心：

- 默认 cwd 是会话工作目录；
- 支持超时、输出上限、后台任务、PTY；
- 输出溢出时返回 `output_handle`，用 `terminal_output_get` 取回；
- 长命令会后台化，返回 job_id，用 `terminal_job_logs/manage` 管理；
- 浏览器 CLI 也通过它执行。

`terminal_rg` / `terminal_glob` 是 ripgrep 与 glob 的封装，属于**并发安全只读**工具。

### 2.2 文件工具

`read_file` / `write_file` / `edit_file` / `search_files` 仍存在，但 Queen 的默认类别已把“文件 I/O”转向终端（`file_ops` 只保留 `pdf_read`、`attach_file`；`code_editing` 才给 `read_file`/`edit_file`）。原因：

- 终端工具更通用，能处理大文件、管道、编码；
- 文件工具更适合精确编辑与 stale-edit 防护（`read_file` 记录状态，`edit_file` 检查）。

### 2.3 `aden_tools` 完整目录

`tools/src/aden_tools/tools/` 有 **104** 个集成目录、**866** 个 `@mcp.tool` 注册点，覆盖：

GitHub、GitLab、Gmail、Google Docs/Sheets/Analytics/Search Console、Slack、Discord、Telegram、Notion、Jira、Linear、Asana、Trello、HubSpot、Salesforce、Pipedrive、Zoho、Intercom、Zendesk、Freshdesk、Stripe、QuickBooks、Razorpay、Plaid、Shopify、BigQuery、Snowflake、Redshift、Databricks、Postgres、MySQL/Redis/Mongo、AWS S3、Cloudflare、Vercel、Docker Hub、Kafka、n8n、Langfuse、W&B、Prometheus、PagerDuty、Twilio、Zoom、Calendly、arxiv、Wikipedia、web search/scrape、安全扫描器（port/SSL/subdomain/headers/tech stack）等。

**Hive 不再在进程内加载完整目录**，只加载 `hive_tools` 子集。要用完整目录：

```bash
uv run python tools/mcp_server.py --stdio
# 或
INCLUDE_UNVERIFIED_TOOLS=true uv run python tools/mcp_server.py --stdio
```

然后用 `hive mcp add` 注册。

## 3. 工具类别、eager 与 searchable

### 3.1 为什么分层

把 94+ 工具全量放进每次请求，会：

- 消耗大量 schema token；
- 让模型选择困难；
- 拖慢首 token。

Hive 的解法是 **eager（全 schema）+ searchable（只给名字和一行描述）**。

### 3.2 类别

`queen_tools_defaults.py` 的 `_TOOL_CATEGORIES`：

```text
file_ops, code_editing,
terminal_basic, terminal_advanced, terminal_core, terminal_extended,
spreadsheet_advanced,
browser_basic, browser_interaction, browser_core, browser_extended,
files_core, context_core, web_core,
research, context_awareness, charts, media, email_senders
```

每个 Queen 的 YAML 声明 `default_tool_categories`，形成角色默认允许列表。

### 3.3 总是启用（eager）

Queen 默认：

```text
file_ops + terminal_basic + context_awareness + browser_core
```

Worker 默认 keep-set：

```text
browser_core + terminal_core + files_core + context_core + web_core
```

这些类别的工具**绕过允许列表**，永远 eager；前端不允许取消勾选。

可通过 `configuration.json` 覆盖：

```json
{"queen_tools": {"always_enabled_categories": ["context_awareness"]}}
{"worker_tools": {"always_enabled_categories": []}}   // 空列表=关闭分层
```

### 3.4 searchable 与 `search_tools`

其余允许的工具只在提示里出现一行（名字 + 摘要），模型必须调用：

```python
search_tools("select:slack_send_message,slack_list_channels")
search_tools("send email")      # 词元重叠，最多 5 个
```

加载后：

- 全 schema 进入当前工具列表；
- 选择持久化到 `meta.json`，恢复会话不需要重新搜索；
- 工具列表对象在变更前保持同一引用，保证 prompt cache 稳定。

### 3.5 分层引擎

`tools/tool_tiers.py` 同时服务 Queen 与 Worker：

- 空 eager 集合 = 关闭分层（fail-open）；
- promote 顺序持久化，重启后重建相同 schema 顺序；
- `search_tools` 对“允许但未注册”的工具也会诚实报告，而不是“没有这个工具”。

## 4. MCP：两种接入模式

### 4.1 工具模式（默认）

服务器启动时被拉起（stdio）或连接（HTTP/SSE/unix），工具进入 Agent 的工具列表，schema 随每次请求发送。

适合：工具数量少、调用频繁、需要模型直接选择。

### 4.2 CLI 模式（`--as-cli`）

```bash
hive mcp add --name comfy --transport stdio --as-cli \
  --description "ComfyUI on this PC" \
  --env COMFYUI_URL=http://127.0.0.1:8000 \
  -- uvx --from comfy-mcp comfy-mcp
```

- 工具**不进入** Agent 的工具列表；
- Agent 只看到一行“这个 server 可以从终端运行”；
- 通过 `mcp2cli` 把 MCP 工具映射成命令，由 `terminal_exec` 调用：

```bash
hive mcp run comfy --list
hive mcp run comfy run-workflow --help
```

适合：工具多、调用少（例如 39 个工具、15,600 token 的 ComfyUI）。

切换：

```bash
hive mcp mode <name> cli
hive mcp mode <name> tools
```

### 4.3 传输与注册表

支持：

| transport | 配置 |
|---|---|
| `stdio` | command + args + env |
| `http` | url + headers |
| `sse` | url |
| `unix` | socket_path |

注册表位置：

```text
~/.hive/mcp_registry/
├── installed.json
├── config.json          # index_url、refresh_interval_hours
└── cache/               # registry index、mcp2cli 工具缓存
```

远程索引默认 `https://raw.githubusercontent.com/aden-hive/hive-mcp-registry/main/registry_index.json`，24h 刷新。

### 4.4 连接管理

- `MCPConnectionManager` 按 server 名池化子进程，跨 Agent 复用；
- 每个 MCP server 的工具名并入全局 `ToolRegistry`，重名 first-wins；
- `max_tools` 可限制用户新增 server 的工具总数，Hive 自带组不受限；
- 健康检查、启用/禁用、配置覆盖、别名（`@server:`）都有 CLI 与 API；
- OAuth token 刷新后，注册表会触发 server 重启以使用新 token。

### 4.5 工具注册表的统一处理

`loader/tool_registry.py`：

- 从模块、MCP server、函数注册工具；
- 维护 `CONCURRENCY_SAFE_TOOLS`；
- `CONTEXT_PARAMS`（`agent_id`、`data_dir`、`profile`、`session_cwd`、`colony_id`、`principal` 等）从 schema 中剥离，由框架强制注入，模型不能伪造；
- MCP 图片工具识别：名称匹配 `screenshot|capture|render_image|...` 或结果是 `{"_images": ...}`；
- `terminal_exec` 的 `hive-browser screenshot` 结果会被识别并**内联图片**到会话，路径带空格也能正确解析；
- 结果统一包装成 `ToolResult`，错误转成结构化文本而不是抛异常。

## 5. 浏览器：驱动用户自己的 Chrome

### 5.1 组成

```text
用户 Chrome
  └── Hive Browser Bridge 扩展（tools/browser-extension/）
        │ WebSocket
        ▼
  bridge_host（独立进程，supervisor 重启）
        │ RPC
        ▼
  gcu MCP server / hive-browser CLI（短生命周期）
        ▲
        │ terminal_exec
  Agent
```

- Bridge 必须比任何单个工具进程活得久；因此 `bridge_host.py` 有 supervisor 模式，崩溃指数退避重启；
- 桌面模式下它监视 `HIVE_DESKTOP_PARENT_PID`，父进程死亡后自行退出；
- 工具服务器被 60s 超时杀掉时，Bridge 与标签页仍存活，CLI 重连即可。

### 5.2 `hive-browser` CLI

常用命令树：

```text
setup / status / stop / open / navigate / reload
interact --action click|type|key|scroll|drag|screenshot|zoom|wait
select / upload / screenshot / evaluate / script
tab list|close|activate
page html|snapshot|text|shadow-query|console|resize
dialog respond
help
```

设计要点：

- 所有命令支持 `--json`，便于 Agent 解析；
- 坐标使用**视口比例**（0..1），不是像素，适配不同分辨率；
- `interact` 走真实浏览器命中测试/焦点/布局，能处理 shadow DOM；
- `evaluate` 支持内联、`@file`、stdin；
- `script` 可运行技能内置的 `run(ctx)` 编排脚本；
- 截图落盘并返回路径，框架再内联为图片消息。

### 5.3 标签组与账号隔离

- 每个 Worker 有**自己的标签组**，与 Queen 及其他 Worker 隔离；
- 但共享同一个 Chrome Profile，因此登录态/ Cookie 共享；
- `WorkerProfile.browser_profile` 可指定不同 Chrome Profile，用于“同一 Colony 驱动多个社媒账号”；
- 每个命令的 JSON 会回显实际使用的 profile，Agent 可以校验是否开错账号。

### 5.4 生命周期规则（技能强制）

`browser-automation` 技能明确禁止：

- 启动 Chrome（任何 `google-chrome` / `--remote-debugging-port` / headless 自启）；
- kill/pkill/killall Chrome、Bridge、gcu、Hive desktop；
- 把“超时”当成“浏览器崩了”。

允许的恢复顺序：

1. 等 30s，用更小的请求重试一次；
2. 关闭自己的标签页再开；
3. 报告失败，转做不需要浏览器的工作；
4. 升级给用户。

这是为了多 Agent 共享一个浏览器时的安全：一个人杀进程会破坏所有人的登录态。

### 5.5 安全含义

- 浏览器里是用户真实登录的账号（邮箱、社媒、银行…）；
- Agent 的点击/输入等同于用户操作；
- 提示注入（网页内容里的指令）可能诱导 Agent 执行危险动作；
- 因此：工具允许列表、Worker profile 隔离、`browser-automation` 技能的行为约束、人工审批（如适用）必须一起用。

## 6. 社媒限速器

`framework/rate_limiter.py` 是一个独立的、SQLite 持久化的限速器，用于防止自动化导致账号封禁：

| 平台/动作 | 每小时默认/上限 | 每日默认/上限 | 每周默认/上限 | 最小间隔 |
|---|---|---|---|---|
| linkedin.profile_view | 25 / 100 | 150 / 400 | — | 5s |
| linkedin.invite | 10 / 25 | 50 / 125 | 200 / 500 | 30s |
| linkedin.message | 20 / 80 | 60 / 200 | — | 15s |
| linkedin.comment | 8 / 30 | 50 / 150 | — | 20s |
| x.reply | 10 / 30 | 50 / 150 | — | 30s |
| x.dm | 20 / 80 | 60 / 200 | — | 20s |
| x.post | 5 / 25 | 30 / 125 | — | — |

用户可通过 `configuration.json` 的 `rate_limits` 或 `HIVE_RATE_<PLATFORM>_<ACTION>_DAILY` 调整，但不能超过硬上限。

它通常由浏览器脚本/技能调用，而不是模型直接调用；这是“把合规约束放进工具层”的典型做法。

## 7. 图片与视觉

- 支持视觉的模型：截图、附件直接进入上下文；
- 纯文本模型：视觉回退模型生成 caption；
- 历史图片只保留最近 2 张，避免 Gemini 等把 base64 当文本计数；
- `image_generate` 走图片代理（gpt-image-2），按张计费，默认不在 always-enabled 中，属于 `media` 类别；
- `attach_file` 可把磁盘上的 PDF/图片重新注入上下文（用于“附件已老化出上下文”的情况）。

## 8. 评价

**优点**

- 三类工具统一成普通函数调用，模型无需理解来源差异。
- eager/searchable 分层是务实的上下文成本控制；`select:` 语法让确定性加载成为可能。
- MCP 的 tools/cli 双模式，直接回应“工具多但调用少”的成本问题。
- 浏览器桥的 supervisor + 独立生命周期设计，解决了工具进程短命与浏览器长命的矛盾。
- 明确禁止杀浏览器/自启，是多 Agent 共享真实浏览器时最重要的工程约束。

**风险**

- 104 个集成、866 个注册点是巨大的供应链与凭据面；默认不加载是正确选择。
- 浏览器自动化 + 用户登录态 + 提示注入 = 高危组合；需要部署侧的最小权限与人工审批。
- `terminal_exec` 是“万能后门”：任何工具白名单最终都可能被它绕过；真正的边界是操作系统权限。
- MCP server 的质量不可控（超时、工具描述错误、返回非结构化），Hive 只能做防御性包装。
