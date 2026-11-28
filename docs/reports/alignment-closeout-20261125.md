# go_hive 对齐闭环（2026-11-25）

## 目标与基线

- 目标：关闭 `package-parity-refresh-20261124.md` 点名的实质缺陷与登记缺口，并给出可复算证据。
- 代码 HEAD：`f11e26bc9`（`test: annotate the A5 window event test`）。
- 参考基线：上游 1.0.15 `6703b434`，Python `core/framework/**` + `tools/src/**`。
- 判定口径：功能一致 = 任一方可观测能力存在且可调用；语义一致 = 同一输入产生相同可观察行为。CI 绿不等于全量语义对齐。

## 本轮闭环

### D-1：用户资料镜像走记忆日志

- `internal/server/profile.go::SyncUserProfileMemory` 不再直接 `os.WriteFile`，改为：
  `memory.Write(memDir, "user-profile.md", content, memory.WriteOptions{Author: AuthorOwner, Reason: "profile settings", KeepAuthor: true})`。
- 因此恢复 `author/learned/confirmed` frontmatter、`log.jsonl` 变更记录、`MaxFileSizeBytes` 上限和 owner 守卫；profile PUT 仍保持 fail-soft，不因镜像失败而失败。
- 测试：`go test ./internal/server ./internal/memory` PASS；覆盖 owner 保留、日志条目、4KB 超限不落盘且 PUT 仍 200。
- 提交：`bebedf60d`，合并 `deaa5ff7a`。

### D-2：memory_reflection 只接受真实整数

- `internal/server/chat_memory_wiring.go` 的反射频率改为 `reflectionPositiveInt` + `strictJSONInteger`。
- `hiveconf.Load` 使用 `json.Decoder.UseNumber()`；只接受整数形 `json.Number`（`Int64()` 成功），拒绝 `true`、`"2"`、`2.0`、`1e1`、0、负数和溢出；其余回落默认值。
- 测试：`go test ./internal/server -count=1` PASS；红测证明旧 `toFloat` 会把 `2.0`/`true`/`"2"` 误读，修复后全部回落。
- 提交：`f63947afe`，合并 `89a5d3ff5`。

### S-2：colony pivot 状态否决

- `internal/server/chat_colony_dispatch.go::sinkColonyPivot` 恢复上游状态检查：
  - `session.ForkKickoffPending` → 返回 fork-kickoff 否决文本，不 stash；
  - 已有 runtime 或 session pending pivot → 返回 concurrent-popup 否决文本，不覆盖；
  - 否则保存 payload。
- 否决文案按上游 `_make_pivot_payload_sink` 逐字复制；intercept 仍负责 `is_error` 包装。
- 测试：`go test ./internal/server -count=1` PASS；新增 6 个用例，覆盖两种否决、nil 安全和正常 stash。
- 提交：`bf930f68b`，合并 `1a125476c`。

### A5：turn 级 doom-loop 窗口与事件

- 确认 `doom_loop_window.go:69 ObserveTurn`、`loop.go:1151`、`loop_guards.go:211 settleDoomLoopWindow` 已接线；worker 升级、queen park/其他警告分流存在。
- 修复事件描述：`DoomLoopSink` 由 `(tool, streak)` 扩为 `(tool, streak, description)`，窗口事件使用完整批次描述，不再退化为 per-call 文案。
- `node_tool_doom_loop` 的生产发布路径：colony worker `colony/worker_doom_loop.go`，queen `server/chat_loop_guards.go`。
- 测试：`go test ./internal/agentloop ./internal/colony ./internal/server -count=1` PASS；新增 server 端到端窗口事件测试。
- 提交：`370ca1b8c`，合并 `d42394646`。
- 已登记残余：queen park 使用 server 的有界 turn park/resume（非 Python turn 内阻塞）；窗口不随冷启动 mid-turn resume 恢复。

### A39：前端 ghost 路由与 i18n 登记

- 已登记为 Go 侧有意改进，写入 `docs/alignment-differences.md` §7.2：Python 未知 colony 路由永久 `Connecting`，Go 结束加载并显示 `Colony not found`；`refresh(): Promise` 是判定查找结束的最小契约扩展。
- B 树无 i18n 层；7 个 Go `t()` key 在 B 侧是 JSX 表达式/模板，不是缺翻译。当前 `i18n_coverage.py`：missing translations 0。
- 决定：保留 Go 行为，不向 `core/frontend` 回填；B 引入 i18n 或修改 `refresh` 契约时复查。

## 门禁结果

在当前代码 HEAD `f11e26bc9` 实测：

- `make ci`：**RC=0**
- 测试：**6,453/6,453**
- parity refs：**7,295 checked，missing 0**
- compat：**C1–C13 全 PASS，13/13**
- C12：**273 registered / 221 ported / 27 partial / 9 out_of_scope / 16 unported**
- `make parity`：**6,453/6,453**
- `check_module_inventory.py --check`：**PASS**
- 目标包：`go test ./internal/agentloop ./internal/colony ./internal/server ./internal/memory` PASS

## 仍未对齐 / 未验证

- **A5 残余**：queen 窗口 park 与 Python 的 turn 内阻塞形态不同；冷启动 mid-turn resume 不恢复窗口。
- **S-1**：Go 仍全 `internal/`，无公共 SDK，衍生项目不能 import。
- **S-3**：Go channels 仍只支持 stdio，不加载 Python `hive.channels` entry points。
- **V-1**：写操作双跑覆盖仍按既有口径约 19/134；本轮新增的是单测，不是全量双跑。
- **V-2**：pipeline 无行为级 cross-check；91 个第三方集成无真实凭据；Windows/真实 MCP 未实机验证。
- **契约**：OpenAPI 仍有 139 条路由无响应快照、无 request body schema；AsyncAPI WS 仍 design-only。
- **未移植面**：C12 仍有 16 个 unported；`global_db` CLI 6 子命令等已登记项不变。
- **E-11/A39**：Go 前端差异已登记，不代表 Python 前端已回填。

## 结论

本轮把报告点名的 **D-1、D-2、S-2、A5、A39** 处理到可复算状态，结构门禁与当前代码测试全绿；但 **仍不能宣称 go_hive 与 hive 全量逐字一致或全量已验证**。对外只能表述为：

> 功能主体对齐，D-1/D-2/S-2/A5 已闭环，A39 已登记；S-1/S-3、V-1/V-2、契约快照缺口和未移植面仍在。
