# 外部集成清单（按优先级）

> 生成：`uv run --no-project python` 扫描 `tools/src/aden_tools/tools/*`，action 数按 `@mcp.tool` 统计。
> 已实现 20 个模块 / 184 action；未实现 **84 个模块**。下表只列未实现项。

## P0 · 默认角色必备（每个 queen 的 default_tool_categories 里就有）

| 模块 | action | 需要的凭据 |
| --- | ---: | --- |
| `senders_tool` | 9 | 无（本地能力） |
| `excel_tool` | 7 | 无（本地能力） |
| `image_gen_tool` | 1 | `HIVE_API_KEY`, `HIVE_HOME`, `HIVE_LLM_BASE_URL`, `HIVE_STORAGE_PATH` |
| `web_search_tool` | 1 | `BRAVE_SEARCH_API_KEY`, `GOOGLE_API_KEY`, `GOOGLE_CSE_ID` |
| `pdf_read_tool` | 1 | 无（本地能力） |
| `web_scrape_tool` | 1 | `HIVE_HOME`, `HIVE_STORAGE_PATH` |
| `attach_file_tool` | 1 | `HIVE_STORAGE_PATH` |
| `file_system_toolkits` | 0 | 无（本地能力） |

**小计：8 模块 / 21 action**

## P1 · 单 token 即可接入的常用 SaaS（验证成本低）

| 模块 | action | 需要的凭据 |
| --- | ---: | --- |
| `linear_tool` | 21 | `LINEAR_API_KEY` |
| `hubspot_tool` | 15 | `HUBSPOT_ACCESS_TOKEN` |
| `notion_tool` | 13 | `NOTION_API_TOKEN` |
| `redis_tool` | 11 | `REDIS_URL` |
| `google_docs_tool` | 11 | `GOOGLE_ACCESS_TOKEN` |
| `google_sheets_tool` | 10 | `GOOGLE_ACCESS_TOKEN` |
| `gmail_tool` | 10 | `GOOGLE_ACCESS_TOKEN` |
| `jira_tool` | 9 | `JIRA_API_TOKEN`, `JIRA_DOMAIN`, `JIRA_EMAIL` |
| `asana_tool` | 9 | `ASANA_ACCESS_TOKEN` |
| `gitlab_tool` | 9 | `GITLAB_TOKEN`, `GITLAB_URL` |
| `confluence_tool` | 8 | `CONFLUENCE_API_TOKEN`, `CONFLUENCE_DOMAIN`, `CONFLUENCE_EMAIL` |
| `postgres_tool` | 8 | `DATABASE_URL` |
| `calendar_tool` | 8 | `GOOGLE_ACCESS_TOKEN` |
| `supabase_tool` | 7 | `SUPABASE_ANON_KEY`, `SUPABASE_URL` |
| `mongodb_tool` | 6 | `MONGODB_API_KEY`, `MONGODB_DATA_API_URL`, `MONGODB_DATA_SOURCE` |
| `obsidian_tool` | 6 | `OBSIDIAN_REST_API_KEY`, `OBSIDIAN_REST_BASE_URL` |

**小计：16 模块 / 161 action**

## P2 · 企业系统 / CRM / 财务 / BI

| 模块 | action | 需要的凭据 |
| --- | ---: | --- |
| `freshdesk_tool` | 17 | `FRESHDESK_API_KEY`, `FRESHDESK_DOMAIN` |
| `attio_tool` | 15 | `ATTIO_API_KEY` |
| `pipedrive_tool` | 13 | `PIPEDRIVE_API_TOKEN`, `PIPEDRIVE_DOMAIN` |
| `intercom_tool` | 11 | `INTERCOM_ACCESS_TOKEN` |
| `salesforce_tool` | 9 | `SALESFORCE_ACCESS_TOKEN`, `SALESFORCE_INSTANCE_URL` |
| `shopify_tool` | 9 | `SHOPIFY_ACCESS_TOKEN`, `SHOPIFY_STORE_NAME` |
| `greenhouse_tool` | 9 | `GREENHOUSE_API_TOKEN` |
| `zendesk_tool` | 8 | `ZENDESK_API_TOKEN`, `ZENDESK_EMAIL`, `ZENDESK_SUBDOMAIN` |
| `quickbooks_tool` | 8 | `QUICKBOOKS_ACCESS_TOKEN`, `QUICKBOOKS_REALM_ID`, `QUICKBOOKS_SANDBOX` |
| `databricks_tool` | 8 | `DATABRICKS_HOST`, `DATABRICKS_TOKEN` |
| `zoho_crm_tool` | 6 | `ZOHO_CRM_ACCESS_TOKEN`, `ZOHO_CRM_DOMAIN` |
| `plaid_tool` | 6 | `PLAID_CLIENT_ID`, `PLAID_ENV`, `PLAID_SECRET` |
| `razorpay_tool` | 6 | `RAZORPAY_API_KEY`, `RAZORPAY_API_SECRET` |
| `sap_tool` | 5 | `SAP_BASE_URL`, `SAP_PASSWORD`, `SAP_USERNAME` |
| `powerbi_tool` | 5 | `POWERBI_ACCESS_TOKEN` |
| `redshift_tool` | 5 | `AWS_ACCESS_KEY_ID`, `AWS_REGION`, `AWS_SECRET_ACCESS_KEY` |
| `azure_sql_tool` | 5 | `AZURE_SQL_ACCESS_TOKEN`, `AZURE_SUBSCRIPTION_ID` |
| `snowflake_tool` | 3 | `SNOWFLAKE_ACCOUNT`, `SNOWFLAKE_DATABASE`, `SNOWFLAKE_SCHEMA`, `SNOWFLAKE_TOKEN`, `SNOWFLAKE_TOKEN_TYPE`, `SNOWFLAKE_WAREHOUSE` |
| `bigquery_tool` | 2 | `BIGQUERY_PROJECT_ID`, `GOOGLE_APPLICATION_CREDENTIALS` |

**小计：19 模块 / 150 action**

## P3 · 基础设施 / 可观测 / 数据平台

| 模块 | action | 需要的凭据 |
| --- | ---: | --- |
| `cloudflare_tool` | 54 | `CLOUDFLARE_API_TOKEN` |
| `pinecone_tool` | 9 | `PINECONE_API_KEY` |
| `huggingface_tool` | 9 | `HUGGINGFACE_TOKEN` |
| `vision_tool` | 9 | `GOOGLE_CLOUD_VISION_API_KEY` |
| `aws_s3_tool` | 8 | `AWS_ACCESS_KEY_ID`, `AWS_REGION`, `AWS_SECRET_ACCESS_KEY` |
| `pagerduty_tool` | 8 | `PAGERDUTY_API_KEY`, `PAGERDUTY_FROM_EMAIL` |
| `vercel_tool` | 7 | `VERCEL_TOKEN` |
| `docker_hub_tool` | 7 | `DOCKER_HUB_TOKEN`, `DOCKER_HUB_USERNAME` |
| `kafka_tool` | 6 | `KAFKA_API_KEY`, `KAFKA_API_SECRET`, `KAFKA_CLUSTER_ID`, `KAFKA_REST_URL` |
| `n8n_tool` | 6 | `N8N_API_KEY`, `N8N_BASE_URL` |
| `wandb_tool` | 6 | `WANDB_API_KEY` |
| `langfuse_tool` | 6 | `LANGFUSE_HOST`, `LANGFUSE_PUBLIC_KEY`, `LANGFUSE_SECRET_KEY` |
| `terraform_tool` | 5 | `TFC_TOKEN`, `TFC_URL` |
| `tines_tool` | 5 | `TINES_API_KEY`, `TINES_DOMAIN` |
| `prometheus_tool` | 2 | `PROMETHEUS_BASE_URL`, `PROMETHEUS_PASSWORD`, `PROMETHEUS_TOKEN`, `PROMETHEUS_USERNAME` |

**小计：15 模块 / 147 action**

## P4 · 数据获取 / 营销 / 通讯长尾

| 模块 | action | 需要的凭据 |
| --- | ---: | --- |
| `similarweb_tool` | 29 | `SIMILARWEB_API_KEY` |
| `microsoft_graph_tool` | 11 | `MICROSOFT_GRAPH_ACCESS_TOKEN` |
| `brevo_tool` | 9 | `BREVO_API_KEY` |
| `calcom_tool` | 9 | `CALCOM_API_KEY` |
| `zoom_tool` | 9 | `ZOOM_ACCESS_TOKEN` |
| `lusha_tool` | 8 | `LUSHA_API_KEY` |
| `serpapi_tool` | 8 | `SERPAPI_API_KEY` |
| `youtube_tool` | 8 | `YOUTUBE_API_KEY` |
| `calendly_tool` | 8 | `CALENDLY_PAT` |
| `google_search_console_tool` | 8 | `GOOGLE_SEARCH_CONSOLE_TOKEN` |
| `cloudinary_tool` | 8 | `CLOUDINARY_API_KEY`, `CLOUDINARY_API_SECRET`, `CLOUDINARY_CLOUD_NAME` |
| `apollo_tool` | 7 | `APOLLO_API_KEY` |
| `exa_search_tool` | 7 | `EXA_API_KEY` |
| `news_tool` | 7 | `FINLIGHT_API_KEY`, `NEWSDATA_API_KEY` |
| `reddit_tool` | 7 | `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET` |
| `twitter_tool` | 7 | `X_BEARER_TOKEN` |
| `twilio_tool` | 7 | `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN` |
| `pushover_tool` | 7 | `PUSHOVER_API_TOKEN` |
| `discord_tool` | 7 | `DISCORD_BOT_TOKEN` |
| `mattermost_tool` | 7 | `MATTERMOST_ACCESS_TOKEN`, `MATTERMOST_URL` |
| `google_analytics_tool` | 7 | `GOOGLE_APPLICATION_CREDENTIALS` |
| `apify_tool` | 6 | `APIFY_API_TOKEN` |
| `google_maps_tool` | 6 | `GOOGLE_MAPS_API_KEY` |
| `email_tool` | 2 | `EMAIL_FROM`, `EMAIL_OVERRIDE_TO`, `GOOGLE_ACCESS_TOKEN`, `RESEND_API_KEY` |
| `subdomain_enumerator` | 1 | 无（本地能力） |
| `tech_stack_detector` | 1 | 无（本地能力） |

**小计：26 模块 / 201 action**

## 汇总

| 优先级 | 模块 | action |
| --- | ---: | ---: |
| P0 | 8 | 21 |
| P1 | 16 | 161 |
| P2 | 19 | 150 |
| P3 | 15 | 147 |
| P4 | 26 | 201 |

## 不属于 aden_tools 的原生缺口（同样按 P0 处理）

| 工具 | 状态 | 需要的条件 |
| --- | --- | --- |
| `terminal_rg` `terminal_glob` `terminal_output_get` `terminal_job_logs` `terminal_job_manage` | 未移植 | 无（本地） |
| `terminal_job_start` `terminal_pty_open/run/close` | 未移植 | 无（本地，需 PTY） |
| `search_messages` `search_timeline` | 未移植 | 无（本地记忆检索） |
| `browser_setup` + `hive-browser` CLI | 未移植 | 本机浏览器 |
| `@server:chart-tools`（图表渲染） | 未移植 | 无（本地渲染） |
| `csv_sql` | 未移植 | DuckDB 等价物 |
