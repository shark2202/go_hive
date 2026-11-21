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
   ✅ `enrichment_search.go` 已拆入 `enrichment/` 子包（2,677 行 → 9 个文件，见 §5.4）；
   ✅ `cloudflare.go` 已拆入 `cloudflare/` 子包（1,901 行 → 21 个生产文件，见 §5.5）。
   ✅ `enrichment_search.go` 已拆入 `enrichment/` 子包（2,677 行 → 9 个文件，见 §5.4）。
   digest 测试，见 §5.7）。
   ✅ `scheduling_chat.go` 已拆入 `schedchat/` 子包（2,219 行 → 9 个生产文件 + digest 测试，见 §5.8）。
   其余仍待拆：`cloudflare.go`（1,973 → zones / dns / security / workers）、
   `web_intelligence.go`、`infra_automation.go`。
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

### 5.5 cloudflare 子包落地（P2 领域拆分 lane `int-p2-cloudflare`）

基线 `63d64352`。`cloudflare.go`（1,901 行）整体下沉为
`internal/tools/integrations/cloudflare/`，根包只保留薄入口
`integrations/cloudflare_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `cloudflare/doc.go` | 包注释：单工具 54 action 的契约、刻意限制与迁移说明 | 47 |
| `cloudflare/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名与 schema/参数原语 | 75 |
| `cloudflare/register.go` | `NewRegistry`/`Register`、`ModuleID`/上限常量与 `modules()` | 61 |
| `cloudflare/catalog.go` | `actionSpec`、按面上游序拼接的 `actionCatalog()`、`newTool()` | 68 |
| `cloudflare/catalog_zones.go` | infrastructure & zones 9 行 | 94 |
| `cloudflare/catalog_dns.go` | DNS management 9 行 | 110 |
| `cloudflare/catalog_analytics.go` | analytics & monitoring 6 行 | 63 |
| `cloudflare/catalog_security.go` | security & firewall 10 行 | 106 |
| `cloudflare/catalog_performance.go` | performance & speed 7 行 | 76 |
| `cloudflare/catalog_accounts.go` | account level 6 行 | 72 |
| `cloudflare/catalog_services.go` | advanced services & routing 7 行 | 91 |
| `cloudflare/dispatch.go` | 声明式分派、路径填充、zone/domain 校验 | 139 |
| `cloudflare/wire.go` | `apiReply`、认证请求路径与 `_make_request` 错误映射 | 193 |
| `cloudflare/accessors.go` | 解码 JSON 访问器 | 121 |
| `cloudflare/zones.go` | zones 面：zone 整形、设置列表、SSL 证书对 | 120 |
| `cloudflare/dns.go` | DNS 面：record 整形 CRUD、域名健康检查 | 273 |
| `cloudflare/analytics.go` | analytics 面：dashboard 归一化与各报表整形 | 68 |
| `cloudflare/security.go` | security 面：firewall 事件/规则、安全设置摘要 | 123 |
| `cloudflare/settings.go` | settings 面：`/settings/<name>` 顺序探针 | 100 |
| `cloudflare/workers.go` | Workers/高级服务面：Workers+LB、SSL-for-SaaS、Zero Trust | 53 |
| `cloudflare/accounts.go` | accounts 面：账户列表/详情与审计日志 | 87 |
| `cloudflare/digest_test.go` | 工具 catalog digest 冻结（迁移前基线） | 49 |
| `integrations/cloudflare_install.go` | 根包薄入口：`InstallBatch7Lane2`/`newCloudflareRegistry` | 22 |

行数对照：`cloudflare.go` 1,901 行 → 子包生产代码 2,140 行 + 根包入口 22 行
（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）；
最大文件 `dns.go` 273 行，低于 800 行门禁。

行为与工具面：

- 1 个工具 / 54 个 action 的 name/description/input_schema 逐字节不变：
  迁移前后探针导出的 `{names, actions, entries}` JSON 均为 11,125 字节，
  sha256 均为 `c7d8d14cfb11a37d3e700a9e542632dc7b354ba6656c6614d993eae0f5db5df6`
  （entries-only：`253a24839d0505257a8b2f5cc608558ac92b1e40f011bdee06c1f84ff024d8fa`）；
  子包 `digest_test.go` 冻结 entries-only 值。
- 根包 `InstallBatch7Lane2` 签名不变，改调 `cloudflare.NewRegistry().Install(target)`；
  1,293 行的 `cloudflare_test.go`（32 个顶层用例；314 处 lane 测试夹具按 devops
  先例保留）只改 8 处生产符号引用（`batch7Lane2NewRegistry`→
  `newCloudflareRegistry`、`batch7Lane2ModuleID`→`cloudflare.ModuleID`）加 1 行
  import，断言未动即全绿。
- 依赖方向：`integrations → cloudflare → integrationkit`；子包不 import 根包或
  `internal/tools`（`ModuleID` 是唯一新增导出常量，供根测试断言模块键）。
  `batch7Lane2*` 标识符在非测试代码中 75 → 0，lane 前缀标识符门禁实测
  1172 → 1097（Δ−75），`lane_prefix_waiver.json` 的 limit 随实测下调至 1097。
- `internal/quality/size_test.go` 删除 `cloudflare.go` 的 1,973 行 waiver。
- 验收：`go build ./...`、`go test -count=1 ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`（既有 allowlist，未新增）、
  `gen_arch.py`（重生成 + `--check`，78 包 / 206 边）全部通过。

### 5.6 infra 子包落地（P2 领域拆分 lane `int-p2-infra`）

`internal/tools/integrations/infra/`，根包只保留薄入口
`integrations/infra_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `infra/doc.go` | 包注释：6 个模块的契约、刻意限制与迁移说明 | 45 |
| `infra/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名与参数读取转发 | 57 |
| `infra/register.go` | `NewRegistry`/`Register`、6 个 `ModuleConfig` 与上限常量 | 89 |
| `infra/common.go` | 共享 helper：`endpoint`/`send`/`doJSON`/`getJSON`/JSON 访问器与 kit seam | 229 |
| `infra/terraform.go` | Terraform catalog + 5 个 action + JSON:API helper | 305 |
| `infra/pagerduty.go` | PagerDuty catalog + 8 个 action | 441 |
| `infra/prometheus.go` | Prometheus catalog + 2 个 action + 上游错误分流 | 187 |
| `infra/kafka.go` | Kafka catalog + 6 个 action | 271 |
| `infra/n8n.go` | n8n catalog + 6 个 action | 351 |
| `infra/tines.go` | Tines catalog + 5 个 action | 266 |
| `infra/digest_test.go` | 六工具 catalog digest 冻结（迁移前基线） | 67 |
| `integrations/infra_install.go` | 根包薄入口：`InstallBatch7Lane3`/`newBatch7Lane3Registry` | 23 |

（新增文件头、包注释、import 块与 kit seam 薄转发；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 6 个工具 / 32 个 action（terraform 5、pagerduty 8、prometheus 2、kafka 6、
  n8n 6、tines 5）的 name/description/input_schema 逐字节不变：迁移前后
  `{names, actions, entries}` JSON 探针均为 8,354 字节，sha256 均为
  `aae7d29de914af0febe69a71d367335d529194e3955c42ebbfbfe2826934aace`；
  子包 `digest_test.go` 冻结该值。
- 根包 `InstallBatch7Lane3` 签名不变，改调 `infra.NewRegistry().Install(target)`；
  测试用 `newBatch7Lane3Registry` 转发 `infra.NewRegistry`。1,487 行的
  `infra_automation_test.go` 随内部符号迁移调整一处：本地 `infraInt` 替代随实现
  迁出的数值字段读取 helper（16 处调用点），断言未改。
- 依赖方向：`integrations → infra → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane3*` 标识符（83 个）全部改为领域名（`terraformTool`、
  `pagerDutyHeaders`、`prometheusRun`、`kafkaTopic`、`n8nCall`、`tinesConfig` …）；
  lane 前缀标识符 1,172 → 1,089（Δ−83），`lane_prefix_waiver.json` 上限随实测
  下调到 1,089。
  子包最大文件 441 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test -count=1 ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py`（重生成 + `--check`）
  全部通过。

### 5.7 webintel 子包落地（P2 领域拆分 lane `int-p2-webintel`）

`internal/tools/integrations/webintel/`，根包只保留薄入口
`integrations/webintel_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `webintel/doc.go` | 包注释：3 个模块的契约、刻意限制与迁移说明 | 33 |
| `webintel/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 别名 + option/参数读取 helper | 76 |
| `webintel/register.go` | `NewRegistry`/`Register`、4 个 `ModuleConfig`、上限常量与 `backoffSleep` 钩子 | 88 |
| `webintel/common.go` | 共享 helper：`doHTTP`/`httpResponse`/`joinQuery`、模块 URL 与凭据解析、JSON 访问器 | 158 |
| `webintel/similarweb.go` | SimilarWeb V5 catalog + 29 个 action + 表驱动 executor | 346 |
| `webintel/news.go` | News catalog + 7 个 action + NewsData/Finlight 双 provider 回退与 429 重试 | 669 |
| `webintel/google_maps.go` | Google Maps catalog + 6 个 action + API 状态映射 | 524 |
| `webintel/digest_test.go` | 三工具 `{names, actions, entries}` digest 冻结（迁移前基线） | 62 |
| `webintel/web_intelligence_test.go` | 原 `web_intelligence_test.go` 随实现移动（断言不变，helper 改语义名） | 1,262 |
| `integrations/webintel_install.go` | 根包薄入口：`InstallBatch7Lane6`/`newBatch7Lane6Registry` | 22 |

（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 3 个工具 / 42 个 action（similarweb 29、news 7、google_maps 6）的
  name/description/input_schema 逐字节不变：迁移前后 `{names, actions, entries}`
  JSON 探针的 sha256 均为
  `59b37c4c9b11c503510cfce9748f62f4729eb754cc2d458afdd375c1461dc132`（9,674 字节）；
  子包 `digest_test.go` 冻结该值。
- 根包 `InstallBatch7Lane6` 签名不变，改调 `webintel.NewRegistry().Install(target)`；
  测试用 `newBatch7Lane6Registry` 转发 `webintel.NewRegistry`。1,262 行的
  `web_intelligence_test.go` 随实现迁入子包：断言未改，测试函数与测试 helper 改
  语义名（它断言 unexported 的 endpoint 表、backoff 钩子与 module ID），20 个
  顶层测试全绿。
- 依赖方向：`integrations → webintel → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane6*` 标识符全部改为领域名（`swExecute`、`newsRetry`、
  `mapsGet`、`backoffSleep` …），lane 前缀标识符 1,172 → 1,115（Δ−57）。
  `lane_prefix_waiver.json` 的 limit 由 1,172 下调至实测值 1,115（只减不增）；
  子包最大文件 669 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test -count=1 ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py`（重生成 78 包 / 206 边，
  再 `--check`）全部通过。

### 5.8 schedchat 子包落地（P2 领域拆分 lane `int-p2-schedchat`）

基线 `63d64352`。`scheduling_chat.go`（2,219 行）整体下沉为
`internal/tools/integrations/schedchat/`，根包只保留薄入口
`integrations/schedchat_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `schedchat/doc.go` | 包注释：5 个模块的契约、刻意限制与迁移说明 | 50 |
| `schedchat/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option`/`ModuleConfig` 等 kit 别名 | 17 |
| `schedchat/register.go` | `NewRegistry`/`Register`、5 个 `ModuleConfig`、上限常量与 `RetrySleep` 钩子 | 96 |
| `schedchat/common.go` | 共享 helper：`sendRequest`/`httpReply`/429 重试、凭据解析与 JSON 访问器 | 567 |
| `schedchat/brevo.go` | Brevo catalog + 9 个 action | 453 |
| `schedchat/calcom.go` | Cal.com catalog + 9 个 action | 326 |
| `schedchat/calendly.go` | Calendly catalog + 8 个 action | 384 |
| `schedchat/discord.go` | Discord catalog + 7 个 action | 270 |
| `schedchat/mattermost.go` | Mattermost catalog + 7 个 action | 262 |
| `schedchat/digest_test.go` | 五工具 catalog digest 冻结（迁移前基线） | 76 |
| `integrations/schedchat_install.go` | 根包薄入口：`InstallBatch7Lane8`/`newBatch7Lane8Registry` | 23 |

行数对照：`scheduling_chat.go` 2,219 行 → 子包生产代码 2,425 行 + 根包入口
23 行（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改）。

行为与工具面：

- 5 个工具 / 40 个 action（brevo 9、calcom 9、calendly 8、discord 7、
  mattermost 7）的 name/description/input_schema 逐字节不变：迁移前后
  `{names, actions, entries}` 探针均为 9,802 字节，sha256 均为
  `b4f85f8ce8485bb2cf8e94f879874b879413ebe388fcc5a7015547bf31b0ea9c`
  （names `83a16caa…`、actions `2db21b42…`、entries `4f031702…`）；
  子包 `digest_test.go` 冻结该值。
- 根包 `InstallBatch7Lane8` 签名不变，改调 `schedchat.NewRegistry().Install(target)`；
  测试用 `newBatch7Lane8Registry` 转发。1,789 行的 `scheduling_chat_test.go`
  仅两处调整：429 钩子改指 `schedchat.RetrySleep`，三个上限常量改指
  `schedchat.DiscordMaxMessage`/`MattermostMaxText`/`MaxRetries`；其余断言未改即全绿。
- 依赖方向：`integrations → schedchat → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch7Lane8*` 标识符全部改为领域名（`sendRequest`、
  `checkResponse`、`brevoSendEmail`、`mattermostCreateReaction` …），
  `account_routing.go` 的 lane-8 provider 映射随实现迁入子包；lane 前缀标识符
  1,172 → 1,091（Δ−81），`lane_prefix_waiver.json` 的 limit 随实测下调至 1,091。
- `internal/quality/size_test.go` 删除 `scheduling_chat.go` 的 2,255 行 waiver；
  子包最大文件 567 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/ ./internal/cli/`、
  `gen_tool_surface_flat.py --check`、`refactor_freeze.sh compare wave15-base`、
  `gen_arch.py --check` 全部通过。

### 5.9 pdf 子包落地（P2 领域拆分 lane `int-p2-pdf`）

基线 `45aa262e`。`pdf.go`（2,008 行）整体下沉为
`internal/tools/integrations/pdf/`，根包只保留薄入口
`integrations/pdf_install.go`。

| 新文件 | 内容 | 行数 |
| --- | --- | ---: |
| `pdf/doc.go` | 包注释：单工具 pdf_read 的契约、刻意限制与迁移说明 | 37 |
| `pdf/kit.go` | `Tool`/`Action`/`Runtime`/`Registry`/`Option` 等 kit 别名与 schema/参数转发 | 53 |
| `pdf/register.go` | `NewRegistry`/`Register`、上限常量、解析错误与 terminal hint | 59 |
| `pdf/pdf_read.go` | pdf_read catalog + action 实现 + 页范围解析 | 290 |
| `pdf/fetch.go` | 本地路径/URL 输入、下载、失败 envelope | 186 |
| `pdf/ssrf.go` | DNS 级 SSRF 守卫（与根包 scrape.go 同构，含重定向逐跳复核） | 84 |
| `pdf/parse.go` | 文档模型、xref 表/流、对象扫描、trailer/catalog 解析 | 477 |
| `pdf/streams.go` | FlateDecode / ASCIIHexDecode、PNG predictor、有界 inflate | 184 |
| `pdf/pages.go` | 页树遍历、页内容解码与文本抽取 | 84 |
| `pdf/info.go` | Info 字典投影 + 共享值/字符串 helper | 78 |
| `pdf/lexer.go` | PDF 词法：数字/字典/数组/字符串/名字/间接引用 | 280 |
| `pdf/text.go` | 内容流文本解释器（操作符/换行/字距） | 137 |
| `pdf/fonts.go` | ToUnicode CMap、WinAnsi 编码与 UTF-16 helper | 290 |
| `pdf/digest_test.go` | pdf_read catalog surface digest 冻结（迁移前基线） | 72 |
| `integrations/pdf_install.go` | 根包薄入口：`InstallBatch3Doc`/`newBatch3Registry`（`pdf.Register` + web_scrape） | 45 |

行数对照：`pdf.go` 2,008 行 → 子包生产代码 2,239 行 + 根包入口 45 行
（新增文件头、包注释与 import 块；工具定义的字符串字面量逐字节未改，工具
定义块在改名后与迁移前逐字节相同）；子包最大文件 `parse.go` 477 行，低于
800 行门禁。

行为与工具面：

- 1 个工具 / 1 个 action（pdf_read，7 个参数）的
  name/description/input_schema 逐字节不变：迁移前后 `{names, actions,
  entries}` JSON 探针均为 614 字节，sha256 均为
  `a973b88d8824c80ef1c16e91e415c86792715928c8bea55be377e988180a020e`；
  子包 `digest_test.go` 冻结该值（并把 614 字节作为回归断言）。
- 根包 `InstallBatch3Doc` 签名不变，仍注册 pdf_read + web_scrape、返回 2；
  `newBatch3Registry` 保留原 Runtime 构造与可注入的 scrape resolver /
  renderer，改为 `pdf.Register(registry)` 组合子包的 pdf 工具后再挂
  `web_scrape`。566 行的 `pdf_test.go` 与 scrape 用例未改动即全绿；
  `tool_output_golden_test.go` 的 3 个 pdf 场景改走
  `pdf.NewRegistry().Execute(...)`（行为等价），断言未改。
- 依赖方向：`integrations → pdf → integrationkit`；子包不 import 根包或
  `internal/tools`。`batch3PDF*` 标识符全部改为领域名（`pdfReadTool`、
  `pdfOptions`、`defaultMaxPages`、`hardMaxPages`、`terminalHint` …）；
  lane 前缀标识符 876 → 867（Δ−9），`lane_prefix_waiver.json` 的 limit 随
  实测下调至 867（只减不增）。
- `ssrf.go` 的 DNS 级守卫与根包 `scrape.go` 的实现同构（直接地址判定、
  DNS 解析复核、重定向逐跳复核），因为子包不能 import 根包且 `scrape.go`
  属 web-scrape lane；待 scrape 拆分后应把该守卫下沉 integrationkit 共用。
- `internal/quality/size_test.go` 删除 `pdf.go` 的 2,014 行 waiver；子包
  最大文件 477 行，低于 800 行门禁。
- 验收：`go build ./...`、`go test -count=1 ./internal/tools/integrations/...
  ./internal/server/ ./internal/quality/`、`gen_tool_surface_flat.py --check`、
  `refactor_freeze.sh compare wave15-base`、`gen_arch.py`（重生成 82 包 /
  214 边，再 `--check`）全部通过。

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
