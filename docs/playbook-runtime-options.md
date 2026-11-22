# Playbook 运行时选项：内嵌 JS/Lua？npm？PyPI？

> 状态：**决策备忘（Draft）**，2026-11-22。只记录结论与依据，不改变现有实现。
> 关联：`playbook-python.md`（`.play.py` 契约与证据）、`playbook.html`（同内容可视化）、`internal/playbook/*`、`internal/colony/playbook_tools.go`。
> 说明：`prompt-parity.md` 是首轮 prompt/skill catalog 对照，与 playbook 语言无关（回答"按 python 相同语义"时被一并点名）。

## 0. 事实基线（已核实）

- 上游与 go_hive 只有两种 playbook：`.play.py`（Python 3.8+，`meta` + `async def run(args)`，注入 `converge / worker / tracker_query / tracker_count / lane / deadletter / log / phase`）与 `.play.json`（Go native JSON 引擎，兼容历史库）。**全仓无 JS/Lua playbook 的任何痕迹**（无 `.play.js`、goja、gopher-lua、Node 相关实现）。
- `.play.py` 的实现是**外部运行时桥**：按需启动宿主 CPython 子进程，行式 JSON-RPC；跨进程只有 `dispatch_one` / `query_rows` 两个动词，其余能力在 bootstrap 内实现；主二进制无 cgo、零新依赖；找不到解释器 → `status="unsupported"`（不假成功）。
- 证据：`playbook-python.md` §1/§2/§9；`docs/e2e-assets/playbook-verbatim/REPORT.md`（120/120 字段一致）与 `REPORT-real-llm.md`（真 LLM 19/19）。

## 1. 对比：外部运行时桥（现状） vs 同进程内嵌 JS/Lua（提议）

| 维度 | 内嵌 JS/Lua（goja / gopher-lua） | 外部进程桥（现状，Python） |
| --- | --- | --- |
| 自包含 | 单二进制可跑脚本（唯一真优势） | 需宿主 Python 3.8+（`HIVE_PYTHON` → `python3` → `python`） |
| 生态复用 | **假复用**：npm 大量包依赖 Node builtins / N-API 原生扩展 / 包解析语义；goja 系只覆盖有限子集；Lua 生态（LuaRocks）远小于 npm | **真复用**：继承宿主 `sys.path` / site-packages，装什么用什么（PyPI 全生态） |
| 语义对齐 | 上游无 JS/Lua 契约；新增 = 新语言新契约，破坏"脚本字节不变"口径 | 已逐字对齐（120/120 + 真 LLM 19/19） |
| 依赖/构建 | goja/gopher-lua 为新 Go 依赖（被禁）；v8go 需 cgo（违反 `CGO_ENABLED=0`） | 零新依赖、零 cgo |
| 安全/隔离 | 不可信脚本进主进程地址空间，沙箱面大 | 崩溃/死循环只杀子进程（一次性进程模型） |
| 维护 | 第二套桥协议/错误语义/超时/调试 | 已有实现与证据 |

## 2. npm 两个问题

- **内嵌 JS 能复用 npm 吗？——不能。** 真复用 npm 需要 Node 运行时语义（builtins、N-API、包解析/版本），内嵌 goja 只能跑"部分 JS"。要真复用只有两条路：外部 Node 进程（与 Python 桥同构）或重造 Node（不现实）。
- **需要 npm 生态吗？——当前不需要。** playbook 的旗舰用法（converge / lane / `dispatch_one` / `query_rows` / tracker / deadletter）标准库即可；重活交给 worker（worker 侧有完整工具面），不在剧本进程堆第三方库。

## 3. PyPI 问题

**可以完全复用，且现状即如此**：`.play.py` 跑在宿主真实 CPython，bootstrap 不动 `sys.path`，第三方 import 继承宿主 site-packages（`playbook-python.md` §234 明示：不做包枚举、不代管依赖；任意特定包可用性取决于宿主环境，本机已实测 3.9.6 / 3.14.7 运行）。

注意事项：
1. 依赖由用户装到该解释器；建议用 venv 并把 `HIVE_PYTHON` 指过去做隔离；
2. 跨进程能力边界刻意只留 2 个 RPC 动词（对齐上游 runner，不是"任意宿主 API"）；
3. 解释器缺失时明确 `unsupported`，不假成功。

## 4. 决策

1. **不内嵌 JS/Lua、不新增 Node 桥**：与两条硬约束冲突（复刻完成前不新增功能；禁新增依赖/cgo），且内嵌方案得不到 npm。
2. 维持现状：native `.play.json` + 外部 `.play.py` 桥。
3. 若未来产品化"多语言 playbook"，优先序：
   - ① 外部运行时桥一视同仁（Node 版桥，语义与 Python 对称）——推荐；
   - ② wasm 路线（wazero 跑 CPython-wasm）——体积/语义成本高；
   - ③ 内嵌 goja——最不推荐（npm 假复用、语义新契约、主进程沙箱面）。

## 5. 复查条件

- 上游出现 JS/Lua playbook 契约（届时按 parity 移植，而非自创）；
- 用户拍板"多语言剧本"立项（新特性，需先完成当前 parity）；
- Python 解释器不可得成为部署阻塞（此时评估 wasm 或 Node 桥，而非内嵌）。

## 6. 成本量级（估算，供立项参考）

| 方案 | 首版成本 | 长期成本 |
| --- | --- | --- |
| Node 外部桥 | ≈ Python 桥（bootstrap + 2 动词 + 测试），1–2 天 + E2E | 低（与 Python 同构） |
| 内嵌 goja | 1 天接入 | 高（沙箱/语义/维护），npm 复用为零 |
| wasm(CPython) | 数天–数周 | 中高（体积/兼容） |
