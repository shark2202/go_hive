# integrations 包结构与迁移计划

本文件是 `internal/tools/integrations` 的结构契约：目录职责、依赖规则、
领域 → 文件 → 工具名归属表、迁移状态与第二阶段计划。`doc.go` 是简版；
两者与代码一起评审。

基线：`main` 98120156（178 个 .go / 99,198 行 / 12 条 size waiver 全冻结）。
第一阶段（本分支 `integrations-structure-p1`）只做结构搬运，行为与工具目录
（name / description / schema）逐字节不变，由 catalog digest 测试锁定：

- 根包 core digest：`0b3e3852dca6eb9544887810b4992e2798d15598c2d7b84552ab5d686423c1dc`
- excel 子包 digest：`ed2bd007fac1995f837a7e11a4d1224315755ea79e3c40c6f1a9f7aa98aa6ff9`

安装面实测：26 个 installer 入口共发布 **112 个互不重名的工具**
（102 个 catalog 定义 + 10 个 spreadsheet 别名；`New()` 的 core 面为 10 tools / 165 actions）。

## 目录结构

```
internal/tools/
├── integrationkit/          # 共享内核（只依赖 stdlib + internal/tools）
│   ├── runtime.go           # Runtime / ActionFunc / ModuleConfig / Option
│   ├── registry.go          # Registry / NewRegistry / AddTool|AddSpecs|AddImpl / Execute / Install
│   ├── catalog.go           # Tool / Action / RestSpec / Pagination / WithDefaults / Guard
│   ├── schema.go            # StringArg / IntArg / FloatArg / BoolArg / StringSliceArg
│   ├── params.go            # Param / ParamDefault / ParamItems / ParamEnumDefault / MapArg
│   ├── httpx.go             # GetBytes/GetJSON/PostForm / Errorf / HTML 清理 / Number / Round
│   ├── files.go             # MapRows / FileExists / FileSize / HasSuffixFold
│   ├── paths.go             # ResolveSafePath / DefaultRoots
│   └── oauth.go             # OAuth2 refresh
└── integrations/            # 装配根 + 领域实现
    ├── catalog_gen.go       # 生成文件（gen_catalog.py；DO NOT EDIT）
    ├── integration.go       # New() 装配
    ├── kit_aliases.go       # 历史名→kit 的类型别名与薄转发
    ├── doc.go               # 包结构说明
    ├── <domain>.go          # 既有领域实现（第二阶段继续下沉）
    ├── excel/               # ✅ 试点领域子包
    │   ├── doc.go / kit.go / register.go
    │   ├── catalog.go / actions.go
    │   ├── xlsx_package.go / xlsx_styles.go / xlsx_sheet.go / xlsx_write.go
    │   └── excel_test.go / digest_test.go
    ├── image/               # ✅ 图像生成领域子包（w210/integrations-image）
    │   ├── doc.go / kit.go / catalog.go / deps.go / register.go
    │   ├── generate.go / postprocess.go / artifacts.go / references.go / ssrf.go
    │   └── image_test.go / digest_test.go
    ├── devops/              # ✅ DevOps/hosting 领域子包（int-p2-devops）
    │   ├── doc.go / kit.go / register.go / common.go
    │   ├── gitlab.go / s3.go / vercel.go / dockerhub.go
    │   └── digest_test.go
    ├── social/              # ✅ 社交媒体领域子包（int-p2-social）
    │   ├── doc.go / kit.go / register.go / common.go
    │   ├── youtube.go / reddit.go / twitter.go / twilio.go / pushover.go
    │   └── social_media_test.go / digest_test.go
    ├── enrichment/          # ✅ Enrichment/search 领域子包（int-p2-enrichment）
    │   ├── doc.go / kit.go / register.go / common.go
    │   ├── apify.go / apollo.go / lusha.go / serpapi.go / exa.go
    │   └── digest_test.go
    └── schedchat/           # ✅ 调度/聊天领域子包（int-p2-schedchat）
        ├── doc.go / kit.go / register.go / common.go
        ├── brevo.go / calcom.go / calendly.go / discord.go / mattermost.go
        └── digest_test.go
    └── infra/               # ✅ 基础设施自动化领域子包（int-p2-infra）
        ├── doc.go / kit.go / register.go / common.go
        ├── terraform.go / pagerduty.go / prometheus.go / kafka.go / n8n.go / tines.go
        └── digest_test.go
    └── webintel/               # ✅ Web-intelligence 领域子包（int-p2-webintel）
        ├── doc.go / kit.go / register.go / common.go
        ├── similarweb.go / news.go / google_maps.go
        └── web_intelligence_test.go / digest_test.go
    ├── gworkspace/             # ✅ Google Workspace 领域子包（int-split-p3）
    │   ├── doc.go / kit.go / common.go / shared.go / register 内建
    │   ├── google_workspace*.go / calendar.go / gmail.go / google_docs.go / google_sheets.go
    │   └── *_test.go / testkit_test.go / digest_test.go
    ├── data/                   # ✅ 数据库/数据平台领域子包（int-split-p3）
    │   ├── doc.go / kit.go / common.go / shared.go / register.go
    │   ├── databases.go / postgres.go / pgwire*.go / redis.go / mongodb.go / supabase.go
    │   ├── data_platform.go / snowflake.go / bigquery.go / databricks.go / redshift.go
    │   ├── azure_sql.go / powerbi.go / sap.go
    │   └── *_test.go / testkit_test.go / digest_test.go
    ├── collab/                 # ✅ 协作/知识库领域子包（int-split-p3）
    │   ├── doc.go / kit.go / common.go / shared.go / register.go
    │   ├── atlassian_asana.go / jira.go / confluence.go / asana.go
    │   ├── notion*.go / linear*.go
    │   └── *_test.go / digest_test.go
    ├── crm/                    # ✅ CRM/客户支持领域子包（int-split-p3）
    │   ├── doc.go / kit.go / common.go / shared.go / register.go
    │   ├── crm_common.go / salesforce.go / pipedrive.go / zoho_crm.go / attio.go
    │   ├── support_ats.go / intercom.go / zendesk.go / freshdesk*.go / greenhouse.go
    │   ├── hubspot_obsidian.go / hubspot.go / obsidian.go
    │   └── *_test.go / digest_test.go
    └── commerce/               # ✅ 电商/财务领域子包（int-split-p3）
        ├── doc.go / kit.go / common.go / shared.go
        ├── finance_commerce.go / shopify.go / quickbooks.go / plaid.go / razorpay.go
        └── *_test.go / digest_test.go
    └── scrape/                 # ✅ web-scrape 领域子包（w213/integrations-scrape）
        ├── doc.go / kit.go / deps.go / register.go
        ├── catalog.go / scrape.go / html.go / browser.go / shared.go
        └── *_test.go / digest_test.go
```

## 依赖规则

1. 领域子包的生产代码只 import `internal/tools/integrationkit`（+ stdlib）。禁止 import
   `integrations` 根包或 `internal/tools`——否则子包无法独立测试，且根→子包→根成环；
   测试文件可以 import `internal/tools`，用于对共享注册表做断言。
2. 根包负责装配：`New()` 构造 kernel registry，`InstallBatch*` 入口把子包/领域实现
   注册进 `tools.ToolRegistry`。历史入口名保持不变，CLI（`internal/cli`）不需要改。
3. 新增领域优先建成子包；根包只保留 catalog 生成物、装配代码与薄别名层。
4. `scrape.go`、`pdf.go` 归 web-scrape lane；本分支只做 kernel 适配所需的最小改动
   （pdf.go 的 Registry 构造改为 `NewRegistry`）。

## 内核下沉清单（第一阶段）

| 能力 | integrationkit | 根包兼容层 |
| --- | --- | --- |
| 注册表 / 分发 | `Registry`、`NewRegistry`、`AddTool`、`AddSpecs`、`AddImpl`、`Execute`、`Install` | `type Registry = integrationkit.Registry`；`NewRegistry` 薄转发 |
| 参数读取 | `StringArg` / `IntArg` / `FloatArg` / `BoolArg` / `StringSliceArg` / `MapArg` | `stringArg` 等薄转发（kit_aliases.go） |
| JSON schema | `Param` / `ParamDefault` / `ParamItems` / `ParamEnumDefault` / `ParamArrayOfObjects` | `param` 等薄转发（kit_aliases.go） |
| HTTP / 错误映射 | `GetBytes(WithHeaders)` / `GetJSON(Object)` / `PostForm` / `Errorf` / `Snippet` / `DecodeJSONObject` | `batch2Get*` 等薄转发（http_fetch.go） |
| 文件 helper | `MapRows` / `FileExists` / `FileSize` / `HasSuffixFold` / `DefaultRoots` | `mapRows` 等薄转发（csv_file.go） |
| HTML 清理 | `StripHTMLTags` / `UnescapeHTML` / `CleanHTMLText` / `EntityPattern` | `stripHTMLTags` 等薄转发 |
| 错误常量 | `ErrUnknownTool` / `ErrUnknownAction` | `var ErrUnknownTool = integrationkit.ErrUnknownTool` |

## 归属表（领域 → 文件 → 工具名）

`catalog_gen.go` 为生成文件，其余为手写领域文件；`excel/` 已迁入子包。
工具名来自源码里的 Tool 定义（catalog literal）；安装入口另见各 `Install*` 函数。

| 文件 | 工具名 |
| --- | --- |
| `ai_platform.go` | `huggingface`, `langfuse`, `pinecone`, `vision`, `wandb` |
| `asana.go` | `asana` |
| `attio.go` | `attio` |
| `azure_sql.go` | `azure_sql` |
| `bigquery.go` | `bigquery` |
| `catalog_gen.go（生成）` | `airtable`, `csv`, `get_account_info`, `get_current_time`, `github`, `risk_score`, `slack`, `stripe`, `telegram`, `trello` |
| `cloudflare.go` | `cloudflare` |
| `cloudinary.go` | `cloudinary` |
| `confluence.go` | `confluence` |
| `databricks.go` | `databricks` |
| `devops/（子包）` | `aws_s3`, `docker_hub`, `gitlab`, `vercel` |
| `email.go` | `email` |
| `enrichment/（子包）` | `apify`, `apollo`, `exa_search`, `lusha`, `serpapi` |
| `excel/catalog.go（子包）` | `excel` |
| `extras_registry.go` | `attach_file`, `subdomain_enumerate`, `tech_stack_detect` |
| `freshdesk.go` | `freshdesk` |
| `google_analytics.go` | `google_analytics`, `google_search_console` |
| `google_workspace.go` | `calendar`, `gmail`, `google_docs`, `google_sheets` |
| `greenhouse.go` | `greenhouse` |
| `hubspot.go` | `hubspot` |
| `image/（子包）` | `image_generate` |
| `infra/（子包）` | `kafka`, `n8n`, `pagerduty`, `prometheus`, `terraform`, `tines` |
| `intercom.go` | `intercom` |
| `jira.go` | `jira` |
| `linear.go` | `linear` |
| `microsoft_graph.go` | `microsoft_graph` |
| `mongodb.go` | `mongodb` |
| `notion.go` | `notion` |
| `obsidian.go` | `obsidian` |
| `pdf.go` | `pdf_read` |
| `pipedrive.go` | `pipedrive` |
| `plaid.go` | `plaid` |
| `postgres.go` | `postgres` |
| `powerbi.go` | `powerbi` |
| `quickbooks.go` | `quickbooks` |
| `razorpay.go` | `razorpay` |
| `redis.go` | `redis` |
| `redshift.go` | `redshift` |
| `research_registry.go` | `arxiv`, `dns_security_scan`, `duckduckgo`, `http_headers_scan`, `openmeteo`, `port_scan`, `search_wikipedia`, `ssl_tls_scan`, `yahoo_finance`, `youtube_transcript` |
| `salesforce.go` | `salesforce` |
| `sap.go` | `sap` |
| `schedchat/（子包）` | `brevo`, `calcom`, `calendly`, `discord`, `mattermost` |
| `scrape/（子包）` | `web_scrape` |
| `shopify.go` | `shopify` |
| `snowflake.go` | `snowflake` |
| `social/（子包）` | `pushover`, `reddit`, `twilio`, `twitter`, `youtube` |
| `supabase.go` | `supabase` |
| `webintel/（子包）` | `google_maps`, `news`, `similarweb` |
| `web_search.go` | `web_search` |
| `zendesk.go` | `zendesk` |
| `zoho_crm.go` | `zoho_crm` |
| `zoom.go` | `zoom` |

说明：`spreadsheet_aliases.go` 不在上表——它不定义新工具，而是在运行时把 csv/excel
的 10 个 action 发布成上游 per-tool 名字（csv_read… / excel_read…）。

## 迁移状态

| 领域 | 状态 | 备注 |
| --- | --- | --- |
| excel | ✅ 已迁入 `excel/`（本分支） | 6 actions；catalog digest 与迁移前一致；文件 ≤800 行 |
| image | ✅ 已迁入 `image/`（w210/integrations-image） | 1 tool / 1 action；`{names, actions, entries}` digest 与迁移前逐字节一致（sha 04c8c45d…，640 bytes）；文件 ≤800 行；根包 `image_install.go` 薄入口，`newBatch3MediaRegistry` 继续同时装配 image 与 web_search；测试随实现迁入子包 |
| cloudflare | ⏳ 第二阶段候选 #1 | 1973 行、73 个 lane 前缀 helper；结构最接近 excel |
| devops | ✅ 已迁入 `devops/`（int-p2-devops） | 4 tools / 31 actions；catalog digest 与迁移前逐字节一致；文件 ≤800 行；根包 `devops_install.go` 薄入口 |
| social | ✅ 已迁入 `social/`（int-p2-social） | 5 tools / 36 actions；`{names, actions, entries}` digest 与迁移前逐字节一致；文件 ≤800 行；根包 `social_install.go` 薄入口；测试随实现迁入子包 |
| ai | ✅ 已迁入 `ai/`（int-p2-ai） | 5 tools / 39 actions；catalog digest 与迁移前逐字节一致（sha 9d261735…）；文件 ≤800 行；根包 `ai_install.go` 薄入口 |
| enrichment | ✅ 已迁入 `enrichment/`（int-p2-enrichment） | 5 tools / 36 actions；catalog digest 与迁移前逐字节一致；文件 ≤800 行；根包 `enrichment_install.go` 薄入口 |
| infra | ✅ 已迁入 `infra/`（int-p2-infra） | 6 tools / 32 actions；`{names, actions, entries}` digest 与迁移前逐字节一致（sha aae7d29d…）；文件 ≤800 行；根包 `infra_install.go` 薄入口 |
| webintel | ✅ 已迁入 `webintel/`（int-p2-webintel） | 3 tools / 42 actions；`{names, actions, entries}` digest 与迁移前逐字节一致（sha 59b37c4c…）；文件 ≤800 行；根包 `webintel_install.go` 薄入口；测试随实现迁入子包 |
| schedchat | ✅ 已迁入 `schedchat/`（int-p2-schedchat） | 5 tools / 40 actions；`{names, actions, entries}` digest 与迁移前逐字节一致（sha b4f85f8c…）；文件 ≤800 行；根包 `schedchat_install.go` 薄入口 |
| scrape | ✅ 已迁入 `scrape/`（w213/integrations-scrape） | 1 tool / 1 action；`{names, actions, entries}` digest 与迁移前逐字节一致（sha 8d80e3b1…，1936 bytes）；文件 ≤800 行；根包 `pdf_install.go` 薄入口，`newBatch3Registry` 继续同时装配 pdf_read 与 web_scrape；测试随实现迁入子包 |
| 其余 40+ 领域文件 | ⏳ 第二阶段/第三阶段 | 按领域逐个迁出，根包只留装配 |
| integrationkit | ✅ 内核已就位 | 新增 registry/params/httpx/files；行为零变化 |

## 门禁与验证

| 门禁 | 位置 | 当前基线 |
| --- | --- | --- |
| lane 前缀预算 | `internal/quality/lane_prefix_test.go` + `lane_prefix_waiver.json` | 1908（brief 标称）→ 1906（冻结实测）→ 1902（excel 迁出后）→ 1818（devops 迁出后）→ 1357→1265（helpers+ai/social）→ 1172（三 lane）→ 1089→1014（infra/cloudflare）→ 957（webintel）→ **876**（八领域全部迁出后实测）→ 867（pdf 迁出）→ **222**（P3 五族迁出后实测，limit 222）；只减不增 |
| 行为冻结 | `make refactor-freeze`（compare wave15-base） | 契约文件 sha256 不变；不进 ci，重构 lane 必跑 |
| 证据审计锚点 | `go_hive/scripts/check_integrations_evidence.py`（qc C9） | `integrations-evidence-audit.json` 的 GA/GSC/GW/freshdesk 四处 auth 证据逐条校验：file 存在、line 在范围内、行内含预期 token（`googleJWT` / `GOOGLE_SEARCH_CONSOLE_TOKEN` / `Bearer` / `basicAuth`）；JSON 证据串必须与锚点登记表一致，拆分/改名再漂移即 FAIL；P2 拆包后已刷新（GA 96→94、72→70；GSC 623→127；GW 192→223；freshdesk 285 复核不变） |

本地验证：
```
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test -count=1 ./internal/tools/... ./internal/quality
uv run python ../scripts/check_integrations_evidence.py   # 证据锚点（--fix 只提示候选行）
make refactor-freeze
make cross && make arch-check
```

注意：`make test` 已改为带 `-count=1`（禁止缓存 PASS 顶替真实运行）。2026-11-21 的
全量实测露出 `internal/playbook` 的既有挂起（Python bridge，5 分钟超时 panic）——该问题
归 `playbook-bridge-hang` lane，与本次结构重构无关（`playbook`/`colony` 不 import
integrations 任何包）；`internal/colony` 的 playbook 迁移用例单跑 0.08s PASS，满载时
受挂起拖累偶发。其余全部门禁（fmt/vet/cross/parity/parity-targets/wiring/compat/arch）
在本地全绿。

## 第二阶段计划（留给后续 lane）

1. `devops_hosting` 已迁入 `devops/`（int-p2-devops），`ai_platform` 已迁入 `ai/`（int-p2-ai），`social_media` 已迁入
1. 全部八个领域文件已迁入领域子包：devops_hosting（devops/）、ai_platform（ai/）、social_media（social/）、
   enrichment_search（enrichment/）、cloudflare（cloudflare/）、infra_automation（infra/）、
   web_intelligence（webintel/）、scheduling_chat（schedchat/）；剩余 pdf/excel/scrape 的拆分按同模板继续；
   每个子包只依赖 integrationkit。
2. 消重 lane 前缀 helper：残留 `batch4AsString` 等收敛为 kit 的 `StringArg`/`Object`/`Number`/`Round`；
   每迁一个领域立即下调 lane prefix limit（当前实测与上限见上表）。
   （当前实测与 limit 见上表）。
3. 拆分 `integrationSizeWaivers` 里 7 个 800–1500 行文件与 fileWaivers 里的大文件，
   每拆完一个删除对应 waiver（只减不增）。
4. 根包最终只保留 `catalog_gen.go`、装配文件与别名层；别名层随调用点迁移逐步删除。
5. 生成器 `gen_catalog.py` 的产物路径与子包 catalog 对齐（如需跨包生成，第二阶段再定）。
6. 每阶段都跑 `make refactor-freeze` + catalog digest；行为面零变化是硬前提。


## 第三阶段（P3）：五族拆出（本分支 `int-split-p3`）

基线 `00d51e22`。目标：按集成族再拆 3–5 个子包，把根包压到 **<100 文件 / <40k 行**，
行为零变化（工具定义/描述/schema/输出不动）。

### 族清单（实测）

| 子包 | 迁出 lane | 文件（prod+test） | 迁出行数 | 工具 / action | 旧构造器 → 新入口 |
| --- | --- | --- | --- | --- | --- |
| `gworkspace/` | batch-5 lane-3 | 9 + 5 = 14 | 4,286 | 4 / 39 | `newBatch5Lane3Registry` → `NewRegistry` |
| `data/` | batch-5 lane-2 + batch-6 lane-4 | 16 + 13 = 29 | 10,004 | 11 / 65 | `newBatch5Lane2Registry` → `NewDatabasesRegistry`；`newBatch6Lane4Registry` → `NewPlatformRegistry` |
| `collab/` | batch-5 lane-1 + batch-4 | 14 + 6 = 20 | 6,772 | 5 / 60 | `newBatch5Lane1Registry` → `NewAtlassianRegistry`；`newBatch4Registry` → `NewNotionRegistry` |
| `crm/` | batch-6 lane-1 + lane-2 + batch-5 lane-4 | 16 + 12 = 28 | 11,327 | 10 / 109 | `newBatch6Lane1Registry` → `NewCRMRegistry`；`newBatch6Lane2Registry` → `NewSupportRegistry`；`newBatch5Lane4Registry` → `NewHubSpotObsidianRegistry` |
| `commerce/` | batch-6 lane-3 | 5 + 5 = 10 | 3,774 | 4 / 29 | `newBatch6Lane3Registry` → `NewRegistry` |
| 合计 | 9 lane | **101** | **36,163** | 34 / 302 | 9 个构造器 |

根包：**199 文件 / 72,652 行 → 99 文件 / 37,050 行**（实测 `ls *.go | wc -l`、
`cat *.go | wc -l`；两处均满足 <100 / <40k）。根包新增的装配/兼容面：
`p3_installs.go`（五族安装 facade 合一，18 个函数）、`test_helpers_test.go`
（跨族测试共用的 batch4*/batch5Lane4*/batch6* recorder 与 JSON helper、`notionVersion`、
`batch5Lane3TestRegistry`）；`account_routing.go` 并入 `oauth.go`，`gmail_shared.go`
（`gmailAuthError`）并入 `email.go`，用于压文件数。

### 导出面处理

1. **每 lane 一个构造器**：子包导出 `New<Lane>Registry(opts ...Option) *Registry`，
   以原构造器逐字节搬迁（tool 顺序、modules 映射、Option 语义不变）；多 lane 子包另导
   `NewRegistry` = 各 lane 并集（仅测试/诊断用）。
2. **根包保留历史名字**：`InstallBatch*` 与 `newBatch*Registry` 在 `p3_installs.go`
   继续存在并转发到子包，CLI（`internal/cli/session_tools.go`）与跨族测试无需改动。
3. **标识符改名**：五个子包内 `batch5Lane3*`/`batch5Lane2*`/`batch6Lane4*`/`batch5Lane1*`/
   `batch4*`/`batch6Lane1*`/`batch6Lane2*`/`batch5Lane4*`/`batch6Lane3*` 共 655 个
   prod 标识符改为领域名（如 `batch6Lane4SAPImpls` → `sapImpls`）；lane 前缀预算
   **867 → 222**（逐族实测 758/538/478/299/222，`lane_prefix_waiver.json` limit=222）。
4. **共享 helper 本地化**：`moduleToken`/`resolveAccountToken`/OAuth2 刷新声明/`notionOptional*`
   等跨族 helper 按需复制进各子包 `common.go`（只 import integrationkit；子包不 import
   根包/`internal/tools`，测试文件按既有约定可 import `internal/tools` 做注册表断言）。
5. **测试随实现迁出**：41 个 `*_test.go` 随族迁入；跨族的 account-alias / OAuth-refresh
   测试留在根包，其依赖的 recorder/JSON helper 在根包 `test_helpers_test.go` 等价保留。

### 回归证据

- **工具面 digest 逐字节不变**：迁移前在根包旧构造器上实测基线，迁移后由各子包
  `digest_test.go` 锁定（11 条 lane digest + 5 条族并集 digest）：
  gworkspace `5bb8117a…`；data `bcb63e53…`/`6814f1fc…`/`4a890430…`；
  collab `e2334c4d…`/`38f807aa…`/`74f76688…`；
  crm `9e97e7ae…`/`8d90a93d…`/`64a178d0…`/`fff012ee…`；commerce `f448b5a7…`。
- **门禁**：`gofmt` clean；`go vet ./...` clean；`go test -count=1 ./...`（见 lane 报告）；
  `merge_postcheck.sh` 四关 PASS（gofmt / wiring 21 文件 68 项 / C12 / arch）；
  `make parity` 5821/5821 注解；`make parity-targets` 6391 refs 0 missing；
  `make compat-check` 12 pass / 0 fail（含 C8 工具定义冻结、C11 CLI 外观 0 漂移、C12 273/273）；
  `tool-surface-check` 与 `tool-output-check` OK；`make refactor-freeze` OK（10 契约 sha 不变）；
  证据审计 `check_integrations_evidence.py` 8/8 anchors PASS（gws/freshdesk 锚点已刷新为
  `gworkspace/google_workspace.go:215`、`crm/freshdesk.go:285`）。
- **架构产物**：`architecture/model.yaml` 登记 5 包，`gen_arch.py` 重生成后 87 包 / 233 边，
  `arch-check` 一致。
- **规模门禁**：integrations 子树文件 ≤800 行、函数 ≤200 行全过（无需新增 waiver；
  `integrationSizeWaivers` 仍为空，`catalog_gen.go` 的生成物 waiver 不动）。

### 编译耗时（参考）

方法：`CGO_ENABLED=0 go build -a ./internal/tools/integrations/...`（强制重建含 stdlib），
3 次取中位；另测 `go test -c -o /dev/null ./internal/tools/integrations/`。前后同机同热缓存。
实测（3 次中位，同机、热缓存、`-a` 强制重建）：拆前 6.22s → 拆后 **5.88s**（−0.34s，−5.5%）；
`go test -c -o /dev/null ./internal/tools/integrations/`（根包测试编译）：拆前 0.50s → 拆后 **0.48s**。
单包构建收益有限是因为 `-a ./internal/tools/integrations/...` 仍编译全部 12 个相关包；
拆分的收益主要是**根包单包重建/测试编译面**（66 prod + 39 test → 60 prod + 39 test，且体量从
72,652 行降到 37,050 行）与并行/审阅边界，而不是全树冷构建。
