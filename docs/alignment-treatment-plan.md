# 处理方案报告（差异 → 方案 → 责任人 → 排期）

> 更新：2026-11-22 18:40 CST · 与 [对齐目标报告](alignment-goals.md) / [差异报告](alignment-differences.md) 配套。
> 原则：**验收只认实测**；可观测差异要么修、要么登记且带复查条件；依赖变更与产品口径类改动**必须用户拍板**。

## A. 本轮已处置（已闭环，留档可查）

| 差异/缺陷 | 处置 | 证据 |
|---|---|---|
| judge J2（重试后 ACCEPT vs 归一 RETRY） | **已修**：瞬态重试/耗尽 fail-open/非瞬态上抛三路对齐 | `judge-parity.md §6.2` + 矩阵测试 |
| dpr>1 注解宽度 800×dpr 偏差 | **已修**：`scale=max_width/(base×dpr)`，真机 dpr 1/2/3 恒定、矩形 ≤1px | `browser-tool-surface.md §4.1` |
| 会话标题 LLM 槽位（worker_llm 偏好） | **已修**：`TitleTriggerLLM` 订阅时读取 + 回退 | `session-titles-parity.md` |
| recall seed / 双 scope 串行 | **已修**：seed 移植（≤5 pins/3s）、并发挑选（保序） | `memory-notes-parity.md` |
| slot 内嵌 dashboard 过期（F1） | **已修**：embed_ui 新鲜度守卫 + release_slot 改走 embed_ui | `e2e-assets/dashboard-memory-1015/` |
| recall 索引/cold-resume 崩溃 | 崩溃恢复与浸泡 **27/27 PASS**（无产品缺陷） | `reliability-soak-recovery.md` |

## B. 待用户拍板（阻塞相应收口项，材料见 `pending-decisions.md`）

| ID | 事项 | 推荐 | 影响面 | 回滚 |
|---|---|---|---|---|
| **D1** | govulncheck：go.mod 升 go1.26.11 | ✅ 已批准并合并（2026-11-22，`d6831999`）；govulncheck 23 OSV→0 | — | `git revert` go.mod 行 |
| **D2** | exports/ 基准：home vs CWD | ✅ 选 A：维持 `$HIVE_HOME` 基准（无代码改动，文档化） | — | — |
| **D3** | MCP 命名：前缀 vs 裸名 | ✅ 已批准标准版并合并（`001fb1a8`）：保留前缀 + 裸名单向回退别名 | — | 整体 revert 2 提交 |

## C. 已登记例外/差异（有意保留，带复查条件）

| 项 | 保留理由 | 复查条件 |
|---|---|---|
| 浏览器 Skia vs PIL JPEG 字节、字体度量标签盒、alpha | 渲染库不同；几何/区域已对齐（≤1px） | 上游换渲染栈或出现字节级依赖场景 |
| Go 扩展路由 5 条 + 别名 2 条 | 兼容与运维需要，契约内逐条登记 | 上游新增同名端点时复核 |
| 14 条工具 schema 形状差 | nullable/oneOf 表达差异，语义等价 | 上游 schema 结构化改造时 |
| MCP R1–R3（折叠不可逆/长名/序列化） | 见 D3；冻结契约保护 | D3 决策后重估 |
| 版本双轨（go 后缀） | 既定口径，不进契约 | 外发布品牌决策时 |
| 91 个第三方集成「未验证」 | 无凭据（2026-11-19 决定不阻塞） | 提供凭据或外发布尽调 |
| worker 转写内存化、antigravity worker 分支 | 实现细节，外部不可观测 | 出现持久化/多进程需求时 |
| cold-resume mid-turn guard、resume 首帧空态 | **在修**（`resume-guards` lane） | 本晚回批即闭环 |

## D. 外部待验证（G3，12-18）

| 项 | 方案 | 前置 |
|---|---|---|
| Windows 实机 | 实机跑 install/version/serve smoke | Windows 宿主或 VM |
| GitHub Actions release | 真实执行一次 release workflow | 仓库写权限/secrets |
| Sentinel 真实投递 | 端到端投递 + 回执 | 外部通道凭据 |
| 性能基线 | `perf-recall-15k` 产出对比上游声称值 | lane 在跑（今晚） |


## F. 存量对齐清单（登记册 24 维逐条 next 项，2026-11-22 18:55 复核）

> 来源：`appearance-scope-registry.json`（18 个非 aligned 维度的 next 项）+ 今日进展复核。状态标记：✅今日闭环 / 🔧在修 / ⏳待拍板 / ⬜待做(带估时) / 🌐外部。

**P0**
- ⬜ cli：`hive mcp` 14 动词补 per-command help（对齐上游 usage/flags，4–8h）；`<verb>` 名以 `-` 开头统一 usage error 2（2–3h）；68 条 flag 级 help 文案分批（低收益，单列）
- 🔧 disk-contract：SC-04 权限宽严、SC-06 会话 meta/tasks/colony sidecar API 级双跑、文件名单侧清单逐名核实（~4h）；storage_compat 接可选 sweep（2h）

**P1**
- ⬜ tools：输出 golden 扩面收敛 12 条例外（6–8h）；`run_playbook` 折叠控制参数口径（2h）；`exa_search` 扁平化另立车道（4h）
- ⬜ env-vars：Go-only 41 项已在差异报告逐类定性 ✅；剩余「高危开关设了有观测效果」实测（4h）
- ⬜ config-json：go_partial 4 / unread 1 逐键接线或降级只读（4–6h）；PUT→重启→行为变化字段级实测扩面（4h）
- ⬜ prompt-turn-context：colony 相位 base prompt live 抓包 P-08（4h）；worker/colony prompt 与提醒文案系统对照（6h）
- ⏳ mcp：命名方案（D3）；标准 streamable HTTP 协商两侧一起改（4h）；干净解释器重跑 crosscheck（1h）
- ⬜ frontend：12 条声明口径写进支持矩阵（1h）；真实浏览器 zh-CN 全路由渲染（今日已做 settings/memory 页，全路由 3h）
- ⬜ browser-bridge：旧 Python CLI ↔ Go 14831 交叉连接实测（3h）；四条量化近似写入支持矩阵（1h）；真机扩面（多平台/扩展版本，外部）
- ✅ orchestration：LongMemEval Go 分数已具备（n=60/200 四组证据）；余 worker 工具面形态#3、escalation 参考差、双存储收敛（6h）；真实模型多 worker + 故障注入（4h）

**P2**
- 🌐 release：真实 CI release job、Windows 实机（G3 外部）；migration 结论回填 RELEASE_CHECKLIST（1h）
- ⬜ observability：结构化 logger（LOG_FORMAT=json 或标 Python-only）、三级运行日志 `sessions/<sid>/logs/`、gate 告警注入点（8–12h）
- ⬜ scheduling：missed-fire/webhook/恢复语义场景测试（4h）；playbook 对比纳入定时（1h）；triggers 字段级对照（3h）；Sentinel 真实验证 🌐
- ⏳ migration：P0-G2-1 exports 基准（D2）；第三方 token 凭据互读冒烟 🌐；G2 夹具纳入 sweep（2h）
- ⬜ desktop：支持矩阵显式声明「桌面壳不在本仓库范围」（0.5h）
- ⬜ wire-proto（P3）：77 端点映射或声明 17 端点子集（4h）；protoc 编译纳入门禁（4h）

**今日新增（不在登记册）**
- 🔧 resume-guards 两项（cold_interrupted 生产者、resume 首帧）—— lane 在跑
- ⏳ D1 govulncheck（工具链 go1.26.11 已装，待批准）；⬜ prep-mcp-alias 已预实现待批准
- ⬜ module-inventory 51 unported 逐条 triage（至少 6 条疑似假标，6–8h）
- ⬜ perf-recall 15k 报告（lane 在跑，今晚出）
- 🌐 91 第三方集成未验证

**可控面合计估时：约 45–60 小时（4–6 个夜班波次）**；其余为决策（D1–D3）与外部环境（G3）。

## E. 排期（倒排，只前不后）

| 时点 | 事项 | 验收 |
|---|---|---|
| 今晚 | `resume-guards` + `perf-recall` 回批合并；wave 复绿 | 四关 + QC 9/9 |
| 今晚 | 20:00/22:00 slot（修复后界面） | manifest+sha+compat |
| 00:05 | 治理重算#5 | 截止日只前不后 |
| 明日 | D1–D3 决策落地（按你的选择执行并复验） | 对应门禁转绿/契约更新 |
| 12-18 | G3 外部三项 + 91 集成尽调（如有凭据） | 外部证据落盘 |
