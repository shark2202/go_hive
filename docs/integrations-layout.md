# integrations 包目录与 integrationkit 边界

> 状态：P1（纯改名）、P2（共享层试点）与领域子包试点（excel、devops、social）已落地。
> 行为、错误文案、对外符号与测试断言不变。
> 适用模块：`go_hive/go-hive`，包 `internal/tools/integrations` 与 `internal/tools/integrationkit`。

## 1. 目标与结论

`internal/tools/integrations` 原来按移植批次组织（`batch2_*` … `batch8_final_*`），
165 个文件、96,584 行，目录名无法说明一个文件负责什么。P1 把 145 个批次文件按领域
改成稳定名字；P2 把与 provider 无关的运行时、凭据、REST 引擎、参数解析和路径沙箱
抽到 `internal/tools/integrationkit`，`integrations` 通过类型别名和薄包装保持所有
调用点可读。

结果：

- `integrations` 中不再有 `batch*.go` / `lane*.go` 文件名。
- provider 文件不再直接访问 `integrationkit` 的未导出字段/方法。
- 新增依赖为 0；`integrationkit` 只依赖标准库。
- 九门禁全部通过（见 §6）。

## 2. 命名规则

目录保持扁平（`internal/tools/integrations`）。文件名表达职责，不表达移植批次。

| 文件类型 | 规则 | 示例 |
| --- | --- | --- |
| 单 provider 模块 | `<module>.go`，与上游模块名一致；测试 `<module>_test.go` | `jira.go`、`quickbooks.go`、`cloudflare.go` |
| 多模块领域注册表 | `<domain>.go`：该领域的 Registry 构造、模块配置与共享 helper | `data_platform.go`、`google_workspace.go` |
| 领域子包 | `<domain>/`：单领域 catalog + action + `register.go`，只 import `integrationkit` | `excel/`、`devops/`、`social/` |
| 共享 helper | 按用途命名，避免裸 `helpers.go` | `http_fetch.go`、`crm_common.go`、`pgwire.go` |
| 批次安装器 | 文件名跟领域，**导出符号不改**（`InstallBatch5Lane1` 等保持原样） | `atlassian_asana.go` 仍导出 `InstallBatch5Lane1` |
| 生成文件 | 保持 `catalog_gen.go`，由 `gen_catalog.py` 生成，不手改 | — |

约束：

- 不再新增带 `batch` / `lane` / `wave` 前缀的文件名；批次信息留在提交历史与
  `docs/CHANGELOG.md`。
- 新文件 ≤ 800 行（`internal/quality` 的 integrations 子树门禁）；超过时先拆模块
  或抽 helper，不新增 waiver。迁入子包时把 `batchN*`/`lane*` 标识符改成领域名。
- 领域注册表与单模块文件不要互相复制 HTTP 样板；先查 `integrationkit` 是否已有原语。

### 2.1 P1 改名清单（145 个文件）

按批次统计：batch2 17、batch3 10、batch4 4、batch5 35、batch6 45、batch7 27、batch8 7。

规则与例外：

- `batch2_*`：去前缀；`batch2_http.go → http_fetch.go`，`batch2_registry.go →
  research_registry.go`，`batch2_scanners_test.go → network_scanners_test.go`，
  `batch2_web_test.go → research_web_test.go`。
- `batch3_*`：去前缀；`batch3_search.go → web_search.go`。
- `batch4_*`：`linear.go` / `notion.go`。
- `batch5_lane1_*`：`jira.go`、`confluence.go`、`asana.go`，注册表
  `atlassian_asana.go`。
- `batch5_lane2_*`：`postgres.go`、`redis.go`、`supabase.go`、`mongodb.go`、
  `pgwire.go`，注册表 `databases.go`。
- `batch5_lane3_*`：`calendar.go`、`google_docs.go`、`gmail.go`、
  `google_sheets.go`，注册表 `google_workspace.go`。
- `batch5_lane4_*`：`hubspot.go`、`obsidian.go`，注册表 `hubspot_obsidian.go`。
- `batch6_lane1_*`：`salesforce.go`、`pipedrive.go`、`zoho_crm.go`、`attio.go`，
  注册表/共享 helper `crm_common.go`。
- `batch6_lane2_*`：`intercom.go`、`zendesk.go`、`freshdesk.go`、
  `greenhouse.go`，注册表 `support_ats.go`。
- `batch6_lane3_*`：`shopify.go`、`quickbooks.go`、`plaid.go`、`razorpay.go`，
  注册表 `finance_commerce.go`。
- `batch6_lane4_*`：`snowflake.go`、`bigquery.go`、`databricks.go`、
  `redshift.go`、`azure_sql.go`、`powerbi.go`、`sap.go`，注册表
  `data_platform.go`。
- `batch7_lane1..8`：`devops_hosting.go`、`cloudflare.go`、`infra_automation.go`、
  `ai_platform.go`、`enrichment_search.go`、`web_intelligence.go`、
  `social_media.go`、`scheduling_chat.go`（测试同名 `_test.go`）。
- `batch7_lane9_*`：`zoom.go`、`google_analytics.go`、`cloudinary.go`、
  `microsoft_graph.go`、`email.go`，注册表 `marketing_productivity.go`。
- `batch8_final_*`：`attach_file.go`、`subdomain.go`、`tech_stack.go`，注册表
  `extras_registry.go`。

同步更新：`internal/quality/size_test.go` 的 waiver 路径（10 条）、
`SECURITY_REVIEW.md`、`REMAINING_MODULES.md`、`docs/support-matrix.md` 中对旧文件名的引用。
所有测试函数名、导出符号、包名保持不变。

## 3. integrationkit 边界

依赖方向：

```
integrations  ──>  integrationkit  ──>  标准库
      │
      └─────────>  internal/tools
```

`integrationkit` 不知道 catalog、不知道 `builtinCatalog`、不导入 `integrations`，
也不导入 `internal/tools`。它只回答“一次外部调用需要什么”。

唯一的外部运行时依赖在 `integrations` 自身：`scrape_browser.go` 用
`go-rod/rod` + `go-rod/stealth`（纯 Go CDP，`CGO_ENABLED=0`）驱动系统
Chrome/Chromium 渲染 web_scrape 页面；它不进入 `integrationkit`，kit 仍只依赖标准库。

### 3.1 已在 kit 的内容

| 文件 | 内容 |
| --- | --- |
| `runtime.go` | `Runtime`、`AccountSource`、`Option`、`WithHTTPClient/WithBaseURL/WithCredential/WithRoots/WithClock/WithAccountSource/WithGetenv`、`ModuleConfig`、`HTTPClient` |
| `catalog.go` | `ActionFunc`、`Action`、`Tool`（`ActionNames/Schema/Description`）、`RestSpec`、`WithDefaults`、`Guard`、`JSONString` |
| `rest.go` | `restClient`、`CallREST`、`CallHelper`、路径占位符填充、query/form 编码、provider 错误映射、响应整形 |
| `schema.go` | `StringArg`、`IntArg`、`FloatArg`、`BoolArg`、`StringSliceArg` |
| `paths.go` | `Runtime.ResolveSafePath`、`DefaultRoots` |

关键访问器（provider 文件不再碰未导出字段）：

- `HTTPClient.Inner`：原 `httpClient.inner`。仍是 `*http.Client`，供扫描器 clone transport。
- `ModuleConfig.ConfiguredToken()`：原 `cfg.token`。
- `ModuleConfig.ResolvedToken(getenv)`：原 `cfg.resolveToken`。
- `Runtime.ResolveSafePath(raw)`：原 `rt.resolveSafePath`。
- `Runtime.CallREST` / `Runtime.CallHelper`：原 `callREST` / `callHelper`。

### 3.2 仍留在 integrations 的内容

- `builtinCatalog`（`catalog_gen.go`）与核心 6 个模块的 `moduleConfigs()`。
- 每个领域的 Registry 构造、`InstallBatchX` 安装器、模块专用配置表。
- 所有 provider action 实现与测试。
- `Registry` 容器本身（`runtime`、`tools`、`order`、`specs`、`impls` 及
  `New/Execute/Install`）。
- `kit_aliases.go`：`Runtime`/`Tool`/`Action`/`RestSpec`/`ModuleConfig`/`Option`
  等类型别名，以及 `stringArg`、`intArg`、`scalarString` 等历史小写 helper。
  这样 1,500+ 处 provider 调用点无需改动。

**为什么 Registry 没在 P2 一起搬**：24 个领域注册表直接写
`&Registry{runtime: rt, tools: ..., specs: ..., impls: ...}` 并直接给 `tools/order`
赋值；`rest_test.go` 还读取 `scoped.specs`。把 Registry 搬进 kit 需要同时增加
`NewRegistry/AddTool/AddSpec/AddImpl/HasSpec` 并把 24 个构造点改成方法调用，属于
行为无关但触点较多的 P3，单独提交更容易审。P2 已经消除了 provider 对未导出字段的
依赖，Registry 迁移不再被字段可见性阻塞。

## 4. P3：Registry 迁移（预估 1–2 人日）

步骤：

1. 在 `integrationkit` 增加 `Registry`，字段与当前一致；导出
   `NewRegistry(rt *Runtime)`、`AddTool(*Tool)`、`AddSpec(tool, action string, RestSpec)`、
   `AddImpl(tool, action string, ActionFunc)`、`HasSpec(tool, action string) bool`。
2. `Registry.Execute/Install/Names/Tools/Lookup/ActionCount/SortedNames/ImplementedActions`
   原样迁移；`Execute` 调 `Guard`/`WithDefaults`/`CallREST`，不再引用 integrations 包。
3. `integrations` 保留 `type Registry = integrationkit.Registry` 与 `New` 门面；
   `registerREST`/`registerLocal` 改用 `AddSpec`/`AddImpl`。
4. 24 个领域注册表把结构体字面量换成 `integrationkit.NewRegistry(rt)` + `AddTool`；
   `rest_test.go` 的 `scoped.specs[...]` 换成 `scoped.HasSpec(...)`。
5. 验证：`go test ./...`、`make ci`；确认 `Tool` 顺序、`ActionCount`、
   `ImplementedActions` 与迁移前一致。

风险：低–中。触点集中在注册表构造，不触碰 provider 行为；`Registry` 不导入
provider，不会形成循环依赖。

## 5. P4：超大文件拆分与可选子包化（预估 3–5 人日）

1. ✅ `devops_hosting.go` 已拆入 `devops/` 子包（2,270 行 → 8 个文件，见 §5.1）；
   ✅ `ai_platform.go` 已拆入 `ai/` 子包（2,846 行 → 9 个生产文件 + digest 测试，见 §5.2）。
   ✅ `social_media.go` 已拆入 `social/` 子包（2,443 行 → 8 个生产文件，见 §5.3）。
   ✅ `enrichment_search.go` 已拆入 `enrichment/` 子包（2,677 行 → 9 个文件，见 §5.4）。
   其余仍待拆：`cloudflare.go`（1,973 → zones / dns / security / workers）、
   `web_intelligence.go`、`infra_automation.go`、`scheduling_chat.go`。
2. 拆 `pdf.go`（解析器/文本抽取/表单）、`excel.go`（OOXML 读写/样式/工作表）、
   `scrape.go`（抓取/HTML 解析/SSRF 校验）。目标是移除 `size_test.go` 的 10 条
   waiver，而不是提高上限。
3. 领域子包化（可选）：`integrations/<domain>` 各自注册，`integrations` 只保留
   `New` 门面与 catalog 汇总。前置条件是 P3 完成；否则 Registry 仍在父包。
   （excel、devops 已按此模式落地。）
4. 给 kit 补独立测试：`fillPath`、`encodeForm`、`providerError`、`ResolvedToken`
   的边界用例，以及一个 `CallREST` 的 httptest 用例，降低对 integrations 测试
   套件的隐式覆盖依赖。

### 5.1 devops 子包落地（P2 领域拆分 lane `int-p2-devops`）

基线 `a955c3b6`。`devops_hosting.go`（2,270 行）整体下沉为
`internal/tools/integrations/devops/`，根包只保留薄入口
`integrations/devops_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `devops/doc.go` | 包注释：4 个模块的契约、刻意限制与迁移说明 | 42 |
| `devops/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名 | 17 |
| `devops/register.go` | `NewRegistry`/`Register`、4 个 `ModuleConfig` 与上限常量 | 80 |
| `devops/common.go` | 共享 helper：`doRequest`/`jsonRequest`/`httpReply`/JSON 访问器 | 297 |
| `devops/gitlab.go` | GitLab catalog + 9 个 action + `gitlabCall` | 561 |
| `devops/s3.go` | AWS S3 catalog + SigV4 签名 + 8 个 action | 643 |
| `devops/vercel.go` | Vercel catalog + 7 个 action | 343 |
| `devops/dockerhub.go` | Docker Hub catalog + 7 个 action | 383 |
| `devops/digest_test.go` | 四工具 catalog digest 冻结（迁移前基线） | 49 |
| `integrations/devops_install.go` | 根包薄入口：`InstallBatch7Lane1`/`newBatch7Lane1Registry` | 22 |

行数对照：`devops_hosting.go` 2,270 行 → 子包生产代码 2,366 行 + 根包入口 22 行
（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 4 个工具 / 31 个 action（gitlab 9、aws_s3 8、vercel 7、docker_hub 7）的
  name/description/input_schema 逐字节不变：迁移前后 `(name, description,
  schema)` JSON 的 sha256 均为
  `9787a660980bfae91aac3878d329826750f088b1017ffb60cdf0bdb9869828c5`
  （entries-only：`490e4e2d2d3ccb952f053e3106e8bed435b2f7cbfe4c2b74123fb7b4894529c4`）；
  子包 `digest_test.go` 冻结 entries-only 值。
- 根包 `InstallBatch7Lane1` 签名不变，改调 `devops.NewRegistry().Install(target)`；
  测试用 `newBatch7Lane1Registry` 转发 `devops.NewRegistry`，1,613 行的
  `devops_hosting_test.go` 未改动即全绿。
- 依赖方向：`integrations → devops → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane1*` 标识符全部改为领域名（`gitlabCall`、
  `s3SignRequest`、`dockerHubListTags` …），lane 前缀标识符 1,902 → 1,818。
- `internal/quality/size_test.go` 删除 `devops_hosting.go` 的 2,276 行 waiver；
  子包最大文件 643 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py --check` 全部通过。

### 5.2 ai 子包落地（P2 领域拆分 lane `int-p2-ai`）

基线 `63aacb15`。`ai_platform.go`（2,846 行）整体下沉为
`internal/tools/integrations/ai/`，根包只保留薄入口
`integrations/ai_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `ai/doc.go` | 包注释：5 个模块的契约、刻意限制与迁移说明 | 37 |
| `ai/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名 | 23 |
| `ai/register.go` | `NewRegistry`/`Register`、7 个 `ModuleConfig` 与上限常量 | 112 |
| `ai/common.go` | 共享 helper：`doRequest`/`httpReply`/JSON 访问器/数值读取，转发 integrationkit | 225 |
| `ai/pinecone.go` | Pinecone catalog + 9 个 action + 控制面/数据面 helper | 562 |
| `ai/wandb.go` | Weights & Biases catalog + GraphQL helper + 6 个 action | 490 |
| `ai/langfuse.go` | Langfuse catalog + 6 个 action | 462 |
| `ai/huggingface.go` | Hugging Face catalog + Hub/Inference/Endpoints helper + 9 个 action | 590 |
| `ai/vision.go` | Google Cloud Vision catalog + 9 个 action | 510 |
| `ai/digest_test.go` | 五工具 catalog digest 冻结（迁移前基线） | 51 |
| `integrations/ai_install.go` | 根包薄入口：`InstallAIPlatform`/`newAIPlatformRegistry` | 26 |

行数对照：`ai_platform.go` 2,846 行 → 子包生产代码 3,011 行 + 根包入口 26 行

### 5.3 social 子包落地（P2 领域拆分 lane `int-p2-social`）

基线 `63aacb15`。`social_media.go`（2,443 行）整体下沉为
`internal/tools/integrations/social/`，根包只保留薄入口
`integrations/social_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `social/doc.go` | 包注释：5 个模块的契约、刻意限制与迁移说明 | 32 |
| `social/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 别名 + option/参数读取 helper | 60 |
| `social/register.go` | `NewRegistry`/`Register`、5 个 `ModuleConfig` 与上限/默认端点常量 | 93 |
| `social/common.go` | 共享 helper：`doRequest`/`providerRequest`/`providerResponse`/JSON 访问器 | 265 |
| `social/youtube.go` | YouTube catalog + 8 个 action + `humanizeDuration` | 533 |
| `social/reddit.go` | Reddit catalog + OAuth token 流程 + 7 个 action | 451 |
| `social/twitter.go` | Twitter/X catalog + 7 个 action | 398 |
| `social/twilio.go` | Twilio catalog + Basic-auth 请求路径 + 7 个 action | 383 |
| `social/pushover.go` | Pushover catalog + form/query token + 7 个 action | 390 |
| `social/digest_test.go` | 五工具 catalog surface digest 冻结（迁移前基线） | 62 |
| `social/social_media_test.go` | 原 `social_media_test.go` 随实现移动（断言不变，helper 改语义名） | 1,305 |
| `integrations/social_install.go` | 根包薄入口：`InstallBatch7Lane7` | 16 |

行数对照：`social_media.go` 2,443 行 → 子包生产代码 2,605 行 + 根包入口 16 行
（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 5 个工具 / 39 个 action（pinecone 9、wandb 6、langfuse 6、huggingface 9、
  vision 9）的 name/description/input_schema 逐字节不变：迁移前后探针导出的
  `{names, actions, entries}` JSON 的 sha256 均为
  `9d2617358b6e6707bdf38f2c56c24a6a1ac42b778c32cedbaa12b3bab581e0ae`
  （entries-only：`9a7b572bc04f7e1368fa327c8326923c241bd0cccdcbff131c419baac3b78571`）；
  子包 `digest_test.go` 冻结 entries-only 值。
- 根包入口改为语义名 `InstallAIPlatform`/`newAIPlatformRegistry`（原
  `InstallBatch7Lane4`/`newBatch7Lane4Registry`），签名与行为不变：CLI 装配表
  `session_tools.go` 同步函数引用，注册标签 `batch7-lane4` 保持不变；
  2,434 行的 `ai_platform_test.go`（71 个用例）仅随两处入口改名与一个常量引用
  （`ai.VisionMaxBytes`）调整，其余未动即全绿。
- 依赖方向：`integrations → ai → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane4*` 标识符全部改为领域名（`pineconeControl`/
  `pineconeData`、`wandbCall`、`langfuseCall`、`huggingFaceGet`、`visionCall` …），
  lane 前缀标识符 1,452 → 1,360（Δ−92）。
- `internal/quality/size_test.go` 删除 `ai_platform.go` 的 3,031 行 waiver，
  `lane_prefix_waiver.json` 的 limit 由 1,535 下调至实测值 1,360（只减不增）；
  子包最大文件 590 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test -count=1 ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py`（重生成 + `--check`）
  全部通过。
- 5 个工具 / 36 个 action（youtube 8、reddit 7、twitter 7、twilio 7、pushover 7）的
  name/description/input_schema 逐字节不变：迁移前后 `{names, actions, entries}` JSON
  均为 8,205 字节，sha256 均为
  `595c1719268b3027c4e2b8e3545e4a5857ed847dc68221ec8388a0819b674cf0`；
  子包 `digest_test.go` 冻结该值。
- 根包 `InstallBatch7Lane7` 签名不变，改调 `social.NewRegistry().Install(target)`。
  原 1,305 行测试套件随实现迁入子包（它断言未导出的 module ID 与
  `humanizeDuration`/`intValue`）；断言未改，12 个顶层测试全绿。
- 依赖方向：`integrations → social → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane7*` 标识符全部改为领域名（`youtubeGET`、
  `redditAccessToken`、`doRequest`、`humanizeDuration` …），lane 前缀标识符
  本 lane 单独测量 1,452 → 1,357（95 个标识符迁出并改名；与 ai lane 合并后以
  `internal/quality` 门禁实测为准）。
- `internal/quality/size_test.go` 删除 `social_media.go` 的 2,532 行 waiver；
  子包最大文件 533 行，低于 800 行门禁。

### 5.4 enrichment 子包落地（P2 领域拆分 lane `int-p2-enrichment`）

基线 `63aacb15`。`enrichment_search.go`（2,677 行）整体下沉为
`internal/tools/integrations/enrichment/`，根包只保留薄入口
`integrations/enrichment_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `enrichment/doc.go` | 包注释：5 个模块的契约、刻意限制与迁移说明 | 32 |
| `enrichment/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名 | 17 |
| `enrichment/register.go` | `NewRegistry`/`Register`、5 个 `ModuleConfig`、上限常量与 `RetrySleep` 钩子 | 90 |
| `enrichment/common.go` | 共享 helper：`doJSON`/`httpReply`、模块 URL 与凭据解析、JSON 访问器 | 190 |
| `enrichment/apify.go` | Apify catalog + 6 个 action + `apifyCall` | 404 |
| `enrichment/apollo.go` | Apollo.io catalog + 7 个 action + `apolloCall` | 577 |
| `enrichment/lusha.go` | Lusha catalog + 8 个 action + `lushaCall` | 463 |
| `enrichment/serpapi.go` | SerpAPI catalog + 8 个 action + `serpapiCall` | 569 |
| `enrichment/exa.go` | Exa catalog + 7 个 action + 429 重试 `exaCall` | 489 |
| `enrichment/digest_test.go` | 五工具 catalog digest 冻结（迁移前基线） | 68 |
| `integrations/enrichment_install.go` | 根包薄入口：`Batch7Lane5Install`/`batch7Lane5NewRegistry` | 22 |

行数对照：`enrichment_search.go` 2,677 行 → 子包生产代码 2,831 行 + 根包入口
22 行（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 5 个工具 / 36 个 action（apify 6、apollo 7、lusha 8、serpapi 8、exa_search 7）的
  name/description/input_schema 逐字节不变：迁移前后 `{names, actions, entries}`
  JSON 探针的 sha256 均为
  `8bd792d97cda598dc3902b9579ccec63e4292b0db4a9307a0ba42bd1c6f9e758`；
  子包 `digest_test.go` 冻结该值。
- 根包 `Batch7Lane5Install` 签名不变，改调 `enrichment.NewRegistry().Install(target)`；
  测试用 `batch7Lane5NewRegistry` 转发 `enrichment.NewRegistry`。两处测试随钩子/
  内部符号迁移调整：`enrichment_search_test.go` 的 Exa 重试钩子改指
  `enrichment.RetrySleep`（3 行），`tool_schema_p1_test.go` 的 exa schema 断言改走
  `batch7Lane5NewRegistry().Lookup("exa_search")`。
- 依赖方向：`integrations → enrichment → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane5*` 标识符全部改为领域名（`apifyCall`、`apolloCall`、
  `exaCall` …）；lane 前缀标识符 1,452 → 1,359。
- `internal/quality/size_test.go` 删除 `enrichment_search.go` 的 2,848 行 waiver，
  `lane_prefix_waiver.json` 上限随实测下调到 1,359；子包最大文件 577 行，低于
  800 行门禁。
- 验收：`go build ./...`、`go test ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py --check` 全部通过。

## 6. 验证

```bash
cd go_hive/go-hive
go test ./...        # 全绿
go vet ./...         # 0
gofmt -l .           # 空
make ci              # fmt-check vet test cross parity parity-targets wiring-check compat-check arch-check
```

九门禁（`make ci`）：`fmt-check`、`vet`、`test`、`cross`、`parity`、
`parity-targets`、`wiring-check`，全部退出码 0。

行为不变量：

- 未改任何 `Tool`/`Action` 名称、参数 schema、错误文案、响应整形或安装器符号。
- `rest.go` 引擎逻辑逐行迁移到 `integrationkit/rest.go`，仅把 `callREST`/
  `callHelper` 改为导出方法。
- 新增的 `module_configs.go` 是原 `rest.go` 中核心 6 模块配置的原样搬移。
