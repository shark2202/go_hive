# go_hive integrations 证据审计（摘要）

- 生成：2026-11-21 05:47 CST（修订） · 基线 main `db5c4c1b` · worktree `.wt/integrations-account-wiring`
- 本次增量（2026-11-21）：`oauth-refresh-pagination` 车道补 OAuth2 refresh 与通用 REST 分页；`global_gaps[0..1]` 与 7 个模块的 `oauth_refresh`/`refresh`、2 个模块的 `pagination` 字段已更新，HTML 视图仍是 05:47 快照。
- 全量数据：`go_hive/docs/integrations-evidence-audit.json`；可视化：`go_hive/docs/integrations-evidence-audit.html`

## 结论

- **A 12 / B 91 / C 0 / D 1 / E 0**（104 行 = 102 模块工具 + senders + browser_setup；actions 864 = 854 + 9 + 1）。
- 结构完整性与上游对账：上游 103 模块 / 865 actions；Go 102 模块 / 854 actions + senders 9 = 863，唯一缺口是 **csv_sql / excel_sql**（DuckDB 依赖，有意排除）。impl/spec 覆盖 **854/854**。
- **没有模块是 E（无实现/无测试）**；也**没有任何真实第三方 API 调用测试**（0 个 t.Skip、0 个真实凭据读取、0 个外网目标）——联网模块最高只能到 B。

## 分级汇总

| 级别 | 数量 | 模块 |
| --- | --- | --- |
| A 真实可用已验证 | 12 | `attach_file`, `browser_setup`, `csv`, `excel`, `get_current_time`, `http_headers_scan`, `pdf_read`, `port_scan`, `risk_score`, `ssl_tls_scan`, `tech_stack_detect`, `web_scrape` |
| B 代码完整+假服务测试 | 91 | 见 JSON/HTML（91 个） |
| C 部分/未接线 | 0 | — |
| D 排除/EOL | 1 | `mongodb` |
| E 未验证 | 0 | — |

## 最严重缺口（按影响排序）

1. ~~**get_account_info（C）**~~ **已修复（C → B，2026-11-21 修订）**：`newSessionToolCatalogue` 现在把真实凭据库作为 `AccountSource` 挂到共享 registry（`tools.ToolRegistry.SetAccountSource` → `credentials.CredentialStoreAdapter`，读 `home/credentials` 加密库 + local alias 账户），`get_account_info` 无凭据返回 `{"accounts":[],"count":0}`；有凭据 / 多账号 / provider 过滤三组测试 + 假服务端到端覆盖。
2. ~~**OAuth 刷新缺失（全包 0 处）**~~ **已实现通用刷新（2026-11-21 修订）**：`integrationkit.TokenRefresher` 提供标准 OAuth2 refresh_token 授权（POST form `grant_type=refresh_token`，过期前 5 分钟刷新，按 endpoint+client+refresh 缓存并并发去重，刷新失败返回 `{error, error_code, help}`，401 → 刷新 → 重试一次）。已接入 7 个模块：`calendar` / `gmail` / `google_docs` / `google_sheets` / `email`（Gmail sender）共用 `GOOGLE_*` 凭据，`hubspot` 用 `HUBSPOT_*`，`zoho_crm` 用 `ZOHO_*`；凭据库经 env sync 额外导出 client_id/client_secret/refresh_token/access_token 过期时间。仍未接入：只声明静态 access token 的 `salesforce` / `microsoft_graph` / `powerbi` / `quickbooks` / `zoom` / `google_search_console` 等条目（上游规格无 client_id/secret/refresh_token），以及 account 别名单 token。
3. **mongodb（D）**：依赖的 MongoDB Atlas Data API 2025-09 EOL，只有指向兼容替代（Delbridge/RESTHeart）时可用。
4. ~~**通用客户端无分页**~~ **已实现声明式分页（2026-11-21 修订）**：`RestSpec.Pagination` 支持 `items_path` / `next_cursor_path` / `cursor_param` / `max_pages`（默认 10 页，空/重复光标即停，合并后清除 next_cursor）；已接线 `airtable_list_records`（offset）与 `slack_list_channels` / `slack_list_users`（`response_metadata.next_cursor`）。残余：github（Link 头）、stripe（starting_after=末条 id）、zoom/gmail/google_docs/google_maps/salesforce（透传）与 bigquery（自研循环）未改为声明式；其余列表 action 单页。
5. **无 multipart 文件上传**：slack 用 external upload URL + PUT 文本、graph 4MB simple PUT、cloudinary 只收 URL、s3 put 只收文本。
6. ~~**多账号 alias 未复刻**~~ **已复刻（2026-11-21 修订）**：hubspot / notion / discord / mattermost / google docs·gmail·calendar 的 `account` 经同一凭据视图解析（`get_by_alias` 语义：别名优先、未知别名返回该工具 auth 错误）。仍不支持：`obsidian`（上游无 account 参数）、`google_sheets`（account 为框架 tracking 参数，工具忽略）、support/ATS 组 `intercom/zendesk/freshdesk/greenhouse`（上游无 account 参数；多值 bundle 仍按 env/单条存储）。
7. **senders ESP 不投递**：`internal/senders` 9 actions feature-gated（HIVE_EMAIL_SENDERS 默认关），仅 SMTP 真实投递。

## A 类（真实可用已验证，12）

| 模块 | 实测证据 |
| --- | --- |
| `attach_file` | TestBatch8AttachFileSinglePDF, TestBatch8AttachFileImageBlockOverCap, TestBatch8AttachFileBlockNoteNamesThePath, TestBatch8AttachFileSinglePNG, TestBatch8AttachFileJPGExtensions |
| `browser_setup` | TestBrowserSetupReturnsDiscoveryEnvelope, TestBrowserSetupRecordsBrowserGroup, TestInstallBrowserSetupIsIdempotent |
| `csv` | TestCSVWriteReadAppendInfo, TestCSVErrorsAndSandbox, TestSpreadsheetAliasesExposeUpstreamNames, TestSpreadsheetAliasesReuseCSVActions, TestSpreadsheetAliasesKeepErrorEnvelopes |
| `excel` | TestBatch3ExcelReadBasic, TestBatch3ExcelReadSpecificSheet, TestBatch3ExcelReadMissingSheet, TestBatch3ExcelReadLimitAndOffset, TestBatch3ExcelReadOffsetBeyondRows |
| `get_current_time` | TestInstallIntoSharedRegistry, TestSingleActionToolDispatch, TestTimeToolFields, TestBatch2RegistryIsolation |
| `http_headers_scan` | TestBatch2HTTPHeadersScan, TestBatch2HTTPHeadersAutoPrefix, TestBatch2HTTPHeadersLeakyAndMissing, TestBatch2CatalogIntegrity, TestInstallBatch2 |
| `pdf_read` | TestBatch3PDFReadLocalTextPDF, TestBatch3PDFReadPageRangeTruncation, TestBatch3PDFReadPageSpecForms, TestBatch3PDFReadLongPageNoVisionFlag, TestBatch3PDFReadScannedReturnsExplicitError |
| `port_scan` | TestBatch2PortScan, TestBatch2PortScanRiskyExposures, TestBatch2PortScanTop100, TestBatch2PortScanValidation, TestBatch2CatalogIntegrity |
| `risk_score` | TestResultJSONRoundTrip, TestRiskScoreGradesAndFindings, TestRiskScoreMissingDataGivesHalfCredit |
| `ssl_tls_scan` | TestBatch2SSLTLSInjectedCertificate, TestBatch2SSLTLSWeakConfiguration, TestBatch2SSLTLSRefused, TestBatch2SSLTLSRealHandshake, TestBatch2CatalogIntegrity |
| `tech_stack_detect` | TestBatch8TechFullDetection, TestBatch8TechFrameworkFromHeader, TestBatch8TechGradeInputKeys, TestBatch8TechServerVersionExposed, TestBatch8TechConnectionError |
| `web_scrape` | TestBatch3WebScrapeStaticArticle, TestBatch3WebScrapeLinks, TestBatch3WebScrapeSelector, TestBatch3WebScrapeNonHTMLSkipped, TestBatch3WebScrapeHTTPError |

## 测试命令与结果（实跑）

```bash
# 全包测试
cd go_hive/go-hive && go test ./internal/tools/integrations/ -count=1
# -> ok github.com/aden-hive/hive-go/internal/tools/integrations 2.458s

# 用例总数
go test ./internal/tools/integrations/ -list 'Test.*' | grep -c '^Test'
# -> 1245（另 internal/cli 会话接线测试 5 个：TestSessionAccount*）

# 本地文件类（PDF/Excel/CSV/scrape）
go test ./internal/tools/integrations/ -run 'TestBatch3PDFRead|TestBatch3Excel|TestCSV|TestBatch3WebScrape' -count=1 -v
# -> 全 PASS；例：TestBatch3PDFReadLocalTextPDF、TestBatch3ExcelWriteNewFile、TestCSVWriteReadAppendInfo、TestBatch3WebScrapePersistsFullBody

# 扫描器（真实 loopback socket/TLS）
go test ./internal/tools/integrations/ -run 'TestBatch2PortScan|TestBatch2SSLTLS|TestBatch2HTTPHeaders|TestBatch2DNSSecurity' -count=1 -v
# -> 14/14 PASS；含 TestBatch2SSLTLSRealHandshake、TestBatch2PortScan（net.Listen 127.0.0.1）

# 本地协议假服务（PG/Redis/Mongo/Supabase）
go test ./internal/tools/integrations/ -run 'TestBatch5Lane2Redis|TestBatch5Lane2Mongo|TestBatch5Lane2Supabase|TestBatch5PG' -count=1
# -> ok 0.285s（进程内假服务，非真实 PG/Redis）

# REST 假服务样例（Salesforce/CRM）
go test ./internal/tools/integrations/ -run 'TestBatch6(Salesforce|Lane1)' -count=1 -v
# -> 16 个顶层用例 PASS（含 401/403/404 错误映射与 Bearer 断言）

# 声明式全量 sweep（stripe/slack/github/telegram/trello/airtable）
go test ./internal/tools/integrations/ -run 'TestEveryDeclarativeActionExecutes|TestStripeRequestShape|TestSlackRequestShape|TestGitHubRequestShape' -count=1 -v
# -> PASS；sweep 对 6 个模块的每个 spec action 都发请求并断言无未填充占位符

# 富化/情报（retry、多 provider 回退）
go test ./internal/tools/integrations/ -run 'TestBatch7Lane5|TestBatch7Lane6SimilarWeb' -count=1
# -> ok 0.326s

# 邮件/Google/Notion/Linear
go test ./internal/tools/integrations/ -run 'TestBatch7Lane9Email|TestBatch5Lane3|TestBatch4Notion|TestBatch4Linear' -count=1 -v
# -> 130 个顶层用例 PASS

# 表格别名 + browser_setup（session 注册面）
go test ./internal/cli/ -run 'TestSessionCatalogueRegistersSpreadsheetToolNames|TestSessionCatalogueBrowserSetupDispatch' -count=1 -v
# -> PASS；csv_*/excel_* 10 个别名与 browser_setup 均由 session catalogue 实测分发

```

未验证边界：B 类全部是假服务测试（httptest / 进程内协议假服务），不代表真实第三方 API 可用；未实测不写 A。
