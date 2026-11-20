# go_hive 外部依赖清单

> 版本：`hive 1.0.26-go.1`（`go_hive` 仓库）。生成日期 2026-11-20。
> 凭据清单来源：`internal/credentials/specs_gen.go`（由上游 `tools/src/aden_tools/credentials` 生成，85 个模块 / 118 条凭据，digest `a7832ddc…`）。运行时端点来自 `internal/llm`、`internal/tools/integrations`、`internal/senders`、`internal/server` 的真实代码。

## 1. 依赖总览

| 类别 | 数量 | 是否需要凭据 | 运行期必需 | 说明 |
| --- | ---: | --- | --- | --- |
| LLM 推理 API | 13+（可自定义 base） | 是（至少一个） | **是** | 没有 LLM 会话无法工作；可用本地 Ollama/vLLM |
| 凭据型 SaaS API | **77 个源模块 / 118 条凭据** | 是 | 否 | 按需；缺凭据时工具不可用或 park 等填表 |
| 无凭据固定 API | 6 | 否 | 否 | arXiv、DuckDuckGo、Wikipedia、Open-Meteo、Yahoo Finance、YouTube 转录 |
| 任意 Web 抓取 | 1 | 否 | 否 | 模型给 URL 即抓，外呼目标不受限 |
| 网络扫描器（4） | 4 | 否 | 否 | DNS/端口/TLS/HTTP 头；**默认关闭**（`HIVE_NETWORK_SCANNERS=false`） |
| MCP 服务器 | 0..N | 视服务器 | 否 | 用户自行安装；stdio 子进程或 HTTP/SSE |
| SMTP / IMAP | 1 套 | 是 | 否 | 发信走 SMTP；读信按服务商 IMAP 主机 |
| 本地运行时 | — | 否 | **是** | 文件系统、SQLite、进程、localhost HTTP/SSE |

**结论**：真正“跑起来必需”的外部依赖只有 **LLM API**（或本地推理服务）。其余全部是按需能力；第三方 SaaS 目前 81 个模块中多数**未做真实凭据验证**（见 §8）。

## 2. LLM 推理 API（必需）

| Provider | 默认端点 | 凭据 | 用途 |
| --- | --- | --- | --- |
| OpenAI | `https://api.openai.com/v1` | `OPENAI_API_KEY` | 通用推理 |
| Anthropic | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` | 推理 + Messages SSE 流式 |
| Google Gemini | `https://generativelanguage.googleapis.com` | `GEMINI_API_KEY` / `GOOGLE_API_KEY` | 推理 |
| DeepSeek | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | 推理 |
| Groq | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | 低延迟推理 |
| Cerebras | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | 低延迟推理 |
| Together | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | 开源模型托管 |
| Mistral | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | 推理 |
| xAI | `https://api.x.ai/v1` | `XAI_API_KEY` | 推理 |
| Perplexity | `https://api.perplexity.ai/v1` | `PERPLEXITY_API_KEY` | 联网推理 |
| MiniMax | `https://api.minimax.io/v1` | `MINIMAX_API_KEY` | 推理（**不流式**，见发布说明） |
| Kimi | `https://api.kimi.com/coding` | `KIMI_API_KEY` | 推理 |
| OpenRouter | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | 模型聚合 |
| 本地推理 | `http://127.0.0.1:11434` 等 | 无 | Ollama / vLLM / LM Studio / llama.cpp |
| 自定义 OpenAI 兼容 | `configuration.json → llm.api_base` | `llm.api_key_env_var` | 代理/私有网关（当前本机用 `token.tokenbroad.com`） |

**为什么依赖**：Queen 分诊、worker 执行、judge 评估、上下文压缩、记忆抽取、soul 反思都走同一个 `internal/llm`。没有可用的 `llm` 配置，`/api/sessions/{id}/chat` 直接 503。

## 3. MCP 服务器（可选，用户自带）

- 配置：`~/.hive/mcp_registry/installed.json` + `configuration.json` 的 `mcp` 块
- 传输：`stdio`（子进程）或 HTTP/SSE
- 命名：工具注册为 `mcp__<server>__<tool>`
- **为什么依赖**：把外部工具生态（文件系统、浏览器、数据库等）接进会话，而不必写进 41 个内置工具
- 风险：stdio 服务器继承当前用户权限；未配置白名单时 MCP 工具默认可见（`applyToolAllowlist` 可收紧）

## 4. 凭据型 SaaS API — CRM / 销售 / 支持

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `apollo` | `APOLLO_API_KEY` | 7 | Apollo |
| `attio` | `ATTIO_API_KEY` | 15 | Attio API key for CRM integration |
| `hubspot` | `HUBSPOT_ACCESS_TOKEN` | 15 | HubSpot access token (Private App or OAuth2) |
| `pipedrive` | `PIPEDRIVE_API_TOKEN` | 13 | Pipedrive API token for CRM management (also set PIPEDRIVE_DOMAIN for custom dom |
| `salesforce` | `SALESFORCE_ACCESS_TOKEN`; `SALESFORCE_INSTANCE_URL` | 9 | Salesforce OAuth2 Bearer access token |
| `zoho_crm` | `ZOHO_CRM_ACCESS_TOKEN` | 6 | Zoho CRM OAuth access token (also set ZOHO_CRM_DOMAIN for non-US regions) |
| `intercom` | `INTERCOM_ACCESS_TOKEN` | 11 | Intercom access token (Settings > Integrations > Developer Hub > Your App > Auth |
| `lusha` | `LUSHA_API_KEY` | 8 | Lusha API key for B2B contact and company enrichment |
| `calcom` | `CALCOM_API_KEY` | 9 | Cal |
| `calendly` | `CALENDLY_PAT` | 8 | Calendly Personal Access Token |
| `greenhouse` | `GREENHOUSE_API_TOKEN` | 9 | Greenhouse Harvest API token for ATS access |
| `freshdesk` | `FRESHDESK_API_KEY`; `FRESHDESK_DOMAIN` | 17 | Freshdesk API key for ticket, contact, agent, and group management |
| `zendesk` | `ZENDESK_EMAIL`; `ZENDESK_SUBDOMAIN`; `ZENDESK_API_TOKEN` | 8 | Zendesk agent email for API authentication |

## 4. 凭据型 SaaS API — 协作 / 项目 / 文档

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `slack` | `SLACK_BOT_TOKEN`; `SLACK_APP_TOKEN` | 51 | Slack Bot Token (starts with xoxb-) |
| `discord` | `DISCORD_BOT_TOKEN` | 7 | Discord Bot Token |
| `telegram` | `TELEGRAM_BOT_TOKEN` | 13 | Telegram Bot Token from @BotFather |
| `mattermost` | `MATTERMOST_ACCESS_TOKEN`; `MATTERMOST_URL` | 7 | Mattermost Personal Access Token |
| `zoom` | `ZOOM_ACCESS_TOKEN` | 9 | Zoom Server-to-Server OAuth access token |
| `asana` | `ASANA_ACCESS_TOKEN` | 9 | Asana personal access token for task and project management |
| `jira` | `JIRA_DOMAIN`; `JIRA_EMAIL`; `JIRA_API_TOKEN` | 9 | Jira Cloud domain (e |
| `linear` | `LINEAR_API_KEY` | 21 | Linear API key for issue tracking and project management |
| `notion` | `NOTION_API_TOKEN` | 13 | Notion internal integration token |
| `confluence` | `CONFLUENCE_DOMAIN`; `CONFLUENCE_EMAIL`; `CONFLUENCE_API_TOKEN` | 8 | Confluence domain (e |
| `trello` | `TRELLO_API_KEY`; `TRELLO_API_TOKEN` | 12 | Trello API key (also set TRELLO_TOKEN for authentication) |
| `obsidian` | `OBSIDIAN_REST_API_KEY` | 6 | Obsidian Local REST API key (64-char hex, from plugin settings) |
| `n8n` | `N8N_API_KEY`; `N8N_BASE_URL` | 6 | n8n API key for workflow management |
| `tines` | `TINES_API_KEY`; `TINES_DOMAIN` | 5 | Tines API key for authentication |
| `microsoft_graph` | `MICROSOFT_GRAPH_ACCESS_TOKEN` | 11 | Microsoft Graph OAuth 2 |

## 4. 凭据型 SaaS API — 工程 / 云 / 可观测

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `github` | `GITHUB_TOKEN` | 19 | GitHub Personal Access Token (classic) |
| `gitlab` | `GITLAB_TOKEN` | 9 | GitLab personal access token |
| `vercel` | `VERCEL_TOKEN` | 7 | Vercel access token for deployment and project management |
| `docker_hub` | `DOCKER_HUB_TOKEN` | 7 | Docker Hub personal access token (also set DOCKER_HUB_USERNAME for listing own r |
| `cloudflare` | `CLOUDFLARE_API_TOKEN` | 54 | Cloudflare API token (DNS/Zone Read-Write) |
| `terraform` | `TFC_TOKEN` | 5 | Terraform Cloud API token (User or Team token) |
| `pagerduty` | `PAGERDUTY_API_KEY`; `PAGERDUTY_FROM_EMAIL` | 8 | PagerDuty REST API key (account-level or user-level) |
| `prometheus` | `PROMETHEUS_BASE_URL` | 2 | Base URL of Prometheus server |
| `langfuse` | `LANGFUSE_PUBLIC_KEY`; `LANGFUSE_SECRET_KEY` | 6 | Langfuse public key (starts with pk-lf-) |
| `wandb` | `WANDB_API_KEY` | 6 | Weights & Biases API Key |
| `huggingface` | `HUGGINGFACE_TOKEN` | 9 | HuggingFace API token for Hub access (models, datasets, spaces) and Inference AP |
| `pinecone` | `PINECONE_API_KEY` | 9 | API key for Pinecone vector database operations |

## 4. 凭据型 SaaS API — 数据 / 数据库 / 存储

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `postgres` | `DATABASE_URL` | 8 | PostgreSQL connection string (postgresql://user:pass@host:port/db) |
| `redis` | `REDIS_URL` | 11 | Redis connection URL (e |
| `mongodb` | `MONGODB_API_KEY`; `MONGODB_DATA_SOURCE`; `MONGODB_DATA_API_URL` | 6 | MongoDB Atlas Data API key |
| `supabase` | `SUPABASE_ANON_KEY` | 7 | Supabase anon/public API key (also requires SUPABASE_URL env var) |
| `snowflake` | `SNOWFLAKE_ACCOUNT`; `SNOWFLAKE_TOKEN` | 3 | Snowflake account identifier (e |
| `bigquery` | `GOOGLE_APPLICATION_CREDENTIALS`; `BIGQUERY_PROJECT_ID` | 2 | Path to Google Cloud service account JSON file for BigQuery access |
| `redshift` | `AWS_ACCESS_KEY_ID`; `AWS_SECRET_ACCESS_KEY` | 5 | AWS Access Key ID for Redshift Data API access |
| `databricks` | `DATABRICKS_TOKEN` | 8 | Databricks personal access token (also requires DATABRICKS_HOST env var) |
| `azure_sql` | `AZURE_SQL_ACCESS_TOKEN`; `AZURE_SUBSCRIPTION_ID` | 5 | Azure Bearer token for SQL management API (scope: management |
| `kafka` | `KAFKA_CLUSTER_ID`; `KAFKA_REST_URL` | 6 | Kafka cluster ID |
| `aws_s3` | `AWS_ACCESS_KEY_ID`; `AWS_SECRET_ACCESS_KEY` | 8 | AWS Access Key ID for S3 API access |
| `cloudinary` | `CLOUDINARY_CLOUD_NAME`; `CLOUDINARY_API_KEY`; `CLOUDINARY_API_SECRET` | 8 | Cloudinary cloud name from your dashboard |
| `airtable` | `AIRTABLE_PAT` | 9 | Airtable Personal Access Token |

## 4. 凭据型 SaaS API — 搜索 / 营销 / 内容

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `search` | `BRAVE_SEARCH_API_KEY`; `EXA_API_KEY`; `GOOGLE_CSE_ID`; `GOOGLE_API_KEY` | 1 | API key for Brave Search |
| `serpapi` | `SERPAPI_API_KEY` | 8 | API key for SerpAPI (Google Scholar & Patents) |
| `similarweb` | `SIMILARWEB_API_KEY` | 29 | API key for SimilarWeb traffic and competitor insights |
| `google_analytics` | `GOOGLE_APPLICATION_CREDENTIALS` | 7 | Path to Google Cloud service account JSON key with Analytics read access |
| `google_search_console` | `GOOGLE_SEARCH_CONSOLE_TOKEN` | 8 | Google OAuth2 access token with Search Console scope |
| `google_maps` | `GOOGLE_MAPS_API_KEY` | 6 | API key for Google Maps Platform (Geocoding, Directions, Places) |
| `gcp_vision` | `GOOGLE_CLOUD_VISION_API_KEY` | 9 | Google Cloud Vision API key for image analysis |
| `reddit` | `REDDIT_CLIENT_ID`; `REDDIT_CLIENT_SECRET` | 7 | Reddit app client ID for OAuth2 authentication |
| `twitter` | `X_BEARER_TOKEN` | 7 | X/Twitter API v2 Bearer Token (app-only, read access) |
| `x` | `X_ACCESS_TOKEN`; `X_ACCESS_TOKEN_SECRET`; `X_API_KEY`; `X_API_SECRET` | 6 | X (Twitter) User Access Token for OAuth 1 |
| `youtube` | `YOUTUBE_API_KEY` | 8 | Google API key with YouTube Data API v3 enabled |
| `news` | `FINLIGHT_API_KEY`; `NEWSDATA_API_KEY` | 1 | API key for Finlight news sentiment analysis |
| `apify` | `APIFY_API_TOKEN` | 6 | Apify API token for running web scraping actors and retrieving datasets |

## 4. 凭据型 SaaS API — 电商 / 财务 / 运营

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `stripe` | `STRIPE_API_KEY` | 54 | Stripe Secret API Key for authenticating all API requests |
| `razorpay` | `RAZORPAY_API_KEY`; `RAZORPAY_API_SECRET` | 6 | Razorpay API Key ID (used with API Secret for HTTP Basic auth) |
| `shopify` | `SHOPIFY_ACCESS_TOKEN`; `SHOPIFY_STORE_NAME` | 9 | Shopify Admin API access token (starts with shpat_) |
| `quickbooks` | `QUICKBOOKS_REALM_ID`; `QUICKBOOKS_ACCESS_TOKEN` | 8 | QuickBooks company (realm) ID |
| `plaid` | `PLAID_CLIENT_ID`; `PLAID_SECRET` | 6 | Plaid client ID for banking data access (also set PLAID_SECRET and optionally PL |
| `powerbi` | `POWERBI_ACCESS_TOKEN` | 5 | Power BI OAuth2 access token for API access |
| `sap` | `SAP_BASE_URL`; `SAP_PASSWORD`; `SAP_USERNAME` | 5 | SAP S/4HANA Cloud base URL (e |

## 4. 凭据型 SaaS API — 邮件 / 消息通道

| 模块 | 凭据（环境变量） | 工具数 | 用途（首句） |
| --- | --- | ---: | --- |
| `email` | `GOOGLE_ACCESS_TOKEN`; `RESEND_API_KEY` | 41 | Google OAuth2 access token (via Aden) - used for Gmail, Calendar, Sheets, and Do |
| `twilio` | `TWILIO_ACCOUNT_SID`; `TWILIO_AUTH_TOKEN` | 7 | Twilio Account SID (starts with AC) |
| `pushover` | `PUSHOVER_API_TOKEN` | 7 | Pushover application API token |
| `brevo` | `BREVO_API_KEY` | 9 | Brevo API key for transactional email, SMS, and contact management |

## 5. 无凭据固定 API（默认可用，仍需出网）

| 模块 | 端点 | 用途 |
| --- | --- | --- |
| `arxiv` | `https://export.arxiv.org` | 论文检索与下载 |
| `duckduckgo` | `https://duckduckgo.com` | 网页/新闻/图片搜索 |
| `search_wikipedia` | `https://{lang}.wikipedia.org` | 百科摘要 |
| `openmeteo` | `https://api.open-meteo.com` | 天气实况/预报 |
| `yahoo_finance` | `https://query1.finance.yahoo.com` | 行情/财报/搜索 |
| `youtube_transcript` | `https://www.youtube.com` | 视频转录 |

另有一个**任意 URL 抓取/抓站**工具：目标由模型决定，出网白名单只能在网络层做。

## 6. 网络扫描器（默认关闭）

`dns_security_scan`、`port_scan`、`ssl_tls_scan`、`http_headers_scan` 会对目标域名/IP 主动探测。
开关：`HIVE_NETWORK_SCANNERS=false`（默认）/ `hive.config.json: network_scanners`。关闭时不注册这 4 个工具。

## 7. 邮件与通知通道

- **SMTP 发送**：`internal/senders`，按主机映射 SendGrid / Gmail / Office365 / Yahoo / Zoho / 通用 SMTP；凭据为发件账号（非 118 条清单内的 SaaS token）。
- **IMAP 读取**：按 SMTP 主机映射到对应 IMAP 主机（如 `imap.gmail.com`）。
- **Telegram**：`https://api.telegram.org/bot{token}`，用于 Sentinel 升级/通知，`TELEGRAM_BOT_TOKEN`。
- **Slack / Discord / Mattermost**：凭据型模块，用于发消息与读频道。
- **Pushover / Twilio / Brevo / Resend**：推送、短信、事务邮件 API。

## 8. 验证状态（重要）

- 81 个需第三方凭据的模块：本阶段**没有提供凭据，未做真实验证**；支持矩阵标“未验证”，不得当可用承诺。
- 已真实验证的：LLM（DeepSeek 兼容网关）、MCP（1 个服务器 2 工具）、26 个免凭据模块（离线/公网只读）、SMTP 单元测试（未对真实邮箱发信）。
- MiniMax 流式：上游 chunk 破坏解析，保持非流式回退，已测试锁定。

## 9. 本地/系统依赖（非外部服务，但部署必需）

| 依赖 | 用途 | 可替代性 |
| --- | --- | --- |
| 文件系统 `~/.hive/` | queens/colonies/sessions/credentials/soul/tracker 全部状态 | 可用 `HIVE_HOME` 改根 |
| SQLite（`modernc.org/sqlite`） | tracker、任务、凭据元数据 | 纯 Go，无 cgo；不可换 Postgres 作内建 |
| `HIVE_CREDENTIAL_KEY` | 凭据加密（Fernet/AES） | 必须持久化，否则已存凭据不可解 |
| 进程/端口 | `hive serve`、MCP 子进程、工具执行 | 可绑 127.0.0.1（默认）或对外 |
| Go 直接依赖 | `purego`、`go-isatty`、`yaml.v3`、`modernc.org/sqlite` | 均为构建期依赖，不是外部 API |
| 前端 | 构建期内嵌，无 CDN 运行时依赖 | 无 |

## 10. 维护方式

- 凭据清单变化时：`uv run --no-project python go_hive/scripts/gen_credential_specs.py` 重新生成 `specs_gen.go`，再更新本文表格。
- 端点变化时：改 `internal/llm/keycheck.go`、`internal/tools/integrations/*registry*.go`、`internal/senders/sender.go`，并同步本文件。
- 本文是人工维护的清单（非生成物）；架构层关系见 `docs/architecture/capability-tech-crosswalk.md` 的“工具与凭据边界”一行。
