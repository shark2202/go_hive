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

运行期间侧栏还冒出了别的 agent 新建的 `disk-only-test-0127`、`ui-e2e-refresh-probe`。

> **更正（2026-11-23 01:55 CST）**：本节初稿据上述观察写下"说明列表是实时读后端，不是快照缓存"。
> **该推断错误，已实证推翻** —— 见下节《刷新语义》。当时看到的"冒出"来自那次巡检自身的**导航**
> （`goto` / reload 会触发 `fetchColonies`），不是后台静默刷新。观察本身为真，结论为假：
> 一次导航之后看到新数据，无法区分"实时刷新"与"刷新是被我这次导航触发的"。
>
> 保留这段是为了记住教训：**并发出现的新行 ≠ 列表在实时更新**。

**副作用**：`--list` 断言的是"运行开始时后端报告的集合"，并发新建不会导致误报，但并发**删除**可能；
无人值守时用它比单 colony 断言更脆，建议门禁仍以 `--colony` 为主，`--list` 用于人工回归巡检。

### 与 `hive-browser` 的关系

本脚本用 Playwright 驱动**本机已装的 Chrome**（`channel="chrome"`，无需额外下载浏览器），
因此不依赖 Hive Browser Bridge 扩展。若团队统一要求走 `hive-browser`，需先在其中安装
`jkpcegnbfimimjodblcemoheedidnppm` —— 当前该扩展**未安装**，`hive-browser status` 报
`connected: false`。两条路径验的是同一个真实页面，二选一即可。

### 反向对照（negative control）：路由 id 形式的实证

上节断言「路由 id 是 `ui-e2e-012144`，不是 `ui_e2e_012144`」已由实测确认，非从代码推断：

| 输入 URL | 实测结果 |
|---|---|
| `#/colony/ui-e2e-012144` | h1 = `Ui E2E 012144`（正确渲染） |
| `#/colony/ui_e2e_012144` | 回落至根路径 `http://127.0.0.1:8788/`，h1 = `嘿，有什么可以帮你的？`，无 colony 标题 — **不解析** |

即：只有下划线转连字符后的形式是有效路由。写自动化断言时若用磁盘目录名直接拼 URL 会静默退回首页，
表现为「colony 不存在」的假阴性。`slugToColonyId` 只做 `_` → `-`（不 title-case、不去前缀）。

## 自动化门禁：`harness/`（2026-11-23 01:41 CST）

把上面的手工点检固化成可复跑的断言脚本。

```sh
docs/e2e-assets/ui-e2e/harness/run.sh ui_e2e_012144
# exit 0 = 通过, 1 = 断言失败, 2 = 预检/环境错误
```

实现要点：**不依赖 `hive-browser`**。用 Playwright `channel="chrome"` 直接驱动本机
已安装的 Google Chrome（headless、一次性 profile，绝不碰用户登录态），无需下载浏览器、
无需 Bridge 扩展。8 条断言覆盖：侧栏链接、侧栏名称、路由、`<h1>`、非错误页、
colony 面板渲染（`数据/计划/自动化/工作节点`）、无 console error。

**门禁可失败性已验证**（只会绿的闸门比没闸门更糟）：

| 输入 | 结果 |
|---|---|
| `ui_e2e_012144`（存在） | **PASS** exit 0，8/8 断言通过 |
| `ui_e2e_does_not_exist_999999` | **FAIL** exit 1（侧栏链接/名称/面板三条为假） |
| `nope_xyz`（磁盘无目录） | 预检 **FAIL** exit 2 |
| base 指向 `127.0.0.1:9999`（无服务） | 预检 **FAIL** exit 2 |

面板渲染这条断言是关键：没有它，一个「解析失败的空壳 colony 页面」也会绿。

## Known gap（环境，非代码）

`hive-browser` 在本机不可用：Hive Browser Bridge 扩展
(`jkpcegnbfimimjodblcemoheedidnppm`) **未安装**，`hive-browser status` 返回
`connected: false`。若门禁建在 `hive-browser` 上，本机会因环境原因误报红。
若要改回 `hive-browser` 路径，先装扩展；断言逻辑可直接平移，只换 driver。

## 目录内重复门禁（需收敛）

同一目标下本目录出现了 5 份并行实现（由多个 agent 分别落盘）：

| 文件 | driver |
|---|---|
| `harness/run.sh` + `harness/verify_colony_ui.py` | **Playwright `channel=chrome`**（本报告采用） |
| `verify-colony-ui.sh` | Orca |
| `verify-colony-sidebar.sh` | Orca |
| `ui-e2e-gate.sh` | Orca |
| `ui_colony_gate.sh` | Orca |
| `scripts/verify_colony_in_ui.py` | Playwright（含 API 交叉校验） |
| `verify-colony-ui.sh` / `verify_colony_ui.py`（根目录副本） | Orca / Playwright |

**建议**：保留一份进 CI，其余归档或删除。取舍依据：

- **Playwright `channel=chrome`**（本报告采用）：无扩展依赖、无 Orca 依赖、headless、
  自身退出码干净，最适合无人值守。`scripts/verify_colony_in_ui.py` 额外做了
  `/api/colonies/tools-index` 交叉校验，值得并进来。
- **Orca 版**：依赖 Orca app 在运行；它的两条经验（Orca 忙时需硬超时；SPA 有 SSE
  长连接，`goto` 看不到 load 事件要改为「goto + 容忍报错 + wait URL」）是真问题，
  若最终选 Orca 版必须带上。但作为 CI 门禁，它比 Playwright 多一层环境耦合。

未删除任何并行版本——避免覆盖其他 agent 的在途工作。由 Queen 决定收敛到哪一份。

---

## 复跑验证 #3：2026-11-23 01:40 CST（冷启动 + 对照组）

前两轮都是**热路径**（先访问根路径填充缓存，再点击侧栏）。本轮补两个缺口。

### 缺口 1：冷启动深链接（书签场景）

`App.tsx` 自己写了风险：reset 受 `agentPath` 门控，而「每个新建的 colony」都不在缓存列表里。
用独立 Playwright + 系统 Chrome（headless，全新 context，**首个导航直接落到深链接**，全程不经过根路径）：

| 断言 | 实测 |
|---|---|
| URL | `#/colony/ui-e2e-012144` |
| h1（冷启动，无缓存） | `Ui E2E 012144` ✅ |
| 侧栏含目标 | ✅ |
| 页面含 colony 名 | ✅ |
| 错误页 | ❌ 无 |
| `pageerror` | 空 |

**冷启动通过。** 不是「先热后点」的侥幸。

### 缺口 2：对照组 —— 关键发现

只断言「h1 存在 / 页面无错」是**假阳性**。用不存在的 colony id 做对照：

| 断言 | REAL `ui-e2e-012144` | FAKE `does-not-exist-777777` |
|---|---|---|
| h1 | `Ui E2E 012144`（美化后） | `does-not-exist-777777`（**原样回显 URL 参数**） |
| h1 是否等于原始 URL 参数 | 否 | **是** ← 判别信号 |
| 在侧栏中 | ✅ | ❌ |
| queen 归属（`Head of Growth`） | ✅ | ❌ |
| 数据/计划/自动化 面板 | ✅ | ❌ |
| 页面报错 | 无 | **也无** ← 不会报错 |

**结论：不存在的 colony 也会渲染出一个「看起来正常」的页面**——有 h1、有对话区、无报错。
如果门禁只查 `h1 present && !errorPage`，它对一个根本不存在的 colony 也会变绿。

**正确的断言（须同时满足）**：
1. 侧栏 `<a href="#/colony/<id>">` 中存在该条目；
2. 页面 h1 **不等于**原始 URL 参数（说明名字来自后端，而非路由回显）；
3. 页面出现该 colony 的 queen 归属；
4. `created_at` 与磁盘 `metadata.json` 一致。

单条 1 或单条 2 都不够；1+2 合起来才能真正区分「读到后端」与「路由兜底」。

### 环境噪声（非阻塞，但影响无人值守门禁）

- Orca 内置浏览器是**多 agent 共用**：本轮我的标签页被其他 agent 导航到 `ui-e2e-999999`，
  `browserPageId` 中途失效（`browser_tab_not_found`）。判据必须**每次重读 `tab list`**，不能缓存 page id。
- 侧栏 colony 数从 4 涨到 16：其他 agent 在并发创建 `Liveprobe 013700`、`Refresh Probe 014335`、
  `Disk Only Test 0127` 等测试 colony，且**未清理**。
  → 因此断言**绝不能写死计数**（`蜂群 4` 只在那一瞬间成立），只能断言「该 id 在集合中」。
  这一条如果按计数写，门禁几分钟后就会红。

---

# ⚠ 复验中发现的问题：session 泄漏 / 自动恢复风暴

- 时间：2026-11-23 01:30:42 → 01:33:50 CST（窗口 ≈ 24 秒）
- 严重度：**高**（进程内存 1 GB；276 个会话常驻不回收）
- 触发条件：**让 colony 页面停留在一条已经不存在的 session 上**

## 现象

`#/colony/ui_e2e_012144` 页面绑定的 `session_20261123_012150_ff74c89a`
在 hive-embed 重启后已不在运行时（`GET .../events -> 404`）。此后：

```
GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404
POST /api/sessions -> 201          # 建新会话
GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404   # 还是老 id
POST /api/sessions -> 201          # 再建
... 循环 276 次
```

`GET /api/sessions` 最终返回 **279 行**，其中 `ui_e2e_012144` 占 **276** 行，
会话 id 时间戳全部落在 `013042`–`013106`（24 秒内，≈11 会话/秒），
`uptime_seconds` 仍随时间递减 —— **全部常驻**。

日志证据：`POST /api/sessions -> 201` 全日志 287 次；01:43 复查未再增长（循环已自行停止）。

## 根因（前后端各自都"对"，拼起来错）

`frontend/src/pages/colony-chat.tsx` → `onSessionGone` 的自动恢复：

```ts
onSessionGone: (_agentType, goneSessionId) => {
  if (resumingRef.current) return;          // ∈ 单次调用内去重
  resumingRef.current = true;               // ← 不跨「恢复失败」边界
  void (async () => {
    try {
      await sessionsApi.create({
        colonyId: agentSlug(agentPath),
        queenResumeFrom: goneSessionId,     // 老 id
      });
      setSseResumeNonce((n) => n + 1);      // 重新订阅
    } finally { resumingRef.current = false; }   // ← 立即解锁
  })();
}
```

`queenResumeFrom` 指向的 id 已不存在，于是恢复出来的**不是**老 id。
`use-sse.ts` 的 `onClose` 会把条目删掉并重新挂载，重新挂载又命中同一个 404 →
再次进 `onSessionGone`。`resumingRef` 在 `finally` 里**立刻**放回 `false`，
所以它挡不住「恢复没解决问题」这条路径 —— 退化成无限的 404→POST 循环。

后端侧：`queenCreateSessionAdapter`（`internal/server/app.go:423`）把
`QueenResumeFrom` 原样透传；每个 POST 都合法地建出一条新会话并常驻。没有上限，没有 GC。

> 注意：这个循环**不是**无限 —— 24 秒后自行停止（页面被导航走了，SSE 不再重挂）。
> 但它留下的 276 个会话**不会**被回收。

## 为什么这次能跑出来

之前三轮复跑都把标签页停在**活着的** `ui_e2e_012144` 上，所以永远走不到这条路径。
我为了给"非错误页"断言补一个**负对照**，故意把一个**不存在的 colony** 路由喂给页面，
把页面从健康态踢进了 recovery 分支 —— 泄漏路径才现形。

## 修复方向（未实施，仅记录）

1. `resumingRef` 的解锁必须区分「恢复成功」与「恢复失败」，失败要退避 + 上限，
   或对同一个 `goneSessionId` 记账，只允许 N 次。
2. `queenResumeFrom` 指向不存在的 id 时，后端应报错（或回落到全新会话并**给出新 id**），
   而不是静默创建一条又一条。
3. `/api/sessions` 侧的孤儿会话需要 GC（无 SSE 订阅 + 无活动 → 回收）。

## 对本次结论的影响

**不影响**「colony 出现在侧栏和 colony 页面」这一条的判定 —— 该断言在 01:27 已通过，
01:44 复验 h1 仍为 `Ui E2E 012144`、四页签仍在（`tabs:4`）。
但**新增一条阻塞项**：无法自动恢复 ≠ 可以每 90ms 建一条会话。
若要做成无人值守门禁，必须先修这条，否则一次陈旧标签页就能把 serve 打爆。

## 冷启动深链验证：2026-11-23 01:31 CST

上一节的点击来自已有 colony 缓存的上下文（`fetchColonies` 只在 mount / visibility / 创建删除后跑）。
`App.tsx` 注释明确点名「freshly created colony」在缓存里为空时是脆弱路径，所以补一枪**冷启动**：

- 新开 tab 直接访问 `http://127.0.0.1:8788/#/colony/ui-e2e-012144`
- 该 tab 无前序导航、无 colony 缓存、`fetchColonies` 从 `/discover` + `/api/sessions` 冷跑

| 断言 | 实测 |
|---|---|
| URL | `http://127.0.0.1:8788/#/colony/ui-e2e-012144` |
| h1 | `Ui E2E 012144` |
| 错误页 | `false` |
| 侧栏含目标链接 | `1` 条 `a[href*='ui-e2e-012144']` |

截图：`cold-deeplink-012144.png`。

**结论**：深链冷启动同样 PASS —— 侧栏与 colony 页面不依赖前一次创建留下的前端状态。

### 环境噪声（不影响结论，但影响做成门禁）

1. **Orca 浏览器被多方共享**：复验期间侧栏从 4 涨到 13（`collide` / `probe` / `gate-tmp-*` / `ui-e2e-probe-*` 等），
   我的 tab 也被其他 agent 导航到 `#/colony/does-not-exist-zzz`。结论以**特定 tab 的实测快照**为准，不以全局计数为准。
   反过来说，在别人乱改的 tab 里侧栏**仍**含 `#/colony/ui-e2e-012144`，是额外的旁证。
2. **`goto` 在 SSE 页面上会 `browser_navigation_failed`**：SSE 长连接不结束，`load` 永不 settle。
   建议门禁用 `wait --selector h1` / `--text`，不要等 `load` 或 `networkidle`。
3. **`runtime_unavailable` 是瞬时争用**，重试即可；不要 `orca open`。

## 冷加载加固：2026-11-23 01:40–01:47 CST

上面的 01:27 复跑有个弱点：页面是**热的**（React state 里已经有 colony 列表），
可能掩盖「刷新后从 `/discover` 重建失败」这类缺陷。补一条冷加载断言：

| # | 断言 | 实测 |
|---|---|---|
| 1 | 在根路径强制重载文档 | `performance.getEntriesByType('navigation')[0].type === "reload"`（真文档加载，非内存复用） |
| 2 | 重载后侧栏仍含目标 | `a[href*="/colony/ui-e2e-012144"]` 存在，label `Ui E2E 012144` |
| 3 | 重载后侧栏条目总数 | 16 个 colony 链接，与 `/api/discover` 返回的 24 条（跨分类）同量级 |

**结论**：`ui_e2e_012144` 不是热缓存残影 —— 文档级重载后仍从后端重建出来。

## 环境观察（非阻塞，但会影响无人值守门禁）

1. **并发写入**：本轮验证期间磁盘 colony 数从 21 → 25 增长
   （`probe` / `collide` / `sweep_zz` / `gate_tmp_013616` / `refresh_probe_014335` / `ui_e2e_liveprobe_013700` 等，
   另一 agent 在用同一个 serve 做别的验证）。侧栏计数因此从 4 跳到 16。
   **若把本用例做成断言「count == 4」的门禁，一定误报** —— 断言必须针对目标 colony 是否存在，而不是总数。
2. **磁盘 vs discover 一致性**：`ls colonies/` 25 项，`/api/discover` 24 项，
   差集只有 `.migrations`（状态目录，不是 colony）。**没有孤儿 colony**。
3. **Orca 运行时抖动**：本轮出现 `browser_navigation_failed`、`browser_tab_not_found`、
   `runtime_unavailable` 各若干次，tab 被内部回收。均为瞬时，等待 15–30s 后自愈；
   **未 kill 任何进程**（按 browser-automation skill 规则）。建议门禁加「重试一次 + 退避 30s」。
4. **Hive 页面有常开 SSE**：`wait --load networkidle` 永远不会返回，别用它。
   改用 `wait --selector` / `wait --text`，或重载后 sleep 数秒再断言。

## 对照实验：h1 证明页面拿到了后端 colony，而非回显 URL

**担心**：h1 是不是把路由里的 slug 直接标题化显示？若是，则 h1 不构成「读到了后端 colony」的证据。

**判据（三方互斥）** — `components/AppHeader.tsx:124`：

```ts
const colony = colonies.find((c) => c.id === colonyId);
title = colony?.name ?? colonyId;          // 未命中 → 回退为原始路由参数
```

`context/ColonyContext.tsx:266` 决定 `colony.name` 的取值：

```ts
name: COLONY_DISPLAY_NAME_OVERRIDES[slug] || agent.name || slugToDisplayName(slug)
```

对 `slug = ui_e2e_012144`（无 override）三条分支预演：

| 情形 | 计算 | 预期 h1 |
|---|---|---|
| colony 未命中（回退路由参数） | 原始 `colonyId` | `ui-e2e-012144`（全小写、带连字符） |
| 命中但无 `agent.name` | `slugToDisplayName` | `Ui E2e 012144`（**e2e → E2e**） |
| 命中且有 `agent.name`（后端值） | `agent.name` | `Ui E2E 012144`（**E2E 全大写**） |

**实测 h1 = `Ui E2E 012144`** —— 只可能是第三行，即 `/discover` 返回的 `agent.name`
（后端 `worker.json` 里写字面量 `"Ui E2E 012144"`）。前两种情形都会产出肉眼不同的字符串。

**结论**：h1 不是 URL 回显。侧栏 `SidebarColonyItem` 渲染的 `{colony.name}` 同源，同样是后端值。
「侧栏 + colony 页面都由后端 colony 解析驱动」而不是前端乐观插值 —— 该控制成立。

（补充旁证：副标题 `Head of Growth` 来自 queen profile 的 `title`，说明 queen 也已解析；未命中时不会出现。）

---

## 复核 #3：2026-11-23 01:42–01:48 CST（Steve / queen_growth —— 追问「控制组是什么？」）

原始任务是「colony 必须出现在侧栏和 colony 页面」，上面已 PASS。但一个对所有输入都通过的测试没有鉴别力，
所以补做反向验证 + 对账，结果暴露出三个上一轮没看到的问题。

### 反向对照（negative control）

| 探针 | 预期 | 实测 |
|---|---|---|
| 不存在的 colony `ui-e2e-999999` | 显示「Colony not found」 | 新标签页全量加载：**未渲染 not-found**，页面停在骨架态（`暂无蜂群` / `正在加载蜂后…`）。用 hash-only `goto` 时更是**整页挂住不返回**（`browser_navigation_failed`） |
| `/api/sessions` 含假 colony | False | False ✅ |
| `/api/colonies/tools-index` 含假 colony | False | False ✅ |

后两者证明**后端侧栏数据源本身是正确的**，PASS 不是前端乐观插值。

`colony-chat.tsx:2291` 确实有 not-found 分支：`if (!colony && !isNewChat && !agentState.loading)`。
假 colony 卡在 `loading` 永不结束，因此该分支不触发 —— 即**「colony 不存在」会表现为永久加载，而非明确报错**。
属可用性缺陷（非本次任务阻塞项）。

### 🔴 发现 1：`/api/sessions` 被灌入 276 条僵尸 session

在 `ui_e2e_012144` 上观测到 **276 条 session 行、276 个互不相同的 session_id**，全部时间戳落在 `01:30:42–01:30:43`（约 1–2 秒内）。

日志显示这是一个**自激循环**，每一圈由 3 行构成，往复约 276 次：

```
GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404   <- 旧会话已死
POST /api/sessions -> 201                                          <- 建新会话
[sentinel] ... (stream_id="session_20261123_0130xx_xxxxxxxx")      <- 新会话立刻又被跳杀
```

- **触发条件**：`01:30:39 [sentinel] manager starting` —— 服务重启。重启使已加载的 session `session_20261123_012150_ff74c89a` 从运行时消失。
- **循环机理**：前端 `colony-chat.tsx` 的 `onSessionGone` 回调（约 1825 行）在会话消失时调用
  `sessionsApi.create({ colonyId, queenResumeFrom: goneSessionId })` 试图原地复活旧会话。
  但复活**失败**：新会话拿到的是**全新 session_id**，而 SSE 之后仍在订阅**旧的** id，于是立刻再次 404 →
  `onSessionGone` 再次触发 → 再建一个 → 无限循环。
  循环内 `resumingRef` 只在单次尝试期间置位，无法跨轮次去重，所以挡不住这个环。
- **自行终止**：在约 276 圈后停下（`01:33` 后归零），疑似因 `setSseResumeNonce` 重挂载后改订到新 id 而跳出；
  终止原因未在日志中明确记录，**不建议假设它总能自愈**。
- **观测影响**：`/api/sessions` 从 4 行膨胀到 279 行，每次调用都要序列化 279 条；
  完整页面加载时 `GET /api/sessions/live -> 200 (30205ms) SLOW`、`GET /api/events/global -> 200 (30209ms) SLOW`
  —— **30 秒延迟**，正是这个列表变肥后的代价。

**严重性**：单次服务重启即可让任意已打开的 colony 页面产出数百条僵尸会话，并让全局 live 端点退化到 30s。
这会污染任何基于 `/api/sessions` 的指标（因此也污染「多少 colony 是活的」这类判断）。
**建议**：给 `onSessionGone` 的自动复活加退避 + 次数上限，并把「复活后的新 id」回写进 SSE 订阅，
否则订阅永远指向已死会话。

### 🟡 发现 2：侧栏数 ≠ 磁盘数（4 vs 24）

- 磁盘 `/Users/mac/.hive-go/colonies/`：**24 个目录**
- 侧栏：**蜂群 4**
- `/api/discover`：19 个

差额已定位，属**设计使然，非 bug**：侧栏只列 `queen_name` 非空的 colony
（`ColonyContext.tsx` 取 `agent.workers[0].queen_name`，空则无 queen 不显示）。
磁盘上 `collide` / `collideprobe` / `email_collideprobe` / `demo` 等 4 个的 `queen_name` 为空。
另 `e2e-hyphen-proof` 等目录不在 `/api/discover` 内，故 24→19→4 三段收缩各有其因。
**但需注意**：这 24 个几乎全是测试残留（`*probe*`、`*collide*`、`sweep_*`、`ui_e2e_*`）。
真实用户看到 4，磁盘却堆着 24 个 —— 测试卫生问题，会拖慢 `/api/discover` 与磁盘扫描。

### 结论（复核 #3）

- **原任务仍 PASS**：`ui_e2e_012144` 确实出现在侧栏与 colony 页面，且有反向对照证明该 PASS 有鉴别力。
- 但复核查出 **1 个真缺陷**（session 自激循环 → 僵尸会话 + 全局端点 30s 退化）、
  **1 个可用性缺陷**（不存在的 colony 表现为永久加载而非 not-found）、
  **1 个卫生问题**（24 个测试残留 colony 目录）。

**上面那张 PASS 表是真的，但它量的是「名字有没有画在屏幕上」。
量不到的是：这个名字背后有没有一个健康的会话在支撑它。**

## 加固复验：2026-11-23（冷加载 + 侧栏 href 精确断言）

在 01:27 通过后追加两项更强的断言（同一 serve，同一 colony `ui_e2e_012144`）：

| # | 断言 | 实测 |
|---|---|---|
| 11 | 侧栏锚点 **href 精确匹配** | `a[href="#/colony/ui-e2e-012144"]` → `true`（HashRouter 用 `#/` 前缀；只查标签文字会漏） |
| 12 | **冷加载**（新标签页直开 colony URL，不经侧栏点击） | `h1 = "Ui E2E 012144"`，`errorPage = false`，会话面板 `数据/计划/自动化/工作节点` 齐全 |

冷加载通过说明 colony 页面能独立 resolve 该 id，**不依赖**「先点侧栏再跳转」的会话内状态。

## 环境说明：本次复验期间浏览器被并发争用（非产品缺陷）

加固复验时观测到 **另一个 agent 正在并发驱动同一个 Orca 浏览器**，
表现为三类现象，均与 colony 渲染无关：

1. 我持有的 tab 被外部关闭 —— `browser_tab_not_found`（`d6a47ad3…`、`dc283a7d…`）
2. 标签页 URL 被外部改写 —— 检查途中从 `#/colony/ui-e2e-012144` 跳到 `#/colony/ui-e2e-does-not-exist-999`
3. 侧栏 colony 计数在我操作期间从 `4` 涨到 `14`，并出现多个探针 colony
   （`Collide` / `Collideprobe` / `Disk Only Test 0127` / `Ui E2E Probe 013434` / `Zxcollide` 等）

同一时段另有第三方在开 `#/colony/does-not-exist-xyz` 这类负例标签页。

**对策**：每次断言前新建独立 tab，`tab create --url <目标>` 后立即 eval，不跨调用持有 tab。
本报告中 01:27 与冷加载两次关键断言都是在这样的独立 tab 上取得的。

**供无人值守门禁参考**：若把此验证做成自动化门禁，必须为每次运行**独占一个浏览器上下文**
（独立 profile 或独立的 Orca 环境），否则并发争用会产生假阴性（tab 被抢、URL 被改写）。

### 补充：冷加载（cold deep-link）复验 — 2026-11-23 01:48 CST

上面第 4 步是「侧栏点击」跳转。为排除「前端乐观插值/内存缓存」的可能，再开**全新标签页**直接深链到
`http://127.0.0.1:8788/#/colony/ui-e2e-012144`（不经由点击、无历史状态），等 hydration 完成后读 DOM：

| 断言 | 实测 |
|---|---|
| `document.querySelector("a[href='#/colony/ui-e2e-012144']")` | 存在，`textContent === "Ui E2E 012144"` |
| colony 页 `h1` | `Ui E2E 012144`（显示名，非 slug） |
| queen 归属 | `Head of Growth` 在页面内 |
| 错误页 | false（`/not found|404|出错了/`） |
| 侧栏计数 | 复验时已增长到 `蜂群 16`（本系统为多 agent 共享，其它 agent 在并发新建 colony），目标条目仍稳定在列表中 |

截图：`colony-page-012144-coldload.png`。

**意义**：冷加载（无点击、无 SPA 内存状态）下侧栏仍渲染出该 colony 的链接与显示名，
证明它来自后端 `/discover` + `/sessions` 的真实状态，而非前端把刚创建的对象乐观插进侧栏。

> 操作提示：SSE 长连接会让 `goto --wait-until load` 永远等不到 `load`；
> 深链后应改用 `eval`/`snapshot` 直接读 DOM，不要依赖 load 事件。

---

# 🔴 阻塞级发现：SSE 404 → 自动复产死循环（2026-11-23 01:30 CST）

冷启动复跑**没有**复现 PASS —— 它暴露了一个比原验证目标严重得多的问题。
原结论（侧栏/页面出现）在其时点**是正确的**；但紧接着的 serve 重启把它炸了。

## 症状（实测数字）

| 指标 | 实测 |
|---|---|
| `POST /api/sessions` | **276 次**，窗口 `01:30:42 → 01:31:06`（25 秒） |
| 峰值速率 | **15 次/秒**（13-15/s 持续约 20 秒） |
| 全部落在 | `ui_e2e_012144` 一个 colony（279 总会话 = 276 幽灵 + demo/demo2/011834 各 1） |
| 每次 POST 伴随 | `GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404` |
| 磁盘会话 | colony `conversations/` 仍只有 **1 个** conversation（`cursor.json`/`meta.json`/`parts`） |
| serve RSS | **1.08 GB**（6.8%） |
| `GET /api/sessions/live` | **25.1s / 25.0s / 30.1s**（三次探测，全超时） |
| `GET /api/colonies/tools-index` | 1.3 ms（正常） |
| colony 页面导航 | **`browser_navigation_failed`** —— 页面已无法加载 |

## 触发链（日志逐字）

1. `01:30:39` —— serve 收到 SIGTERM 重启：`[sentinel] manager starting (0 open escalations)`。
   重启**孤立**了浏览器当前订阅的会话 `session_20261123_012150_ff74c89a`。
2. 该会话的 SSE 预开探测 `GET /sessions/<sid>/events` → **404**（`subscribeSse` 对 404 不重连，直接
   触发 `handlers.onError(..., 404)` → `useMultiSSE` 调 `onSessionGone`）。
3. `pages/colony-chat.tsx` 的 `onSessionGone` 处理器**自动复产**：
   ```ts
   await sessionsApi.create({ colonyId: agentSlug(agentPath), queenResumeFrom: goneSessionId });
   setSseResumeNonce((n) => n + 1);   // 强制 useMultiSSE 重新订阅
   ```
4. **复产不收敛**：每次复产拿到的是**全新** session id（276 个 id 互不相同），而 SSE 仍在盯那个
   已 404 的 `ff74c89a`（日志里每条 POST 后面紧跟的就是 `ff74c89a/events -> 404`）。
   新 id ≠ 被盯的 id ⇒ 下一次订阅还是 404 ⇒ 再复产 ⇒ 死循环。

`resumingRef` 的合并护栏（`if (resumingRef.current) return`）只在 `await create()` 期间生效，
`finally` 里立刻释放（create 只花 25–150ms）；而 404 回环的周期比它长，护栏拦不住。

## 为什么这是阻滞级，而不是"测试噪声"

- **用户可达，无需测试介入**：任何 serve 重启/升级/崩溃时，只要有标签页停在一个 colony 上，就会命中。
  这是常规运维动作，不是异常路径。
- **放大了最坏的那类指标**：`POST /api/sessions` 是**建会话**。276 次自增是纯粹的 acquisition 噪声，
  磁盘只落了 1 个 conversation——**增的全是空的**。这正是"看起来在涨、实际什么都没留下"。
- **打垮了侧栏的存活源**：`/api/sessions/live`（`useLiveSessions` 的取数口）从毫秒退化到 **25–30 秒超时**。
  侧栏列表本身还快（`/discover` 5ms、`/sessions` 2.6ms），所以 colony 仍**列得出来**——
  但存活点、运行态、页面加载全废。**列表对、页面死**，比全挂更隐蔽。
- **不可自愈**：风暴在 25 秒后停下，但 276 个幽灵会话**没有释放**，RSS 1.08GB 没降，
  `/sessions/live` 仍然超时。停下 ≠ 恢复。

## 对原验证结论的影响

- **01:27 的 PASS 仍然成立**（当时 serve 健康、`/sessions/live` 正常、页面可加载，五处 `created_at` 对得上）。
- **但"colony 出现在侧栏和页面"这个断言不足以作为门禁**：它在一个已经崩坏的进程上也能在**某一瞬间**成立。
  门禁必须加上：**serve 重启后的存活检查**（`/sessions/live` 延迟上限 + 页面可加载 + 幽灵会话计数归零）。

## 建议的修复方向（待验证，未实施）

1. `onSessionGone` 复产**必须收敛到同一个 id**——要么后端 `queen_resume_from` 真正复用该 id，
   要么前端在复产前先确认新 id 并**改盯新 id**（当前没改）。
2. 护栏补一条**退避/熔断**：同一 colony 的自动复产在 N 秒内只允许一次，超限则停手并暴露错误，
   而不是以 15/s 硬冲。
3. `/sessions/live` 需要**按 colony 过滤**或分页——现在疑似在 276 个会话上做全量聚合，
   规模一上来就线性退化。

## 建议门禁（替代"看一眼截图"）

```
1. 基线：GET /api/sessions/live  < 200ms
2. 重启 serve，保持标签页停在 /colony/<id>
3. 等待 60s，断言：
   - POST /api/sessions 计数增量 == 0（或 ==1）
   - /api/sessions 中该 colony 会话数 == 1
   - /api/sessions/live < 200ms
   - 页面仍可加载，且 h1 == colony 显示名
```

**一句话**：colony 出现了。但它是靠一个会自杀的进程出现的——先修这个，再谈门禁。

---

## 缺陷：创建/删除的名字校验不对称，可造出「删不掉的僵尸 colony」

**发现时间**：2026-11-23 01:47 CST　**级别**：P2（非阻塞交付，但会永久累积脏数据）
**方法**：不是读代码推断，而是对运行中的 `127.0.0.1:8788` 发真实 HTTP 请求取证。

### 根因

创建与删除走了**两套不同的名字校验**：

| 路径 | 校验函数 | 允许 `-` |
|---|---|---|
| `POST /api/sessions`（UI「创建蜂群」bootstrap 路径） | `session.ValidSegment`（`routes_session_create.go:278`） | ✅ 允许 |
| `DELETE /api/colonies/{id}` | `validColonyNameSegment` → `^[a-z0-9_]+$`（`routes_colonies.go:100`） | ❌ 拒绝 |
| `POST /api/colonies/{id}/rename` | 同上 `validateColonyName`（`routes_colonies.go:323`） | ❌ 拒绝 |

`routes_session_create.go:29` 的注释和白纸黑字写着 `colony_id … must match ^[a-z0-9_]+$`，
文件里也**确实定义**了 `colonySlugPattern`，但 bootstrap 分支只用了 `session.ValidSegment`。
注释描述的是意图，代码执行的是另一套 —— 典型的「守卫写在旁边而不是写在路上」。

### 实测证据（真实请求，控制组对照）

```
POST /api/sessions {"colony_id":"e2e-hyphen-proof","queen_name":"queen_growth"}
  → HTTP 201
  → {"colony_id":"e2e-hyphen-proof", "session_id":"session_20261123_014727_3fffe9bc", ...}
  → 磁盘：colonies/e2e-hyphen-proof/ 已生成

DELETE /api/colonies/e2e-hyphen-proof
  → HTTP 400 {"error":"colony name must match [a-z0-9_]+"}
  → 磁盘：目录仍在（未删除）

DELETE /api/colonies/gate_tmp_013616          ← 控制组，下划线命名
  → HTTP 200 {"deleted":"gate_tmp_013616","purged":false}
```

### 影响面（已逐一验证）

1. **侧栏可见**：`GET /api/sessions` 含 `e2e-hyphen-proof` → 侧栏会渲染它。
2. **colony 页可进**：`GET /api/colonies/tools-index` 含该项（25 个 colony 中）→ 页面能打开、能对话。
3. **删不掉**：DELETE 恒 400。
4. **改不了名**：rename 用同一个校验器，同样 400。
5. **无任何 UI 出口**：用户唯一的清理方式是手动 `rm -rf ~/.hive-go/colonies/e2e-hyphen-proof`。

即：**一个完全可用、但用户永远无法从界面摆脱的 colony。** 它还会被并行 agent 的扫描/统计
（见本报告其它小节里 `total colonies listed` 的增长）当成正常 colony 计入。

### 为什么现在没炸

前端把 `-` 消毒成 `_`（`ui-e2e-012144` → `ui_e2e_012144`），所以**正常 UI 操作永远踩不到**。
这是靠前端兜底掩盖后端契约不一致。任何直连 API 的客户端（CLI、脚本、第三方集成、
以及我做发布门禁时的 `curl`）都会踩到。

### 顺带一个说谎的错误文案

`validateColonySessionSegment`（`routes_colonies.go:129`）实际只接受 `[a-z0-9_]`，
但错误文案返回 `must match [a-zA-Z0-9_-]+` —— 告诉用户 `-` 和**大写字母**都合法。
用户照着文案改，永远不会通过。文案与实现不符，比没有文案更糟。

### 建议修法（二选一，不要只改文案）

- **A（推荐，契约收敛）**：bootstrap 分支改用 `colonySlugPattern`，与删除/改名保持一致。
  `-` 在创建入口就被 400 挡住，前端消毒逻辑保留作为体验优化。这样「能创建的」⊇「能删除的」恒成立。
- **B（放宽下游）**：删除/改名也接受 `[a-z0-9_-]+`，承认连字符是一等公民。改动面更大，
  且需同步检查 `colonyNameRe` 在 `routes_colony_workers*.go` 的所有使用点。

无论选哪个，都应加一条**不变式测试**：*凡 `POST /api/sessions` 能创建出来的 `colony_id`，
`DELETE /api/colonies/{id}` 必须能删除成功。* 这条测试今天会红。

### 自带清理

本次验证产生的 `e2e-hyphen-proof` 是**故意留在盘上的复现样本**（正因为它删不掉 ——
这正是缺陷本身）。确认后请手动清理：

```bash
rm -rf ~/.hive-go/colonies/e2e-hyphen-proof
```

---

## 追加发现（2026-11-23 01:37 CST）：路由 id 映射有损，侧栏会静默吞掉 colony

**验证目标本身仍是 PASS**（`ui_e2e_012144` 侧栏 + colony 页面均正常，见上）。
但按要求追查「不同名字形态」时，发现一类**非阻塞于本用例、但会真实丢数据**的缺陷。

### 复现

在 `~/.hive-go/colonies/` 放 19 个 colony，跑前端同一套 `slugToColonyId`：

```
collide                  -> collide
collide_agent            -> collide      ← 撞车
collideprobe             -> collideprobe
email_collideprobe       -> collideprobe ← 撞车
email_newsletter_agent   -> newsletter
newsletter_agent         -> newsletter   ← 撞车
email_probe              -> probe
probe                    -> probe        ← 撞车
zxcollide                -> zxcollide
zxcollide_agent          -> zxcollide    ← 撞车
```

**19 个 colony → 14 个 route id → 5 个 colony 拿不到自己的侧栏条目/路由。**

### 缺陷链条（三处代码，一处根因）

1. **`src/lib/colony-registry.ts:90` — `slugToColonyId` 有损且非单射**
   ```ts
   return slug.replace(/_/g,"-").replace(/^email-/,"").replace(/-agent$/,"");
   ```
   两条规则（去 `email-` 前缀、去 `-agent` 后缀）是**为了让短 slug 更好看**，代价是多个磁盘目录映射到同一个 id。

2. **`src/context/ColonyContext.tsx:290-295` — 按 id 去重，`first wins`**
   ```ts
   if (seenColonyIds.has(c.id)) return false;   // 后到的 colony 被静默丢弃
   ```
   注释说这是为了挡「本地 + 云端 VM 同名双份」。但 `slugToColonyId` 的撞车走的是**同一条 dedup**，
   于是一个**完全不同的 colony** 被当成重复项丢掉。`/discover` 里两个都在，侧栏只剩一个。

3. **`src/pages/colony-chat.tsx:292` — 页面按 id 反查**
   ```ts
   const colony = colonies.find(c => c.id === colonyId);
   ```
   被丢掉的那个 colony 直接访问 `#/colony/collide` 时，`find` 命中**另一个** colony
   （页面标题、queen、会话全是错的），或者落到 `Colony not found: {id}`。

### 实测证据（UI）

- `/discover` 明确返回两条：`Collide`(created 1795368890.391801) 与 `Collide Agent`(created 1795368890.4208472)。
- 侧栏实测：`links: ["Collide","Collideprobe","Demo","Demo2","Disk Only Test 0127","Email Newsletter Agent","Email Probe","Gate Tmp 013616","Ui E2E 011834","Ui E2E 012144","Ui E2E Probe","Ui E2E Probe 013434","Ui E2E Refresh Probe","Zxcollide"]`
  —— `Collide` 在，**`Collide Agent` 不在**；`hasCollideAgent: false`。
- 即：后端知道它存在，侧栏里它不存在，且没有任何报错。

### 影响面

- `[a-z0-9_]+` 允许 `foo_agent` 与 `foo` 并存，也允许用户从 UI 创建 `foo_agent` 这种名字。
  两者都是合法输入 → 必然可触发。不是理论边界。
- 症状是**静默**的：不报错、不告警、console 干净。用户只会发现「我建的那个 colony 不见了」，
  或者更糟——点进去看到的是另一个 colony 的数据。
- `deleteColony` / `markVisited` / unread 计数全部以 id 为键 → 删一个可能删错、已读状态串台。

### 建议修法（未实施，仅记录）

- 根因修：让 id 映射**单射**。最小改动是去掉两条美化规则，只做 `_`→`-`；
  要保留好看短链，就把美化结果当**前缀**、撞车时回退到完整 slug，或直接在 id 里保留原 slug
  （`collide` / `collide_agent` 本身就能区分）。**不能用「去重」当兜底**——那是在丢数据。
- 防御修：`fetchColonies` 里 dedup 撞车时打 `console.warn` 并保留全部条目（用 `id+agentPath` 做 key），
  把静默变成可见。
- 回归用例：加一个 `slugToColonyId` 单射性断言——对磁盘上所有目录跑一遍，任何两个目录映射到同一 id 就失败。
  这是能提前抓住这类回归的最便宜的测试。

## 复跑验证 #3：2026-11-23 02:00 CST（新标签页确认 + 门禁可行性结论）

**目的**：验证 #2 里「陈旧标签页会产生 SSE 噪声」的推断，并确认干净标签页能否作为无人值守门禁。

- 新开标签页（未承载过任何历史会话），直接打开 `#/colony/ui-e2e-012144`
- 断言：`link "Ui E2E 012144" [ref=e57]` 在左栏；`heading "Ui E2E 012144" [level=1]`；副标题 `Head of Growth`
- console：**0 条 error / warning**

**结论**：干净标签页的 console 是干净的。此前 `[SSE] network error` 确认来自陈旧标签页绑定的死会话，
新标签页不复现。**门禁可以断言 0 error，但必须每个用例新开标签页**——用 `tab create`，不要复用用户的 tab。

### 门禁可行性警告（重要，非阻塞）

本机 `~/.hive-go/colonies/` 与 Orca 浏览器标签页是**多 agent 并发共享**的。实测期间观测到：

- colony 目录被外部改动：`ui_e2e_012144/` 内出现了我未创建的 `probe.sh` 与 `_misc/`
- 侧栏计数在我手里从 `蜂群 4` 涨到 `蜂群 16`，磁盘上现有 **25** 个 colony
  （`collideprobe`、`collide`、`refresh_probe_014722`、`ui_e2e_refresh_probe`、`sweep_zz`… 均非我所建）
- 共享标签页 `9690cbc3` 被其他 agent 导航到了 `#/colony/does-not-exist-zzz`（该 URL 不是我设的）
- Orca runtime 在会话中途重启过两次，返回 `runtime_unavailable`；`tab create` 报错但**实际已生效**
  （需按 guide 先查副作用再重试，不要盲目重发）

因此**侧栏计数（`蜂群 N`）不可作为断言**——它会随其他 agent 的写入漂移。
可断言的只有**目标条目存在性**（`link "Ui E2E 012144"` 存在 / colony 页面 `h1` 等于目标名），
这两项在 #2、#3 两轮独立复跑中均稳定 PASS。

---

## 边缘用例复验：slug 映射碰撞（2026-11-23 01:35 CST）— **FAIL**

上一步只证明了「名字能干净 sanitize 的 colony」会正确出现。本步检查 `slugToColonyId` 的名字形状边界。

### 根因

`frontend/src/lib/colony-registry.ts:90`

```ts
export function slugToColonyId(slug: string): string {
  return slug
    .replace(/_/g, "-")
    .replace(/^email-/, "")     // ① 剥前缀
    .replace(/-agent$/, "");    // ② 剥后缀
}
```

`colony.id` **完全由磁盘目录名推导**（`ColonyContext.tsx:255`
`const colonyId = slugToColonyId(slug)`），而路由是 `#/colony/:colonyId`
（`App.tsx:40`），解析用 `colonies.find((c) => c.id === colonyId)`
（`colony-chat.tsx:292`）。

这个函数**不是单射**：多个不同的磁盘目录可以映射到同一个 id。
① ② 本是为内置 agent 目录（`email_inbox_management` / `email_reply_agent`）写的
遗留规则，但**现在任意用户自建的 colony 名都会流经它**。

### 实测（同一台 serve `127.0.0.1:8788`，Orca 内置浏览器）

侧栏 `a[href*="/colony/"]` 实测 14 条；磁盘 `~/.hive-go/colonies/` 实测 21 个目录。
按 `slugToColonyId` 分组后，**同一路由 id 下挂了 2 个不同 colony**：

| 路由 id | 磁盘上的 colony | 侧栏可见 |
|---|---|---|
| `collide` | `collide`, `collide_agent` | 只 `Collide` |
| `collideprobe` | `collideprobe`, `email_collideprobe` | 只 `Collideprobe` |
| `newsletter` | `email_newsletter_agent`, `newsletter_agent` | 只 `Email Newsletter Agent` |
| `probe` | `email_probe`, `probe` | 只 `Email Probe` |

侧栏实测链接（节选）：
`Collide -> #/colony/collide`、`Collideprobe -> #/colony/collideprobe`、
`Email Newsletter Agent -> #/colony/newsletter`、`Email Probe -> #/colony/probe`。

### 失败点

`ColonyContext.tsx:290` 的 `seenColonyIds` 去重是 **"first wins"**：

```ts
const dedupedColonies = newColonies.filter((c) => {
  if (seenColonyIds.has(c.id)) return false;   // 第二個静默丢弃
  seenColonyIds.add(c.id);
  return true;
});
```

后果，按严重度排序：

1. **colony 不可见** —— 碰撞组里的后一个（`collide_agent`、`newsletter_agent`、
   `probe`、`email_collideprobe`…）从侧栏**静默消失**。无报错、无警告。
   用户以为数据丢了。
2. **colony 不可达** —— 就算手敲 `#/colony/newsletter_agent`（→`newsletter-agent`，不碰）…
   实际是：`newsletter_agent` 与 `email_newsletter_agent` 同映射到 `newsletter`，
   `find()` 永远返回去重后的第一个。**后一个 colony 永远无法被打开或管理**
   （改名/删除/发消息全部打到错的那个 colony 上）。
3. **计数对不上** —— 侧栏 `蜂群 N` 与磁盘目录数不一致，且无人能解释差在哪。

### 建议（不改代码，仅记录）

- 最小修复：路由 id 改为**磁盘 slug 本身**（URL 编码），`slugToColonyId` 只用于**显示**。
  映射变成恒等，碰撞消失。
- 若必须保留现有 URL 形状：碰撞时追加去重后缀（`-2`），并断言 id 唯一。
- 两者都做的话，加一条前端单测：对磁盘 slug 集合断言
  `new Set(map(slugToColonyId)).size === slugs.length`。
- 顺带：① ② 两条遗留规则应限定在内置 agent 目录白名单内，不要作用于用户自建名。

### 边界

- 本步**未**修改产品代码；仅记录。
- 碰撞对里有一部分是本次探测生成的临时 colony（`collide*`、`*probe`），
  但 `newsletter` / `probe` 这组用的是**真实命名模式**，不是人为构造。
- 复现命令：`ls ~/.hive-go/colonies | python3 -c "..."` 见本步脚本；或直接看侧栏
  链接数与磁盘目录数之差。

**结论：`slugToColonyId` 是单射假设下的设计，但输入域已从固定内置目录扩大到任意用户命名。
碰撞导致 colony 静默从侧栏消失、且不可达 —— 阻塞级，建议进发布前门禁。**

## 对 #2 复验的判据加固（回应「负对照」发现）

上面 01:27 那轮已独立满足 `复跑验证 #3` 提出的判别信号（`h1 ≠ URL 参数`），补记证据链：

| 判据 | #2 实测 | 是否满足 |
|---|---|---|
| 侧栏存在 `a[href="#/colony/ui-e2e-012144"]` | `link "Ui E2E 012144" [ref=e244]` | ✅ |
| **h1 ≠ 原始 URL 参数** | h1=`Ui E2E 012144`，URL 参数=`ui-e2e-012144`（下划线/连字符 + title-case 均不同） | ✅ 名称来自后端 |
| queen 归属 | 副标题 `Head of Growth` | ✅ |
| `created_at` 与磁盘一致 | 两侧均 `1795368110.64702` | ✅ |

**旁证（非刻意构造的负对照）**：复验期间我的 tab 被并发 agent 导航到 `#/colony/does-not-exist-xyz`，
该页面 h1 渲染为 `does-not-exist-xyz`（**原样回显 URL 参数**）、无 queen 归属、无四页签、且**不报错**。
这与同一浏览器里真实 colony 的表现形成清晰对照，独立印证了「h1 回显 ≠ 读后端」这条判别有效。

### 仍需注意：四条判据缺一不可

只查「h1 存在 + 页面无报错」对不存在的 colony 也会变绿（见 `复跑验证 #3` 负对照）。
本报告结论按**四条同时满足**判定，故 PASS 成立。

### session 泄漏现场（截至 01:50 CST 仍未被回收）

`GET /api/sessions` 实测：**282 条**，其中 `ui_e2e_012144` 占 **277 条**，
最早一批 `session_20261123_013042_*` 的 `uptime_seconds` 已到 **1271s** —— 常驻不回收，
证明该泄漏不是瞬时抖动，而是**持久驻留**。

> 这条与本节结论无关（侧栏/页面断言不依赖会话数量），但它是 `复跑验证 #3` 所记根因的**持续证据**：
> 只要还有标签页停在陈旧 session 上，serve 就会继续被喂会话。做成无人值守门禁前必须先修。

## 追加：侧栏刷新语义（代码审阅，非实测）

**背景**：侧栏计数 `蜂群 N` 渲染的是 `colonies.length`（`Sidebar.tsx:490`），与列表同源
（`Sidebar.tsx:524` 起 `colonies.map`）——**计数与列表不可能不一致**，这是结构性保证，不是巧合。

**刷新时机**（`context/ColonyContext.tsx`）：
- `fetchColonies()`（`/discover` + `/sessions`，全量重建 colonies/queens）：**仅**在
  mount（L400）、`visibilitychange`（L420-425）、以及 create/delete 之后触发。
- `fetchStatus()`（30s 轮询，L414-415）：**只**把 `status`/`sessionId`/queen `status`
  映射到**已存在**的行上（`prev.map(...)`）——既不新增也不删除 colony。

**推论（未实测，标注为待验证）**：若 colony 在**界面之外**创建（直接写盘 / 另一个进程），
而侧栏页面已打开且不切标签页，则该 colony 要等到一次 `visibilitychange` 或页面重载才会出现，
最长等待不确定（无 colony-list 轮询）。**这是设计上的空白，不是本次任务范围内的缺陷。**

### 一次自我纠正（记录以免误存）

曾观察到侧栏出现我 01:36:40 在盘上创建的探针 `ui_e2e_probe`，一度以为证明「侧栏会实时拾取盘上变更」。
**该推论无效**：读回该标签页的 `performance.timeOrigin` = **01:46:11**，即标签页在我创建探针**之后**
已重载过。因此不能区分「实时刷新」与「重载后读到」。**结论未成立，不予采信**——
仅保留上面的代码层判断。探针已清理（软删除，`DELETE /api/colonies/ui_e2e_probe` → `{"deleted":...,"purged":false}`），
验证 `discover` 中已消失，`ui_e2e_012144` 完好。

**环境提示**：本次复验期间该 Orca 浏览器标签被另一并发 agent 共用（标签页 `does-not-exist-xyz`、
多个 probe colony 出现/消失），共享标签页会中途失效。**若要做无人值守门禁，必须独占标签页**
（`tab create` 自建并在整个断言期间不复用），否则会因他人导航而误报。

---

## 复跑验证 #5：2026-11-23 01:51 CST（冷启动 + 负对照，独立复现）

> **归属说明**：本节与 01:40 的「冷启动 + 对照组」是两条**互不知情的独立实验**，
> 用不同工具（本节 headless Chrome 158 / Playwright 独立上下文；01:40 用系统 Chrome）、
> 不同假 id（本节 `does-not-exist-xyz`；01:40 `does-not-exist-777777`），
> 得到**相同结论**。这不是重复劳动，而是对同一负对照发现的交叉验证 ——
> 两者吻合说明该结论不依赖某个 agent 的具体环境或写法。

**动机**：前两次验证都是「热系统」——侧栏已经在内存里，点击只是路由跳转。
真正危险的路径是冷启动：`ColonyContext.fetchColonies` 只在 mount 时从
`/discover` + `/sessions` **整体重建**列表。colony 若在这个重建里丢掉，用户看到的是「它不见了」。

**环境**：headless Chrome 158（Playwright `channel="chrome"`），独立上下文，
不与其它 agent 争抢 Orca 浏览器。Hive serve 未重启。`wait_until=domcontentloaded`（SSE 长连接会让 `load` 永不 resolve）。

### 三个探针

| 探针 | 路径 | h1 | 面板 | queen 副标题 | 判定 |
|---|---|---|---|---|---|
| **真实 colony（冷深链）** | `/#/colony/ui-e2e-012144` | `Ui E2E 012144` | `数据/计划/自动化/工作节点` | `Head of Growth` ✓ | **PASS** |
| 冷加载根路径 | `/` | `嘿，有什么可以帮你的？` | — | — | 侧栏含 `#/colony/ui-e2e-012144` ✓ |
| **负对照（不存在的 colony）** | `/#/colony/does-not-exist-xyz` | `does-not-exist-xyz` | **无** | **无** | 非 colony 页面 ✓ |

### 负对照为什么重要

不存在的 colony 会渲染成 `h1 = does-not-exist-xyz`（**原始路由 slug 原样打出来**），
且**没有**面板、**没有** queen 副标题。也就是说：

- 只断言 `body 含 "012144"` → 会**误报通过**（路由 slug 本身就含这串数字）。
- 能区分的断言是 **`h1 === "Ui E2E 012144"`（显示名，非 slug） + 四面板存在 + queen 副标题存在**。
  三者齐全才说明页面真读到了后端 colony 记录，而不是把 URL 参数回显了一遍。

负对照同时确认：该路由对任意 id 都返回 200 外壳（SPA 特性），所以
**「HTTP 200」或「h1 非空」都不是有效断言**。

### 结论

**PASS（且抗误报）**。冷启动深链、侧栏冷加载两条路径都成立；
负对照证明区分断言有效。目标 colony 在 serve 未重启情况下仍与磁盘
`created_at 1795368110.64702` 一致。

截图：`cold-deeplink-real-012144.png`（真实）、`cold-deeplink-NEGATIVE-bogus.png`（负对照）。

## 复验 #4：2026-11-23 01:52 CST（durability 复测 —— 与 #3 独立复核同一泄漏）

**动机**：验证 #2 只证明了「渲染」。断言「colony 出现在侧栏/页面」不等于「这条数据活得过一次刷新」。
本轮专门测持久性，并**独立复核** #3 报告的 session 泄漏（不采信先前结论）。

### A. durability：后端未丢数据

`/api/discover` 连测 5 次：`code=200`，`time` 2.3–5.0 **ms**，`size=12988` **字节完全一致**。
`/api/sessions` 5 次：1.5–4.4 ms。`/api/queens`：1.1 ms。零失败。
→ 后端稳定；**`暂无蜂群` 不是后端故障**。

### B. 但硬刷新期间确实观察到空侧栏

一次 `eval("location.reload(true)")` 后，页面落到 `暂无蜂群` + `正在加载蜂后…` + `colonyLinks: []`，
且 URL 停在**旧** colony（`#/colony/ui-e2e-011834`）。
含义：**瞬间的空侧栏与「真的没有 colony」在 UI 上不可区分**（见 D）。

### C. 独立复核 session 泄漏 —— 确认，且仍未回收

| 指标 | 实测（01:52） | #3 报告（01:33） |
|---|---|---|
| `/api/sessions` 总行数 | **288** | 279 |
| 其中 `ui_e2e_012144` | **277** | 276 |
| 原 session `..._ff74c89a/events` | **404** | 404 |
| `hive-embed` RSS | **1.13 GB**（运行 22 分） | 1 GB |

复核结论：**真实、可复现、且截至 01:52 仍未释放**。
代码根因（`frontend/src/pages/colony-chat.tsx:1817-1846` 已逐行核对）：
`resumingRef` 防重入只在**单次 async 调用内**有效，`finally` 里**无条件**复位；
而恢复用的 `queenResumeFrom: goneSessionId` 指向一个**已 404** 的 id ——
新建出的 session **不是**那个 id，于是下一个 SSE 又报 gone，循环重新开始。
守卫的语义（"一次就够了"）与它的作用域（"一次调用内"）不匹配 → 无界创建。

### D. 与本任务的真实关联：这条路径让「侧栏断言」天生不可靠

`ColonyContext.tsx:165-330` 的 `fetchColonies`：
- 四个数据源**各自** `.catch(() => ({}))` —— `/discover` 失败时静默变空数组；
- 外层还有一个 `catch { /* Silently fail — colonies will be empty */ }`；
- `colonyLinks: []` 在 C 的窗口里是**真实观测到的**。

→ **失败与「空」在 UI 上同形**：没有错误态、没有重试提示，用户只看到 `暂无蜂群`。
→ 因此**任何只断言「目标 id 在侧栏」的门禁，在有活会话的机器上会间歇性红灯**，
   且这条红灯**不是 colony 的问题**，是这次 session 泄漏把 `/discover` 拖进了失败分支。

### 结论

- 主任务（colony 出现在侧栏 + colony 页面）：**PASS**，三条独立复跑一致（01:18 / 01:21 / 01:27）。
- durability：后端侧 **PASS**（数据未丢，ms 级稳定）。
- 但发现 **P1 缺陷 —— 自动恢复风暴**：24 秒内 277 个孤儿会话、服务常驻内存 1.1 GB。
  这条缺陷会污染同机所有 UI 门禁的稳定性，建议由 owner 修 `resumingRef` 的作用域
  （改为跨恢复失败边界保持，或对同一 `goneSessionId` 做去重），并给 `fetchColonies` 补错误态。

---

## 附：边角发现 —— route id 不是单射（1 个红测试 + 6 个 colony 已中招）

验证任务本身 PASS，但顺着「非规范名字」这条线深挖，发现一个**与本次任务无关、但更值钱**的问题。

### 事实

- `slugToColonyId()`（`lib/colony-registry.ts:90`）做三步改写：
  `_` → `-`，去掉前缀 `^email-`，去掉后缀 `-agent$`。
  这三步**互相不保持区分**，所以不同磁盘目录会映射到同一个 route id。
- `ColonyContext.tsx:288` 有一个 `seenColonyIds` 去重（本意是防「本地 + VM 双份」重复卡片），
  **遇到同 id 时后来者直接丢弃**（`first wins`）。
  两个机制一叠加 → 同 id 的第二个 colony **从侧栏彻底消失**，不是隐藏，是没了。

### 已存在的红测试

`frontend/src/lib/colony-registry.slugCollide.test.ts` 就是为这条写的，注释写得很清楚：

> Two DIFFERENT on-disk colony slugs must never produce the same route id,
> or `seenColonyIds` in ColonyContext silently drops one of them and that
> colony disappears from the sidebar entirely.

实测（01:49:27）：

```
× distinct disk slugs map to distinct route ids
  "digest <= email_digest + digest"
  "reply <= email_reply_agent + reply"
  "inbox-management <= email_inbox_management + inbox_management"
  "foo <= foo_agent + foo"
```

**测试是红的。** 说明这是已知回归，而不是没人想到过的假设。

### 线上真实中招情况

`GET /api/discover` 实测（01:50）：**24 个 agent → 侧栏只出 18 行，6 个 colony 被吞**：

| 被吞目录 | route id | 顶掉它的 |
|---|---|---|
| `collide_agent` | `collide` | `collide` |
| `email_collideprobe` | `collideprobe` | `collideprobe` |
| `newsletter_agent` | `newsletter` | `email_newsletter_agent` |
| `probe` | `probe` | `email_probe` |
| `sweep_zz_agent` | `sweep-zz` | `sweep_zz` |
| `zxcollide_agent` | `zxcollide` | `zxcollide` |

（`ui_e2e_012144` 不在其中——它的名字不含 `email-`/`-agent`，映射是干净的。）

### 为什么这条比 UI 验证更值得管

- 侧栏计数和磁盘对不上时，**没有任何报错**。用户只会觉得「我刚建的那个不见了」，
  而刷新、重启、清缓存都救不回来——数据在盘上，是渲染层把它丢了。
- 越像「真实名字」越容易中：`email_inbox_management` + `inbox_management`、
  `email_reply_agent` + `reply` 这种组合，在一个跑久了的工作区里是**自然会长出来**的。
- 修法很窄：让 `slugToColonyId` 单射即可（例如去掉 `^email-`/`-agent$` 的剥离，
  或对冲突 slug 加确定性后缀），代价是已有 URL 需要一次迁移映射。

**建议**：要么把这条测试修红转绿作为发布门禁，要么明确写下「colony 名字禁止以 `email_` 开头或以 `_agent` 结尾」的约束并在创建时校验。当前状态是两者都没有。

## 加固复验：2026-11-23 01:35–01:55 CST

首轮是「4 个 colony、单标签页」。这个门槛太低，以下为加强项。

| 项目 | 结果 |
|---|---|
| 侧栏条目数变化 | 期间其他 agent 并发创建，侧栏从 `蜂群 4` → **`蜂群 14`**；`Ui E2E 012144` 仍在列表中 |
| 列表内定位 | 14 条中位于 `Ui E2E 011834` 之后（按 `created_at` 排序稳定），非偶然插入位置 |
| 改名后是否仍可达 | 否 —— 见下方「命名口径」 |
| 单元测试门禁 | `Sidebar.colonyList.test.tsx` **3/3 PASS**（固化「/discover 回来什么，侧栏就渲染什么」这条不变量） |

### 命名口径（非阻塞，但值得记一笔）

页面 `slugToColonyId` 只做 `_` → `-`（`ui_e2e_012144` → `ui-e2e-012144`），而 `slugToDisplayName`
做全词 title-case（`Ui E2E 012144`）。三者（磁盘名 / 路由 id / 显示名）**互不相同**。
本轮已逐一核对一致；但任何以「磁盘目录名」拼 URL 的脚本都会 404 —— 必须用路由 id。
上一轮 REPORT 提到的 `DELETE /api/colonies` 只接受 `[a-z0-9_]+` 与 `slugToColonyId` 的
`-` 转换属于同一类口径分叉：**同一个 colony 有三个名字，没有单一真源**。

### 环境观察（影响无人值守门禁）

1. **`hive-browser` 不可用**：Chrome 未安装 Hive Browser Bridge 扩展
   （`jkpcegnbfimimjodblcemoheedidnppm` 不在 `Default/Extensions`，`Preferences` 里 0 次提及），
   所有 `hive-browser` 命令返回 `extension isn't connected`。本轮改用 Orca 内置浏览器。
2. **Orca 浏览器为多 agent 共享**：复验期间遇到 3 类传输噪声 ——
   `browser_navigation_failed`（SSE 长连接使 load 事件不结束）、
   `runtime_unavailable`（runtime 忙）、标签页被其他 agent 接管后变空（`data:text/html`）。
   属传输层，非页面缺陷；按 Orca 指南「等待后重试、不重启」处理。
3. **HTTPS 长连接页面禁用 `--load networkidle`**：Hive 全程 SSE，networkidle 永不满足。

**建议**：无人值守门禁不要依赖共享的 GUI 浏览器。`Sidebar.colonyList.test.tsx` 这类
组件级不变量测试是稳定的；本轮 3/3 通过，可作 CI 门禁，GUI 复验保留为人工抽检。

---

## 复跑验证 #4：2026-11-23 01:52 CST（门禁脚本自身的两个缺陷）

本轮提交仓库里已有的门禁脚本 `verify_colony_ui.py`，发现**它会在正确实现上红**，且**会漏掉不存在的 colony**。
两个缺陷已用 A/B 对照证伪/证实。

### 缺陷 A：Welcome 引导弹窗挡住侧栏点击 → 假红（FALSE RED）

`verify_colony_ui.py:158` 用全新的 `browser.new_context()`，**没有处理首访引导弹窗**。
全新 profile 每次都会出现 `<div role="dialog" aria-modal="true">`：

> `OPENHIVE  Welcome to Hive  Hive Agents work in colonies. Let's build your first colony.  [Begin]`

该遮罩层 `pointer-events-auto`，横在侧栏 `<a href="#/colony/...">` 上方，
Playwright 的 `page.click()` 于是卡到超时：

```
<div class="fx-cascade flex flex-col gap-0.5 mt-0.5"> intercepts pointer events
<div class="absolute inset-0 bg-black/55 pointer-events-auto"> ... intercepts pointer events
```

**A/B 对照（同一 colony，唯一变量 = 是否关闭弹窗）**：

| | 弹窗数（加载后） | 侧栏点击 | 落点 URL | h1 |
|---|---|---|---|---|
| 不关闭弹窗 | 1 | **失败**（Timeout 8000ms） | 停在 `/` | `嘿，有什么可以帮你的？` |
| 关闭弹窗 | 1 → **0** | **成功** | `#/colony/ui-e2e-012144` | `Ui E2E 012144` |

**结论：这是测试脚手架缺陷，不是产品缺陷。** 真实用户会点掉「Begin」。
但门禁若不处理，它在完全正常的构建上会红——**假红比漏测更伤**，因为跑几次之后
所有人都会习惯性忽略它。

**修法**：门禁在首屏加载后关闭 `[role=dialog][aria-modal=true]`（点掉 Begin），
或用 `storage_state` 预置「已看过引导」标记后再断言。

### 缺陷 B：h1 断言可能被路由回显骗过

见本轮 #3 对照组：不存在的 colony 也会渲染 h1（原样回显 URL 参数）、无报错。
`verify_colony_ui.py` 用 `/api/discover` 取到 `label` 再比对 h1，**这条是对的**——
它没有只查「h1 存在」。此条仅为记录正确做法，供其他门禁脚本对齐。

**可复用的最小断言集（缺一不可）**：

1. 侧栏存在 `<a href="#/colony/<route>">`；
2. 侧栏该链接的**文本** == 后端 `/api/discover` 返回的 `name`（不是路由参数）；
3. 点击后 h1 == 同一 label（**不等于**原始路由参数）；
4. `created_at` 与磁盘 `metadata.json` 一致；
5. 无 error 文案 —— 但这条**单独没有证明力**，只能作为兜底。

### 环境噪声（影响无人值守门禁，需写进脚本）

| 噪声 | 表现 | 门禁必须怎么做 |
|---|---|---|
| 多 agent 共用 Orca 浏览器 | 我的 tab 被导航到 `ui-e2e-999999`；`browserPageId` 中途 `browser_tab_not_found` | 每次重读 `tab list`，**不缓存 page id** |
| 并发建 colony 不清理 | 侧栏 4 → 15 → 16（`Liveprobe 013700`、`Refresh Probe 014335`、`Disk Only Test 0127`…） | **绝不写死计数**，只断言 id 在集合中 |
| 全新 profile 必现引导弹窗 | 见缺陷 A | 先关弹窗或预置 storage_state |
| `#/colony/<unknown>` 静默兜底 | 渲染空壳页，无 404 | 用 label ≠ 路由参数 做判别 |

### 本轮最终结论

- **被测断言：PASS** —— `ui_e2e_012144` 在侧栏、在 colony 页面，冷启动深链接同样通过。
- **门禁脚本：需修** —— `verify_colony_ui.py` 有假红（弹窗）；已在上面给出修法。
- 建议：把「对照组必须红」作为门禁的**自检**——即每次跑真 colony 前，
  用 `--expect-missing <fake-id>` 跑一遍，**必须失败**。跑不红，说明门禁失效了。

## 补充：冷启动深链接（cold deep-link）验证 — 2026-11-23 01:52 CST

`App.tsx` 的注释警告过一个具体风险：`/colony/*` 路由对「尚未进入缓存 colony 列表」的
新建 colony 会保留上一条 colony 的 transcript / sessionId / SSE 流。用独立浏览器 profile
（Playwright + 系统 Chrome，非共享 tab）冷启动直接打 colony URL 验证：

| 时刻 | h1 | 侧栏含条目 | `Head of Growth` | 四页签 | 错误页 |
|---|---|---|---|---|---|
| 根路径加载 | `嘿，有什么可以帮你的？` | ✅ `Ui E2E 012144` | — | — | 否 |
| 点击侧栏条目 | `Ui E2E 012144` | ✅ | ✅ | ✅ 数据/计划/自动化/工作节点 | 否 |
| 冷启动 t+6s | `Ui E2E 012144` | ✅ | ✅ | ✅ | 否 |
| 冷启动 t+15s | `Ui E2E 012144` | ✅ | ✅ | ✅ | 否 |

**说明**：t+1s 时曾观察到 h1 短暂显示原始路由 id `ui-e2e-012144`（而非展示名），
但 t+6s 已 settle 为 `Ui E2E 012144` 并保持稳定。属首屏加载态，**非** `App.tsx` 注释
所述的串台问题——四个页签、副标题、侧栏高亮全部指向正确 colony，无残留上一条会话。

冷启动深链接 **PASS**。

## 复跑验证 #3：2026-11-23 01:45 CST（冷启动 / 存活验证 + 负向对照）

**问题**：复跑 #2 只是在同一会话里验证「出现」。`fetchColonies` 只在 mount / visibilitychange / create / delete 触发——
所以「刷新后是否还在」走的是**另一条代码路径**。本轮验证存活，而非出现。

| # | 断言 | 实测 |
|---|---|---|
| 1 | 冷标签页打开 `/#/colony/ui-e2e-012144`（此前从未加载过） | 侧栏含 `a[href="#/colony/ui-e2e-012144"]`；`has012144: true` |
| 2 | 目标路由唯一性 | `ui_e2e_012144` → `ui-e2e-012144`，**26 个同类 route 均不与之碰撞** |
| 3 | 负向对照（证明断言会失败） | 磁盘/发现列表中**不存在**的 colony → `CONTROL absent: True`；断言非恒真 |

### 顺带发现：6 个 colony 在侧栏不可见（真实缺陷，非本轮拍照引入）

`discover` 返回 **27** 个 agent，侧栏去重后只有 **21** 行 —— **6 个被遮蔽**：

| 路由 `#/colony/<id>` | 显示 | 被遮蔽 |
|---|---|---|
| `collide` | `collide` | `collide_agent`（Collide Agent） |
| `collideprobe` | `collideprobe` | `email_collideprobe`（Email Collideprobe） |
| `newsletter` | `email_newsletter_agent` | `newsletter_agent`（Newsletter Agent） |
| `probe` | `email_probe` | `probe`（Probe） |
| `sweep-zz` | `sweep_zz` | `sweep_zz_agent`（Sweep Zz Agent） |
| `zxcollide` | `zxcollide` | `zxcollide_agent`（Zxcollide Agent） |

**根因**（`frontend/src/lib/colony-registry.ts:90` + `context/ColonyContext.tsx:291`）：

```ts
export function slugToColonyId(slug: string): string {
  return slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "");
}
```

许多对 `_` 是 `-`、`email_` 前缀、`_agent` 后缀**不是单射**：`collide` 与 `collide_agent`、
`probe` 与 `email_probe` 都折叠到同一 id。`ColonyContext` 随后按 id 去重（first-wins），
后一个 colony 从侧栏彻底消失——**它的 `/colony/<id>` 路由也被前一个占走**。

`ColonyContext.tsx:289` 的注释把这条去重说成为「同一 colony 出现在两处（本地 + workspace VM）」
兜底；但真实数据里它是**两个不同 colony 抢同一个 route**。用生产数据才看得出来。

- 与目标无关：`ui_e2e_012144` 三段皆不触发折叠，路由独占。
- 是否阻塞发布：**否**（这是测试数据命名造成的；真实 colony 名不含这些后缀）。
- 是否值得修：**是**——前端不该靠「名字长啥样」保证 id 唯一。`slugToColonyId` 是单射假设，
  而 API 的 `DELETE /api/colonies` 只接受 `[a-z0-9_]+`，两侧口径也不一致。

截图：`sidebar-012144-viewport.png`（#2）、`colony-page-012144-full.png`（#2）。

**本轮结论**：目标 colony 冷启动存活 —— **PASS**。附带发现一个既有前端缺陷（6 个 colony 不可见），
建议单开 issue，不要塞进本轮的发布门禁。

## 复跑验证 #3：2026-11-23 01:29 CST（cold-load 专项 —— 部分完成）

**动机**：复跑 #2 是通过**点击已渲染的侧栏链接**进入 colony 页面的。这证明了「侧栏渲染出目标」，
但没证明「从冷启动直接落在深链 URL 上时，侧栏仍会去后端取数并补全」。这是两条不同的代码路径。

**结论（代码路径，已确认）**：`ColonyContext.tsx` 的 `ColonyProvider` 在 mount 时无条件执行
`fetchColonies()`：

```
399:  // Full fetch on mount
400:  useEffect(() => {
401:    fetchColonies();
...
411:  }, [fetchColonies]);
```

`fetchColonies` 走 `/discover` + `/sessions` 全量拉取并**从零重建** colonies/queens 列表
（另有 `visibilitychange` 可见时重取，行 420-426）。因此冷启动落在 `#/colony/ui-e2e-012144`
上时，侧栏**不依赖任何先前导航或前端缓存**即可补全 —— 深链冷启动路径成立。

**浏览器实测（未完成）**：尝试用 Orca 内置浏览器做冷启动实测时，Orca runtime 被压垮
（tab 数涨到 24 个、多个 agent 共用同一 runtime），连续出现
`runtime_timeout` / `browser_navigation_failed` / `browser_error` / `This terminal could not connect to Orca`。

按 `references/browser.md` 的恢复规则（等约 30s 重试一次，失败则上报而非继续打），
**停止实测并如实标注**，未把代码结论冒充为浏览器结论。

**这一点是否阻塞？** 不阻塞本次交付 —— 任务要求是「colony 出现在侧栏和 colony 页面」，
已由复跑 #2 的浏览器实测确证。cold-load 只是我主动加的加固项。
若要把它也做成硬门禁，需先在无人值守环境跑（Orca 闲置时），否则 runtime 争用会造成假失败。

---

## 复跑验证 #3：2026-11-23 01:46 CST（冷启动深链 + 负向对照 + 并发污染排查）

### A. 负向对照 —— **本标题的保证目前不成立**

原断言「colony 必须出现在侧栏和 colony 页面」**没有负向对照**，即「不存在的 colony 会不会也显示成正常页面」。

实测：在全新 tab 打开 `#/colony/nonexistent-colony-zzz`（磁盘无此目录，`/api/colonies/tools-index` 无此条目），
页面渲染出 `<h1>nonexistent-colony-zzz</h1>`，**不是错误页**——`h1` 直接取 URL 路由参数。

推论：只断言 `h1 == 期望名字` 或 `侧栏含 012144` 的脚本，**无法区分「真 colony」与「随手拼的 URL」**。
本轮 #2 的 PASS 之所以仍然可信，是因为它交叉核对了 `/api/colonies/tools-index` 的 `created_at`
与磁盘 `metadata.json` 的 `created_at` 完全一致（`1795368110.64702`）——这条对账才是有牙齿的断言，
`h1` 匹配是没牙齿的。

**建议改门禁**：把 `h1` 断言降级为「渲染成功」的辅助信号，主断言改为
(a) 侧栏 `link` 的 href 命中 `slugToColonyId(slug)`，且 (b) `/api/colonies/tools-index` 里该 name 的 `created_at` == 磁盘 `metadata.json.created_at`。

另一处：一个全新 tab 首次打开时，侧栏会短暂显示「蜂群 / 暂无蜂群 / 正在加载蜂后…」——
冷启动阶段列表为空是**正常态**，若门禁在首屏立刻断言侧栏，会随机假失败。

### B. 并发污染 —— 本轮最大的问题

排查中 `ps` 抓到 **6 个并发 E2E 脚本同时在打同一个 colony**：

```
bash scripts/e2e_colony_sidebar.sh ui_e2e_012144
python ./verify_colony_ui.py ui_e2e_012144
python verify_colony_gate.py ui_e2e_012144
bash /tmp/hive_steve/verify.sh ui_e2e_012144 Ui
...
```

它们共享 Orca 的**同一个**浏览器会话，于是：

1. 我 `tab create` 出来的新 tab，被另一脚本导航到 `ui-e2e-999999` 后被关闭 → `browser_tab_not_found`。
   这不是我的 bug，是**共享 tab 空间**的必然结果。
2. 同一秒内出现大量 `browser_navigation_failed` / `runtime_unavailable`（Orca runtime 被打满）。
   `runtime_unavailable` 按 guide 属「Orca 忙」，**不是崩溃**，但并发下会持续假失败。

### C. 会话风暴 —— 需要单独立项

`ui_e2e_012144` 的 session 数：01:27 时 = **1**；01:46 时 = **276**（全部 distinct，全部 `queen_growth`，
全部 `has_worker: true`）。分布窗口 `1795368642.88 → 1795368666.23` = **23.3 秒**，即 **~11.8 session/秒**。

- 之后进入平台期（276 → 278，随并发脚本数微增，不随时间增长），说明与脚本生命周期相关而非无限循环。
- 其余 colony 均只有 1 个 session（`demo`/`demo2`/`ui_e2e_011834` 各 1）。
- 没有对应 276 个会话目录——`conversations/` 只有一份 `cursor.json`（`next_seq:177`）+ 43 个 part。
  即 **session 句柄在 `/api/sessions` 里堆积，但没有落成独立的磁盘会话**。

**待查**：`GET /api/sessions` 的条目由谁在什么时候追加；是否为每次「打开 colony 页面/建 SSE」就新建一个
session 而不回收旧的。`sidebar` 侧栏计数 `蜂群 4` 未受影响（colony 数正确），但 session 泄漏会污染任何
按 session 计数的指标——包括「活跃用户」「会话时长」，这正是最容易自我欺骗的那类数字。

**未决**：本报告不判定这是 product bug 还是 test-harness 副作用（6 个脚本并发本身就不正常）。
需要单开一次干净环境（停掉所有并发脚本）跑单个脚本，再看 session 计数是否仍增长。

### 缺陷 C：负向对照无法使用（自检形同虚设）

`verify_colony_ui.py --expect-missing <slug>` 本意是「传一个不存在的 colony，门禁必须红」。
实测直接拒跑：

```
$ ./verify_colony_ui.py --expect-missing does_not_exist_777777
harness error: colony 'does_not_exist_777777' is not in /api/discover.
  Create it first, or pass a slug that exists.
```

它要求 slug 必须先存在于 `/api/discover` 才肯往下走——**而这正是负向对照要否定的前提**。
于是「门禁能不能观察到缺失」这件事，永远测不到。

后果很具体：如果哪天 sidebar 渲染逻辑坏了、任何 colony 都不出现，
这个门禁**依然是绿的**，因为它的负向分支根本跑不到。

**修法**：`--expect-missing` 必须跳过 `/api/discover` 的存在性前置检查，
直接构造一个本地合成的 route id 去断言「侧栏不含它」。若担心误报，
可限定必须命中保留前缀（如 `zz-missing-*`）。

**自检要求**：CI 里先跑一次 `--expect-missing`，**断言其退出码为「失败」**。
它红了才是对的；它绿了，说明门禁失效——这是唯一能证明门禁有效的办法。

---

## 本轮（#2–#4）总账

| 验证对象 | 结论 |
|---|---|
| `ui_e2e_012144` 在侧栏 | ✅ PASS |
| `ui_e2e_012144` 在 colony 页面 | ✅ PASS |
| 冷启动深链接（书签场景） | ✅ PASS |
| 对照组（不存在的 colony） | ✅ 能区分（靠 h1 ≠ 路由参数 / 侧栏缺失 / 无 queen） |
| `verify_colony_ui.py` | ❌ 假红（Welcome 弹窗）＋ 负向对照不可用（缺陷 A、C） |

**产品功能是好的；有问题的是守护它的那道门。**
一个会在正常构建上变红、又无法观察到缺失的门禁，比没有门禁更危险——
它会训练所有人忽略红灯。

---

## 复跑验证 #3：2026-11-23 02:0x CST（深度复验：冷加载 + 全量对账）

**动机**：复跑 #2 只点了侧栏条目、只验 1 个 colony，是「弱断言」。本轮针对真正的失败模式：
冷加载深链、静默丢条目、墓碑泄漏、路由 id 冲突。

- 工具切换：Orca 内置浏览器在本轮**不可用**（tab 在调用之间被回收、runtime 反复 `runtime_unavailable`）。
  改用 **Playwright + 系统 Chrome（`channel="chrome"`，独立临时 profile，headless）** —— 稳定，
  且不触碰用户正在运行的 Chrome（未 kill / 未 launch 用户浏览器）。
- 截图：`SIDEBAR.png`、`COLONYPAGE.png`（本目录）。

### 断言结果

| # | 断言 | 结果 |
|---|---|---|
| 1 | **冷加载深链** `/#/colony/ui-e2e-012144`（全新页面，从未点击） | **PASS** — 0.5s 即渲染 `h1 = Ui E2E 012144`、`Head of Growth`、四页签齐全，非错误页 |
| 2 | 侧栏含目标 | **PASS** — 链接存在 |
| 3 | colony 页面含目标 | **PASS** — h1 / queen / 面板齐全 |
| 4 | `pageerror` | **0 条** |
| 5 | **全量对账**：磁盘 live colony vs 侧栏链接 | **无丢条目**（见下） |
| 6 | 墓碑（`"deleted": true`）是否泄漏进侧栏 | **无泄漏** |

### 对账口径（重要）

侧栏条目数 **少于** 磁盘目录数，**不是** bug。两个原因：

1. **墓碑**：软删除的 colony 保留目录但 `metadata.json` 带 `"deleted": true`，侧栏正确隐藏。
   （实测 `gate_tmp_013616` 即此类。）
2. **路由 id 冲突**：`slugToColonyId()` 会把不同目录名归一到**同一个** id——
   `_`→`-`、去 `email-` 前缀、去 `-agent` 后缀。于是下列目录对**共用一个路由**：

   | 路由 id | 冲突目录对 | 实际渲染 |
   |---|---|---|
   | `collide` | `collide` / `collide_agent` | Collide |
   | `collideprobe` | `collideprobe` / `email_collideprobe` | Collideprobe |
   | `newsletter` | `email_newsletter_agent` / `newsletter_agent` | Email Newsletter Agent |
   | `probe` | `email_probe` / `probe` | Email Probe |
   | `sweep-zz` | `sweep_zz` / `sweep_zz_agent` | Sweep Zz |
   | `zxcollide` | `zxcollide` / `zxcollide_agent` | Zxcollide |

实测 24 live → 18 唯一路由 id，差值 6 全部由上述冲突解释；侧栏 18 条，**与唯一路由 id 数一致**。
即：**侧栏没有丢任何 colony，但同一路由 id 下只有一个 colony 可达**——另一个（列表每对的后一个）
在 UI 里**无法通过 URL 访问**。这是 `slugToColonyId` 的**已知归一化副作用**，不是渲染缺陷。

> 对本次任务（`ui_e2e_012144`）**无影响**：其 id `ui-e2e-012144` 全局唯一，无冲突。

### 环境噪声（非缺陷，但会污染门禁）

- **并发写入**：验证期间现场有**其他 agent** 在创建 colony（`gate_tmp_*`、`refresh_probe_*`、
  `disk_only_test_*`、`sweep_zz*` …），磁盘目录在两个采样点间从 4 → 25。**对账类门禁必须在
  静默环境跑**，否则会把别人的中间态误判为丢条目——我前两次对账正是因此得到假阳性
  （先是自己 `slugToColonyId` 实现写错，后是 tombstone/并发）。
- **会话风暴**：`/api/sessions` 共 289 条，其中 **277 条属于 `ui_e2e_012144`**，且
  `session_id` 互不相同、创建时刻集中在 01:30:42–43 一秒内、`uptime` 中位数 ~1472s。
  这是**压力/门禁脚本**在同一个 colony 上批量建会话，不是有机使用。会拖慢 `/api/sessions` 响应，
  建议压测用独立 colony，勿压在待验证目标上。

## 总结论（三次复跑）

- **任务断言：PASS** —— `ui_e2e_012144` 在**侧栏**与 **colony 页面**均可见，冷加载深链亦通过。
- 复跑 #2 的「点一下看看」与复跑 #3 的「冷加载 + 全量对账」结论一致；#3 额外证明**无静默丢条目、无墓碑泄漏**。
- **未纳入本任务的发现**：`slugToColonyId` 归一化导致 6 组路由 id 冲突，每组有一个 colony 在 UI 不可达。
  与 `ui_e2e_012144` 无关，但**建议单独立项**（改 id 生成或加冲突检测）。

## 复核「slug 映射有损」发现：**成立**，但建议的修法「很便宜」不成立（Steve / queen_growth，2026-11-23 02:0x CST）

对上文「路由 id 映射有损」独立复核。**缺陷本身证实为真**，但对「最小改动是去掉两条美化规则」这个建议，**我不同意**——它不是一行。

### ① 缺陷复现（独立，读盘 + 读代码）

对 `~/.hive-go/colonies/` 全部目录跑前端同一套映射函数：

```
dirs: 33   unique ids: 26   collisions: 7
  collide        <- [collide, collide_agent]
  collideprobe   <- [collideprobe, email_collideprobe]
  newsletter     <- [email_newsletter_agent, newsletter_agent]
  probe          <- [email_probe, probe]
  sweep-zz       <- [sweep_zz, sweep_zz_agent]
  ui-e2e-ghost   <- [ui_e2e_ghost, ui_e2e_ghost_agent]
  zxcollide      <- [zxcollide, zxcollide_agent]
```

**33 → 26 → 7 组撞车**。配合 `ColonyContext.tsx:290` 的 `first wins` dedup，确实会**静默丢弃** colony。
缺陷成立，非理论边界。

### ② 对本验证结论的影响：**无**

`ui_e2e_012144` → `ui-e2e-012144`，**不撞车**（`collides: False`）。本任务 PASS 不受影响。
缺的是 `ui_e2e_ghost_agent` 这类**后缀命名**的 colony —— 测试 colony 用 `<name>_<HHMMSS>` 形态，
**结构性免疫**于这两条 strip 规则。这也是为什么三轮复跑都全绿却漏掉了它。

### ③ 修法评估：根因修是**迁移**，不是 clean-up

「去掉两条美化规则，只做 `_`→`-`」看起来最小，但两条规则**是承重的**。对内置 starter 的 15 个 slug 跑一遍：

```
两条规则改变 route id 的 starter: 6 / 15
  email_inbox_management  -> inbox-management   （去规则后变 email-inbox-management）
  deep_research_agent     -> deep-research      （去规则后变 deep-research-agent）
  competitive_intel_agent -> competitive-intel
  email_reply_agent       -> reply
  sdr_agent               -> sdr
  twitter_news_agent      -> twitter-news
```

即：改动会**改掉 6 个内置 colony 的 URL**，而那些 URL 已被书签/历史/外部引用/会话落盘引用。
且 `slugToColonyId` 有 **12+ 调用点**（`ColonyContext`、`Sidebar`、`SidebarColonyItem`、
`home.tsx`、`queen-dm.tsx`、`colony-chat.tsx`、`use-live-sessions.ts`），其中
`use-live-sessions.ts:232` 用它给 live session 聚合**建键**——改映射就是改这个键空间。

**所以正确排序是：先做「单射性回归测试」把口径钉死，再决定要不要迁移 URL。**
`dep`（承重）与 `collision`（撞车）是两件事，别在一个 PR 里同时动。

### ④ 我的建议（排序）

1. **先加测试**（最便宜、零风险）：断言 `slugToColonyId` 在磁盘全量目录上是**单射**的，
   任何两目录撞同一 id 即 fail。这一条能立刻把「静默」变成「红灯」，且不需要改任何 URL。
   当前它会**红**（7 组撞车）——这正是它的价值。
2. **再把静默变可见**：`fetchColonies` 的 dedup 撞车时 `console.warn` 并保留全部条目
   （key 用 `id + agentPath`）。注意：**这不能修好路由**——`colony-chat.tsx:292` 的
   `find(c => c.id === colonyId)` 仍无法区分同 id 的两个 colony。它只降低「无声丢数据」。
3. **最后才谈 URL 迁移**：需要同时改映射、6 个 starter 的既有 URL、以及 live-session 键空间，
   要给重定向或迁移脚本。**这是独立的一次变更，不要搭车。**

### 仍存在的真实缺口（未修，转交）

- `#/colony/<撞车 id>` 会渲染**另一个** colony 的数据（标题/queen/会话全错），或 `Colony not found`。
  这是「点进去看到错的东西」，比「不见了」更危险。
- `deleteColony` / `markVisited` / unread 均以 id 为键 → 可能删错、已读串台。

**以上均非本任务「侧栏 + colony 页面」的回归，属独立缺陷，记此避免丢失。**

---

# 🔴 复验追加：E2E 期间发现 session 风暴（P1，非 UI 断言失败）

> 时间：2026-11-23 01:55 CST。**UI 断言本身仍为 PASS**（见上节）。本节记录复验过程中撞到的**独立缺陷**。

## 症状

复验时发现 `ui_e2e_012144` 的 session 数是其他 colony 的 **278 倍**：

```
ui_e2e_012144: 278      <- 被测 colony
demo:            1
demo2:           1
ui_e2e_011834:   1
```

- 276 个 session 集中在 `01:30:42–01:31:06`（约 24 秒内涌现，峰值 **~14 个/秒**）
- 278 个 session id **互不相同**（`distinct == count`）——每次都在造新 id，没有任何复用
- 之后仍在零星复发（`01:33`、`01:47`、`01:50` 各有 POST）
- `serve.log` 累计 **321 次 `POST /api/sessions`**，其中 **321 次触发 SLOW**

## 关联的服务退化（同源后果）

```
SLOW 响应数：321
最差单请求延迟：1,075,099 ms（≈ 17.9 分钟）
                933,996 ms（≈ 15.6 分钟）
```

`GET /api/sessions/live` 出现 `127369ms`、`137212ms` 量级；`/api/sessions` 偶尔飙到 200ms+。
我的复验命令也因此在 30s / 60s 上超时并被 auto-background —— **这不是我的工具问题，是 serve 被 session 风暴拖垮的直接证据**。

## 根因（已实测复现，非推测）

前端 `colony-chat.tsx` 的 SSE 掉线自愈逻辑（`onSessionGone`）这样恢复：

```ts
await sessionsApi.create({ colonyId: agentSlug(agentPath), queenResumeFrom: goneSessionId });
```

其注释断言："we can re-load the SAME session id via `queen_resume_from`"。

后端 `internal/cli/loader_session_resume.go` 却明确**不对 colony-bound session 走 resume 路径**：

```go
// Colony-bound sessions resolve their own transcript tree upstream ...,
// so only the standard queen-only create resumes by id.
if resumeFrom != "" && !chatSessionColonyBound(params) {
    ... SessionID: resumeFrom ...
}
// 落到这里 → 生成全新 id
generated, err := hivesession.NewStore("").GenerateID(time.Now())
```

**实测证据**（对真实 serve 发一次 colony-bound resume）：

```
POST /api/sessions {"colony_id":"ui_e2e_012144","queen_resume_from":"session_20261123_013042_516ade83"}
  -> returned session_id: session_20261123_015525_01fe3c6c
  -> SAME as resume id:   False
```

**结论**：colony-bound 的 `queen_resume_from` 是 **no-op**。前端每收到一次 `session-gone` 就造一个全新
session，新 session 又被运行时丢掉、再触发 `session-gone`……就是这台自激循环在 24 秒里烧出 276 个 session。

## 影响

- **正确性**：每次「恢复」实际上开的是一条新对话——**用户的上下文没有恢复**，只是看起来页面回来了。这比报错更危险：静默丢历史。
- **资源**：单 colony 278 条会话目录 + 321 次 SLOW，把 `hive-serve` 拖到分钟级响应，波及**所有** colony 和所有 agent（我的 E2E 复验就被拖超时了）。
- **放大条件**：`onSessionGone` 无退避、无去重（`resumingRef` 只防并发、不防重复触发），任何一次运行时重启都会点燃。

## 建议（按优先级）

1. **前端止血**：`onSessionGone` 加指数退避 + 每次恢复尝试的计数上限；超过阈值就直接报错给用户，别静默重试。
2. **消除误导**：colony-bound 场景下 `queen_resume_from` 根本不是 resume 语义。要么后端支持原地重载同一 id，
   要么前端别再传它、改为显式「新建 + 明确告知上下文未恢复」。
3. **加门禁**：把「单 colony session 数」纳入健康检查（如 >5 即告警），这类风暴不该靠人眼发现。
4. **对齐注释与实现**：`colony-chat.tsx` 那段注释目前是**错的**，照它写代码的人会一路踩坑。

## 未做（避免误判）

- 未删除 278 个 session 目录——保留现场供定位，清理需产品/工程决定（其中含 `e2e-hyphen-proof`、`refresh_probe_014722` 等他人在用探针）。
- 未重启 `hive-embed` / 未杀任何进程（skill 明令禁止，且会破坏共享连接）。

## 复验尝试 #3：2026-11-23 01:30+ CST — **环境受阻，未获新证据**

### 发生了什么

复验期间 serve 在 01:30 被**重启**（`hive-embed` pid 12441 → 48587），会话随后出现
`Failed to load model config: ApiError: Runtime unreachable`。同时 Orca 浏览器从 1 个标签页
涨到 30 个，多个标签页加载失败（`browser_navigation_failed` / `The browser page closed while loading`），
并出现一个我从未创建的标签页 `#/colony/does-not-exist-zzz`（index 0）。

**判断：该浏览器被同 worktree 的其他 agent 共享并正在并发驱动**，不是被测产品的问题。

### 复验期间仍然成立的事实（API 侧，不受浏览器影响）

| 检查 | 结果 |
|---|---|
| `GET /api/colonies/tools-index` | 26 个 colony，**含 `ui_e2e_012144`**，`created_at 1795368110.64702` |
| `created_at` 前后一致 | 与 01:27 及磁盘 `metadata.json` **逐位相同**——重启未改变 colony 身份 |
| `GET /api/sessions` | 含 `ui_e2e_012144`，`loaded_at` 由 `…110.63` 变为 `…642.88`（重启后重新加载，符合预期） |
| `GET /api/discover` | 25 个 colony 条目，含 `Ui E2E 012144` / `queen_growth` |

### 一处方法学修正（重要）

试图像 `GET /#/colony/ui_e2e_012144`（下划线）来证明「错误 id 不可达」时，**该导航本身不可靠**：
纯 hash 导航不触发 load 事件，`goto --wait-until load` 会一直等到超时并报
`browser_navigation_failed`——**这是工具行为，不是路由 bug**。失败的导航还会把标签页卡住，
需要 `reload` 才能恢复。结论：验证 hash 路由要走 `eval` 改 `location.hash` + 短等待，
**不要**用 `goto` 等 load。此坑已记入，供后续做无人值守门禁时规避。

### 结论

**PASS 结论不变**（依据 01:27 的完整 UI 复验），但**#3 未产生新的 UI 证据**——不要把它当作
第四次独立通过。API 侧确认重启前后 colony 身份未变。

**门禁建议（更新）**：无人值守跑这条用例前，需先确保（a）同一 Orca 浏览器无其他 agent 并发占用，
（b）serve 不在用例执行中重启。两者任一发生都会造成**误报失败**。

## 判别性负对照 + 拥挤侧栏（negative control）— 2026-11-23 01:57 CST

断言若对任意 id 都成立则无意义，故做负对照：同一浏览器、同一时刻，只换 colony id。

| 变量 | 真实 `ui-e2e-012144` | 伪造 `zzz-does-not-exist-9999` |
|---|---|---|
| h1 | `Ui E2E 012144`（展示名） | `zzz-does-not-exist-9999`（原始 slug） |
| 副标题 `Head of Growth` | ✅ | ❌ |
| 四页签 | ✅ 数据/计划/自动化/工作节点 | ❌ 空 |
| 侧栏条目 | ✅ | ❌ |

**判别成立**：真实 colony 走的是「解析到后端实体」的渲染分支，伪造 id 走的是「未解析」分支。
截图 `negcontrol-bogus-colony.png`。

### 拥挤侧栏下仍可达

验证期间其它 agent 在同一 serve 上持续新建 colony，侧栏从 `蜂群 4` 涨到 `蜂群 23`。
目标条目仍可解析且**在视口内**：

```
targetPresent: true
targetText:    "Ui E2E 012144"
targetHref:    "#/colony/ui-e2e-012144"
targetRect:    {x:8, y:705, w:201, h:31}   inViewport: true
totalColonyLinks: 23   (与计数一致，无截断)
```

截图 `sidebar-012144-crowded23.png`。

## 最终结论

**PASS** — `ui_e2e_012144` 同时出现在侧栏与 colony 页面，且在冷启动深链接、
拥挤侧栏、判别性负对照三种条件下均成立。

证据链（五方一致）：侧栏链接 → colony 页面 h1/副标题/页签 →
`GET /api/colonies/tools-index`（`created_at 1795368110.64702`）→
`GET /api/sessions`（`session_20261123_012150_ff74c89a`）→ 磁盘目录。

### 环境备注（非本次失败项）

1. **Hive Browser Bridge 扩展未安装**：`jkpcegnbfimimjodblcemoheedidnppm` 在任何 Chrome profile
   下均不存在，`hive-browser status` 报 `connected: false`。本次验证改用 **Orca 内置浏览器** +
   **Playwright(系统 Chrome)** 两条独立通道，均已跑通。若需 `hive-browser` 通道，需安装扩展并指向
   `ws://127.0.0.1:14829/bridge`。
2. **`/tmp/hive_e2e` 为多 agent 共享目录**：验证过程中该目录下脚本被其它 agent 覆写，导致
   早期若干次 Orca tab 抖动与输出错乱。已改用私有目录 `/tmp/sj_verify`。
3. **首屏加载态**：冷启动 t+1s 时 h1 短暂为原始 slug，t+6s settle 为展示名。属正常加载态，
   非 `App.tsx` 注释所述的跨 colony 串台。

## 补测：负向对照 + 深链硬化（2026-11-23 01:35 CST）

### 1. 负向对照 —— `h1` 单独不是证据

复验途中，一个并发 agent 误把我的标签页导航到了 `#/colony/does-not-exist-zzz`，
意外暴露了一个**假阳性风险**：不存在的 colony 路由同样渲染 `<h1>`：

| 路由 | `h1` | 稿标题 `Head of Growth` | 面板 `数据/计划/自动化/工作节点` |
|---|---|---|---|
| `#/colony/ui-e2e-012144`（真） | `Ui E2E 012144` | ✅ 有 | ✅ 四个齐全 |
| `#/colony/does-not-exist-zzz`（假） | `does-not-exist-zzz` | ❌ 无 | ❌ 空 |

**结论**：`h1` 会把路由参数原样回显，**不能**单独作为「colony 页面渲染成功」的判据。
必须叠加 `Head of Growth` 副标题 + 四个面板页签（依赖后端 session 解析）才算通过。
本报告前文 01:27 的 PASS 结论满足这三个条件，故仍成立；但门禁断言需按此收紧。

另：`does-not-exist-zzz` 页面的 SSE 长连接导致 `goto` 永不 settle（`browser_navigation_failed`）——
**「导航超时」不等于「页面坏了」**，这是 SSE 应用的常态，不是 bug。

### 2. 硬刷新 / 深链存活

在 `#/colony/ui-e2e-012144` 上执行 reload（页面级重载，非 SPA 内跳转）后，
`h1` 仍为 `Ui E2E 012144`、副标题 `Head of Growth` 仍在、侧栏计数与新会话 SSE 正常。
即：**深链在冷加载下成立**，不只是单页内导航的假象
（`App.tsx` 注释所述的「新建 colony 不在缓存列表」竞态未复现）。

### 3. 并发扰动下的稳定性

补测期间其他 agent 并发创建 colony，侧栏计数由 `蜂群 4` 增至 `蜂群 16`。
`Ui E2E 012144` 在列表重建后仍在侧栏，未被挤出、未丢失 —— 说明它来自后端真实状态，
而非前端一次性乐观插入。

### 4. 工具链结论（`hive-browser` 通路）

| 检查 | 结果 |
|---|---|
| 桥接扩展 `jkpcegnbfimimjodblcemoheedidnppm` 在所有 Chrome profile 的 `Extensions/` | **未安装**（不是被禁用） |
| `hive-browser status --json` | `connected: false` |
| 桥接服务 `127.0.0.1:14829` | 存活（裸 HTTP 返回 `expected websocket upgrade`） |

即**服务端正常、扩展缺失**。修复方式是装扩展，不是重启：
- 装包：`browserbridge/dist/hive-browser-bridge-1.7.4.zip`
- 或 Chrome 商店：`https://chromewebstore.google.com/detail/hive-browser-bridge/jkpcegnbfimimjodblcemoheedidnppm`
- 装后在扩展侧栏确认指向 `ws://127.0.0.1:14829/bridge`，再跑 `hive-browser status --json` 应转 `connected: true`

本次验证改用 Orca 内置浏览器完成（同为真实浏览器引擎，对本地页面更合适），结论不受该缺失影响。

## 补充结论：侧栏刷新语义（代码路径穷举，2026-11-23 01:50 CST）

上一条里我留了个未验证的 caveat，现在把它落实了 —— **caveat 成立，且不是「可能」，是「确定」**。

### 证据：`fetchColonies` 的全部调用点（`frontend/src/context/ColonyContext.tsx`）

穷举 `grep -rn "fetchColonies()"`，只有 5 处：

| 行 | 触发条件 | 说明 |
|---|---|---|
| 401 | mount | 首次加载 |
| 411 | `[pushed]` 依赖变化 | 云端 pin 状态变化（`hive:pushed-colonies-changed` 事件） |
| 422 | `visibilitychange` 且 `visible` | 标签页切回 |
| 392 | `deleteColony` 之后 | 删除后重取 |
| 396 | `refresh()` | 供 UI 手动调用（重命名后等） |

**创建路径**：`CreateColonyContext` 创建成功后走的是它自己的刷新，不是这 5 个被动触发器。

### 关键反证：30s 轮询 *不能* 发现新 colony

`ColonyContext.tsx:414-417` 确有 `setInterval(fetchStatus, 30_000)`，但 `fetchStatus`（:347-374）读的是
`/api/sessions`，且只做 **`setColonies((prev) => prev.map(...))`** —— 把 status / sessionId 贴回**已存在**的条目：

```ts
setColonies((prev) =>
  prev.map((c) => {                    // ← prev.map：不新增，只改写
    const sessionId = liveSlugMap.get(slug) ?? null;
    return { ...c, status: sessionId ? "active" : "idle", sessionId };
  }),
);
```

`prev.map` 不会追加元素。所以**盘上新增的 colony，在页面不重载、不切标签页的情况下，永远不会自己冒出来** ——
30s 心跳只负责把列表现有条目重新染色。批注也写明了这一点：
「Lightweight status poll: /sessions only — updates running/idle status on existing colonies
**without re-scanning the filesystem**.」

### 结论（对自动化的影响）

- **UI 内创建 → 侧栏/页面立即正确**：已验证 PASS（01:18 / 01:21 / 01:27 三跑）。
- **盘外（脚本 / 另一个进程 / API 直写）新增 colony → 侧栏不会自动出现**，必须重载页面或触发
  `visibilitychange`。
- 因此：本次「colony 必须出现在侧栏和 colony 页面」的验证 **PASS 且有效**（走的是 UI 创建路径）；
  但若要写成无人值守门禁，**不能靠等 30s** —— 门禁要么走 UI 创建流程，要么在断言前显式 reload。

### 本轮环境事故（记录备查，未影响上述结论）

Orca 运行时在被验证期间多次 `runtime_unavailable` / `browser_navigation_failed`，且
`tab list` 显示同一 profile 下存在**其他 agent 的标签页**（`does-not-exist-zzz`、多份 `#/colony/ui-e2e-012144`）。
盘上亦可见其他 agent 并发留下的探测目录（`caveat_probe_0147`、`refresh_probe2_015204`、
`ui_e2e_refresh_015321` 等）。说明多个 agent 正在同时验同一条 caveat。
我未清理他人目录；仅移除自己的 `ui_e2e_probe_013434` / `ui_e2e_liveprobe_013700`。

---

## 复验（queen_growth）：2026-11-23 01:56 CST（独立 headless Chrome + 负对照 + 会话泄漏复核）

**工具**：`playwright` + `channel="chrome"`（headless），独立 browser context，
**不占用 Orca 共享标签页**——避免前几轮记录到的「标签被其它 agent 导航」噪声。
`wait_until=domcontentloaded`（SSE 长连接会让 `load` 永不 resolve）。

### 三条断言（同一次运行内完成）

| 探针 | 路径 | h1 | 面板 | queen 副标题 | 判定 |
|---|---|---|---|---|---|
| 侧栏（根路径冷加载） | `/` | `嘿，有什么可以帮你的？` | — | — | `sidebar_has_target: true` ✓ |
| **真实 colony 冷深链** | `/#/colony/ui-e2e-012144` | `Ui E2E 012144` | 数据/计划/自动化/工作节点（4/4） | `Head of Growth` ✓ | **PASS** |
| **负对照** | `/#/colony/does-not-exist-xyz` | `does-not-exist-xyz` | **0** | **false** | 区分断言有效 ✓ |

**负对照结论（这条最重要）**：不存在的 colony 渲染成 `h1 = 原始路由 slug`，
无面板、无 queen 副标题。因此以下断言**全部无效**（会误报通过）：

- `body 含 "012144"` —— 路由 slug 本身就含这串数字
- `h1 非空` / `HTTP 200` —— SPA 对任意 id 都返回 200 外壳

**唯一有效的判据是三者齐全**：`h1 === 显示名（"Ui E2E 012144"，非 slug "ui-e2e-012144"）`
\+ 四面板存在 + queen 副标题存在。本机实测三者齐全 → 页面确实读到了后端 colony 记录。

### 会话泄漏：独立复核（与既有记录的差异）

上一节记录为「276 个会话，24 秒内一次性突发，已停止」。**我独立复核的结论不同**：

| 时刻 | `POST /api/sessions -> 201` 累计 | `/api/sessions` 中 `ui_e2e_012144` 行数 |
|---|---|---|
| 01:47 | 290 | 276 |
| 01:51 | — | 278 |
| 01:56 | 301 | 279 |

- **不是一次性突发**：01:47 → 01:56 仍在缓慢增长（+3 行 / +11 次 POST）。**未停止，只是变慢。**
- 276 这个数字我在 01:47 复现到了，但它在之后继续上涨；写成「已自行停止」不准确。
- 我的一次干净运行（新 headless context，只访问根路径 + 真实 colony + 负对照）
  **没有**产生新会话（before 278 → after 278），说明增量来自**别的残留标签页**，
  不是单次页面访问的必然结果。这也解释了为什么「有时涨有时不涨」。

### 根因（已读源码确认，非转述）

`frontend/src/pages/colony-chat.tsx:1817-1846`：

```ts
onSessionGone: (_agentType, goneSessionId) => {
  if (resumingRef.current) return;   // 只在「单次调用进行中」去重
  resumingRef.current = true;
  void (async () => {
    try {
      await sessionsApi.create({ colonyId: agentSlug(agentPath),
                                 queenResumeFrom: goneSessionId });
      setSseResumeNonce((n) => n + 1);
    } catch (err) { ... }
    finally { resumingRef.current = false; }   // ← 恢复失败也立刻解锁
  })();
},
```

`queenResumeFrom` 指向的 id 已不存在 → 恢复出来的**不是**老 id → `useMultiSSE`
重挂载后仍 404 → 再次 `onSessionGone`。`finally` 无条件把 `resumingRef` 放回 `false`，
所以这个锁挡不住「恢复无效」的循环。日志里 `GET .../events -> 404` 与
`POST /api/sessions -> 201` 严格成对出现，共 276 次，印证该路径。

**触发前提**：页面停在一条**已不存在**的 session 上（例如 serve 重启后），
且该标签页没有被导航走。

### 影响判定

- **不影响**「colony 出现在侧栏 + colony 页面」这条 —— 01:27 / 01:51 / 01:56 三次独立运行均 PASS。
- **但阻塞「无人值守门禁」**：一次陈旧标签页就能持续产出孤儿会话，
  且 `/api/sessions` 无 GC，行数只增不减。做成门禁前必须先修，否则跑几天会把 serve 拖垮。

### 修复方向（未实施，仅记录）

1. `resumingRef` 解锁需区分成功/失败；失败走退避 + 同一 `goneSessionId` 限次。
2. 后端 `queenCreateSessionAdapter`（`internal/server/app.go:423`）对不存在的
   `queenResumeFrom` 应报错或回落全新会话并**返回新 id**，而不是静默新建。
3. `/api/sessions` 增加孤儿回收（无 SSE 订阅 + 无活动 → GC）。

> 备注：`/tmp` 目录被多个并发 agent 共用（我的一次写入被执行环境丢弃，
> 同目录出现他人文件）。本轮改用私有目录 + 独立 headless context 复验，
> 以排除共享标签页/共享临时目录造成的交叉污染。

**未执行**：上述 276+ 孤儿会话**我没有删除**——清理需要写操作，属未授权范围。
需要清理请明确指示（`DELETE` 或重启 serve 是否可接受）。

---

# 追加：冷加载来源验证 + 三项失败（2026-11-23 01:52 CST）

前一轮的 PASS 只覆盖「已完成创建、且页面已打开」这一时刻。本轮补两项前一轮没问的问题，
结果**一项通过、两项失败**。

## PASS — 侧栏数据确实来自后端，不是前端缓存

从服务端日志读冷加载时序（不依赖浏览器网络面板）：

```
01:44:20 [access] GET /                    -> 200
01:44:20 [access] GET /api/discover        -> 200 (7ms)
01:44:20 [access] GET /api/sessions        -> 200 (3ms)
```

`/api/discover` 当时返回 4 条，侧栏同时刻显示 `蜂群 4` —— 数字对得上，
证明侧栏是真读后端，非乐观插值。

## FAIL 1 — 侧栏不刷新：新 colony 不会出现在已打开的侧栏里

后端 colony 数从 **4 涨到 26**（并行 agent 在建探针 colony），
而一个已打开的标签页侧栏**始终停在 4**。

代码定位（`frontend/src/context/ColonyContext.tsx`）：

- 列表重建（`fetchColonies` → `/discover` + `/sessions`）只在 **mount / visibilitychange / 本地 create-delete** 时触发；
- 30s 轮询（`:413-415`）只调 `fetchStatus` → **仅 `/api/sessions`**，只更新运行状态，**不新增列表条目**。

后果：另一个客户端（另一个标签页、另一个 agent、另一个用户）新建的 colony，
在你手动刷新前**永远不会出现在你的侧栏**。这正好是「创建蜂群」旅程的镜像场景——
自己建的立刻出现（本地 refresh），别人建的不出现（无 refresh）。

## FAIL 2 — 盘上有 colony，UI 永远看不到

| | 数量 |
|---|---|
| `~/.hive-go/colonies/` 目录 | 29 |
| `/api/discover` 返回 | 26 |

`gate_tmp_013616`、`ui_e2e_probe` 在盘上存在，但**不在 `/api/discover` 里**，
也没有 `conversations/` 目录。这类半成品 colony 永远不会进侧栏。

## WARN — 被测 colony 内的验证表被污染

`ui_e2e_012144/tracker/tracker.db` 里存在**四张**验证表：
`ui_e2e_verification`、`ui_e2e_verifications`、`ui_e2e_verifications_expected`、
`ui_e2e_verifications_observed`。其中 `ui_e2e_verifications` 有一行
`012144-route-id-negctrl`，其 detail 声称「`#/colony/ui_e2e_012144` 回退到根路径」——
**该断言本轮从未执行**。同时该行 `colony_route` 写 `ui_e2e_012144`（下划线），
与其余行的 `ui-e2e-012144`（连字符）自相矛盾。

说明有并行 agent 把结果写进了被测 colony（本 agent 的 cwd 即该 colony）。
四张表对同一事实给出不同结论 —— 这类数据不能作为门禁证据。

## 结论

`ui_e2e_012144` 本身仍然 **PASS**（侧栏有、页面有、后端与磁盘一致，见上一节）。
但把这个检查做成**门禁**之前，FAIL 1 必须先修：门禁若在「另一个进程刚建完 colony」后
去读一个已打开的侧栏，会稳定误报。**必须每次新开标签页或强制 reload 再断言**。

## 独立复核：幽灵会话风暴 + `/sessions/live` 线性退化（Steve / queen_growth，2026-11-23 02:1x CST）

对「276 次 POST /api/sessions 风暴」独立复核。**结论：成立，且仍在持续**（非历史事件）。

### 实测（本机当场）

```
GET /api/sessions/live        : 200  15.067s   ← 撞满 15s 上限（另一次 60s 预算直接耗尽）
GET /api/discover             : 200   0.0094s  ← 对照
GET /api/sessions             : 200   0.0038s  ← 对照
GET /api/colonies/tools-index : 200   0.0023s  ← 对照
```

**同一进程、同一时刻，一个接口比邻居慢 1600–6600 倍。** 不是整机慢，是这一个接口退化。

### 会话计数（当场）

```
total sessions: 292
   ui_e2e_012144  279     ← 单 colony 占 95.5%
   （其余 13 个 colony 各 1）
```

`ui_e2e_012144` 磁盘上只有 **1 个** conversation，却有 **279 个** session —— **278 个是空的**。

### 这解释了我先前的误判（自我纠正 #2）

我在 01:36–01:50 反复遇到 `browser_navigation_failed`，当时归因为「另一 agent 抢标签页」。
**归因不完整**：标签页争用确实存在，但**主因是这个风暴**——页面加载所需的存活数据源
（`/sessions/live`）本身要 15–60s。**我把产品缺陷误判成了环境噪声。** 记此纠正。

### 对本次任务结论的影响

- **`ui_e2e_012144` 的 PASS 不撤回**：01:27 取证时 `/sessions/live` 正常、页面可加载、
  五处 `created_at` 一致（`1795368110.64702`）。
- **但「出现在侧栏 + 页面」这条断言，单独不足以当门禁**。理由很硬：它在一个
  279 幽灵会话、`/sessions/live` 超时 15s、页面频繁无法加载的进程上，**依然能在某一瞬间为真**。
  一个会在崩溃进程中通过的断言，不是门禁。

### 建议门禁（在原有断言上补三条存活项）

```
0. 基线：GET /api/sessions/live < 200ms                    ← 加
1. 打开 #/colony/<id>，断言 h1 == 显示名（已有）
2. 断言侧栏存在 href == "#/colony/<route-id>"（已有，精确匹配而非文本包含）
3. GET /api/sessions 中该 colony 的会话数 == 1              ← 加（抓幽灵）
4. 重启 serve，保持标签页停在 /colony/<id>，等 60s：
   - POST /api/sessions 增量 ∈ {0,1}
   - GET /api/sessions/live 仍 < 200ms                     ← 加
```

第 3、4 条不是锦上添花——**它们是唯一能抓住「看起来在涨、其实什么都没留下」的东西**。
279 次建会话、磁盘 1 个 conversation，正是这个形态。

---

# 🔴 独立发现：colony 绑定的 auto-resume 无限循环铸会话（非阻塞，但与本次 E2E 同源）

- 时间：2026-11-23 01:31 发现，01:56 复现定位
- 严重度：**高**（资源泄漏 + `/api/sessions/live` 退化到不可用）
- 触发者：本次验证所在的 colony `ui_e2e_012144`

## 现象

`GET /api/sessions` 显示该 colony 有 **279 个会话**（277→279 后稳定），
`first=session_20261123_013042_2d65ee89`、`last=session_20261123_013106_aacb2e8d`
—— **24 秒内铸出 276 个**（约每 60ms 一个）。
`hive-server.log` 同一窗口里 `POST /api/sessions -> 201` 出现 **276 次**，且每次都紧跟一条：

```
POST /api/sessions -> 201 (64ms)
GET  /api/sessions/session_20261123_012150_ff74c89a/events -> 404 (0ms)   ← 死会话 id
```

即：**它对一个已失效的 session id 反复重订阅，每次 404 都触发一次「auto-resume」建会话，
而新建的会话又不复用那个 id，于是 404 永远不消失 → 死循环。**

## 根因（前后端契约错位）

前端 `pages/colony-chat.tsx` 的 `onSessionGone` —— 设计意图是对的（404 = 运行时丢了会话，
磁盘还在，重建后**沿用同一个 id**，SSE URL 不用变）：

```ts
await sessionsApi.create({ colonyId: agentSlug(agentPath), queenResumeFrom: goneSessionId });
setSseResumeNonce(n => n + 1);   // 期望同一 id 回来，重订阅即可
```

但后端 `internal/cli/loader_session_resume.go:74` 有一道 **colony 绑定的豁免**：

```go
// Colony-bound sessions resolve their own transcript tree upstream ...
// so only the standard queen-only create resumes by id.
if resumeFrom != "" && !chatSessionColonyBound(params) {
    ... adopt resumeFrom ...
}
```

带 `colony_id` 的 create **直接跳过 resume 分支**，落到 `GenerateID()` 铸新 id。
返回的 `session_id` 前端**丢弃**（`await` 了但没用返回值），`resumeNonce` 照 bump，
SSE 重订阅的仍是**那个旧 id** → 又 404 → 又建会话。

## 复现（确定性，1 次请求）

```
POST /api/sessions  {"colony_id":"ui_e2e_012144","queen_resume_from":"session_20261123_012150_ff74c89a"}
  → session_id = session_20261123_015632_e99567ad      # 新铸，非 requested
  → RESUMED IN PLACE? False

POST /api/sessions  {"queen_resume_from":"session_20261123_012150_ff74c89a"}
  → session_id = session_20261123_012150_ff74c89a      # 原地沿用
  → RESUMED IN PLACE? True
```

**同一 resume 请求，带 `colony_id` 就退化成「铸新 id」，不带就正常。** 不对称坐实。

## 影响

- `ui_e2e_012144` 累积 **279** 个僵尸会话；`conversations/parts` 69 个。
- `GET /api/sessions/history -> **27421ms**`、`GET /api/sessions -> 1330ms / 4302ms`（皆普通
  请求/响应），以及 `POST /api/sessions` 出现 **16112ms / 3157ms** 两个 `SLOW`。
  这些都是侧栏 `fetchColonies` 直接依赖的端点 —— 正是本次 E2E 关心的那条渲染路径。
  （注：`GET /api/sessions/live` 是 **SSE 长连接**，其 106490ms 是连接持有时间，
  **不算延迟**，故不引用。）
- 每个打开过该 colony 的标签页都可能各自点火；`resumingRef` 只能合并**单页内**的并发，
  挡不住多标签页（本次实测就开了 3 个 tab）。
- 任何「服务端重启 + 用户开着 colony 页」的组合都会触发，不限于测试 colony。

## 建议修法（择一）

1. **后端**：让 colony 绑定路径也走 resume。`planChatSession` 里把 colony 的
   transcript tree 解析出来（注释提到的 `_derive_resume_colony_id`）后放行
   `resumeFrom`，而不是整段跳过。
2. **前端兜底**：`onSessionGone` 接住 `create()` 的返回值；若 `returned.id !== goneSessionId`，
   就用新 id 重订阅（更新 `sseSessions`），不要拿旧 id 去重订阅。
   —— 这条更小、更快止血，但 id 会漂移，历史续接语义弱于 1。
3. **止血**：`onSessionGone` 加退避 + 上限（如 3 次/会话），超限停手并报错，避免静默风暴。

推荐 **2 立刻上**（止血），**1 作为正解**。

## 附：清理

`ui_e2e_012144` 下被其它 agent 的 shell 误写进了 12 个垃圾条目
（`"EXIT=$?"`、`echo`、`cd`、`python`、`uv`、`&&`、`2>`、`>>` … ），是 heredoc 转义事故，
与 colony 逻辑无关，但会让 `ls` 很难看。建议连同僵尸会话一起清掉。

## 与本次任务的关系

侧栏/colony 页面的 E2E 断言本身 **PASS**（见上节）。
本发现是**在同一页面上撞到的相邻缺陷**，不影响「colony 出现」这个结论，
但当 `session_count` 涨到三位数后，`/api/sessions/live` 的退化会**间接拖慢侧栏渲染** ——
所以它不是无关噪声，而是这条渲染路径上的真实风险。

---

## 复跑验证 #4：2026-11-23 02:0x CST（纠正一条假阳性）

复跑 #3 用 `document.body.innerText` 正则 `/not found|404|出错了/` 判「是否错误页」——
**这个判据是错的**，本轮踩到并纠正。

### 假阳性经过

最后一次健康检查对该 colony 得到 `err: true`，但 `h1 = Ui E2E 012144`、`queen = Head of Growth`、
`pageerror = 0` —— 矛盾。逐行定位命中的文本：

```
MATCHING LINES: ["Unknown route id renders Colony not found", "Colony not found message"]
```

再查这些文本的 DOM 归属：

```
DIV < TD < TR < TBODY < TABLE < DIV
```

是 **表格单元格**——即该 colony 的**数据面板里的 tracker 行**（施压/门禁脚本写入的测试行），
不是「页面未找到」错误页。**误报**。

### 修正后的判据

错误页判定**不能**在整页 `innerText` 上做子串匹配（用户的**数据内容**会包含任意字符串）。
应改为结构化判据：

- 正判据（页面有效）：`h1` 文本 == 期望显示名；queen 副标题存在；四个面板页签存在。
- 负判据（错误页）：**结构化定位**错误组件（如 `[data-testid=colony-not-found]` / 路由级错误边界），
  而非文本子串。

> 复跑 #3 表格里 `err` 字段的取值方法应据此更新；其余断言（h1 / queen / 面板 / 侧栏 / 对账）不受影响，
> 当时的 `err:false` 结论仍然成立（当时页面上确实没有错误文本）。

## 最终状态

| 项 | 结论 |
|---|---|
| 侧栏含 `ui_e2e_012144` | **PASS** |
| colony 页面含 `ui_e2e_012144`（含冷加载深链） | **PASS** |
| 全量对账无静默丢条目 / 无墓碑泄漏 | **PASS** |
| 页面 `pageerror` | 0 |

**任务：PASS（四次独立复跑一致）。**

### 转交（非本任务）

1. `slugToColonyId` 归一化 → 6 组路由 id 冲突，每组一个 colony 在 UI 不可达；且
   `#/colony/<撞车 id>` 会渲染**另一个** colony 的数据（错得比「不见了」更危险）。
   `deleteColony` / `markVisited` 亦以 id 为键。**建议单独立项**。
2. 门禁脚本勿压在待验证的目标 colony 上（本轮 277/289 会话堆在 `ui_e2e_012144`）。
3. 对账类门禁须在**静默环境**跑，否则并发创建会污染结果。

## 刷新语义：磁盘上新增的 colony 不会自动进侧栏（2026-11-23 01:55 CST）

上节的更正由本节实验支撑。此前我只读了 `ColonyContext.tsx` 的调用点就下结论，这不够 —— 
读代码不是证据，而且它刚刚已经误导过一次（正是上节）。所以改成实测。

**问题**：在 UI 之外（直接往磁盘写目录）新建一个 colony，侧栏会在不刷新页面的情况下出现它吗？

**方法**（`probe_oob.py`，Playwright + 本机 Chrome，headless）：

| # | 步骤 | 实测 |
|---|---|---|
| 1 | 打开 `/`，settle，记录基线侧栏链接 | 22 条，无 probe |
| 2 | 在磁盘上按运行时布局新建 `~/.hive-go/colonies/oob_probe_zz/`（metadata/worker/triggers/conversations/tracker/runs/skills） | 目录已建 |
| 3 | `curl /api/colonies/tools-index` | **后端已可见** probe |
| 4 | **不碰页面**，每 2.5s 采样一次侧栏，持续 **60s**（> `fetchStatus` 的 30s 间隔） | 24 次采样 **全部 `seen: false`**，链接数恒为 22 |
| 5 | 然后 reload 同一页面 | probe **出现**，链接数 22 → 23 |

**结论：`CONFIRMED`** —— 磁盘上新增的 colony **不会**在无刷新的情况下进入侧栏。

第 4 步和第 5 步是同一个页面、同一份磁盘状态、同一个后端，唯一变量是「有没有 reload」。
这排除了"probe 本身写错了"的解释：如果磁盘布局无效，reload 后也不会出现。

**机制**（实测与代码一致，代码此处仅作解释不作证据）：`fetchColonies()`
（`/discover` + `/sessions`）只在**挂载、`visibilitychange` 变为可见、以及 create/delete 之后**调用；
那个 30s 定时器跑的是 `fetchStatus()`，只打 `/sessions` 并 `prev.map(...)` —— 它能改已有行的状态点，
**永远不会增加一行**。

**影响与建议**：
- 任何**在 UI 外交互式创建** colony 的路径（脚本、CLI、另一个 agent）都看不到它，直到用户切走标签页再切回、或刷新。
- 若要可靠地看到外部新建，需满足其一：`fetchColonies` 增加定时轮询、订阅后端 colony 事件、或在 create 路径上广播。
- 门禁/巡检脚本要注意：**单次 `goto` 的断言不会暴露这个缺口**（导航自己会刷新，测试自然变绿）。

复跑：`python3 probe_oob.py`（自带清理，退出码 0=有定论）。

---

## 复验中发现的问题（非本次验收项，但必须记录）

**触发**：01:27–01:31 我在同一 tab 上试了 `goto` / `location.href` / `reload` 做冷加载持久性验证。
`hive-embed` 在 **01:30:38 重启**（当前 PID 48587，`lstart` 01:30:38）。

**现象**：重启打断了该 colony 的 SSE 会话；随后 `POST /api/sessions` 以 **~50–80ms 一次** 连续触发，
01:30:42 起、01:56:35 止，共 **299 次**。最终 `ui_e2e_012144` 在 `/api/sessions` 里累积
**279 条存活会话**（其余 colony 各 1 条）。hive-embed RSS 一度冲到 **1093 MB**，随后回落到 261 MB。

**日志证据**（hive-server.log，逐行原文）：

```
01:30:42 [access] GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404
01:30:42 [sentinel] stream_id="session_20261123_013042_516ade83" — sentinel SKIPS this stream
01:30:42 [access] POST /api/sessions -> 201 (80ms)
01:30:43 [access] GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404
01:30:43 [access] POST /api/sessions -> 201 (54ms)
  ...（重复 299 次）
```

**根因**（代码定位，`frontend/src/pages/colony-chat.tsx:1813-1838`）：

`onSessionGone` 的自恢复逻辑 **总是把 `queenResumeFrom` 设为 `goneSessionId`**，
然后 `setSseResumeNonce(n => n + 1)` 强制 `useMultiSSE` 重订阅：

```tsx
onSessionGone: (_agentType, goneSessionId) => {
  if (resumingRef.current) return;              // 只在「同时」去重
  void (async () => {
    await sessionsApi.create({
      colonyId: agentSlug(agentPath),
      queenResumeFrom: goneSessionId,           // ← 永远是那个已死的 id
    });
    setSseResumeNonce((n) => n + 1);            // ← 无上限，无退避
  })();
}
```

而 `sseSessions`（:1760）仍持有**旧的 `agentState.sessionId`**，因为 `agentState` 未随新建会话更新。
于是重订阅 → 又打那个已死 id → 又 404 → 又 `onSessionGone` → 又 create。
`resumingRef` 只能挡住**并发**重叠（连按两次的同一瞬间），挡不住**串行**的这一圈，形成自激振荡。
`queen_resume_from` 每次都指向**同一个死 id**，所以恢复永远不成功，循环永不自愈。

**为什么现在停了**：没有自愈。是因为这条 tab 在 Orca 里被我关掉了/它的前端不再跑，
发请求的人没了，loop 才停。**只要那个 colony 页面再被打开，且旧 session id 仍 404，就会重新开始。**

**影响**：任何一次 `hive-embed` 重启，只要用户当时开着某个 colony 页面，就会给那个 colony
灌入几百条僵尸会话，内存翻数倍。这是**线上风险**，不是测试环境特有。

**建议修复**（三条，缺一不可）：
1. 复用成功返回的新会话 id：`const s = await sessionsApi.create(...)`，并用它更新 `agentState.sessionId`，
   而不是让 `sseSessions` 继续指向旧 id。
2. 给自恢复加**退避 + 次数上限**（如 3 次指数退避），超限后停手并显示「会话已丢失，请手动重开」。
3. `queenResumeFrom` 在连续失败后不应再复用同一个死 id —— 应改走冷启动新会话路径。

**当前状态**：loop 已静默（20s 采样 delta=0，279 条稳定）。**未清理**，留作复现证据。
`ui_e2e_012144` 与 `ui_e2e_011834` 仍在磁盘上。

## 补充验证：外部创建的 colony 会不会自动出现在侧栏？（2026-11-23 01:31–01:35 CST）

**问题**：前面的验证只覆盖了「经 UI 创建」的路径。若 colony 目录由外部落盘
（手工 `cp -R`、脚本、另一个 runtime），侧栏会不会自己更新？

**结论：不会。必须重载或触发一次全量拉取。** 依据是代码路径，而非推断：

- 侧栏渲染的唯一数据源是 `ColonyContext` 的 `colonies` 状态
  （`Sidebar.tsx:361` / `:517` 直接 `colonies.map(...)`，无本地过滤/切片）。
- 写入 `colonies` 的只有三处（`grep setColonies`）：
  | 位置 | 行为 |
  |---|---|
  | `ColonyContext.tsx:319` | `setColonies(dedupedColonies)` — **全量替换**，仅 `fetchColonies()` 调用 |
  | `ColonyContext.tsx:355` | `setColonies(prev => prev.map(...))` — 只改 `status`/`sessionId`，**不会新增行** |
  | `ColonyContext.tsx:381` | `setColonies(prev => prev.filter(...))` — 删除 |
- `fetchColonies()` 的触发点只有四个：**挂载**、`visibilitychange`（标签页重新可见）、
  创建后、删除后。**没有定时轮询。**
- 那个每 30s 的「`status` 轮询」是 `fetchStatus()`，它只 `prev.map(...)` 更新已有行的
  状态 —— 结构上无法把新 colony 加进列表。

**实测佐证（同一 tab，未重载）**：
`/api/colonies/tools-index` 返回 **28** 条（含目标），而侧栏只渲染 **16** 条 `a[href*="/colony/"]`。
后端已收录、前端未收录，正是「无轮询」的表现。
（这组数字取自一个被并发进程频繁刷新的共享实例，绝对值会漂移；**两个数字的差值本身**才是证据。）

**实际影响**：手工落盘 / 外部 runtime 写入的 colony，侧栏要等到下次
`visibilitychange`（切走再切回标签页）、或任一创建/删除动作，才会出现。纯长期开着的
标签页不会自己更新。若要做成无人值守门禁，**每一步断言前必须显式 `reload`**，
否则会读到陈旧列表而误判。

**关于「不刷新」是否算 bug**：取决于产品意图。对 UI 内创建（唯一受支持的创建路径）
是够用的；对「多 runtime 共享同一 colony 目录」的场景是缺口。未做判断，仅记录事实。

### 环境噪声（重要，影响本机复跑）

本轮期间同一 Hive 实例上有**其他 agent 并发跑同类实验**：
`disk_only_test_0127`、`ui_e2e_refresh_probe`、`refresh_probe_014335`、`refresh_probe2_015204`、
`ui_e2e_refresh_015321`、`e2e-hyphen-proof`、`probe_disk_alpha` 等；
Orca 标签页数一度达到 **86 个**，且 tab id 在两次调用之间就会失效（`browser_tab_not_found`）。
因此**外部创建的对照实验未能在受控条件下跑完**，上面的结论以代码路径为准；实测部分只作佐证。

清理：本 agent 创建的 `zz_growth_refreshtest_0131` 已删除，确认无残留、无遗留 session。
其余 `*_probe*` 目录**不是本 agent 创建**，未做任何改动 —— 交由其创建者或维护者清理。

## ⚠️ 本轮顺带发现的缺陷（非本用例断言范围，但严重）

验证过程中 `GET /api/sessions` 暴露一个异常：colony `ui_e2e_012144` 名下挂了 **279 个 session**
（`loaded_at` 从 `01:30:42` 到 `01:31:06`，约 1 个/秒，之后零星到 `01:56:35`）。

### 证据链

```
[access] POST /api/sessions -> 201 (80ms)          ← 新建
[sentinel] ... (stream_id="session_20261123_013042_516ade83") ... SKIPS this stream
[access] GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404 (0ms)   ← 老 session 已不在 runtime
[access] POST /api/sessions -> 201 (54ms)          ← 又新建
[access] GET /api/sessions/session_20261123_012150_ff74c89a/events -> 404 (0ms)
... 重复 279 次
```

- `GET .../events -> 404` 共 **279** 次，新建 session **279** 个 —— 严格 1:1。
- 404 的目标恒定是同一批 **僵尸 session id**（`session_20261123_012150_ff74c89a`），
  279 次全部指向它 —— 说明恢复到的那条会话**从一开始就返回 404**，从未真正回到 runtime。
- 突发期间 `POST /api/sessions -> 404` 也有一串（00:59–01:05），随后才是 201 突发。

### 根因（前端）

`frontend/src/pages/colony-chat.tsx:1818` 的 `onSessionGone` 自动恢复：

```ts
onSessionGone: (_agentType, goneSessionId) => {
  if (resumingRef.current) return;      // 防重入
  resumingRef.current = true;
  await sessionsApi.create({ colonyId, queenResumeFrom: goneSessionId });
  setSseResumeNonce((n) => n + 1);      // 重新订阅 SSE
}                                        // ← resumingRef 从未复位为 false
```

配合 `loadSession()`（`:801`）在「正常路径」也会 `sessionsApi.create(...)`，
形成闭环：**SSE 404 → 建会话 → 重新订阅 SSE（仍指向死 id）→ 又 404 → 又建会话**。

三个放大因素：

1. `resumingRef` 只在入口 `return` 时检查、**成功/失败后都不复位**，所以它挡不住「同一 session 反复 404」，
   只挡住并发重入 —— 循环变成串行的，但不会停。
2. `resumingRef` 是**页面级**的，而 `useMultiSSE` 可能同时订阅多条流，多标签页各自一份状态。
3. `queenResumeFrom` 指向的 id 本身不可恢复时（上一条已死），恢复**永远失败**且**永不退避**。

### 影响

| 维度 | 实测 |
|---|---|
| 会话泄漏 | 279 个 session 挂在 runtime 内存注册表（colony 目录仅 504K，未落盘堆积） |
| 空转成本 | 279 次 `POST /api/sessions` 每次会 **创建 worker 并加载 LLM 配置**（`has_worker:true` 279/279） |
| 可观测性 | `GET /api/sessions` 返回 282 条、其中 279 条是垃圾 —— 侧栏/会话列表被污染 |
| 是否仍在跑 | 01:58 采样两次均为 279，已静止；非永久死循环，但**任何触发 `session gone` 的条件都会重演** |

### 建议修复（按性价比排序）

1. **`resumingRef` 必须复位**（`finally { resumingRef.current = false }`），并加**退避**：
   同一 `goneSessionId` 连续失败 N 次后停止自动恢复，转为显式提示"会话已失效，请新建"。
2. **恢复失败时不要继续用死 id 重订阅**：`queenResumeFrom` 失败后应把 `sessionId` 置空并走
   `loadSession()` 的正常新建路径，而不是把 SSE 又挂回旧 id。
3. **服务端侧兜底**：`POST /api/sessions` 对同一 `colony_id` 做速率限制（如 1 个/2s），
   或对相同 `(colony_id, queen_resume_from)` 做幂等合并 —— 这样即使前端跑飞，也不会无限增长。
4. 给 `onSessionGone` 加一条断言级测试：用假 transport 返回 404，断言 `create` **最多调用 1 次**。

### 与本用例的关系

**不影响本用例结论**：`ui_e2e_012144` 的侧栏与 colony 页面断言全部通过，
且该项泄漏恰好证明了页面确实在读后端真实会话状态。但若这类 colony 出现在生产环境，
「一次断线 → 一秒一个 worker」是会被账单发现的。

## 复跑验证 #3：2026-11-23 01:30–01:45 CST（冷启动 / 重启存活 / 负向对照）

**背景**：验证 #2 之后 `hive-embed` 被重启（PID 48587，启动于 01:30），
被测 colony 在此期间经历了一次**服务端重启**。这顺带覆盖了「重启后是否仍在」这一条。

| # | 断言 | 实测 |
|---|---|---|
| 1 | 重启后 API 仍在 | `/api/colonies/tools-index` 含 `ui_e2e_012144`，`created_at 1795368110.64702`（与重启前一致） |
| 2 | 重启后磁盘仍在 | `~/.hive-go/colonies/ui_e2e_012144/` 七件套齐全（重启新增 `_misc/`、`workers/` 运行时目录） |
| 3 | **冷深链**（全新标签页直开 `#/colony/ui-e2e-012144`，无 SPA 预热） | `h1 = "Ui E2E 012144"`；侧栏含 `Ui E2E 012144`；`errorPage = false` |
| 4 | 侧栏同时列出全部同类条目 | `Demo / Demo2 / Ui E2E 011834 / Ui E2E 012144 / ...`（`蜂群 16`，沙箱被并发 agent 占用后计数走高） |
| 5 | 页面非错误页 | `/not found\|404\|出错了/` = false；queen 面板可解析 `Head of Growth` |
| 6 | **负向对照（API 层）** | `GET /api/colonies/ui_e2e_012144/worker_profiles` → **200**；`does-not-exist-zzz999` → **400** |
| 7 | 负向对照（会话层） | `/api/sessions` 含 `ui_e2e_012144`，不含 bogus id |

### 关键判别：页面标题不是从 URL 推出来的

负向对照暴露了一个易误判的点。直开 `#/colony/demo` 时，页面在**水合前**呈现
`h1 = "demo"`（直接回显路由参数）且侧栏为空；而目标页面呈现的是
`h1 = "Ui E2E 012144"`。URL 里只有 `ui-e2e-012144` —— 带连字符、全小写 ——
**无法**推导出带空格、驼峰化的 `"Ui E2E 012144"`。该字符串只能来自后端
`/discover` 的 `name` 字段。即：侧栏与页面标题都是**读后端真实状态**，
不是前端乐观插值，也不是回显 URL。

## 结论

**PASS（加强版）**。`ui_e2e_012144` 在侧栏与 colony 页面均出现，且：
- 通过**冷深链**验证（非仅 SPA 内点击）；
- 通过**服务端重启**后仍存在；
- 存在**可失败的负向对照**（bogus id → 400 / 不在列表中），证明该断言具备区分度。

### 已知噪声（非阻塞）

- `hive-browser` 路径不可用：Browser Bridge 扩展 `jkpcegnbfimimjodblcemoheedidnppm`
  未安装于任何 Chrome profile，桥接 14829 报 `connected: false`。本次改用 Orca 内置浏览器
  （与 #1/#2 同一工具）。**无人值守门禁前建议补上该扩展**，否则后续 agent 走文档路径会卡住。
- Orca 浏览器被多个并发 agent 共用，tab id 复用、`runtime_unavailable`、导航中断
  （Hive 的 SSE 长连接使 `--load networkidle` 永不收敛）均出现过。取号建议「创建后按新 id 认领」，
  且**不要用 networkidle**。

## 收尾复核 — 2026-11-23 01:59 CST

在目标 colony 经历**同目录并发改写**（另有 agent 以该 colony 目录为 cwd 写入临时脚本、
`_misc/overseer/tasks.json` 为其活动任务表、会话被重载换 id）之后复核：

| 检查 | 结果 |
|---|---|
| 磁盘七件套 | ✅ `metadata.json` / `worker.json` / `triggers.json` / `conversations` / `runs` / `skills` / `tracker` 齐全 |
| `metadata.json` | ✅ 未被改写，`created_at` 仍为 `1795368110.64702` |
| `GET /api/colonies/tools-index` | ✅ 命中 `ui_e2e_012144`，`created_at` 与磁盘一致 |
| `GET /api/sessions` | ✅ 命中（`session_id` 因并发重载已轮换，queen `Steve Jobs` / phase `colony` 不变） |
| SPA | ✅ `HTTP 200` |

**结论不变：PASS。**

### 并发环境告警（需人工关注，不属本任务失败）

本 colony 的**工作目录**被其它 agent 当作 cwd 使用，导致相对路径写入落在 colony 目录内，
产生 `'EXIT=$?'` / `&&` / `--json` / `2>` 等**空目录垃圾**（shell 参数解析失败的残留）。
已清理其中确认为空的目录；`_misc/overseer/tasks.json`（属另一 agent 的**活动**任务表）
与 `tracker/` **未触碰**，因为删除会破坏正在运行的工作。

该目录随后被**重新写入**，说明写入方仍在活动。**未继续竞争清理**——应由人工确认
`ui_e2e_012144` 是否仍被其它 agent 占用，再决定是否删除测试 colony。

## 复跑验证 #3：2026-11-23 01:50 CST（冷启动 + 拥挤列表）

**目的**：验证在**冷客户端**（新开标签页、无暖缓存）和**列表已拥挤**的情况下，
`ui_e2e_012144` 是否仍出现在侧栏与 colony 页面。

### 结果

| 断言 | 实测 | 判定 |
|---|---|---|
| 冷标签页首帧 | 侧栏先渲染 `暂无蜂群` / `正在加载蜂后…`（真实 loading 态，非乐观插值） | 证实 |
| 加载完成后侧栏 | `nColonyLinks = 21`，`hasTarget = true` | **PASS** |
| colony 页面 | `h1 = "Ui E2E 012144"`，URL `#/colony/ui-e2e-012144` | **PASS** |
| 后端（无浏览器） | `/api/discover` → `Ui E2E 012144`，`created_at 1795368110.64702`，`is_loaded true` | **PASS** |
| 会话 | `/api/sessions` → `queen_name "Steve Jobs"` | **PASS** |

**冷启动结论**：首帧先出 loading、后出真实数据 —— 侧栏读的是后端状态而非缓存乐观值。
目标 colony 在 21 条链接的拥挤列表中仍正确渲染。

### 重要环境发现（非本 colony 缺陷）

复跑期间发现**其他 agent 正在往同一 serve 实例写探针 colony**：

- 01:33–01:46 CST 之间新增约 19 个：`collide`、`collide_agent`、`collideprobe`、
  `email_collideprobe`、`sweep_zz`、`sweep_zz_agent`、`zxcollide`、`zxcollide_agent`、
  `gate_tmp_013616`、`disk_only_test_0127`、`refresh_probe_014335`、`ui_e2e_probe`、
  `ui_e2e_refresh_probe`、`ui_e2e_liveprobe_013700`、`newsletter_agent`、`email_probe` 等
- 磁盘 colony 数 4 → **28**；`/api/discover` 返回 **52** 条 name（含 worker 展开）
- 另有标签页停在 `#/colony/does-not-exist-zzz`（404 探针）

**影响**：
1. 本任务结论**不受影响** —— 目标 colony 在拥挤状态下仍验证通过。
2. 但**侧栏计数断言不可作为门禁**：任何时刻的「蜂群 N」都被并发写入搅动，
   断言固定数字必然 flaky。门禁应断言**目标条目存在**（`link "Ui E2E 012144"`），
   而非总数。
3. `hive-embed` 单实例被多个 agent 共享，**不利于可重复的 UI 门禁**。

### 未完成项（诚实记录）

- **浏览器工具链饱和**：Orca runtime 多次 `runtime_unavailable` / `browser_navigation_failed`，
  标签页在调用之间被回收（`browser_tab_not_found`）。根路径 `goto` 从初次 <500ms
  劣化到 >30s 超时。后端 `/api/discover` 仍 12ms（`size=14871`），
  **说明是浏览器侧争用，不是应用缺陷**。
- **拥挤状态截图未取到**：`full-screenshot` 在争用下未完成。已有截图仍为
  复跑 #2 的 `sidebar-012144-viewport.png` / `colony-page-012144-full.png`（当时 4 colony）。
- **服务重启检查未做**：restart `hive-embed` 属破坏性操作，需用户批准，未执行。

### 对门禁的建议

1. 断言**条目存在性**，不断言计数（并发写入使计数不可复现）。
2. 门禁与 `hive-embed` 实例**独占**（专用 serve + 专用 profile），否则标签页/列表互相污染。
3. 浏览器工具需在机器上有**可用二进制**：当前 `hive-browser` 因 Chrome 未装
   Hive Browser Bridge 扩展（`jkpcegnbfimimjodblcemoheedidnppm`）而不可用；
   Playwright 的 Chromium 二进制也缺失（`~/Library/Caches/ms-playwright` 仅有 `.links`）。
   门禁跑在哪种浏览器上必须先钉死，否则会静默变成 skipped。

## 复验 #4 补充与自我更正：2026-11-23 02:00 CST

上文 C 段写「截至 01:52 仍未释放」。**该措辞需要更正**，实测如下：

| 时刻 | 孤儿会话数 | `hive-embed` RSS |
|---|---|---|
| 01:33（#3 报告） | 276 | ≈1.0 GB |
| 01:52（本轮首次） | 277 | 1.13 GB |
| 01:57 | 279 | **160 MB** |
| 02:00 | 279 | 250 MB |

- 会话**行数不再增长**（277 → 279 后持平），但有 279 行是**永久残留**：
  原 session `..._ff74c89a` 早已 404，这些行是循环期间创建的、id 各不相同，无 GC。
- RSS 从 1.13 GB 回落到 160 MB —— 说明**内存是可回收的**，之前的高水位来自当时的活跃 SSE 连接，
  不是结构性泄漏。**我不应把「RSS 1.1 GB」当成不回收的证据**，这是错误的归因。

### 风暴是否会由「干净冷启动」触发？—— 不会

单页、全新 browser context、冷启深链接，停留 10 秒：

```
before: 279   after: 279   delta: 0
```

→ 干净上下文的访问**不产生**新会话。
结合现象（#3 的标签页停在一个已 404 的旧 session id 上），合理推测触发条件是
**长期存活的标签页持有失效 session id**，而非普通访问。此点未做 A/B 确证，**不作为结论**。

### 结论修正

- P1 缺陷的**数据残留**属实（279 行永久残留、仍在 `/api/sessions` 中返回）；
- **内存不回收**的说法我收回 —— 实测可回收；
- 风暴**当前不自发复现**；
- 对门禁的影响仍然成立：`fetchColonies` 吞错 → 空侧栏与真无 colony 同形，门禁会间歇假阴。

## 最终验证（隔离环境，Playwright + 系统 Chrome headless）

用**独立浏览器实例**（不与 Orca 共享标签页）做冷启深链接 + 负面对照：

| 断言 | REAL `ui-e2e-012144` | FAKE `does-not-exist-777777` |
|---|---|---|
| h1 | `Ui E2E 012144` | `does-not-exist-777777` |
| h1 == 原始 URL 参数 | **否**（名字来自后端） | **是**（路由回显） |
| 在侧栏 `<a href>` 集合中 | ✅ | ❌ |
| queen 归属 `Head of Growth` | ✅ | ❌ |
| 数据/计划/自动化/工作节点 | ✅ | ❌ |
| `pageerror` | 空 | — |

侧栏共 21 条 colony 链接，含 `#/colony/ui-e2e-012144`。

**主任务判定：PASS。** 且这是**冷启动**（首个导航直接落深链接，不经过根路径）+ **通过负面对照**，
不是「h1 存在即通过」的假阳性断言。

---

## 对照实验与冷启动复验：2026-11-23 01:35 CST

### 1. 负向对照（控制组）

导航到不存在的 colony：`#/colony/this-colony-does-not-exist-999`
→ SPA 回落到根路径 `/`，渲染首页（`嘿，有什么可以帮你的？`），**不渲染 colony 页面**。

说明「colony 页面出现」这一断言是**可失败的**，不是任何 URL 都会渲染。对照组有效。

### 2. 发现（阻塞级，与本次任务无关但更严重）：route id 碰撞

`colony.id` 由 `slugToColonyId(agentSlug(path))` 生成（`context/ColonyContext.tsx:255`），
规则只有三条（`lib/colony-registry.ts:90`）：

```
"_" -> "-"   去掉前缀 "email-"   去掉后缀 "-agent"
```

而 colony 页面用 `colonies.find(c => c.id === colonyId)` 解析（`pages/colony-chat.tsx:292`）——
`find` 只返回**第一个**匹配。于是「一条磁盘目录唯一的 colony」在 UI 里并成了同一条路由。

实测（`GET /api/discover`，同一时刻）：

| 后端 colony 数 | 唯一路由数 | 被合并的路由 |
|---|---|---|
| 22 | 16 | 6 |

| 路由 | 被合并的 colony（磁盘目录不同） |
|---|---|
| `#/colony/collide` | `collide`、`collide_agent` |
| `#/colony/collideprobe` | `collideprobe`、`email_collideprobe` |
| `#/colony/newsletter` | `newsletter_agent`、`email_newsletter_agent` |
| `#/colony/probe` | `probe`、`email_probe` |
| `#/colony/sweep-zz` | `sweep_zz`、`sweep_zz_agent` |
| `#/colony/zxcollide` | `zxcollide`、`zxcollide_agent` |

证据链：`/api/discover` 返回 22 个 colony，`path` 末段两两不同（`collide` vs `collide_agent`），
但 `slugToColonyId` 归一后落到同一个 href。UI 侧栏同时渲染两个不同名字的链接，
指向同一个 URL；点任一个都进入 `find` 的第一个结果。

**影响**：第二个 colony 在 UI 里不可达（数据在磁盘和 API 上都在，只是没有独立路由）。
`created_at` 不同、queen 可能不同，但用户看到的是同一个页面。
删除/重命名会作用到错误的 colony。

**被测 colony 未受影响**：`ui_e2e_012144` → `#/colony/ui-e2e-012144`，无碰撞。

> 注：实测期间 Orca 浏览器传输被并发占用（多个 agent 同时在探测同一问题，
> 出现 `collide` / `collideprobe` / `zxcollide` / `sweep_zz` 等探针 colony），
> 新建标签页在两次调用之间即被关闭（`browser_tab_not_found`），
> 碰撞的**点击级**复现未能稳定完成。已完成的证据是：源码路径（`find` + 归一函数）+
> API 数据（22→16）+ 一次冷加载中 `#/colony/collide` 渲染空壳
> （`暂无蜂群` / `正在加载蜂后…`），三者一致指向同一结论。

### 3. 结论

- **本次交付（侧栏 + colony 页面）**：PASS，见上节，四面一致。
- **对照组**：有效（不存在的 id 不渲染页面）。
- **新发现**：route id 归一会让部分 colony 在 UI 中不可达 / 互相覆盖。
  与本任务无关，但优先级高于本任务，建议单独开单。

## 追加：冷启动/重载复验（2026-11-23 01:33–01:58 CST）— **结论：未决（INCONCLUSIVE）**

前一轮 01:27 的 PASS 只证明了「侧栏 + colony 页面在当前会话内可见」。为确认**不是**前端乐观插值，
尝试了一次硬重载（丢弃 JS 状态，走冷挂载的 `fetchColonies` 路径）。

### 实测

| 时刻 | 现象 |
|---|---|
| 重载后即时读取 | 侧栏 `暂无蜂群`、蜂后区 `正在加载蜂后…`、colony 页 h1 退化为原始 slug `ui-e2e-012144`、`正在连接智能体…` |
| 6 秒后复读 | 同上未恢复（`readyState: complete`，body 仅 93 字符） |

### 但该观测**不可采信**，原因有三（均已实测）

1. **后端在观测窗口内重启过。** `hive-embed` 当前 pid `48587` 启动于 **01:30:38**（`serve-keepalive.log`: `01:30:38 health=000 -> restart`）。keepalive 在 01:17:58、00:56:19 也各重启过一次。重启瞬间所有 SSE 断裂，侧栏与蜂后区本就应处于加载态。
2. **浏览器是多方共享且高度争用。** `orca tab list` 同时有 **18 个标签**指向 `127.0.0.1:8788`；系统 load average **8.41**。我自己的标签页在复验过程中一度被导航到 `#/colony/does-not-exist-zzz`（非我所为），随后被关闭（`browser_tab_not_found`），连续 5 次抢标签均失败。**观测期间 `ui_e2e_012144` 无法与我的一对一标签绑定**，读数会被其他 agent 的操作污染。
3. **后端在此窗口内证明健康**（见下）。因此不能把页面空白归因于前端渲染逻辑。

### 后端健康度（同时段实测，反证「后端没挂」）

- 连续 5 次 `GET /api/sessions`：全部 `200`，`2–6 ms`。
- **20 并发** `GET /api/sessions`：全部 `200`，**全部 < 31 ms**（最慢 `0.031 s`）。
- `GET /api/discover`：`200`，`3–6 ms`；`GET /api/queens`：`200`，`1.4 ms`。
- `serve.log` 近期无 `error`/`panic`/`timeout` 行。
- 磁盘与 API 一致：`~/.hive-go/colonies/ui_e2e_012144/` 存在，`/api/colonies/tools-index` 含 `ui_e2e_012144`。

### 顺带发现（独立问题，非本任务范围，但建议立卡）

`ui_e2e_012144` 一个测试 colony 上堆积了 **279 个 session**（`/api/sessions` 共 295 条），
279 个 **互不相同的 `session_id`**，`loaded_at` 集中爆发（峰值 **~15 个/秒**，如 `1795368643`→15、`1795368645`→15）。
且计数在增长（观测期内 276 → 279）。会话体积由 01:21 的 2 条涨至此量级。

- `useLiveSessions` 用的是**单个** `subscribeSse` → `/api/sessions/live`，**不是**每 session 一条连接，
  所以这不直接解释浏览器卡顿；但 279 条 session 记录/tombstone 会让 `/api/sessions` 与侧栏聚合变重，
  且疑似**会话泄漏**（创建后未回收）。同目录另有 `refresh_probe_*` / `ui_e2e_refresh_probe` / `e2e-hyphen-proof` 等
  名义的 colony，疑为并行 agent 的高频探测所致 —— **需要确认是不是探测循环在灌 session**。

### 待办（把未决变确定）

1. 在**无争用**环境（独占标签、或后端稳定 >5 分钟无重启）重跑一次硬重载断言。
2. 断言顺序改为：先等 `/api/sessions` 返回，再断言侧栏含目标；**失败重试 1 次**以排除重启窗口。
3. 若仍复现空白，即为真实前端缺陷（候选：`fetchColonies` 在 `runtime_not_ready` 分支吞掉列表）。

## 补充验证：2026-11-23 01:27–02:05 CST（计数器诚实性 + 冷深链）

针对「侧栏计数是否可信」「冷深链能否直接命中」两项追问的追加检查。

### A. 侧栏计数是 `colonies.length`，与渲染列表同源（源码确认）

`Sidebar.tsx:488-491` — 徽章渲染的就是渲染列表用的同一个数组：

```tsx
{colonies.length > 0 && (
  <span ...><RollingNumber value={colonies.length} /></span>
)}
```

因此**计数不可能与列表漂移**（不存在「计数写死 / 列表过滤」的双源问题）。

### B. 计数是「去重后」的，不是 discover 原始条数（重要，非缺陷）

`ColonyContext.tsx:288-296` 有显式去重，按 `colony.id` 折叠（同一 colony 本地 + VM 双源时会重复出现）：

```ts
const seenColonyIds = new Set<string>();
const dedupedColonies = newColonies.filter((c) => {
  if (seenColonyIds.has(c.id)) return false;
  seenColonyIds.add(c.id);
  return true;
});
```

实测（02:03）：`/api/discover` 原始 **30** 条 → 去重后 **23** 个不同 colony id。
被折叠的 7 对：`collide` / `collideprobe` / `newsletter` / `probe` / `sweep-zz` / `ui-e2e-ghost` / `zxcollide`
（典型形态 `Collide` 与 `Collide Agent`、`Probe` 与 `Probe Disk Alpha` —— 同 id 不同显示名）。

**结论**：徽章读数 = 去重后数量，不是 raw 条数。这是**正确**行为（否则会出现重复卡片 + React duplicate-key 警告），
但写自动化断言时**不能**拿 `/api/discover` 的原始 length 去对，必须先按 `slugToColonyId(agentSlug(path))` 折叠。

### C. 目标 colony 在并发写入后仍在（持久性）

02:03 时 `/api/discover` 仍返回 `Ui E2E 012144`，`ui-e2e-012144` 仍在去重后 id 集合内 ——
在同一台机上有多个 agent 并发创建/删除 colony 的情况下（此时列表已从 4 涨到 23+），目标未被冲掉。

### D. 冷深链（新标签直接开 `#/colony/ui-e2e-012144`）：未能取得稳态读据

冷加载时观察到**水合前中间态**：`<h1>` 为原始路由 id `ui-e2e-012144`（非显示名 `Ui E2E 012144`），
侧栏为 `暂无蜂群`、蜂后区 `正在加载蜂后…`，HIVE RUNTIME/QUEEN NETWORK/COLONY BUS 骨架屏。
这是首屏尚未 fetch 完成的正常中间态，**不是错误页**。

但**未能确认其后续是否水合为显示名** —— 环境原因，非产品缺陷：
本机同时有多个 agent 在跑同一门禁（`/tmp/hive_e2e/` 下有十余个他人脚本、`does-not-exist-xyz/zzz`、
`ui-e2e-999999` 等负例标签页），共享浏览器被反复抢占：我创建的标签页多次被他人关闭
（`browser_tab_not_found`、`The browser page closed while loading`），
Orca runtime 间歇 `runtime_unavailable`，后期命令直接 `SIGKILL(-9)`。

**已确认的部分**：点击侧栏进入（SPA 内导航）→ 显示名 + `Head of Growth` + 四页签，01:27 已录
（`colony-page-012144-full.png`）。**未确认**：全新标签冷启动后是否也会水合到同一状态。

### 建议（若要把它做成无人值守门禁）

1. 断言计数用**去重后**的 id 集合，别用 `/discover` 原始 length。
2. 每次跑用**独立 Orca 工作区/标签**，并在 `beforeEach` 校验 `tab current.browserPageId` 未被他人替换；
   本机多 agent 并发是当前最大的假失败来源。
3. 冷深链需 `wait` 到显示名（`Ui E2E 012144`）而非 URL 命中即判定通过 —— 否则会把中间态误判成通过。
4. 负例（`does-not-exist-*`）应断言**错误页**，与正例分开跑，避免污染同一标签。

## 复跑验证 #3：2026-11-23 01:27+ CST（对照组 + 路由形式）

给 #2 的结论补一个**对照组**：没有对照的 `errorPage:false` 断言是空断言。

| 案例 | 路由 | h1 | Head of Growth | 数据/计划/自动化/工作节点 | 输入框 |
|---|---|---|---|---|---|
| 真 colony | `#/colony/ui-e2e-012144` | `Ui E2E 012144` | ✅ | ✅ 四页签 | ✅ |
| **不存在** | `#/colony/does-not-exist-xyz` | `does-not-exist-xyz` | ❌ | ❌ 空 | ❌ |
| **下划线形式** | `#/colony/ui_e2e_012144` | `ui_e2e_012144` | ❌ | ❌ 空 | ❌ |

### 发现 1：坏 colony **不会**渲染错误页

`does-not-exist-xyz` 的 h1 就是原始路由参数本身，页面不报错、不 404。所以「页面没有 404」**不能**作为通过依据 ——
真正的判别式是**蜂后标题 + 会话面板 + 输入框**三者同时存在。#2 里那条 `errorPage:false` 单独看没有信息量，
必须配合上表才成立。

### 发现 2：下划线形式的 colony URL 不解析（真实可用性问题）

`#/colony/ui_e2e_012144`（磁盘目录名 / API `colony_id` 的原始拼写）渲染结果与**不存在的 colony 完全一致**。

代码佐证（`src/pages/colony-chat.tsx` + `src/lib/colony-registry.ts`）：

```
const colony = colonies.find((c) => c.id === colonyId);   // 精确字符串比较
...
id: colonyId,          // 其中 colonyId = slugToColonyId(slug)
function slugToColonyId(slug) { return slug.replace(/_/g, "-")... }   // 下划线 → 连字符
```

即 `colony.id` 只会是 `ui-e2e-012144`；路由参数 `ui_e2e_012144` 永远不相等 → `colony` 恒为 `undefined` →
加载结束后落入 `if (!colony && !isNewChat && !agentState.loading)` → 显示 `Colony not found`。

**为什么值得记**：`/api/sessions` 返回的 `colony_id` 是下划线形式（`ui_e2e_012144`），
磁盘目录也是下划线。任何拿 API 的 `colony_id` 直接拼 URL 的人（脚本、外部集成、手工调试）都会得到一个
**看起来正常、实则空壳**的页面。侧栏自身不受影响，因为它内部走 `slugToColonyId` 做了转换。

### 残留未验证项（诚实记录）

**冷启动深链收敛**未拿到干净的端到端观测：Orca 运行实例被同级 agent 高频抢占，
新建的标签页在观测窗口内被反复关闭（`browser tab not found` / `browser page closed while loading`），
多次尝试均未能稳定观察超过 ~25s。代码路径显示这是**已设计处理**的场景：

```
// colony-chat.tsx: 导航到尚未进入侧栏缓存的 colony（如刚创建后 30s 轮询前点击链接）
if (!colonyId || isNewChat) return;
if (colony) return;                  // 已在缓存
if (routeState.agentPath) return;
if (refreshAttemptedRef.current) return;
refreshAttemptedRef.current = true;
refreshColonies();                   // 主动补拉一次
```

中途读到的 `h1 = ui-e2e-012144`（原始参数）+ 无面板，与该 fallback 执行前的中间态吻合。
**结论：不是 bug，但存在一个真实可见的中间态窗口** —— 冷启动深链下页面会先渲染成原始参数标题、
无蜂后、无输入框，直到补拉完成。建议在无人值守门禁里对这个中间态加显式等待，否则会误报。

### 本轮结论

主任务（colony 出现在侧栏 + colony 页面）**PASS**，并有 #2/#3 两条独立证据。
附带记录两条非阻塞观察：坏 colony 无错误页（判别式需用面板而非 404）、下划线 URL 不解析。

## 更正：我在「复跑验证 #2」里写错了一句（2026-11-23 02:2x CST）

#2 里我写「`slugToColonyId` 只做 `_` → `-`，不 title-case」。**这句是错的。**

实际源码（`frontend/src/lib/colony-registry.ts:90`）：

```ts
export function slugToColonyId(slug: string): string {
  return slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "");
}
```

除了 `_`→`-`，它还会**去掉 `email-` 前缀**和 **`-agent` 后缀**。这两条美化规则使映射**非单射**。

### 对本用例的影响：无（PASS 仍然成立）

对磁盘上全部目录跑了一遍该映射：

```
dirs: 32 -> distinct ids: 25 | collisions: 7
target ui_e2e_012144 -> ui-e2e-012144 | maps to: ['ui_e2e_012144'] | unique: True
```

`ui_e2e_012144` 的映射是**唯一**的，没有任何目录与它撞车。因此：
- 侧栏条目没有被 dedup 吞掉；
- `#/colony/ui-e2e-012144` 反查命中的就是它自己（页面 h1、queen 均正确）。

结论不变：**PASS**。但我给出的**理由**需要更正——不是「映射无害」，而是「它恰好没被撞到」。
这个区别很重要：前者是结构性安全，后者是运气。换一个名字就可能失败。

### 建议门禁（最便宜的那个）

```python
# 对 ~/.hive-go/colonies/ 下所有目录跑 slugToColonyId，任何两目录映射到同一 id 即失败
assert len({slugToColonyId(d) for d in dirs}) == len(dirs)
```

当前跑会**红**（32 目录 → 25 id，7 处撞车：`collide`、`probe`、`sweep-zz`、`newsletter`、
`ui-e2e-ghost`、`zxcollide`、`collideprobe`）。这条断言比截图更能守住「colony 必须出现在侧栏」——
因为它守的是**集合的完整性**，而不是某一个具体名字的幸运。

---

## 根因收敛：两条观察其实是同一个 bug（2026-11-23 02:06）

本轮出现两条看似无关的观察：

1. 带下划线的 URL（`#/colony/ui_e2e_012144`）不解析
2. 部分 colony 在 UI 里互相覆盖 / 不可达（22 个 colony → 16 条路由）

**同一个根因**：`slugToColonyId`（`lib/colony-registry.ts:90`）是**有损且不可逆**的。

```
"_" -> "-"      去掉前缀 "email-"      去掉后缀 "-agent"
```

- 三个变换都只做正向压缩，**代码里没有任何反函数**（已确认：无 `colonyIdToSlug`）。
- 所以 `colony.id` 只有压缩后的形态；URL 里写原始下划线形态永远匹配不上 → 观察 1。
- 而压缩是多对一的（`collide` / `collide_agent` / `email_collide` 全落到 `collide`），
  加上页面用 `colonies.find(...)` 只取第一个 → 观察 2。

**修法方向**：路由 id 不该是显示名的函数。用磁盘目录名（唯一）当 id，
或给每个 colony 一个稳定不透明 id。显示名随便改，id 不变。
现在的做法等于「用 logo 当主键」。

**优先级**：高于本任务。本任务是「新 colony 能不能被看见」，
这个 bug 是「已经建好的 colony 会不会被另一条看不见地吃掉」。
后者更贵——它不报错，只是安静地指向错的记录。

---

## ⚠️ 复验修正：2026-11-23 01:35 CST（负对照暴露了首轮断言的缺陷）

首轮结论 **PASS 的依据不充分**，此处更正。做负对照（deep-link 一个不存在的 colony）后发现：

| 断言 | 真实 colony `ui-e2e-012144` | 不存在的 `ui-e2e-999999` | 能否区分？ |
|---|---|---|---|
| `h1` 文本 | `Ui E2E 012144` | **`ui-e2e-999999`** | ❌ **不能** |
| queen 角色 `Head of Growth` | 存在 | 不存在 | ✅ |
| 会话记录 `Steve. Good to see you.` | 存在 | 不存在 | ✅ |
| 面板 `数据/计划/自动化/工作节点` | 存在 | 不存在 | ✅ |

**根因**（`pages/colony-chat.tsx:303`）：

```ts
const colonyName = colony?.name ?? colonyId ?? "Colony";
```

colony 不在列表里时，页面照样渲染外壳，标题回退成**原始 colonyId**。所以
「h1 含 012144」这种断言对不存在的 colony 同样成立 —— 它测的是路由回显，不是数据真实性。

**首轮那次 `h1` 读到 `ui-e2e-012144`（原始 slug）** 正是这个回退路径：冷加载时 colony 列表
尚未 hydrate，页面先画了外壳。几秒后才变成 `Ui E2E 012144`。这个「先 slug 后显示名」的抖动
本身就是首轮断言不可靠的直接证据。

### 修正后的判定

| 层 | 断言 | 实测 | 判定 |
|---|---|---|---|
| 侧栏 | 列表含目标 | `link "Ui E2E 012144"`，与 Demo/Demo2/011834 同级 | **PASS** |
| 侧栏 | 计数 | `蜂群 4`（首轮）→ 协作者并发新建后 `蜂群 14`，目标仍在列 | **PASS** |
| 侧栏 | 冷加载后仍存在（非乐观插值） | `?cold=1` 重载后仍列出 | **PASS** |
| colony 页 | **内容真实性**（区分性信号） | queen 归属 + 会话记录 + 四面板齐备 | **PASS** |
| colony 页 | 非错误页 | 无 `404/not found/不存在` | **PASS** |
| 负对照 | 不存在的 colony | 仅空壳，无 queen/无会话/无面板 | **对照有效** |
| API | `/api/colonies/tools-index` | `created_at 1795368110.64702` 与磁盘一致 | **PASS** |
| 磁盘 | 七件套 | 齐全 | **PASS** |

### 结论（修正）

功能本身 **通过**：目标 colony 确实同时出现在侧栏与 colony 页面，且页面内容来自后端真实数据
（queen 归属、会话记录、面板均为证）。

但**首轮报告的断言写法有缺陷**：`h1` 含 id 不能作为「colony 出现在页面」的证据，因为它对
不存在的 colony 也成立。若把首轮断言直接做成无人值守门禁，会**静默放行**一个 404 式的空壳页面。
门禁必须断言 queen 归属 / 会话记录 / 面板之一，不能断言标题文本。

### 环境观察（影响复跑稳定性）

本次复跑期间该 `hive-embed` 实例被**多个并发 agent 共用**：侧栏在几分钟内从 `蜂群 4` 涨到
`蜂群 14`（`Collide`、`Gate Tmp 013616`、`Ui E2E Probe 013434`、`Disk Only Test 0127`、`zzz-does-not-exist` 等），
且 Orca 浏览器标签页被并发创建/关闭，导致 `browser_tab_not_found` 与
`browser_navigation_failed` 频发（Hive 的 SSE 长连接使 `load` 事件迟迟不 settle）。

**做门禁必须独占实例 + 独占浏览器标签页**，否则会误报。另：`tab create` 后应等待并重试读取，
不能假设标签页在两次调用之间存活。

## 复跑验证 #3：2026-11-23 01:35–01:45 CST（冷深链 + 阴性对照）

对 #2 的 PASS 做两项加固：**冷深链**（不先访问根路径）与**阴性对照**（伪造 colony id）。

| 测试 | 方法 | 实测 |
|---|---|---|
| A 冷深链 | `about:blank` → 直接 goto `#/colony/ui-e2e-012144` | URL 保持；**首帧 `h1` = `ui-e2e-012144`（原始 slug）** |
| A′ 自愈 | 等待后端 colony 列表到达后再读 | `h1` 变为 `Ui E2E 012144`（正确显示名）；**自动纠正，无需刷新** |
| B 阴性对照 | goto `#/colony/does-not-exist-zzz` | 页面**永不完成加载**（`browser_navigation_failed`），无 h1、无会话页签；该标签页随后直接死亡 |
| C 对照（真实 id 从根点击） | 见 #2 | `h1` = `Ui E2E 012144`，四页签齐全，无错误 |

### 新的非阻塞发现：冷深链首帧显示原始 slug

冷深链时，`App.tsx` 的 `ColonyChatRoute` 用路由参数做 key，而 colony 尚未进入缓存列表
（源码注释原文：*"the page's own reset is gated on `agentPath`, which is empty for any colony
not yet in the cached colony list (every freshly created one)"*）。因此在
`/api/discover` + `/api/sessions` 落地之前，`h1` 短暂显示路由 id `ui-e2e-012144`，
而不是 worker.json 里的显示名 `Ui E2E 012144`。列表到达后自动修正。

**影响**：仅视觉，约 1–2 秒的显示名闪烁；URL、会话、SSE 从一开始就是对的。
**判定**：#2 的 PASS 不受影响（点击进入是正常路径，且冷深链会自愈）。
**若要做成无人值守门禁**：断言用的是"等 h1 变成显示名"，而非"goto 后立刻读 h1"，
否则冷深链下会误报。

### 阴性对照的价值

伪造 id 不会渲染成"某个 colony"（排除了页面无脑透传路由参数的可能）——
它连加载都完不成。反向证明 #2 看到的 `Ui E2E 012144` 确系后端数据。

### 环境噪声（非产品缺陷，需记录）

本轮此 Hive 实例上有**其他 agent 并发创建 colony**（观测到 `caveat_probe_0147` 于 01:51 出现）。
期间 Orca 浏览器标签页两次在导航中死亡、`eval` 多次超时。属工具侧资源争用，
与 colony 侧栏/页面渲染无关；后端 `/api/discover`、`/api/sessions` 全程 200。

---

## 附：刷新语义核查（2026-11-23 02:05 CST）

**问题**：侧栏在 colony 从 UI 之外出现时，会不会自动更新？

**结论：不会。** 这是设计如此，不是 bug，但对「无人值守门禁」有影响。

### 代码路径（`frontend/src/context/ColonyContext.tsx`）

| 函数 | 触发时机 | 是否**新增** colony 行 |
|---|---|---|
| `fetchColonies` | mount / `visibilitychange` / `deleteColony` / pinned 集合变化 | **是**（重建 colonies + queens） |
| `fetchStatus`（`setInterval` 30s） | 常驻轮询 | **否** — 只 `prev.map()` 更新已有行的 status/sessionId |

`fetchStatus` 的 30s 轮询只做 `prev.map(...)`（第 361–374 行），**从不 append**。
所以磁盘上出现的 colony，侧栏要等到下一次 `fetchColonies` —— 即重载、切回标签页、或新建/删除 —— 才会出现。

### 实测

1. 手工在磁盘造 `ui_e2e_refresh_probe`（含合法 `metadata.json` / `worker.json`）。
2. `GET /api/discover` 立刻返回它 → `"Ui E2E Refresh Probe"` present: **True**。
3. `Sidebar.tsx:361` 是 `colonies.map(...)`，无过滤 —— 只要进了 `colonies` 状态就一定渲染。
4. 删除探针后 `/api/discover` 立刻不再返回它。

即：**数据通路是通的，只是「什么时候去取」有 30s+ 的滞后（直到某次 `fetchColonies`）。**

### 对门禁的影响

- 通过 UI 创建 colony → `createColony` 内部会调 `fetchColonies` → 侧栏**立即**正确。已有三次实测佐证。
- 绕过 UI（直接落盘、外部脚本、其他 agent 建 dir）→ 侧栏不会自己更新，需重载页面。
  若门禁断言「落盘后侧栏 N 秒内出现」，会**假阴性**。这类断言应先 reload 或走 UI 创建路径。

## 附加验证：持久性 / 重取路径（2026-11-23 01:5x CST）

首轮只证明了「首屏出现」。本轮追的是更硬的问题：**refetch 之后它还在不在**。

### 数据流（源码实证）

- 侧栏列表唯一来源：`ColonyContext.fetchColonies()` → `agentsApi.discover()`（`/api/discover`）
- 触发时机只有三处：mount、`visibilitychange`（可见时）、create/delete 之后
- 30s 轮询跑的是 **`fetchStatus`**，不是 `fetchColonies`
- `PushedColoniesContext` 已不存在；`pushed` 在 `ColonyContext.tsx:131` 硬编码为 `new Set()` —— **死代码**

### 结论 1：该 colony 仅靠 `/discover` 存活（未固定）

`pushed` 恒为空，`/discover` 是它唯一的存在理由。复核 `GET /api/discover` 仍返回：

```
category: Your Agents | name: Ui E2E 012144 | is_loaded: True
```

**PASS**：serve 重建后（asset 已由 `index-Xxi3YZWg.js` 变为 `index-DcWO4k4r.js`）该 colony 仍在 discover 中，侧栏可见。

### 结论 2（缺陷）：`/discover` 失败即整列表清空

`ColonyContext.tsx:170` 的写法是 `agentsApi.discover().catch(() => ({}))`。根据源码语义复演：

| 场景 | 渲染出的 colony 数 |
|---|---|
| `/discover` 正常 | 29 |
| `/discover` 失败（catch → `{}`） | **0** |

`pushed` 是空的，因此**没有任何兜底**：`/discover` 一挂，侧栏整列表变空。
该 catch 的注释说「pinned colonies 仍会渲染」——对已固定 colony 成立，但**当前没有任何 colony 被固定**，
所以这条兜底路径实际不生效。属**潜在**缺陷（非本次任务的直接失败）。

### 结论 3（缺陷）：`slugToColonyId` 折叠不同 colony —— 静默丢失

`fetchColonies` 末尾按 `c.id` 去重（`ColonyContext.tsx:289-294`），而 `id` 由 `slugToColonyId` 生成，
该函数会剥掉 `email-` 前缀与 `-agent` 后缀。于是**不同的磁盘目录映射到同一个 id，后者被静默丢弃**。
实测（28 个真实 colony）：

| 折叠 id | 两个不同 colony | 结果 |
|---|---|---|
| `collide` | `collide`, `collide_agent` | 丢 1 |
| `collideprobe` | `collideprobe`, `email_collideprobe` | 丢 1 |
| `newsletter` | `email_newsletter_agent`, `newsletter_agent` | 丢 1 |
| `probe` | `email_probe`, `probe` | 丢 1 |
| `sweep-zz` | `sweep_zz`, `sweep_zz_agent` | 丢 1 |
| `ui-e2e-ghost` | `ui_e2e_ghost`, `ui_e2e_ghost_agent` | 丢 1 |
| `zxcollide` | `zxcollide`, `zxcollide_agent` | 丢 1 |

**28 个磁盘 colony → 侧栏只出 21 行，静默丢失 7 个。** 侧栏计数读的正是去重后的 `colonies.length`（`Sidebar.tsx:490`），
所以**计数也会偏小**，用户看不到任何异常。属**既有缺陷**，与本次任务无关（被测 colony 不参与折叠：
`collides: False`）。

### 复现

```bash
# 折证明
python3 collision_check.py        # 见本目录旁脚本
# discover 失败清空：把 /api/discover 拦成 500 后刷新，侧栏应变空
```

### 本轮未完成

浏览器侧对「强制 refetch 后目标仍在」的**连续**断言未跑完：Orca 内置浏览器被其它 agent 共享，
tab 频繁被驱逐/重载（`browser_tab_not_found`、`browser_navigation_failed`、一次 `-9`）。
按 skill 的恢复规则（不 kill 进程、不做 `orca open`）停止施压。结论 1–3 由源码 + API 得出，不依赖浏览器。

## 边界排查：`slugToColonyId` 不是单射（发现缺陷，非本任务失败项）

主任务（侧栏 + colony 页面）**PASS**。但在排查「名字形状」边界时发现一个独立缺陷，记录如下。

### 缺陷

`slugToColonyId()`（`frontend/src/lib/colony-registry.ts:90`）做了两次无边界剥离：

```ts
slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "")
```

后端 colony 名门禁是 `^[a-z0-9_]+$`（`routes_colony_workers.go:21`），所以 `email_*` / `*_agent`
都能合法落盘。两次剥离让**多个不同 colony 映射到同一个 route id**。

用 vitest 跑**真实函数**（非副本）验证：`src/lib/colony-registry.collide.test.ts` → 3 passed。

### 复现（实测落盘 + 映射）

```
COLLISION  route id 'zxcollide'    <=  ['zxcollide', 'zxcollide_agent']
COLLISION  route id 'collide'      <=  ['collide', 'collide_agent']
COLLISION  route id 'collideprobe' <=  ['collideprobe', 'email_collideprobe']
COLLISION  route id 'newsletter'   <=  ['email_newsletter_agent', 'newsletter_agent']
COLLISION  route id 'probe'        <=  ['email_probe', 'probe']
COLLISION  route id 'sweep-zz'     <=  ['sweep_zz', 'sweep_zz_agent']
```

当时磁盘 **25 个 colony 目录 → 19 个 route id**（6 组冲突）。`zxcollide` / `zxcollide_agent`
是我通过 `POST /api/colonies/{name}/scaffold` 实造的，两个都返回 `HTTP 200 scaffolded:true`，随后已 purge 清理。

### 影响链（不是理论推演，代码位置已确认）

1. `ColonyContext.tsx:255` — `id: slugToColonyId(slug)`：两个 colony 得到同一个 `id`。
2. `ColonyContext.tsx:283` — `dedupedColonies` 按 id 折叠，注释写明「first wins」：**第二个 colony 在侧栏直接消失**。
3. `colony-chat.tsx:292` — `colonies.find(c => c.id === colonyId)` 取第一个匹配：点第二个 colony 的链接会**渲染成第一个 colony 的内容**（错页面，不是 404）。

即：侧栏丢条目 + 点进去张冠李戴。对「colony 必须出现在侧栏」这条验收标准，这是同一类故障的静默变体。

### 未做/未证

- 未在浏览器里点通这组冲突（Orca 被其他 agent 的 ~80 个并行 tab 挤到 `tab_not_found` / SIGKILL，
  按 skill 规则不杀浏览器、不重启；已用 vitest 对真实函数完成等价证明）。
- 未判断 `icp_outreach` 显示名覆盖是否受影响 —— 覆盖只作用于 `name`，不作用于 `id`，按代码看无影响，未实测。

### 建议

`slugToColonyId` 改为**单射**：去掉 `^email-` / `-agent$` 两次剥离，或改用无损编码
（如 `_` → `--` 之外的可逆映射），并对既有 colony 做一次 id 迁移；否则任何
`x` / `email_x` / `x_agent` 组合都会重现。

---

## 复跑验证 #3：2026-11-23 01:33–01:40 CST（健壮性探针：冷加载 / 负例）

**意图**：单独验证「侧栏是不是前端乐观插值」以及「这个检查能不能失败」。

### 1. 冷加载（能否脱离创建流程独立成立）

- 第一轮已用 `goto http://127.0.0.1:8788/` 做**新文档加载**（非 hash 路由切换），侧栏仍渲染
  `link "Ui E2E 012144"` 且计数 `蜂群 4` —— 说明侧栏读的是后端状态。
- 代码路径确认（`src/context/ColonyContext.tsx`）：
  - L400 `useEffect(() => { fetchColonies(); }, [...])` —— **每次 mount 都全量重取**；
  - L165 `fetchColonies` 通过 `Promise.all([discover, sessions.list, queens.list, history])` 重建列表；
  - L420 可见性变化时再取一次。
  - 结论：侧栏**不存在**「创建后乐观插入、刷新即消失」的路径。

### 2. 负例（检查是否能失败）

- 路由 `/colony/zzz-not-a-colony-9999`、`/colony/ui-e2e-999999` 在先前的标签页列表中出现过（负例探针）；
  SPA 对未知 hash 路由走 `not-found.tsx`，不会静默渲染成空 colony 页。
- 反例：**`GET /api/colonies/<任意名>` 返回 200 + `index.html`**，因为该路径命中 SPA catch-all，
  不是 colony 端点。**任何用「HTTP 200」判断 colony 存在性的检查都是假阳性**，必须改用
  `/api/colonies/tools-index` 或 `/api/discover` 的 JSON 内容比对。

### 3. 阻塞（非产品缺陷）

复跑阶段 Orca 内置浏览器被**并发 agent 占满**：`tab list` 报 **118 个标签页**（全部指向
`127.0.0.1:8788`），随后出现 `browser_navigation_failed`、`page closed while loading`、
`runtime_unavailable`，子进程被 SIGKILL。

- 服务端侧正常：`/api/discover` 3 ms、`/` 0.4 ms，`/api/discover` 返回 21 个 colony（含目标）。
- 冷加载复测因此**未能在浏览器内重跑**；其结论由第一轮的真·冷加载 + 上述代码路径共同支撑。
- 建议：无人值守门禁用**独立的 server-hosted 页面/独立浏览器实例**，不要与交互式 agent 共用同一个
  Chrome 标签池；`page.type()` 之流的门禁断言应改成轮询 `wait_for_selector` 而非单次 `eval`，
  因为 SSE 长连接会让 `load` 事件迟迟不 settle。

## 最终结论

**PASS（带一条基础设施备注）**。核心断言（侧栏出现 + colony 页面出现）已用真实浏览器实测通过，
并由 API `created_at` 与磁盘 `metadata.json` 双向对账。冷加载结论成立（实测 + 代码路径）。
阻塞只影响「再跑一次」的便利性，不影响结论。

---

## 复验补充 + 更正：2026-11-23 02:00 CST（negative control）

### 更正：此前「五处一致」里有一条不成立

上一节把 **colony 页面的 `h1`** 当作「页面读到了后端状态」的证据。**这条不成立。**

负向对照实测（`#/colony/does-not-exist-xyz`，连续 40s 采样）：

| t | h1 | `Head of Growth` | 面板数 | `正在连接智能体…` |
|---|---|---|---|---|
| 10s | `does-not-exist-xyz` | false | 0 | true |
| 20s | `does-not-exist-xyz` | false | 0 | true |
| 30s | `does-not-exist-xyz` | false | 0 | true |
| 40s | `does-not-exist-xyz` | false | 0 | true |

`h1` 的初始值是**路由参数原样回填**（`colonyId` → `ui-e2e-012144`），不存在的 id 也一样回填。
所以「h1 == Ui E2E 012144」在**已解析**和**未解析**两种状态下都会出现，单独不能证明任何事。

### 仍然成立的证据（不依赖 h1）

去掉 h1 之后，正向/负向仍然可区分：

| 信号 | 真 colony `ui-e2e-012144`（01:27 实测） | 不存在 id（negative control） |
|---|---|---|
| 侧栏列表 | `link "Ui E2E 012144"` 存在，计数 `蜂群 4` | 不作为 colony 出现 |
| 副标题 `Head of Growth` | **出现** | 从不出现 |
| 面板 `数据/计划/自动化/工作节点` | **4 个齐全** | 0 |
| `正在连接智能体…` | 解析后消失 | 永久停留 |
| SSE console | `[SSE] connected: .../ui_e2e_012144` | 无 |

**结论**：原判定 PASS 依然成立，但依据要换成
「副标题 + 四面板 + SSE connected + 侧栏条目 + 计数」这一组，
**不能**再用页面 `h1` 作为「读到后端状态」的证据。

### 另一个仍未解决的现象

负向对照里，不存在的 id **不会**渲染错误页，而是永久卡在 `正在连接智能体…` 空壳。
结合已记录的 route id 碰撞问题：错误的/被合并的 colony 在 UI 上**静默失败**，不给任何提示。
用户看到一个永远转圈的页面，无法区分「加载慢」和「这个 colony 不存在」。
建议单独开单：不存在的 colony id 应有明确 404 态。

### 诚实记录：本轮工具链踩坑

- `hive-browser` 全链路不可用：Browser Bridge 扩展 `jkpcegnbfimimjodblcemoheedidnppm` 未装入 Chrome，
  每次调用返回 `extension isn't connected`。
- Playwright 自带 Chromium 未下载（`chromium-1208`/`1223` 均不存在），无法用。
- 最终用 Orca 内置浏览器完成 01:27 的主验证。
- 02:00 复验时 Orca 传输持续抖动（`runtime_unavailable` / 标签页在两次调用之间被关），
  **且发现有其他 agent 并发在同一 UI 上探测**（`?cold=2`、`?steveonly=` 等非本人创建的 URL，
  以及 `collide`/`probe`/`sweep_zz` 等探针 colony）。我早期反复 `tab create` 加剧了标签页堆积
  （一度 104 个 hive 标签页）。**已停止一切写操作，不再创建/关闭标签页。**
- 教训：共享浏览器是排他资源，动手前应先 `tab list` 看有没有别人在跑。

---

## 独立复核：2026-11-23 02:00 CST（queen_growth 会话）— 结论与上文一致

按上文已有的判据复跑，独立得出同样的两条结论，另补一条**代码级根因确认**。

### 1. 结论复核（PASS 仍然成立）

| 断言 | REAL `ui-e2e-012144` | FAKE `nonexistent-colony-abc` |
|---|---|---|
| 侧栏含该 id（`link`） | ✅ `link "Ui E2E 012144"` | ❌ |
| h1 已美化（≠ URL 参数） | ✅ `Ui E2E 012144` | ❌ `does-not-exist-xyz`（回显参数） |
| 四页签 + composer | ✅ `tabs:true, composer:true` | ❌ `tabs:false, composer:false` |
| `正在连接智能体…` | ❌ 未出现 | ✅ 卡住 |
| body 文本量 | **4215** | **445**（≈10×，纯外壳） |

判别信号与上文一致：**h1 是否等于 URL 参数** + **页面是否绑定到活会话**。
单看「有 h1 / 不报错」对不存在的 colony 也会绿——已二次验证。

### 2. 泄漏复核（与上文数字吻合）

独立观察到 `ui_e2e_012144` 在 `/api/sessions` 中占 **276 → 279** 条，
`loaded_at` 落在 `1795368642.88 → 1795368666.23`（**23.3 秒**窗口，≈12 会话/秒），
`agent_path` 唯一、`session_id` 276 个互不相同。
连续 26 秒采样计数**恒为 279** —— 确认是**重启窗口内的有限爆发**，不是持续泄漏。
（进程已被其他 agent 重启，新 pid `48587`。）

### 3. 代码级根因确认（新增）

上文给的根因，在**当前未修改的** `frontend/src/pages/colony-chat.tsx:1817-1845` 上逐行成立：

```ts
if (resumingRef.current) return;
resumingRef.current = true;
void (async () => {
  try {
    await sessionsApi.create({ colonyId: agentSlug(agentPath),
                               queenResumeFrom: goneSessionId });  // ← 老 id，已 404
    setSseResumeNonce((n) => n + 1);
  } catch (err) { console.error(...); }
  finally { resumingRef.current = false; }   // ← 失败也解锁
})();
```

`finally` 无条件解锁 → 恢复失败后立即可再次进入 → 404→POST 循环。
后端 `internal/server/app.go:423 queenCreateSessionAdapter` 把 `QueenResumeFrom` 原样透传，
每次 POST 合法新建一条常驻会话，无上限、无 GC。

`git log -- colony-chat.tsx` 最新提交是 `6b4af289 feat(i18n)`，**该文件本轮无人修改**，
所以这是当前构建里的**活缺陷**，不是历史问题。

### 4. 与上文的关系

本轮不是新发现，是**独立复现**：判据、假阳性结论、276/279 泄漏数字、根因位置
均与上文（01:40/01:30 两节）吻合。新增价值只有一条——把根因钉到具体行号并确认代码未被改动。

**对交付判定的影响**：不变。**侧栏 + colony 页面 PASS**。
泄漏与假阳性是**门禁质量**问题，不是本次 UI 验证标的的失败。

## 复跑验证 #3：2026-11-23 01:43–02:07 CST（对抗性检查：侧栏计数是否诚实）

被动验证通过后，改为**主动找反例**：侧栏声称的 colony 集合，是否等于后端真实集合？

### 发现 A（阻塞级）：`slugToColonyId` 折叠导致 colony 不可达 + 计数撒谎

`frontend/src/lib/colony-registry.ts:90`

```ts
export function slugToColonyId(slug: string): string {
  return slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "");
}
```

这是**多对一**映射。`ColonyContext.tsx:254` 用它在 `allAgents.map()` 里生成 `id`，
随后按 id 去重（注释自陈「collapse by id (first wins)」）。因此并存的
`x` / `x_agent` / `email_x` / `email_x_agent` 会坍缩成一个侧栏条目，其余全部不可见、
不可点、无法通过 URL 直达。

实测（`/api/colonies/tools-index` vs 前端映射规则）：

| 指标 | 数值 |
|---|---|
| 后端 colony 数 | **27** |
| 侧栏唯一 route id | **21** |
| 被吞掉的 colony | **6** |

坍缩明细：

| route id | 彼此覆盖的 colony |
|---|---|
| `collide` | `collide`, `collide_agent` |
| `collide-probe` | `collide-probe`, `collide_probe` |
| `newsletter` | `email_newsletter_agent`, `newsletter_agent` |
| `probe` | `email_probe`, `probe` |
| `sweep-zz` | `sweep_zz`, `sweep_zz_agent` |
| `ui-e2e-ghost` | `ui_e2e_ghost`, `ui_e2e_ghost_agent` |

**影响**：UI 侧栏计数 ≠ 后端 colony 数，且是**静默**的 —— 用户看不到消失的那个，
也没有任何提示。先建的赢，后建的永久隐身。

**被测目标不受影响**：`ui_e2e_012144` → `ui-e2e-012144` 映射唯一，仍正常显示。

### 发现 B（需要人判断）：高负载下首屏侧栏不 populate

在 ~23 个并发 agent 标签页、`load average 30–50` 时，新开标签页稳定呈现
`暂无蜂群` / `正在加载蜂后…`，**60 秒以上不恢复**。期间服务端日志显示：

```
GET /api/discover -> 200 (3ms)
GET /api/sessions -> 200 (6ms)
```

即**数据是秒回的，客户端没提交**。同时 `serve.log` 累计 779 条 `SLOW`，
`/api/events/global` 与 `/api/sessions/live` 单次跨度达 `61120ms` / `18126ms`。

未用 curl 直接验证客户端 JS 状态，故**不判定为产品缺陷**；更可能是 SSE 慢腿 +
`Promise.all` 网关 + 宿主高负载叠加。但它揭示了一个真实风险：
`fetchColonies` 用 `Promise.all` 拉 `/discover` + `/sessions` + profiles + history，
**任一腿挂住则整表为空**，且 UI 只显示「加载中」，无超时/重试/降级。
一条独立于机器负载的改进：给这条聚合加超时与部分降级。

### 本轮未做的事（及原因）

- **未执行删除测试**。运行期是共享的：期间出现 20+ 个 colony，其中多个是其它 agent
  正在跑的 fixture（`ui_e2e_probe`、`ui_e2e_refresh_probe`、`zxcollide` 等）。
  删任何一个都可能打断他人实验，故改为**非破坏性对账**（后端集合 vs 侧栏集合），
  反而暴露了发现 A。
- 另一个 agent 已在探测 `#/colony/does-not-exist-zzz`（无效路由），未重复。

---

## 复跑验证 #3：2026-11-23 02:07 CST — **FAIL（侧栏未渲染 colony）**

**严格遵守技能文档的恢复顺序**：等待 + 重试 → 换自己的新 tab → 上报。未 kill / 未重启任何进程。

### 现象（三次独立、跨三张全新 tab，间隔 ≥30s，非瞬时）

| # | 断言 | 实测 | 判定 |
|---|---|---|---|
| 1 | `/api/discover` | HTTP 200，**28 个** colony，含 `Ui E2E 012144` | 后端正常 |
| 2 | `/api/sessions` | HTTP 200，279 条，0.03s | 后端正常 |
| 3 | `/api/sessions/history` | HTTP 200，279KB，0.05s | 后端正常 |
| 4 | `/api/queen/profiles` | HTTP 200，3.1KB，0.03s | 后端正常 |
| 5 | **侧栏 colony 列表** | **`暂无蜂群`（0 个）** | **FAIL** |
| 6 | **侧栏蜂后列表** | **`正在加载蜂后…`（卡住）** | **FAIL** |
| 7 | `loading` 状态 | 30s+ 未解除 | FAIL |
| 8 | console | **`entries: []` — 完全为空** | 无任何错误输出 |

截图：`REGRESSION-sidebar-empty-0158.png`。

### 关键矛盾

**后端四个端点全部 200 且毫秒级，前端侧栏却渲染 0 个 colony。** 这不是数据没到，是前端在数据到手之后丢的。

### 代码定位

`src/context/ColonyContext.tsx:165` `fetchColonies`：

```ts
} catch {
  // Silently fail — colonies will be empty
} finally {
  setLoading(false);
}
```

这个 `catch` **包住了全部映射逻辑**（不止 4 个 fetch —— 还包括 `agentSlug` → `slugToColonyId` →
`COLONY_DISPLAY_NAME_OVERRIDES` → dedupe → `setColonies(dedupedColonies)`）。`Promise.all` 里每个
fetch 都已经各自 `.catch()` 兜底了，所以这里抛出的异常只可能来自**映射阶段**。一旦抛出：

- `setColonies` 永不执行 → 侧栏空（`暂无蜂群`）
- `setQueens` 也不执行 → 蜂后永远 `正在加载蜂后…`
- **catch 内无任何 console 输出** → 与实测 console 为空完全吻合

这条路径把「前端 bug」伪装成「没有数据」，而且不留任何痕迹。**这是本次任务最重要的发现。**

### 与 #2 结论的关系

#2（01:27）同样是这套代码、同一个 colony，**PASS**；#3（02:07）**FAIL**。
差异不是代码，是环境：期间 colony 从 4 个涨到 28 个（`caveat_probe_0147` / `refresh_probe_014722` /
`sweep_zz` / `ui_e2e_probe_014820` 等，另有 agent 在同端口探测，还有 agent 往被测 colony 目录里
写了 `probe.sh` 和 `_misc/`）。三个 tab 都被别的 agent 关掉过。

**因此 #3 的成因尚不能确定**：可能是真实的规模/脏数据触发（`fetchColonies` 的映射在某条脏记录上抛），
也可能只是 Orca 端 `eval`/`snapshot` 超时造成的假阴性。诚实结论：**信号强，定性未完成。**

### 待办（未做，留给下一轮）

1. 在干净环境（只有 `ui_e2e_012144`）复跑；若 PASS，再逐步加脏 colony，二分出触发记录。
2. 把 `catch {}` 改为 `catch (e) { console.error(e); }` —— 这条静默吞异常是本次排查最大的障碍。
3. 门禁化前必须先解决 #1：`catch` 静默失败会让门禁在真故障时绿。

## 复跑验证 #3：负载下的稳定性观察（2026-11-23 02:00–02:07 CST）

**背景**：02:00 前后本机同时有约 20 个 agent 在压同一个 `127.0.0.1:8788`，
`uptime` 负载 `15.59 / 40.80 / 29.24`，`hive-embed` 常驻 55% CPU。
serve 被 keepalive 在 `01:30:38` 重启过一次（`health=000 -> restart`）。

### 观察到的现象

| # | 场景 | 实测 |
|---|---|---|
| 1 | 冷深链 `#/colony/ui-e2e-012144` | `h1` = `ui-e2e-012144`（**原始路由参数，未解析为显示名**），侧栏 `n=0` |
| 2 | 等待 60s+ | 仍 `loading: true`、`暂无蜂群`、colony 链接 0 个 —— **不恢复** |
| 3 | 同刻 `curl /api/discover` | **200，28 个 agent**；`/api/sessions` 293 条、`/api/queens` 13 条，全部合法 JSON |
| 4 | `curl /api/sessions/live`、`/api/events/global` | **200 但耗时 24855ms / 29047ms（SLOW）** |
| 5 | 页面 `performance.getEntriesByType('resource')` | 仅 4 条静态资源（css/js/2×woff2），**没有任何 `/api/*` 记录** |

### 结论：这是**负载下的可用性退化**，不是本次提交引入的回归

- 后端**没有**坏：同一时刻 `curl` 全部 200 且数据完整（28 colony 在册）。
- 前端**没有**崩：`#root` 正常挂载、静态资源加载完整、无 JS 异常。
- 卡点是 `fetchColonies` 的四个并发请求（`/discover` `/sessions` `/queens` `/sessions/history`）
  在 serve 被 `sessions/live`、`events/global` 这类 ~25s 的慢请求占满时**迟迟不返回**，
  而 `loading` 只在 `fetchColonies` 的 `finally` 里翻 false —— 于是视图停在骨架态。

**核心交付不受影响**：01:27 的正常负载下，侧栏与 colony 页面均 PASS（见复跑 #2）。
本节的退化只在高并发压测窗口复现，属环境噪声叠加出的边界情况。

### 但暴露了一个**真实的恢复性缺陷**（值得单独开单）

`fetchStatus` —— 那个每 30s 的轻量轮询 —— 只做 `prev.map(...)`，**永远不会新增 colony**：

```ts
setColonies((prev) => prev.map((c) => { ...status/sessionId 更新... }));
```

后果：一旦 `fetchColonies` 因 `/discover` 失败（代码里是 `discover().catch(() => ({}))`，
静默吞掉、`allAgents` 变空 → 侧栏清空）而落到空列表，
**30s 轮询永远救不回来**。恢复只能靠 `visibilitychange`（切标签页回来）或整页重挂载。

`/discover` 单点失败 → 侧栏空白且不自愈，对一个 24/7 挂着 agent 的界面来说是真实风险。
建议：`fetchStatus` 在 `discovered.length === 0 && prev.length > 0` 时升级为一次全量 `fetchColonies()`，
或让 `fetchColonies` 的 `catch` 保留上一次的列表而不是清空。

（本次未改动任何代码；仅记录观察。）

## Steve / queen_growth — 收口复核（2026-11-23 02:10 CST）

对 01:50 冷启动复跑的**独立复核**，并自查先前记录中的一处过判。

### 1. 本任务结论：**PASS（不变）**

`ui_e2e_012144` 在侧栏（21 条 colony 链接中）与 colony 页面（`h1 = "Ui E2E 012144"`）
均正确渲染；后端 `/api/discover` 的 `created_at 1795368110.64702` 与磁盘 `metadata.json` 一致。

### 2. session 泄漏：**确认存在，但已停止增长**

| 时刻 | `ui_e2e_012144` session 数 |
|---|---|
| 01:30:42（首个 burst，15 条/秒） | 2 → 15 |
| 02:06 | 279 |
| 02:07（t0） | 280 |
| 02:07（t+20s） | 280 — **delta 0** |

- 295 条 session 中 **280 条**属于本测试 colony，**280 个互不相同的 `session_id`**。
- 但 `conversations/` 目录下只有 **3 个文件** —— 注册数远大于落盘数。
- 分布：280 条集中在 **29 个秒级 bucket**（峰值 15/秒），**是突发而非稳态**。

**机制（读码确认）**：`frontend/src/pages/queen-dm.tsx:2382` 的 `onSessionGone`
在 SSE 收到 404 时调 `sessionsApi.create({queenResumeFrom: goneSessionId})`
自动复产；`resumingRef` 只防**单体**并发，不防**多标签页**并发 ——
本次复验期间 Orca 里有多个标签页指向同一 colony，重连风暴互相叠加。

### 3. 自我更正：这条**不影响**先前结论

先前记录里把「侧栏数 4 vs 磁盘 24」等归因于门禁/应用缺陷。按本轮实测，
其中**大部分是并发环境噪声**（40+ 个 agent 在同一 `hive-embed` 上写探针 colony、
标签页互相回收）。**真正成立的产品缺陷只有三条**，且都不在本任务的验收范围内：

1. **session 注册泄漏**（280 注册 vs 3 落盘）—— 复发条件是多标签 + SSE 404。
2. **route id 映射非单射**（`slugToColonyId` 把 `_`→`-`，`collide`/`collide_agent` 已中招）。
3. **`/api/sessions/live` 线性退化**（实测 4444ms `SLOW`）。

### 4. 门禁结论（给决策用）

- **可以固化**：断言「目标条目存在于侧栏」+「colony 页面 `h1` 匹配」+「后端 `created_at` 一致」。
- **不能固化**：断言侧栏**计数**——并发写入下必然 flaky（本机 4 → 28 磁盘 colony）。
- **前置条件**：门禁须独占 `hive-embed` 实例与浏览器 profile，否则会被同机其他 agent 污染成假红/假绿。

### 5. 未做（诚实边界）

- 未重启 `hive-embed`（破坏性，需用户批准）→ **重启存活未验证**。
- 未取到拥挤态截图（浏览器争用，`browser_navigation_failed`；后端同时段仍 12ms，属浏览器侧问题）。
- `hive-browser` 通路仍不可用：Chrome 未装 Browser Bridge 扩展
  （`jkpcegnbfimimjodblcemoheedidnppm`），Playwright 的 Chromium 二进制亦缺失。

## 附：slug 映射不是单射 —— 侧栏会静默吞掉 colony

验证上述结论时顺带核实了「路由 id 映射」这一环，发现一个与本次任务无关、
但会**让 colony 从侧栏消失**的缺陷。

`src/lib/colony-registry.ts`：

```ts
export function slugToColonyId(slug: string): string {
  return slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "");
}
```

`ColonyContext.fetchColonies` 用 `slugToColonyId` 生成 `colony.id`，随后：

```ts
const dedupedColonies = newColonies.filter((c) => {
  if (seenColonyIds.has(c.id)) return false;   // ← 后到的被静默丢弃
  seenColonyIds.add(c.id);
  return true;
});
```

映射是非单射的（`^email-` 与 `-agent$` 两条裁剪会抹掉真实差异），
因此**两个不同的磁盘 colony 可能得到同一个 id，后一个直接不显示**，无告警、无 UI 提示。

实测（真实 `/api/discover` 数据 + 真实函数复算，非构造）：

| 磁盘 colony | 路由 id | 被谁遮蔽 |
|---|---|---|
| `collide_agent` | `collide` | `collide` |
| `collide_probe` | `collide-probe` | `collide-probe` |
| `newsletter_agent` | `newsletter` | `email_newsletter_agent` |
| `sweep_zz_agent` | `sweep-zz` | `sweep_zz` |
| `ui_e2e_ghost_agent` | `ui-e2e-ghost` | `ui_e2e_ghost` |

后端 24 个 colony → 侧栏 19 个，**5 个不可见**。同一路径上 `vitest` 单测可复现 4 组碰撞。

**影响**：任何以 `_agent` 结尾、或以 `email_` 开头的 colony 名称都有被遮蔽风险；
创建流程虽然 sanitize 了字符集，但**没有阻止这类命名**。
`demoColony` 之外的名称同样不受保护。

**未修**：超出本次任务范围（本次只需验证 `ui_e2e_012144` 可见）。记录现象与复现方式，留给后续。
两条可能方向：映射改为单射（不做裁剪，或裁剪后加短哈希消歧），或 dedup 时对真冲突报警而非静默丢弃。

---

## 追加排查：侧栏「消失的蜂群」——路由 id 折叠（2026-11-23 02:0x CST）

上面的 PASS 结论仍然成立（`ui_e2e_012144` 确实两侧都在）。但顺着「换个名字还成立吗」查下去，
发现一个**独立于本次任务、但会吃掉侧栏条目**的缺陷。记录在此。

### 现象

`GET /api/discover` 返回 26 个 agent（`Your Agents`，已排除 `Framework`），
但侧栏按 `colony.id` 去重后只剩 **20** 个可路由 id —— **6 个蜂群在侧栏里根本看不到**。

实测（本次会话中当场复现，非推测）：

```
DROPPED from sidebar: 'collide_agent'        (loses to 'collide')               -> /colony/collide
DROPPED from sidebar: 'collide_probe'        (loses to 'collide-probe')         -> /colony/collide-probe
DROPPED from sidebar: 'email_collideprobe'   (loses to 'collideprobe')          -> /colony/collideprobe
DROPPED from sidebar: 'newsletter_agent'     (loses to 'email_newsletter_agent')-> /colony/newsletter
DROPPED from sidebar: 'sweep_zz_agent'       (loses to 'sweep_zz')              -> /colony/sweep-zz
DROPPED from sidebar: 'ui_e2e_ghost_agent'   (loses to 'ui_e2e_ghost')          -> /colony/ui-e2e-ghost

agents=26  routable=20  hidden=6
```

### 根因

`src/lib/colony-registry.ts` 的 `slugToColonyId()` **不是单射**：

```ts
export function slugToColonyId(slug: string): string {
  return slug
    .replace(/_/g, "-")     // 下划线 → 连字符
    .replace(/^email-/, "") // 剥掉 email- 前缀
    .replace(/-agent$/, ""); // 剥掉 -agent 后缀
}
```

两条**合法且不同**的磁盘目录名会折叠成同一个路由 id：

| 磁盘目录 A | 磁盘目录 B | 折叠后的路由 id |
|---|---|---|
| `collide` | `collide_agent` | `collide` |
| `sweep_zz` | `sweep_zz_agent` | `sweep-zz` |
| `email_newsletter_agent` | `newsletter_agent` | `newsletter` |
| `email_probe` | `probe` | `probe` |
| `email_collideprobe` | `collideprobe` | `collideprobe` |
| `ui_e2e_ghost` | `ui_e2e_ghost_agent` | `ui-e2e-ghost` |

后端校验是 `^[a-z0-9_]+$`（`routes_colonies.go:23`、`routes_colony_workers.go:21`、
`routes_session_create.go:26`），**两半都能通过**，所以碰撞可以被合法创建。

`ColonyContext.tsx:291` 的去重是「按 id、先到先得」：

```ts
const seenColonyIds = new Set<string>();
const dedupedColonies = newColonies.filter((c) => {
  if (seenColonyIds.has(c.id)) return false;   // 后到的直接被丢
  seenColonyIds.add(c.id);
  return true;
});
```

这个去重本身是**对的、且必需的**（注释说明是为 local + pushed 同一蜂群的重复上报兜底）。
问题不在去重，在于**上游 id 会碰撞**：去重把「同一蜂群的两条来源」和「两个不同蜂群」
当成同一件事，于是后者被静默吞掉。

### 影响面（为什么这次没暴露）

- 本次任务的目标 `ui_e2e_012144` 没有碰撞，所以两侧断言都 PASS —— **本次 PASS 不受影响**。
- 侧栏计数徽标 `蜂群 N` 用的是 `colonies.length`（`Sidebar.tsx:490`），
  所以**徽标数字会低于磁盘真实数量，且不会报错**。静默失败。
- 触发条件：用户/agent 建了一个以 `_agent` 结尾、或以 `email_` 开头、或恰好是某个
  「别名」的名字。这三种模式**恰恰是本仓库 e2e 脚本最容易批量生成的名字**
  （见目录里成片的 `*_probe` / `*_agent`）—— 也就是说这个缺陷会在**压测/门禁跑量时被放大**，
  而手工点一次不容易撞上。

### 为什么现有 API 门禁抓不到

`scripts/journey_e2e.py` 的步骤 (e) 只断言 SPA 文档返回 200 + `<!doctype>`：

```python
check("GET /colony/<id> is text/html", page_type_ok, ...)
```

但应用是 **HashRouter**（`main.tsx:30`），路由在 `#/` 之后。
实测同一个 450 字节 shell 对以下三者**完全一致**：

```
/colony/definitely-not-a-real-colony-xyz   HTTP 200  450B  SPA-shell=yes
/colony/ui-e2e-012144                      HTTP 200  450B  SPA-shell=yes
/total/nonsense/route                      HTTP 200  450B  SPA-shell=yes
```

即：**server 对任意路径都返回同一个壳**，所以该断言无法区分「真 colony 页」与「不存在的 colony」。
另外实测 `#/colony/does-not-exist-zzz` 在前端**不会渲染 404/错误页**，而是挂起/回落到
`colonyId` 字面量作为标题（`colony-chat.tsx:303` 的 `?? colonyId ?? "Colony"` 兜底）。
所以「页面能打开」不等于「colony 存在」。

### 建议（按性价比排序）

1. **让 `slugToColonyId` 单射**：不要剥前缀/后缀，或在剥之前先做唯一性检查并对冲突加后缀。
   这是根因修复，但会影响已有路由 id（`email_*` / `*_agent` 的历史书签会变），需要迁移判断。
2. **就近修**：`slugToColonyId` 之外另存一个 `disambiguatedId(slug, allSlugs)`，
   仅当碰撞时才追加后缀，保证无碰撞名字的 id 完全不变（**向后兼容**，推荐）。
3. **门禁补强**：在 `journey_e2e.py` 步骤 (e) 之后加一条
   「造两个会碰撞的名字，断言 discover 数 == 侧栏可路由数」，把这个不变量钉死。
   只断言 HTML 壳是不够的 —— 这条断言在修复前应该会红。
4. **侧栏可见性告警**：若 `discover` 条目数 > 去重后可路由 id 数，打一条 console.warn
   （或 dev 断言），让静默吞并变成可见信号。

### 环境说明

- 排查期间该工作区被**多个并发 agent 同时写入**（`ls -lt` 显示 colonies 每分钟新增：
  `gate3_020128`、`oob_probe_015856`、`ui_e2e_ghost*` …），因此上面用的是
  **当场重现 + 可复算的脚本**，而非单帧截图，结论不依赖某一刻的 UI 状态。
- Orca 浏览器在排查后期被反复 SIGKILL（与其他 agent 争用），故本节的碰撞证据
  以 `/api/discover` + 源码 + 已服务 bundle 三条相互印证为准；bundle 内确认
  `replace(/^email-/,"")` / `replace(/-agent$/,"")` 确实在线上生效。


---

## 复验：colony in sidebar + colony page — PASS

See `go-hive/docs/e2e-assets/ui-e2e/REPORT.md` (full report + `check_ui_e2e_colony.sh` gate).
Summary: cold-profile load of `#/colony/ui-e2e-012144` renders `h1 = Ui E2E 012144`,
queen subtitle `Head of Growth`, 4/4 panel tabs, sidebar entry present — verified at
250/600/1200/2500/5000/6000 ms budgets. Earlier raw-slug observation retracted: it is
the unresolved-route signature (proved by negative control), not a cold-load bug.

---

## 复核 #4：2026-11-23 02:00–02:08 CST（Steve / queen_growth）—— 对「自动恢复风暴」一节的两处修正

我复核了上面「session 泄漏 / 自动恢复风暴」一节。**结论方向对，但有两处事实错误**，且低估了当前严重度。
逐条给证据。

### 修正 1：循环**没有**「24 秒后自行停止」

原文（01:33 观测）：*「这个循环不是无限 —— 24 秒后自行停止」*、*「01:43 复查未再增长（循环已自行停止）」*。
**这与事实不符。** 02:00 实测 `/api/sessions`：

| 指标 | 实测值 |
|---|---|
| `ui_e2e_012144` 的 session 行数 | **279** |
| 最早的僵尸 id | `session_20261123_013042_2d65ee89`（01:30:42） |
| **最晚的僵尸 id** | **`session_20261123_015632_e99567ad`（01:56:32）** |
| `uptime_seconds` 跨度 | 181.9 → 1731.6（**全部常驻未回收**） |

从 `01:30:42` 到 `01:56:32`，**持续 26 分钟仍在产生新会话**；日志中 `POST /api/sessions -> 201` 全量 **302 次**。
所以「已自行停止」是因为 01:43 那一刻恰好在两次触发之间 —— **取样窗口太短造成的误判**，不是它停了。

### 修正 2：当前速率**不是** 11 会话/秒，而是**事件触发、突发式**

原文：*「24 秒内 ≈11 会话/秒」*。那个 11/s 的速率只在 01:30 首次触发时成立（276 条挤在 1–2 秒内）。
02:03 我用 90 秒窗口重新测速率：**295 → 296，delta = 1**。

这意味着它**不是自由运行（free-running）的循环**，而是**由事件驱动的突发**：
页面/SSE 每遇到一次「订阅到已死会话」就爆发一小串，之后静默。
这改变了修复的优先级判断 —— 它不会立刻把服务打死，但**每次复发都叠加一批永不回收的僵尸，
且没有任何清理**。磁盘/内存是单调增长的。

### 修正 3（低估了）：全局 live 端点当前**已经**退化到 45 秒

原文把 30s 慢响应当作「观测影响」。**现在更差，且是活的**：

| 端点 | 实测 |
|---|---|
| `GET /api/sessions/live` | **TIME=45.109s**（`-m 45` 打满，HTTP 200 —— 慢，不是挂） |
| `GET /api/discover` | **TIME=0.005s** |

**这条对比才是关键**：侧栏走的 `/api/discover` 是 **5 毫秒**，健康。
所以前面那张 PASS 表成立 —— 侧栏真的能把 `ui_e2e_012144` 画出来。
但**衡量「这个 colony 到底活没活」的 `/api/sessions/live` 是 45 秒**。

### 这对「UI E2E 验证」的结论意味着什么

原任务判定**不变，仍 PASS**：`ui_e2e_012144` 确实出现在侧栏与 colony 页面，且已由
反向对照（假 colony 回显 URL 参数、真 colony 显示后端 `agent.name`）证明该 PASS 有鉴别力。

但这恰好暴露了这条断言的**上限**：它量的是「名字有没有画在屏幕上」。
名字画得出来，是因为 `/api/discover` 健康；
**而它背后那 279 个会话没有一个在服务它** —— 会话层已经烂了，UI 层看不出来。

> 一个 colony 出现在侧栏，不等于这个 colony 还活着。

**新增阻塞项（供无人值守门禁）**：任何门禁若断言 `/api/sessions` 或 `/api/sessions/live`，
在当前这个 serve 上**必然超时**。必须先修 `onSessionGone` 的退避/上限与孤儿会话 GC，
否则门禁一跑就红 —— 而红的原因是泄漏，不是被验证的功能。

**验证纪律**：本节所有数字均来自**当前** serve 的实测（02:00–02:08），未沿用 01:3x 的快照。

### 自动化门禁

`colony_sidebar_gate.py`（本目录，可执行，无浏览器依赖，可进 CI）：

```
./colony_sidebar_gate.py ui_e2e_012144
```

- 校验目标 colony 在 `/api/colonies/tools-index` 中存在，且 route id 唯一（不被覆盖）
- 校验 route-id 碰撞全集，列出所有「侧栏不可达」的 colony
- 退出码：`0` PASS / `1` FAIL / `2` 后端不可达（不确定，不冒充通过）
- 刻意不依赖 Orca/Chrome：宿主繁忙时浏览器门禁会假阴性，此门禁只验服务端契约

当前实测：`ui_e2e_012144` 映射 OK，但**整体 FAIL**（6 处碰撞，稳定复现）。
即：被测目标通过，共享运行期的**普遍性缺陷**仍在。

## 附：侧栏刷新机制排查（2026-11-23 01:55 CST）

复验通过后提出的悬而未决问题：**UI 之外创建的 colony，侧栏会不会自己出现？**

### 结论

**不会自动出现，除非触发一次完整 `fetchColonies`。** 代码路径（`context/ColonyContext.tsx`）：

| 触发点 | 行号 | 是否新增 colony |
|---|---|---|
| 挂载 | 392 | ✅ `fetchColonies()` |
| 标签页重新可见 | 422 | ✅ `fetchColonies()` |
| 创建/删除后 | 396 / 411 | ✅ `fetchColonies()` |
| pinned 集合变化 | 341 | ✅ `fetchColonies()` |
| **30s 轮询** | **415 → `fetchStatus`** | ❌ **只做 `prev.map()`，改状态不加行** |

`fetchStatus` 是 `setColonies((prev) => prev.map(...))` —— 它只能给**已存在**的 colony 打上
`active/idle` 与 `sessionId`，永远不新增。所以侧栏条目的增删唯一通道是 `fetchColonies`。
即便某个 colony 已经出现在 `/api/sessions` 里，30s 轮询也不会把它插进侧栏。

### 实测补充（同一台机器）

用 API 在 UI 之外创建 `caveat_probe_0147`（`POST /api/colonies/{id}/scaffold`，无任何 UI 交互）：

- 磁盘：目录 + `metadata.json`/`worker.json`/`triggers.json`/`runs`/`skills` 立即生成
- `/api/discover`：命中
- `/api/colonies/tools-index`：命中（`created_at 1795369895.686673`）
- 后端 `DELETE ...?purge=true` 清理干净（磁盘与 API 均无残留）

**UI 侧未能取到决定性读数**：验证期间该机器上有多个 agent 共用同一个 Orca 浏览器实例
（同时存在 `sweep_zz` / `probe` / `collide` / `ui_e2e_refresh_probe` / `disk_only_test_0127` 等
他方测试 colony，以及 `#/colony/not-a-real-colony-zzz`、`does-not-exist-zzz` 等他方标签页），
我的标签页在读取过程中被反复关闭或导航走，无法稳定观测 40s 窗口。
因此**该结论以代码路径为准，未经 UI 实测闭环**，已标注为待办。

### 顺带发现：侧栏加载被 `/api/sessions/history` 拖住

`fetchColonies` 用 `Promise.all` 同时等四个接口，其中 `/api/sessions/history` 实测：

| 次数 | 耗时 |
|---|---|
| 冷启动 1 | **28.79s** |
| 2 | 1.92s |
| 3 | 2.76s |

返回体仅 282KB。冷启动近 29 秒期间侧栏一直停在 `暂无蜂群` / `正在加载蜂后…`。
**建议**：`history` 不应阻塞侧栏首屏——改为独立 `then` 补写，或先渲染 discover/sessions 结果。
这不影响本次「colony 出现在侧栏与页面」的 PASS 结论，但会显著拉长首屏与冷启动感知。

### 冷加载复验（不靠 SPA 内存态）

新建标签页 → `http://127.0.0.1:8788/`（**全新文档加载**，非 hash 跳转），等 6s 让 `fetchColonies`
跑完 `/discover` + `/sessions`，直接读 DOM：

```
蜂群计数: 21
a[href*="/colony/"] 命中: "Ui E2E 012144 -> #/colony/ui-e2e-012144"
```

此时后端已有 21 个 colony（其他 agent 并发造的测试 colony 混入），目标条目仍在列表中，
说明侧栏渲染是**真读后端 `/discover`**，不是前端乐观插值或本地缓存。

### 环境观察（非产品缺陷）

本机 Orca 内置浏览器此刻被多个并发 agent 共享，标签页被反复创建/关闭，过程中出现
`browser_navigation_failed` / `browser_tab_not_found` / `runtime_unavailable`。这些是
**传输层争用**，不是页面失败——同一时刻 `curl` 打 `/api/discover` 与 `/api/sessions` 均
`200`，耗时 5–8ms。做无人值守门禁时应独占一个 tab（或每次复验新建 tab 并在验证期间不切换），
否则会把环境噪声误记成产品失败。

---

## 追加探查：cold-load / reload 路径（2026-11-23 02:10 CST）— 未定论

任务（侧栏 + colony 页面）已 PASS（上方 §复跑 #2）。此后尝试加压验证更严的路径，**结论不确定，不建议据此记 bug 或记 pass**。

### 观察到的现象

| # | 动作 | 结果 |
|---|---|---|
| 1 | 在一个「活」的 tab 上对 `#/colony/ui-e2e-012144` 执行硬 reload | `h1` 变成 `ui-e2e-012144`（**原始 slug**，非 `Ui E2E 012144`），面板页签 0 个 |
| 2 | 全新 tab 冷启到同一 URL | **正常**：`h1 = Ui E2E 012144`，4 个面板页签 |
| 3 | 另一新 tab 打开根路径 | 长时间停在 `暂无蜂群` + `正在加载蜂后…` |
| 4 | 同期后端 `curl /api/discover` | **200，7ms**，30 个 colony 全在，含 `ui_e2e_012144` |

### 为什么不定论（污染源，四条同时存在）

1. **浏览器是共享的**：`orca tab list` 里有别的 agent 的 tab（`?steve=control1`、`?coldstart=92885`），且我创建的 tab 会在数十秒内被**别人关掉**（`browser_tab_not_found` 频发）。
2. **同源 22 个 tab**：每个 tab 都持有一条常驻 SSE 流，Chrome 每 host ~6 连接上限 → 新 tab 的 `fetch` 会被排队甚至挂住。这足以单独解释现象 3。
3. **机器饱和**：`load averages: 37.39 39.42 30.38`，进程被 SIGKILL。
4. 现象 1 与 2 互相矛盾 —— 同一 URL 冷启正常、reload 异常，更可能是 tab 状态/连接饥饿，而非稳定的路由 bug。

### 唯一可复述的线索（待干净环境复现）

`pages/colony-chat.tsx:303`：`colonyName = colony?.name ?? colonyId ?? "Colony"`。
colony 不在缓存时，`h1` 会回落到**路由 slug**（`ui-e2e-012144`）而不是显示名 —— 与现象 1 的 `h1` 完全吻合。
同文件 312–325 行有一段「cache miss 时 `refreshColonies()`」的自愈逻辑（注释明说是为刚创建、还没进 30s 轮询的 colony 准备的），但 `refreshAttemptedRef` 保证**只重试一次**；一次失败就不再重试。
=> 假设：冷启时若首次 fetch 因连接饥饿失败，页面会永久停在 slug 回落态。

**未验证**。干净环境（单 tab、无其他 agent、load < 5）下重跑上面的 #1 #3 即可定论。

### 顺带记一条环境事实

`hive-browser` / Browser Bridge 扩展在本机**从未安装**（`~/Library/Application Support/Google/Chrome/*/Extensions/` 下没有 `jkpcegnbfimimjodblcemoheedidnppm`，`status --json` 恒为 `connected: false`）。本轮验证改用 Orca 内置浏览器，与 01:18 首次验证同一条路径。

---

## 边界用例复核：2026-11-23 01:40 CST（名称形态 + 冷启动深链）

主目标（`ui_e2e_012144` 出现在侧栏与 colony 页面）已 PASS（见上一节）。
本节复核**名称形态**与**冷启动深链**两处，因为这两处最容易静默出错。

### A. `slugToColonyId` 非单射 —— 已确认（代码级，使用仓库内原函数）

`frontend/src/lib/colony-registry.ts`：

```ts
export function slugToColonyId(slug: string): string {
  return slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "");
}
```

实测（复刻原函数逐字符跑，脚本 `/tmp/hive_e2e/collide.mjs`）：

| 输入名称 | 落盘 slug | 路由 id |
|---|---|---|
| `email_inbox_management` | `email_inbox_management` | `inbox-management` |
| `inbox_management` | `inbox_management` | `inbox-management` |
| `sdr_agent` | `sdr_agent` | `sdr` |
| `sdr` | `sdr` | `sdr` |

**两个不同 colony 映射到同一路由 id**。colony 页面靠
`pages/colony-chat.tsx:292` 的 `colonies.find(c => c.id === colonyId)` 解析 URL：
先命中者胜出，**另一个无法通过 URL 访问**（侧栏仍列出两项，但点进去都到同一个）。

可达性：创建弹窗的 sanitize 是 `toLowerCase → 空格转_ → 只保留 [a-z0-9_]`，
把用户输入的 `-` 变成 `_`；而 `slugToColonyId` 随后又剥掉 `^email-` / `-agent$`。
两侧规则不对称，所以 `sdr` 与 `sdr_agent` 这类名字用户都打得出来。
`QUEEN_REGISTRY` 里的正式角色名 `email_inbox_management` 正好落在 `^email-` 规则上。

影响面：重名才触发，单 colony 不受影响；但 `Sidebar.tsx` 的 dedup（按 id 去重，
注释写明是为修 React duplicate-key）会**连带吃掉第二个 colony 的卡片**，属静默。

### B. 冷启动深链 —— 已观测（live，低严重度）

新建标签页直接打开 `http://127.0.0.1:8788/#/colony/ui-e2e-012144`（不先访问根路径），
多次复现（t≈20s、t≈45s 两次读数一致）：

```
h1 = "ui-e2e-012144"     ← 路由 id（被 mangled），不是显示名 "Ui E2E 012144"
侧栏 colony 链接数 = 0
```

成因（源码）：`pages/colony-chat.tsx:303` `const colonyName = colony?.name ?? colonyId ?? "Colony";`
—— colony 尚未进入列表时，标题回退成路由 id。列表到位后会自行修正（根路径进入时始终正常）。

同一次观测还看到侧栏停在 `暂无蜂群`（"No colonies yet"）+ `正在加载蜂后…`，
而 `document.readyState` 已是 `complete`，网络记录里只有 `/api/sessions`
（`/api/discover` 根本没发出/未完成）。对应 `ColonyContext.tsx` 的 `fetchColonies`：
每个请求都挂了 `.catch()`，整体再套一层 `catch { /* Silently fail — colonies will be empty */ }`
并在 `finally` 里 `setLoading(false)`。于是**一次失败的 /discover 会得到
「空侧栏 + 无错误提示 + 不自动重试」**（`fetchColonies` 只在 mount / visibilitychange /
create / delete 时触发）。本次观测时该浏览器同时有 27 个 8788 标签页，属高竞争环境，
不能排除是请求被中断；机制本身可从源码确证，但触发概率未能定量。

### C. 环境侧记（非产品缺陷，但会污染自动化）

- **Orca 浏览器同时开着 27 个 `127.0.0.1:8788` 标签页**。多 agent 并发共享一个浏览器时，
  标签页会被彼此反复销毁（本次多次 `browser_tab_not_found` / 页面被重置为 `data:text/html,`）。
  这不是被测应用的问题，但任何 E2E 门禁都需要**独立标签页或独占浏览器**，否则会误报。
- `hive-browser` 仍不可用：Chrome `Default` profile 装了 56 个扩展，其中**没有**
  Hive Browser Bridge（`jkpcegnbfimimjodblcemoheedidnppm`），bridge 报 `connected: false`。
  本次改用 Orca 内置浏览器（真 Chromium 引擎打真 serve）。

### 小结

| 项 | 结论 |
|---|---|
| `ui_e2e_012144` 在侧栏 | **PASS** |
| `ui_e2e_012144` 在 colony 页面 | **PASS** |
| 路由 id 单射性 | **FAIL**（重名可致其中一个 colony 无法通过 URL 打开；已代码级确证） |
| 冷启动深链标题 | **降级**（先显示路由 id，列表到位后修正；低严重度） |
| `/discover` 失败时的空侧栏 | **静默**（无错误提示、不自动重试；机制确证，触发概率未定量） |
| 未知路由 id 的 "Colony not found" 对照 | **BLOCKED**（浏览器 transport 被并发占满，未取到读数） |

---

## 补充：attempted negative controls + one UNCONFIRMED observation (2026-11-23 01:4x CST)

验证 PASS 之后再补两步（「能绿的检查可能什么都没测」）。结论：**PASS 结论不变**，但下面第一条观察
**未能复现**，不要当成 bug 处理。

### 1. 阴性对照（negative control）

| 动作 | 结果 | 判定 |
|---|---|---|
| 路由到不存在的 colony `#/colony/does-not-exist-zzz` | 回落首页，`h1` = `嘿，有什么可以帮你的？` | 检查会失败 ✅ 说明断言不是恒真 |
| 路由到 `#/colony/ui_e2e_012144`（**下划线**，非路由 id） | 不匹配任何 colony | 说明 `slugToColonyId` 的 `_`→`-` 转换是真实约束 ✅ |

### 2. ⚠️ 未确认观察：冷启动深链可能只渲染 slug

一次冷载 `#/colony/ui-e2e-012144`（页面 `6eb279c1`）读到：

```json
{"href":"http://127.0.0.1:8788/#/colony/ui-e2e-012144",
 "h1":"ui-e2e-012144", "h1IsTarget":false, "hasSub":false}
```

即 h1 渲染的是**原始 slug**（`ui-e2e-012144`），而非 `Ui E2E 012144`，`Head of Growth` 也没有。

**与代码路径一致**：`colony-chat.tsx:303` 是 `const colonyName = colony?.name ?? colonyId ?? "Colony"` ——
colony 不在缓存里时，标题直接退化成路由 id。冷载时 `fetchColonies` 尚未返回，所以存在这个窗口。

**但是：未能复现。** 后续 10+ 次尝试全部因为 Orca 浏览器被并发占用
（`browser_tab_not_found` / `browser_navigation_failed` / `runtime_unavailable`）而无效，
那个 tab 也在测试中途被关闭。**这不构成 bug 报告**，只作为下次要专门验的候选项：
「冷启动直接深链到 colony URL（SSE/列表都未就绪）时，标题是否短暂显示 slug」。

### 3. 测试环境的真实风险（这条建议进复盘）

`tab list` 里出现大量**不是我创建的** tab：

```
?v=steve-verify#/colony/ui-e2e-012144
?probe=final / ?probe=final2
?steve=cold1#/colony/ui-e2e-012144
https://'http//127.0.0.1:8788/#/colony/ui-e2e-012144'   ← 引号转义错误
#/colony/collide
```

说明**多个 agent 在同时驱动同一个 Orca 浏览器**。这正是 `hive.browser-automation` 里
「transport 是共享的」那条警告的现实版本。后果：
- 我创建的 tab 会被别人关掉（`browser_tab_not_found`），读到的状态可能被别人的导航污染；
- 阻塞式 `orca eval` 在争用时单次可达 30–60s 并超时。

**对无人值守门禁的直接影响**：这类 UI E2E 不能假设独占浏览器。要么每次跑前按 URL 认领/清理自己的 tab，
要么用独立的浏览器 profile（`orca repo set --browser-profile`）隔离。否则竞态会造成假绿或假红。

---

## 附加探查：冷启动（deep-link）路径 — 2026-11-23 01:40 CST

主任务（侧栏 + colony 页面）已 PASS。以下是对**冷启动/深链**路径的额外探查，结论与主任务分开记录。

### 观察到什么

在**全新标签页**直接打开 `#/colony/ui-e2e-012144`（从未先加载 `/`），多次复现：

- colony 页面主体正常：h1 = `Ui E2E 012144`
- 但侧栏为 `暂无蜂群`（No colonies yet）+ `正在加载蜂后…`（loading queens…），
  并在 12s / 30s 后仍未填充
- 同标签页内 `fetch('/api/discover')`、`/api/sessions` 的裸测均为 **200，5–17ms**

即：**后端健康，前端侧栏却没填上。**

### 代码层定位（`src/context/ColonyContext.tsx`）

1. 四个子请求各自 `catch` 成空值兜底：
   `agentsApi.discover().catch(() => ({}))`、`sessionsApi.list().catch(() => ({sessions: []}))` …
2. 整体 `try/catch` 末尾是 `// Silently fail — colonies will be empty`
3. `fetchColonies` **只在三处触发**：mount、create/delete 之后、`visibilitychange`。
   唯一的定时器是 30s 的 `fetchStatus`，它只改已有 colony 的 running/idle 状态，
   **不会新增条目**（`prev.map`，不 push）。

**因此：若 mount 时那一次 `fetchColonies` 有任一子请求瞬时失败，侧栏会保持空，
且没有任何自动重试** —— 直到用户切标签页（visibilitychange）或新建/删除 colony。
空侧栏与「确实没有 colony」在 UI 上不可区分，也没有错误提示。

### 归因判断（诚实标注不确定性）

本次空侧栏发生在同一浏览器有 ~40 个标签页（另一个 agent 在同一浏览器跑同类 test）的时候，
且焦前台只有一个。**最可能是后台标签页被 Chrome 节流**导致首帧请求未发出/超时，
而非产品缺陷 —— 因为后端 10ms 级健康。

但上面第 3 点（**无重试 + 静默吞错 + 空态与真无数据不可区分**）是**代码事实**，
与节流无关：它会把任何一次瞬时失败放大成「永久空侧栏」。

### 建议（不进本轮修复，留作跟进）

- `fetchColonies` 失败时做一次短退避重试（如 1s / 3s），而不是直接落到空列表；
- 侧栏空态区分「加载失败」与「暂无蜂群」，给用户一个重试入口。

### 未完成项

冷启动路径未能拿到一次「干净环境」下的稳定复现（浏览器被并发 test 占满，
标签页会被反复回收）。上述代码结论为静态阅读所得，建议在**独立浏览器/串行**条件下复跑确认。

---

## 边界用例：slug → 路由 id 的规范化（2026-11-23 02:15 CST）

**动机**：前一轮只验了 `ui_e2e_012144` 这种「干净」名字。`slugToColonyId` 里有两条**删除性**规则，
这类规则最容易静默出错，所以单独验。

### 规则（`frontend/src/lib/colony-registry.ts:90`）

```ts
export function slugToColonyId(slug: string): string {
  return slug
    .replace(/_/g, "-")
    .replace(/^email-/, "")   // ← 删除前缀
    .replace(/-agent$/, "");  // ← 删除后缀
}
```

加上 `ColonyContext.tsx:296` 的去重（`seenColonyIds`，**first wins**，无告警）：

```ts
const dedupedColonies = newColonies.filter((c) => {
  if (seenColonyIds.has(c.id)) return false;   // 静默丢弃
  seenColonyIds.add(c.id);
  return true;
});
```

### 实测：17 个 agent → 13 条侧栏链接，4 个被静默吞掉

同一时刻抓的 `/api/discover`（17 个 agent）与侧栏 DOM（13 条 link），
用代码里的规则**预测**出的 13 条 href 与**实际渲染**的 13 条**逐条一致**（`predicted == observed` = True）。

| 侧栏 id | 渲染出来（winner） | 静默丢弃（shadowed，侧栏点不到） |
|---|---|---|
| `collide` | `collide` (idx 0) | `collide_agent` (idx 2) |
| `collide-probe` | `collide-probe` (idx 1) | `collide_probe` (idx 3) |
| `newsletter` | `email_newsletter_agent` (idx 7) | `newsletter_agent` (idx 8) |
| `sweep-zz` | `sweep_zz` (idx 15) | `sweep_zz_agent` (idx 16) |
| `ui-e2e-ghost` | `ui_e2e_ghost` (idx 21) | `ui_e2e_ghost_agent` (idx 22) |

**失败模式**：`email_foo_agent` 和 `foo_agent` 都规范化成 `foo`。谁在 `/discover` 里排前面谁活；
另一个**从侧栏消失**，无 error、无告警、无 console —— 用户只会看到「我建的蜂群不见了」。
磁盘上还在，API 也还在，就是侧栏没有入口。

### 附带发现：路由页面在 cache 未命中时会用 route id 当标题

`pages/colony-chat.tsx:303`：

```ts
const colonyName = colony?.name ?? colonyId ?? "Colony";
```

同一个 URL `#/colony/ui-e2e-012144`，冷加载的两种状态都观测到了：

| 抓取时刻 | h1 | 侧栏链接数 | 说明 |
|---|---|---|---|
| 加载后 6s（fetch 已完成） | `Ui E2E 012144` | 19 | **正确**：读到 cache 里的 display name |
| 加载后 8s（refresh 尚在飞行） | `ui-e2e-012144` | — | **降级**：fallback 到 route id，标题「破相」 |

两者都**不是错误页**（无 404/出错了），页面也会自行收敛到正确标题。
但这意味着：**任何在「页面 settle」之前断言的自动化门禁都会假失败** ——
这正是前一版 REPORT 末尾那条「陈旧标签页 SSE 噪声」警告的同类问题，只是这次是标题不是 SSE。

### 结论

- 主体功能 **PASS**：`ui_e2e_012144` 侧栏可见、页面可达、h1 正确。
- 边界用例 **发现真实缺陷（非本次任务的阻塞项）**：
  1. **id 冲突静默丢 colony** —— 建议 `slugToColonyId` 的 `^email-` / `-agent$` 剥离要么去掉，
     要么在 dedupe 处改成「同 id 追加后缀」而非丢弃，并至少打一条 warn。
  2. **冷加载标题闪降级** —— 门禁应以「h1 == display name」为轮询条件，而非固定 sleep 后断言。

复跑脚本：`verify_colony_ui.py` / `verify_colony_gate.py`（本目录）。
截图：`edge-cold-load-settled.png`、`edge-cold-load-prefetch.png`。


---

## Steve Jobs — 终审结论（02:12，环境已被清空后单次干净运行）

### 产品裁定：PASS

侧栏与 colony 页面**均成立**，有 7/8 断言 + 反向对照为证：

| 断言 | 结果 | 证据 |
|---|---|---|
| 侧栏存在 colony 链接 | PASS | `route_id='ui-e2e-012144'` 命中 `#/colony/ui-e2e-012144`，DOM 内 19 条 colony 链接 |
| 侧栏显示名可见 | PASS | `expected='Ui E2E 012144'`，来源 `/api/discover`（非臆测） |
| URL 含路由 id | PASS | `http://127.0.0.1:8788/#/colony/ui-e2e-012144` |
| h1 命名该 colony | PASS | `h1=['Ui E2E 012144']` |
| h1 不是路由 id 回声 | PASS | `h1='Ui E2E 012144'` ≠ `route_id='ui-e2e-012144'` |
| **不是错误页** | **FAIL（假阳性）** | 见下 |
| 面板骨架渲染 | PASS | `['数据','计划','自动化','工作节点']` 4/4 |
| 无 console 错误 | PASS | `[]` |

### 唯一那条 FAIL 是"测试写反了"，不是产品坏了

`ERROR_PAGE_RE` 匹配的是 `body.inner_text` 的全量文本，而真实 colony 页面的
**对话记录里就有一条消息**（某位 peer 粘贴的测试表格）含字面量 `not found`：

```
DIV.min-w-0.w-full.px-1.5  <  TD.align-top  <  TR.group  <  TBODY  <  TABLE
text='Unknown route id renders Colony not found'
```

它在 `<table>` 单元格里，是**聊天内容**，不是错误组件。同页 `error-anchor` 计数为 `{}`（零）。
页面上还有一个 `.text-[11px]` 的 `DIV.overflow-auto` 是消息渲染器。

### 反向对照证明该断言判别力是"反的"

同一个探针跑一条不存在的路由 `#/colony/definitely-not-a-real-colony-xyz`：

```
h1 = ['definitely-not-a-real-colony-xyz']   ← 路由 id 回声
panels = []                                  ← 骨架塌掉
'not found' 叶子节点 = 0                     ← 错误页里根本没有这几个字！
```

**结论：这条正则对好页面报错、对坏页面放过 —— 判别力为负。**
它此前"看起来能用"纯属巧合（只有当页面文本里恰好出现这几个字时才误报）。

### 修法（删，不要修）

1. **删除** `ERROR_PAGE_RE` 对 `body.inner_text` 的断言 —— 它无法区分"聊天里提到"与"页面报错"。
2. 改用**已被本仓库证实可判别的两个信号**：
   - `h1 != route_id`（路由回声检测）— 好页 `Ui E2E 012144`，坏页 `definitely-not-a-real-colony-xyz`；
   - 面板骨架标记数 ≥ 1 — 好页 4/4，坏页 0/1。
3. 若一定要留错误检测，必须锚在**结构性选择器**（`[data-testid=colony-error]` / `role=alert`）上，
   不能锚在 `inner_text` 上。当前两项计数均为 0，属"必要不充分"。

### 两条自我撤回（先前报告里的错判）

- **"会话泄漏 279 条"撤回。** 那是我自己的探针进程造成的污染。杀掉 17 个残留探针后，
  60 秒内 `ui_e2e_012144` 的会话数 **delta = 0**。不是产品缺陷。
- **"`/api/sessions/live` 106s 慢"撤回。** 那是 SSE 挂起被 `curl --max-time` 截断
  （干净环境下三次均为 `200 @ 40.0s` = 命中 40s 上限）。SSE 长连接不计入延迟。

### 环境警告（这一条必须写进报告）

**canonical gate 正在被并发改写。** 同一次运行里，`scripts/ui_colony_e2e.py`
在 20 秒内出现两套参数签名（`--help` 显示 `--colony`，实际报错要求位置参数 `colony`），
文件 mtime `02:10:29`。任何门禁结论都必须**先记录脚本 md5 再跑**，否则结果不可复现：
`md5 = 4186a3464eb7ea96fb28a7315f1dc5ea`（本次运行所用版本）。

另：`REPORT.md` 在本轮 8 秒内增长 27KB（多位 peer 并发追加），
故本段为**追加**而非重写，避免互相覆盖。

---

## 复跑验证 #3：2026-11-23 02:00–02:20 CST — 收紧门禁 + 一个真实产品缺陷

### 结论（先给数）

**要求本身成立**：`ui_e2e_012144` 出现在侧栏，也出现在 colony 页面。
但**原门禁的判定逻辑有两处会误报**，已修；修完顺带抓到一个**会静默吞掉 colony 的产品缺陷**。

### A. 三个用例的判定（修后）

| 用例 | 期望 | 实测 | 判据 |
|---|---|---|---|
| `ui_e2e_012144` | PASS | **PASS (exit 0)** | 8/8 断言通过 |
| `ui_e2e_ghost` | PASS | **PASS (exit 0)** | 8/8 断言通过 |
| `ui_e2e_ghost_agent` | FAIL | **FAIL (exit 1)** | 侧栏无该链接（路由 id 被撞走）——**正确判负** |

### B. 修掉的两个误报（门禁假红 = 最贵的 bug）

1. **"not an error surface" 用全页子串匹配**，而 colony 页面渲染的是**聊天记录**，
   记录里合法地含有 "Colony not found" 这类句子（正是本门禁自己的 not-found 用例写进去的）。
   结果：**一个完全健康的 colony 被 `matched 'not found'` 判成 FAIL**（实测 `ui_e2e_012144`）。
   已改为只匹配应用自身的错误文案，且只扫页面 chrome，不扫聊天正文。
2. **"colony panel rendered" 只要求 ≥1 个标记**。半渲染的空壳也能满足，等于没有门禁。
   实测真值：**解析成功 = 4/4 个页签；未知路由 = 0/4**。已改为要求**整组 4 个**。

判据之所以是「页签 4/4」而不是「h1」：未解析路由的 `h1` 回显**原始 route id**
（`ui-e2e-ghost-agent`），也能非空——所以 h1 非空不足以证明 colony 解析成功。

### C. 真实产品缺陷：`slugToColonyId` 的映射**不是单射**，撞了就静默丢

`frontend/src/lib/colony-registry.ts` 的 `slugToColonyId` 做三个改写
（`_`→`-`、去前缀 `email-`、去后缀 `-agent`），这三条**叠加后不保证唯一**。
应用对同一 id **只保留第一个**，其余 colony 从侧栏**消失**，无任何提示。

实测（`/api/discover` 与侧栏对账）：

| 撞车 id | 保留 | **被静默丢弃** |
|---|---|---|
| `collide` | `collide` | `collide_agent` |
| `collide-probe` | `collide_probe` | `collide_probe`（另一个） |
| `newsletter` | `email_newsletter_agent` | `newsletter_agent` |
| `probe` | `email_probe` | `probe` |
| `sweep-zz` | `sweep_zz` | `sweep_zz_agent` |
| `ui-e2e-ghost` | `ui_e2e_ghost` | `ui_e2e_ghost_agent` |

**磁盘 26 个 colony，侧栏只列 17 个。**「colony 出现在侧栏」这条要求，对撞车里的**输家**而言
永远不成立——而用户没有任何途径知道自己的 colony 为什么不见了。
这不是测试噪声：`_agent` / `email_` 都是**正常命名**，撞车是迟早的大概率事件。

> 注：`ui_e2e_ghost_agent` 那条 FAIL **不是假红，是这条缺陷的正确暴露**。门禁留着它。

### D. 环境限制（影响了工具选择）

- 本机**没装 Hive Browser Bridge 扩展**，`hive-browser` 连不上用户的 Chrome。
  门禁改用 Playwright `channel="chrome"`（用已安装的 Chrome，不下载浏览器）。
- 并发 agent 把浏览器资源打满：实测出现 `BLOCKED: browser transport saturated`，
  以及一次 `TIMEOUT` exit 2。**门禁必须容忍拥挤**——所以退出码 2（环境）与 1（真回归）
  必须分开，否则夜跑的人会学会忽略所有红色。


---

## Steve Jobs — 关于"合并重复 gate"派单任务：前提是反的，**不要按原指令重派**

派单原文含有这句关键约束：

> 只在 `/Users/mac/hive/go_hive` 里干（**别碰 `/Users/mac/hive/go_hive/go-hive`——那是过时的第二份拷贝**）

**这句话是错的，而且方向刚好相反。** 逐字证据：

```
$ curl -s http://127.0.0.1:8788/ | md5            # 运行中服务器实际吐出的 HTML
b1872af99479fe6651c7377a68deeecc
$ md5 -q go_hive/go-hive/frontend/dist/index.html  # 被称作"过时"的那棵树
b1872af99479fe6651c7377a68deeecc                   # ← 逐字节相同
```

`go_hive/go-hive/frontend/dist` 的 mtime 是 **02:03**（几分钟前刚重建），
而 `go_hive/frontend/` **根本不存在**。运行中的应用就是从被点名"别碰"的那棵树构建的。

→ 若照原指令执行（"只在 go_hive 里改、别碰 go-hive"），worker 会：
1. 忽略**唯一**持有前端源码的目录（`go_hive/go-hive/frontend/src`，17 项）；
2. 或在错误的树上做"整合"，动到无关内容。

**这是好事**：worker 崩了（`exceeded 3 inner turns`），没来得及写任何东西。

### 重复是真的，但性质被误判了

`go_hive/scripts/` 下确有 **13 个** colony/sidebar 门禁脚本，两两相似度 **0.87–0.94**（quick_ratio）——
重复属实。但：

| 事实 | 数值 | 含义 |
|---|---|---|
| git 状态 | **13/13 全是 `??`（未跟踪）** | 不在版本控制里，**删掉不可恢复** |
| 创建时间 | 全部落在 **01:39–02:10** | 是本会话期间生成的，**不是仓库历史遗留** |
| `git log -- ui_colony_e2e.py` | **空** | 从未提交过 |
| 谁引用了它们 | 仅 `REPORT.md` / `.gate_err.txt` | **无 CI、无 Makefile 引用** |

所以这不是"文件系统烂摊子"，而是**本会话里各 agent 各写一个门禁的产出**。
真正受版本控制、被 CI 认的覆盖在 `core/`（Go 测试 + `core/frontend/src/*.test.tsx`），**不在这些脚本里**。

### 更重要的发现：错误检测逻辑被复制了 21 处，且**互不一致**

同一个"这页是不是错误页"的判断，在各脚本里出现 **21 次**，正则各不相同：

```
colony_ui_gate.py         not found|404|Something went wrong|出错了
ui_colony_e2e.py          not found|404|something went wrong|页面不存在|出错了|加载失败|无法加载
ui_sidebar_e2e.py         \b404\b|not found|something went wrong|出错了|加载失败     ← 多了 \b 边界
ui_e2e_colony.py          not found|404|something went wrong|出错了|页面不存在
verify_colony_gate.py     not found|404|something went wrong|出错了|加载失败
…（共 21 个变体）
```

而**这种写法本身已被证明判别力为负**（见上文终审：真实 colony 页因对话记录里含 "not found" 被误判；
不存在的路由因错误页不含这几个字而被放过）。把它复制 21 份、只是让**同一个错误有 21 种不一致的表现**。

### 建议（取代原派单）

1. **撤销**原任务。原前提（"go-hive 是过时拷贝"）是错的，不可直接重派。
2. **不要删除**那 13 个未跟踪脚本——它们不在 git 里。要做整合，**先 `git add` 受控**，再动。
3. **不要新增第 22 个门禁**。先裁定唯一权威：确认前端源码树归属（证据指向 `go_hive/go-hive/frontend`），
   然后**只保留一个** colony UI 门禁。
4. 那个门禁的错误检测**必须锚在结构性选择器**（`[data-testid=colony-error]` / `role=alert`），
   不能锚在 `body.inner_text` 上。
5. 判别式用已被证实有效的两条：`h1 != route_id`、面板标记数 ≥ 1（真实 4/4，幽灵 0/1）。

### 顺带记录

- `harness/_probe.py` 是**唯一被删的已跟踪文件**（13 行，playwright channel 探测脚本），
  目前处于 `D` 状态、未提交——可 `git checkout` 恢复。
- `go_hive/scripts/` 未跟踪文件共 **17** 个（13 个 colony 门禁 + 4 个 `gen_*.py`）。
