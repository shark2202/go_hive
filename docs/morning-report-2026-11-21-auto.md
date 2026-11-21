# go_hive 晨报 · 2026-11-21 09:00 CST

> 生成方式：夜间自主推进；数据来自 git、/tmp 门禁日志、看门狗与时间台账。未验证项不写成通过。

## 目标

按 `go_hive/00-product-goals.md`，以 Hive 当前 commit 为基准推进 API / tool / 内置 Skill / env / configuration.json 的逐字与功能一致。

## 状态快照

| 项 | 值 |
| --- | --- |
| HEAD | `8c691881`（main） |
| 夜间提交（20:00 起） | 145 |
| 今日提交（00:00 起） | 85 |
| worktree / 未提交 | 55 / 12 |
| serve 8788 / bridge 14830 | 200 / 200 |
| 最近 CI 结论 | check_parity: OK — 5170 个测试全部带有 parity 注解或豁免说明。 |
| 最近测试总数 | TOTAL                                       5170       5170        0 |
| 最近 QC | **8/8 PASS** |

## 夜间提交

```
8c691881 (HEAD -> main) docs(report): 晨报 HTML 版（暗色，含 14 行证据表）
19578fbc docs(report): 09:00 晨报（83 提交/24 合并/5170 测试/8-8 QC，含未对齐清单）
fcff0b71 qc: wave9 合并后 5170 测试，8/8 PASS
85917221 merge worker-stall-autofail: text-only stall 3 回合自动失败（对齐上游 grace=2）
e1876872 (worker-stall-autofail) feat(agentloop): 移植 worker text-only stall 自动失败（合成 report_to_parent(failed)）
362ea97e merge askuser-turn-dedup: ask_user 回合提前返回，消除重复模型调用
d101b1b3 (askuser-turn-dedup) fix(agentloop): ask_user 回合提前返回，对齐上游停车语义
02f50a58 qc: wave8 合并后 5159 测试，8/8 PASS
36377753 fix(server): sedimentSoul/反思配置移入 chat_memory_wiring，chat_runtime 1546→1494
d52844c3 merge sse-payload-parity: 探针 + 六项 SSE 载荷修复
e9ef8703 merge config-put-hotswitch: 模型槽位会话级热切换 + provider-role 对齐
d2847fda (sse-payload-parity) feat(go-hive): SSE payload 双跑对齐——探针 + 六项载荷修复（committed/phase/context usage/title/过滤/replay）
173f8d22 (config-put-hotswitch) test(server): 修函数预算与接线门禁 + 热切换 E2E 证据入库
bf3ab238 test(config): 热切换 live E2E + 审计/契约同步
a82cd9fc merge oauth-refresh-pagination: OAuth2 refresh + 通用 REST 分页
fe677e1c (oauth-refresh-pagination) feat(integrations): OAuth2 refresh_token 通用刷新 + REST 声明式分页
2abca799 feat(config): 模型槽位会话级热切换（ProviderSlot 重建 + 回合级 pin）
05f178e2 qc: wave7 合并后 5123 测试，8/8 PASS
4dcd8938 merge escalation-doomloop: parallel worker 无 escalate + doom-loop 自动升级
21734e38 merge integrations-account-wiring: get_account_info 凭据视图 + account 别名（C→B）
ecf2c0d5 (escalation-doomloop) feat(escalation): parallel worker 对齐上游无 escalate + doom-loop 自动升级生产者
1e38d0dc (integrations-account-wiring) feat(integrations): 接通 get_account_info 凭据视图并复刻 account 别名
ffdb516e qc: browser 返回契约合并后 5096 测试，8/8 PASS
2f86ff5f merge browser-return-contract: 截图/快照返回契约 + upload 退出码（R01/R02/R07）
951115d7 (browser-return-contract) fix(browser): 对齐截图/快照返回契约与 upload 退出码（R01/R02/R07）
db5c4c1b fix(publish): 以远端 ref 校验推送是否落地，避免本地提交误报成功
96afddda merge appearance-matrix-refresh: API 14/14 升级、工具/调度/browser/orchestration 数字回填
f14182a0 (appearance-matrix-refresh) docs(go-hive): 同步 alignment-dimensions 中已过期的维度结论
9cdeac7f docs(appearance): 回填已过期维度结论并重生成总览矩阵
88d68534 fix(watchdog): 休眠后跳过假 stall/ledger 告警，短睡眠补采
3a3f2f5c fix(timegov): 短睡眠累加，系统休眠唤醒后立即补采
edf734bb ops: 镜像发布失败重试（每 5 分钟，直到远端含当前 HEAD）
9ffdf985 qc: escalation 合并后 5093 测试，8/8 PASS
2e46d956 merge worker-escalation-channel: escalate → 收件箱 → reply_to_worker → 继续（真实 E2E）
d19f2cee (worker-escalation-channel) docs(go-hive): worker escalation 真实 E2E 证据与说明（e2e 网关 + 资产 + 审计页更新）
755fa224 feat(go-hive): worker escalation 生产链路（escalate → 收件箱 → reply_to_worker → 继续）
1aade7b9 qc: config 合并后 8/8 PASS 报告
7a0e5a57 merge reminders-coverage: 补齐 stream_stall/worker tracker/overseer source + 预载 colony queen scope
6feff1cd docs(contracts): 合并后 env/config 数字与外观矩阵同步（89/100/69/20/31，134 键）
72750b53 contract(env/config): 合并后重算（python89/go100/common69/py20/go31），config 134 键
789be159 (reminders-coverage) reminders: 补齐 STREAM_STALLED / worker / overseer source 与预载 colony queen scope
156d3a8a merge config-env-align: 模型槽位/配置键运行时消费 + env/config 契约刷新（产物待按合并后重算）
3428a92c merge integrations-evidence-audit: 104 行集成证据分级（A12/B90/C1/D1）
ee080f09 chore(timegov): 记账更新
e6a15756 (integrations-evidence-audit) docs(integrations): 证据审计 JSON+HTML+MD（102 模块 / 854 action，A12/B90/C1/D1/E0）
e300525d merge browser-panel-status: 面板状态探测/路由形状/CLI 退出码（R03/R04/R05）
55d8514c docs(playbook): 记录进程内真实多模态 E2E；test: parity 注解
9c35f40a docs(playbook): HTML 文档 + 真实 LLM 多模态/E2E 证据；test: 真实模型 playbook 桥 E2E
fb3aaecc (config-env-align) docs(contracts): 刷新 env/config 契约与支撑台账，登记新接线点
94231791 feat(config): 运行时消费模型槽位与全局配置键
6e83a48b fix(mcp): HIVE_MCP_CALL_RESULT_TIMEOUT 优先于 mcp.timeout_seconds
86011c7b (browser-panel-status) fix(browser): 面板 status/3 路由对齐上游 + CLI 退出码收口（R03/R04/R05）
c2da5728 merge appearance-scope-master: 23 维外观总览矩阵 + 登记册
f96f8301 merge play-py-support: Python .play.py 外部解释器 + stdio JSON-RPC
0927ea40 (play-py-support) docs(go-hive): Python playbook 支持口径（support-matrix + 审计/矩阵/依赖页）
19362eab feat(go-hive): Python .play.py playbook 支持（外部 python3 + stdio JSON-RPC）
16975175 chore(timegov): 记账更新
11a7c340 qc: scanner 合并后 5004 测试，8/8 PASS
2ffab134 ops: 公共 go_hive 镜像发布脚本（pull → export → commit → push）
d0ce4fd8 merge network-scanners-parity: 扫描器默认注册，移除 Go 独有开关
```

## 质量门禁证据

- 最新 CI 日志：`/tmp/main_ci_wave9.log`
- 最新 QC 日志：`/tmp/qc_wave9.log`
- QC 报告：`/Users/mac/hive/go_hive/QC_REPORT.md`

## 时间与效率

```
2026-11-21 08:23:38,8c691881,224,5170,1,
2026-11-21 08:28:39,8c691881,224,5170,1,
2026-11-21 08:33:40,8c691881,224,5170,1,
2026-11-21 08:38:41,8c691881,224,5170,1,
2026-11-21 08:43:42,8c691881,224,5170,1,
2026-11-21 08:48:42,8c691881,224,5170,1,
2026-11-21 08:53:42,8c691881,224,5170,1,
2026-11-21 08:58:42,8c691881,224,5170,1,
```

## 告警

```
- 11-20 22:57 [STALL] 4 个 worktree 有未提交改动，可能卡住
- 11-20 23:37 [STALL] 4 个 worktree 有未提交改动，可能卡住
- 11-21 00:33 [STALL] 4 个 worktree 有未提交改动，可能卡住
- 11-21 00:37 [STALL] 5 个 worktree 有未提交改动，可能卡住
- 11-21 00:41 [STALL] 6 个 worktree 有未提交改动，可能卡住
- 11-21 00:45 [STALL] 6 个 worktree 有未提交改动，可能卡住
- 11-21 00:49 [STALL] 5 个 worktree 有未提交改动，可能卡住
- 11-21 00:53 [STALL] 4 个 worktree 有未提交改动，可能卡住
- 11-21 01:01 [STALL] 4 个 worktree 有未提交改动，可能卡住
- 11-21 01:05 [STALL] 5 个 worktree 有未提交改动，可能卡住
- 11-21 01:09 [STALL] 5 个 worktree 有未提交改动，可能卡住
- 11-21 01:13 [STALL] 4 个 worktree 有未提交改动，可能卡住
```

## 契约/待办快照

### `go_hive/docs/tool-contract-recheck.json`
```json
{
 "kind": "hive-tool-contract-recheck",
 "schema_version": 1,
 "generated_at_utc": "2026-11-20T16:12:43Z",
 "scope": "model-visible tools[] of a default queen_growth session, re-captured after terminal-schema-p0 (b9a85d51) and tool-desc-schema-p1 (85988b03) merged",
 "baseline": {
  "frozen_at_commit": "1b9c458a19244379ac65dabd4827addb891045ef",
  "manifest": "go_hive/contracts/tool-definitions.json",
  "report": "go_hive/docs/tool-contract-diff.md §1-§10 (left untouched)",
  "statistics": {
   "python_surface": 54,
   "go_llm_request": 144,
   "shared": 41,
   "python_only": 13,
   "go_only": 103,
   "description_identical": 13,
   "input_schema_raw_identical": 8,
   "input_schema_ctx_stripped_identical": 8,
   "input_schema_normalized_identical": 8,
   "python_params": 156,
   "go_params": 167,
   "python_only_params": 14,
   "go_only_params": 25,
   "type_mismatches": 7,
   "optionality_mismatches": 11,
   "required_mismatches": 7
  }
 },
 "recheck": {
  "python": {
   "role": "reference",
   "version": "1.0.14",
   "checkout": "/Users/mac/hive",
   "head_at_capture": "889bbfdedac1572b31488e97effcc4bec9bc50fd",
   "head_after_capture": "98b3a0abaad1408f972ba253de672439561877bf",
   "core_commit": "a756b1e51765bc75b0a40e362859452a0a54081b",
   "tools_src_commit": "68b6c230f20e237953dea7a3af7bb583b9fbe0fd",
   "capture": "temporary HIVE_HOME + fake OpenAI-compatible gateway (llm.api_base -> gateway); POST /api/queen/queen_growth/session; POST /api/sessions/<id>/chat; first request body's tools[] recorded verbatim; GET /api/queen/queen_growth/tools before session creation for the Tool Library layer"
  },
  "go": {
```
### `go_hive/contracts/tool-definitions.json`
```json
{
 "schema_version": "1.0",
 "kind": "hive-tool-definition-contract",
 "generated_at_utc": "2026-11-20T15:35:11Z",
 "reference": {
  "name": "python",
  "package": "aden-hive / hive (core/)",
  "version": "1.0.14",
  "commit": "4e737cbc63523ba5eeda84820c09f28ebd43a670",
  "core_commit": "a756b1e51765bc75b0a40e362859452a0a54081b",
  "tools_commit": "68b6c23095aef8bc9a0dcbc05aeb16e61b1ba8db",
  "surface": {
   "llm_request": 24,
   "mcp_catalog": 26,
   "lifecycle": 16,
   "synthetic": 1,
   "union": 54
  }
 },
 "target": {
  "name": "go_hive",
  "version": "1.0.26-go.1",
  "commit": "1b9c458a19244379ac65dabd4827addb891045ef",
  "surface": {
   "llm_request": 144,
   "mcp_catalog": 0,
   "lifecycle": 0,
   "synthetic": 1,
   "union": 144
  }
 },
 "contract": {
  "baseline": "reference",
  "rule": "name/description/input_schema are the Python runtime's verbatim values; the go_* fields are the target side recorded for alignment, never the baseline",
  "canonical_order": "bytewise by name (Python sorted())",
  "sources": [
   "python:llm_request",
   "python:mcp_catalog:<server>",
   "python:lifecycle",
   "python:synthetic"
```

## 已知未对齐（以文档为准）

- Tool 名称形状：分组 vs Python 扁平（若夜间未完成，仍不能宣布逐字一致）。
- API：14 端点探针中的 tools/SSE/snapshot/stats/credentials 差异。
- Config：worker_llm / vision_fallback / queen_llm 的生产消费路径、Python-only 键。
- 未验证：Windows 实机、81 个凭据集成、真实视觉模型、LongMemEval 真实分数、GitHub Actions 发布。

## 下一步

1. 复核夜间合并的 lane 与契约复测数字。
2. 若 Tool 扁平化未完成，优先收口；否则转向 API 响应契约与 config 运行时消费。
3. 任何新增/变化的差异更新到 `docs/tool-contract-diff.md`、`docs/api-contract-diff.md`。
