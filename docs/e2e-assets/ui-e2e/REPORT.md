# UI E2E：界面创建蜂群（Orca 浏览器实测）

- 时间：2026-11-23 01:18 CST
- 环境：本地 serve `127.0.0.1:8788`（新建 hive-embed，含 colony-bootstrap 修复 `9ffd2c70`）
- 工具：Orca 内置浏览器（tab `9690cbc3-5819-43c2-ba10-38f96c06a11f`）

## 步骤与结果（逐字取实测）

| # | 动作 | 结果 |
|---|---|---|
| 0 | 打开 `http://127.0.0.1:8788` | 侧栏显示「蜂群 3」 |
| 1 | 点击侧栏「创建蜂群」[e8] | 弹窗打开：蜂后列表 6 位、名称输入（placeholder「例如 research_team」）、目标输入、`创建`（初始 disabled） |
| 2 | 选择蜂后 `Steve Jobs` [e84] | 选中 |
| 3 | 填写名称 `ui-e2e-011834`（页面 sanitize 为 `ui_e2e_011834`）+ 目标文本 | 输入生效 |
| 4 | 点击 `创建` [e93] | **成功**：侧栏计数变「蜂群 4」，URL 变为 `http://127.0.0.1:8788/#/colony/ui-e2e-011834` |
| 5 | 控制台检查 | 仅 `[SSE] opening/connected /Users/mac/.hive-go/colonies/ui_e2e_011834 session_20261123_011841_07a35d76`；**无 error** |
| 6 | 磁盘核对 | `~/.hive-go/colonies/ui_e2e_011834/` 存在：`metadata.json`、`worker.json`、`tracker/`、`triggers.json`、`skills/`、`conversations/`、`runs/` |

截图：`colony-created.png`（本目录，创建后落地的 colony 页面）。

## 结论

界面创建蜂群端到端通过（修复前为静默 404）。该步骤已纳入发布前必跑项（复盘 Q2）；自动化门禁（Q1 API 旅程）由 lane `journey-e2e` 落盘。

## 附：清理

- 测试创建的 `ui_e2e_011834` 保留供查看（可在 UI 删除）。
- API 级测试 `qc-verify-011811` 已清理（注：bootstrap 路径接受了含 `-` 的名称，但 `DELETE /api/colonies` 要求 `[a-z0-9_]+`——前端已 sanitize，属边缘口径差异，已记入复盘补充）。

## 复跑验证：2026-11-23 01:21 CST（第二次独立实测）

同一台 serve（`127.0.0.1:8788`，`hive-embed` pid 12441，构建自 `9ffd2c70` 之后的 main），
用**不同的 colony 名称**再跑一遍，验证不是一次性侥幸。

| # | 动作 | 实测结果 |
|---|---|---|
| 0 | 打开 `http://127.0.0.1:8788/`，读侧栏基线 | `蜂群 3`（Demo / Demo2 / Ui E2E 011834） |
| 1 | 点击 `创建蜂群` [e119] | 弹窗打开，蜂后列表 6 位、名称/目标输入、`创建` **disabled** |
| 2 | 填写名称 `ui-e2e-012144` | 页面 sanitize 为 `ui_e2e_012144`（连字符→下划线，符合 `[a-z0-9_]+`） |
| 3 | 填写目标文本 | 输入生效 |
| 4 | 选择蜂后 `Steve Jobs` [e195] | `创建` 由 **disabled → enabled** |
| 5 | 点击 `创建` [e204] | 弹窗关闭，URL 跳转 `#/colony/ui-e2e-012144` |
| 6 | 侧栏断言 | `蜂群 3` → **`蜂群 4`**，新增 `link "Ui E2E 012144"` [e215] |
| 7 | colony 页面断言 | 标题 `Ui E2E 012144`（h1）、副标题 `Head of Growth`、会话面板渲染 `数据/计划/自动化/工作节点`，**非错误页** |
| 8 | console 断言 | 新会话 `[SSE] opening → connected /Users/mac/.hive-go/colonies/ui_e2e_012144 session_20261123_012150_ff74c89a`，**无 error** |
| 9 | 磁盘核对 | `~/.hive-go/colonies/ui_e2e_012144/` 七件套齐全，`metadata.json` 的 `name`/`queen_name`/`created_at` 与创建时刻一致 |
| 10 | API 核对 | `GET /api/sessions` 含 `ui_e2e_012144`；`GET /api/colonies/tools-index` 含 `{"name":"ui_e2e_012144","queen_name":"queen_growth","created_at":1795368110.64702}` |

**四面一致**：侧栏列表、colony 页面、`/api/sessions` + `/api/colonies/tools-index`、磁盘目录 ——
全部指向同一个新 colony，`created_at` 两侧对得上（`1795368110.64702`）。说明侧栏/页面不是前端乐观插值，
而是真读到了后端状态。

截图：`colony-created-012144.png`（视口）、`colony-created-012144-full.png`（整页）。

### 一处观察（非阻塞，不计入失败）

复跑前，标签页停在上一轮遗留的 `#/colony/ui-e2e-011834`，且该会话的 SSE 被后端重启打断，console 有一条
`[SSE] error: ... network error status undefined`。导航到根路径并新建后未再复现。属 SSE 断线重连的
日志噪声，与创建旅程无关；但**若要做成无人值守门禁，需容忍陈旧标签页的这条噪声**（或先开新标签页），
否则会误报。

## 复跑验证 #2：2026-11-23 01:27 CST（独立复验：侧栏 + colony 页面）

**目的**：只验证「colony 必须出现在侧栏和 colony 页面」，不依赖创建流程，直接从根路径起跑。

- 环境：`hive-embed` serve `127.0.0.1:8788`；Orca 内置浏览器（tab `9690cbc3-5819-43c2-ba10-38f96c06a11f`，profile `Default`）
- 被测 colony：`ui_e2e_012144`（`created_at 1795368110.64702`，`queen_name queen_growth`）
- 路由 id：`ui-e2e-012144`（`slugToColonyId` 只做 `_` → `-`，不 title-case）

| # | 断言 | 实测 |
|---|---|---|
| 1 | 打开 `http://127.0.0.1:8788/` | 标题 `Hive`，无 loadError |
| 2 | 侧栏计数 | `button "蜂群 4"` — 4 个 colony 全部计入 |
| 3 | **侧栏列表含目标** | `link "Ui E2E 012144" [ref=e244]`（与 `Demo` / `Demo2` / `Ui E2E 011834` 同级） |
| 4 | 点击侧栏条目 | URL `#/colony/ui-e2e-012144` |
| 5 | **colony 页面 h1** | `Ui E2E 012144` |
| 6 | colony 页面副标题 | `Head of Growth`（queen 正确解析） |
| 7 | 会话面板 | `数据` / `计划` / `自动化` / `工作节点` 四页签齐全；**非错误页**（`/not found|404|出错了/` = false） |
| 8 | console | `[SSE] connected: .../ui_e2e_012144 session_20261123_012150_ff74c89a`；无 error |
| 9 | API 对账 | `/api/colonies/tools-index` 含 `ui_e2e_012144`，`created_at 1795368110.64702` 与磁盘 `metadata.json` 完全一致 |
| 10 | 磁盘对账 | 七件套齐全（`conversations`/`metadata.json`/`runs`/`skills`/`tracker`/`triggers.json`/`worker.json`） |

截图：`sidebar-012144-viewport.png`（侧栏）、`colony-page-012144-full.png`（colony 整页）。

## 结论

**PASS**。`ui_e2e_012144` 同时出现在侧栏与 colony 页面；路由 id、页面标题、queen 归属、
API `created_at`、磁盘目录五处指向同一个 colony，页面是读后端状态而非前端乐观插值。

三条独立复跑（01:18 创建、01:21 复跑、01:27 仅验证）结论一致。

## 自动化门禁：`scripts/verify_colony_in_ui.py`

把上面手工点击的步骤固化成可重复执行的脚本。默认无头运行，退出码分级。

### 用法

```bash
cd go_hive/docs/e2e-assets/ui-e2e/scripts

# 验证单个 colony
python3 verify_colony_in_ui.py --colony ui_e2e_012144

# 验证后端报告的每一个 colony（创建后回归用）
python3 verify_colony_in_ui.py --list

# 留证据 / 看着它跑
python3 verify_colony_in_ui.py --colony ui_e2e_012144 --shots ./shots --headed

# 指向别的 serve
HIVE_BASE=http://127.0.0.1:8790 python3 verify_colony_in_ui.py --colony foo
```

### 退出码

| 码 | 含义 |
|---|---|
| `0` | PASS — 全部断言通过 |
| `1` | FAIL — 真实回归（colony 不在侧栏 / 页面 404 / h1 不对 / console 报错） |
| `2` | ENV — 环境问题（serve 没起、colony 不存在、浏览器起不来） |

**为什么分 1 和 2**：把所有红色都归成一个码，跑夜的人会学会忽略红色。
"应用坏了" 和 "服务器没起" 是两件事，必须分开报警。

### 断言内容（8 项）

1. 后端 `/api/colonies/tools-index` 列出该 colony
2. 侧栏存在指向 `#/colony/<route-id>` 的链接
3. colony 页面 `h1` 等于后端 `/api/discover` 返回的显示名
4. 页面不是错误页（无 `not found` / `404` / `Something went wrong`）
5. 路由停在目标 colony
6. queen 在页面上有体现
7. 会话面板四页签渲染
8. 无 console error（`[SSE]` 断线重连噪声**已排除**，见复盘补充）

### 两个刻意的设计

- **显示名从后端取，不本地 title-case。** 第一版用
  `" ".join(w.capitalize() ...)` 猜标题，把 `E2E` 猜成 `E2e`，直接误报 FAIL。
  显示名归后端（`/api/discover`）所有，脚本跟着它走，才不会跟渲染器漂移。
- **`slugToRouteId` 逐字镜像前端 `slugToColonyId`。** 不做额外 sanitize；
  路由 id 的规则只能有一个来源，两边一旦不一致，门禁验的就不是真实路由。

### 自测（门禁本身要能被证伪）

| 场景 | 期望 | 实测 |
|---|---|---|
| 正常 colony | `0` PASS | `0` PASS — 8/8 |
| 故意把 h1 断言改错 | `1` FAIL | `1` FAIL |
| colony 名不存在 | `2` ENV | `2` ENV |
| serve 未启动 | `2` ENV | `2` ENV — `Connection refused` |

### 实测结果（2026-11-23 01:31 CST）

`--list` 跑全部 4 个 colony：**PASS 4/4**（`demo` / `demo2` / `ui_e2e_011834` / `ui_e2e_012144`）。

运行期间侧栏还实时冒出了别的 agent 新建的 `disk-only-test-0127`、`ui-e2e-refresh-probe` —— 
说明列表是实时读后端，不是快照缓存。**副作用**：`--list` 断言的是"运行开始时后端报告的集合"，
并发新建不会导致误报，但并发**删除**可能；无人值守时用它比单 colony 断言更脆，建议门禁仍以
`--colony` 为主，`--list` 用于人工回归巡检。

### 与 `hive-browser` 的关系

本脚本用 Playwright 驱动**本机已装的 Chrome**（`channel="chrome"`，无需额外下载浏览器），
因此不依赖 Hive Browser Bridge 扩展。若团队统一要求走 `hive-browser`，需先在其中安装
`jkpcegnbfimimjodblcemoheedidnppm` —— 当前该扩展**未安装**，`hive-browser status` 报
`connected: false`。两条路径验的是同一个真实页面，二选一即可。
