# 实际差异报告（go_hive ↔ Hive 参考）

> 更新：2026-11-23（§2 工具契约 round 9：归一化残差 0；§5 补触发器登记；MCP 行改记 D3 终选 B′——默认裸名，前缀模式可选；总表与 §1–§4 保持 2026-11-22 快照，wave-76 全量重算）。
> 分类：**一致** / **登记差异（有意保留，带复查条件）** / **已修（本轮闭环）** / **待拍板（需决策）** / **外部未验证**。

## 0. 一页总表

| 面 | 实测 | 状态 |
|---|---|---|
| API 路由 | 参考 222 / Go 225：有效缺失 **0**、别名覆盖 2、Go 扩展 5、方法差 1（登记） | 一致（7 条登记差异） |
| REST 端点形状/语义 | 18/18 PASS（107 端点语料） | 一致 |
| SSE 事件帧 | 剔除 Go 独有每回合信封后 **42/42 逐帧一致** | 一致 |
| 工具定义（LLM 侧） | desc **48/48** 逐字；schema 归一 **48/48**（raw 40/48，8 条纯表示差：数值字面 4 + 参考层 `session_cwd` 4）；round 9 把 14 条归一化残差（12 lifecycle + `browser_setup` + `task_create`）归零 | 一致 |
| 工具面扁平 | always-on 24 / deferred 72 / unique 39 | 一致 |
| 触发器（timer/webhook） | 契约页 v2 + 手册 + `session-create.md`；响应码逐字；payload/批合并/重复键/body 上限**全已修**（3597215d/1e8ef0a1/a91ae182） | 一致+登记（§5：2 条数字微差 + 3 条形态微差） |
| env 变量 | Python 90 / Go 125 / 共有 84 / Python-only 6（GCU）/ Go-only 41（含 runner 与验证脚本开关） | 一致（口径分类明确） |
| configuration.json | 137 键 / 22 sections；aligned 128 / go_only 4 / go_partial 4 / unread 1 | 一致+登记 |
| CLI 外观 | hive 47 路径 / hive-browser 27 / hive-cred 全对齐；4 usage slot 0 漂移 | 一致 |
| 内置 Skill | 25 文件逐字节一致（229,354 B） | 一致 |
| 前端 | 缺失 0；content_diff 63（i18n 类，全归因）；python_only 0 | 一致+登记 |
| MCP 命名空间 | 默认注册 server 侧裸名（与 Python 同键，不折叠/不消毒）；显式 `mcp.namespace` 才启用前缀模式。13 用例默认模式全一致，前缀模式剩折叠/长名登记 | **一致（默认）+ 可选模式登记（R1–R3）** |
| 判定语义（judge） | J2 已对齐（瞬态重试/耗尽 fail-open ACCEPT/非瞬态上抛）；边界 5 条登记 | 已修 |
| memory notes（1.0.15） | notes 推送/recall 索引/log.jsonl/路由/前端全族；探针 51/51、上游 pytest 95 passed | 已修 |
| 恢复与可靠性 | 崩溃恢复 7/7 part 字节一致、events append-only、park 跨重启；浸泡 50/50、SSE 53/53 | 已修（2 项新观察登记） |
| 升级迁移 | v1/v2/v3 三代 home 每 slot 57 项 PASS；旧二进制读回无写入 | 一致 |
| 浏览器渲染 | dpr 注解宽度已修（≤1px）；Skia vs PIL JPEG 字节、字体度量标签盒、alpha 为登记例外 | 已修+例外 |
| exports 基准 | Python=CWD vs Go=$HIVE_HOME（实测矩阵）；`.migrations` 列入 colony 两侧逐字一致 | **待拍板 D2** |
| 版本串 | Go 1.0.26-go.1 / Python 1.0.15（双轨，不进契约） | 登记（既定口径） |
| 安全 | govulncheck 红：stdlib CVE 需 go1.26.6+ | **待拍板 D1** |
| 第三方集成 | 91 个集成「未验证」（无凭据） | 外部未验证 |
| 平台 | darwin/linux 六平台编译+Docker 真跑 20/20；Windows 两平台无实机 | 外部未验证 |

## 1. API / WS

- **路由**：参考 222 条 / Go 225 条。有效缺失 **0**（2 条由别名覆盖：`DELETE /api/sessions/{first}/{second}`、`POST /api/skills/reload` 属 Go 扩展）；Go 扩展 5 条；`/api/sessions/{}/colony` 方法集合差 1（登记）。证据：`contracts/api-routes.json`（reference=python@e5c7603e…）、`docs/api-contract-diff.md`。
- **SSE**：42/42 帧一致（帧级 golden `history-core-equal`）；Go 独有每回合信封 +12 帧为设计差异。证据：`docs/sse-payload-parity.md`。

## 2. 工具 / Skill / MCP

- **工具定义**：desc **48/48** 逐字（live recheck，round 9）；schema 归一 **48/48**、raw 40/48（8 条纯表示差：4 条数值字面拼写 + 4 条参考层 `session_cwd`）。round 9 把 12 条 lifecycle 参考改记运行时层（注册 Tool 对象经 `_tool_to_openai_format` 投影），并机械收口 `browser_setup`（去 `additionalProperties`）与 `task_create`（recheck 安装 DM pivot overlay），此前的 14 条归一化残差全部归零。证据：`docs/tool-contract-diff.md` §3.1/§18、`contracts/tool-definitions.json`、`docs/e2e-assets/schema-residuals/REPORT.md`。
- **Skill**：25 个内置文件逐字节一致（229,354 B），有测试门禁。colony worker/overseer 的目录作用域已对齐上游 `_build_default_skills_config`：framework/default → preset → user（`~/.agents/skills` / external / `~/.hive/skills`）→ queen_ui → colony_ui（含 legacy `.hive/skills`），后扫覆盖先扫；目录提示按 phase `colony` 过滤 visibility；preset 默认关闭，按所属 queen profile 的 `default_preset_skills` 在内存启用（自定义 profile 亦生效）。证据：`internal/colony/skills_surface_test.go`（framework/user/[colony] 入、[independent]/[worker] 出、preset 开关、colony 覆盖 framework、overseer 目录）与 `internal/queen/profiles_test.go`。
- **MCP**：D3 终选 B′（2026-11-23）——默认注册 server 侧裸名（不折叠、不消毒），与 Python 同键；`a-b`/`a_b`、`x-y`/`x_y`、70 字符名、跨 server 同名 first-wins、`preserve_existing_tools=False` 覆写全部与 Python 实跑一致（13 冻结用例默认模式全 PASS）。显式 `mcp.namespace` 非空时启用 Go-only 前缀模式（`mcp__<ns>__<srv>__<tool>`，非 `[A-Za-z0-9_]`→`_`），其折叠不可逆（R1）与长名前缀加长（R2）按原样登记；多 content 块逐项一致（仅 JSON 键序 L3 与畸形 mimeType 处理差异）。证据：`docs/mcp-namespace-residuals.md`、`docs/e2e-assets/mcp-bare-default/REPORT.md`。

## 3. env / configuration.json

- **env**：Python 90 / Go 125 / 共有 84；Python-only 6（GCU 平台，无 Go 读取点）；Go-only 41（Browser Bridge 部署面、MCP2CLI、成本限额、runner `HIVE_LME_*` 与验证脚本开关等）。读取点：Python 139 / Go 106。证据：`docs/env-config-contract-diff.md`、`contracts/env-vars.json`。
- **configuration.json**：137 键 / 22 sections；aligned 128 / go_only 4 / go_partial 4 / unread 1（全部在 `contracts/config-keys.json` 带锚点）。1.0.15 新增 `memory_recall`{mode,wait_seconds}、`memory_reflection`{model,every_turns,long_every} 已对齐。

## 4. CLI / 磁盘 / 迁移

- CLI：hive 47 路径全共有；`queen-mail-mcp` 为 Go 隐藏子命令（Python 走模块入口，功能等价，已登记）。
- 磁盘：会话 `meta.json/state.json/events.jsonl/conversations/parts`、凭据、tracker schema v3 两侧一致；存储契约 19 类自洽（C4）。
- 迁移：v1/v2/v3 三代 home 每 slot 验证（57 项）；Go 写 → 旧二进制读回不写入；exports 基准差异见 D2。

## 5. 语义与特性（本轮变化）

- **judge J2 已对齐**：瞬态重试、耗尽 fail-open ACCEPT、非瞬态上抛；上游边界 5 条登记于 `judge-parity.md §6.2`。
- **1.0.15 memory notes 全族**：notes 推送（index/wait）、recall 索引（冷建让路 3s、删除可重建）、`log.jsonl` 历史/恢复、8 条 `/api/memories` 路由、Settings→Memory、用户自有标记、笔记年龄=最后确认日。
- **恢复**：kill -9 后冷恢复 7/7 part 字节一致、events 29→29 append-only、pre-crash 工具结果进入上下文；`ask_user` park 跨两次重启保留。
- **并发**：10 会话 × 5 轮 50/50，0 错误；RSS +12MB；SSE 53/53 无丢帧。
- **长等待/并行工具**：A 表 10 项、B 表 7 项实测；修复 colony worker 等待 60→600s。
- **新登记（非阻断）**：① cold-resume mid-turn guard（`cold_interrupted` 无生产者）② resume 后快照/SSE 首帧空态。两条由 `resume-guards` lane 收口中。
- **凭证校验已闭环（lane `cred-check`）**：`POST /api/credentials/check-agent` 由 500 占位改为上游语义全实现（presence + 健康检查两阶段、Aden 平台行、`alternative_group` 任一满足、14 字段响应 + `has_aden_key`）；`POST /api/credentials/resync` 生产 hook 接线为本地默认（Aden 云端 token 拉取仍缺，P2 `credentials/aden/client.py`）。前端 CredentialsModal 真实打开不再报错。证据 `docs/e2e-assets/credentials-check-agent/`、`docs/unported-triage.md` 末节。
- **`context_compacted` → long reflection 已接线（lane `memory-compaction-reflect`，2026-11-23）**：此前 Go 侧 `ReflectionScheduler.OnCompaction` 全仓无调用点、agentloop 也不发布 `CONTEXT_COMPACTED`，压缩丢内容前不会先沉淀记忆。现由 loop 在 LLM 压缩成功路径产出上游同字段 payload（`level`/`usage_before`/`usage_after`/`tokens_before_k`/`tokens_after_k`，取整为 Python `round` 半偶口径；`ratio_before>=1.0` 时才附 `message_inventory`），chat runtime 以 queen 流订阅 session bus 并调用 `OnCompaction`（反思进行中/Closed 由 scheduler 跳过）。**残余**：① `message_inventory` 的键与形状同上游，但 `content_chars`/`preview` 用 Go 的字节长度（`len(string)`），非 ASCII 内容与上游 Python 的字符长度不同；② Go 的 `compactConversationIfNeeded` 没有 microcompact/prune 阶段（proactive microcompaction 在 `add_tool_result`，上游该路径同样不发事件），因此不存在“仅 microcompact 的 compact() 完成”事件形态；③ worker loop 未接 `OnContextCompacted`（只接 queen，与上游 reflection 订阅口径一致）。
- **触发器（timer/webhook）字段级契约落盘（2026-11-23）**：定义/请求/响应/事件/注入五层字段的类型、必填、默认与语义逐条记录于 `docs/triggers-webhook.html`（v2）；响应码文案逐字镜像（202/404/405/400/413/401）。**登记差异**：① 非 object JSON body payload——**已修**（lane `webhook-payload`，3597215d，任意 JSON 类型原样保留；剩余微差：数字统一 float64、NaN/Infinity 走 raw_body）；② 多触发器注入形态——**已修**（lane `trigger-batch`，1e8ef0a1，批合并单 stamp）；③ 重复同名头/query 取首值 & body 上限 1MiB（≥1MiB 回 400，无 413）——**已实测对齐**（lane `webhook-semantics`，a91ae182；残 3 条形态微差：头名拼写/JSON 形态/超限 drain）。

## 6. 证据与质量

- parity **5,833/5,833**；C12 **273**（220/27/9/17：四 lane 并集）；config **131 aligned / 4 go_only / 1 go_partial / 1 unread**；QC **9/9**；compat **12/12**；arch **87 包/234 边**（P3 五族拆包 + chat-images）；govulncheck **0 OSV**。
- 性能：`perf-recall-15k`（15,000 轮冷建/warm/搜索/冷启动/RSS）产出中，落 `docs/perf-recall-15k.md`。
- 大样本行为：LongMemEval n=200 四组（`e2e-assets/longmemeval/2026-11-22-200q-real-run.md`）；配对检验均不显著，新增 140 题 126:126。
