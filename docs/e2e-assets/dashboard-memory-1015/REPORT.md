# Dashboard 记忆面 E2E 报告 · 1.0.15（Settings → Memory / 笔记历史与恢复）

- 日期：2026-11-22 15:00–15:13（CST）
- 分支/提交：worktree `dash-memory` @ `e7cfa14e`（基线）+ 修复提交 `5aea7e1a`
- 环境：macOS 26.7.1（arm64）、真实 Chrome（Hermes browser_exec / CDP）、视口 1280×577
- HIVE_HOME（临时）：`/Users/mac/.hermes/profiles/omh-tui/cache/scratch/hive-e2e-dash-memory-1015`
- 服务地址：`http://127.0.0.1:18787`（修复后本树构建）；缺陷复现另起 `18788`（slot 二进制）

## 一、结论

1. **产品面（Go API + 前端源码）无缺陷**：Settings → Memory（`MemorySettingsSection`）渲染、
   GET/PUT `/api/config/memory` 落盘、笔记创建 → 历史（`log.jsonl`）→ 恢复旧版本 → 已移除列表，
   全部断言 PASS（详见断言表）。
2. **发现一个构建流水线缺陷（F1）**：`go_hive/dist/slots/20261122-14/hive` 二进制嵌入的 dashboard
   早于 1.0.15 记忆面前端提交 `912056d1`（2026-11-22 08:36）——Settings 里根本没有「记忆」项。
   根因：`release_slot.sh` 直接 `go build -tags embed_ui`，从不刷新 `uiembed/dist` 暂存树；
   而 `embed_ui.sh` 只在 `dist/index.html` 缺失时才构建前端，存量 dist 一律当“新鲜”。
   已在 `5aea7e1a` 修复（embed_ui.sh 新鲜度判定 + release_slot.sh 走 embed_ui.sh），
   并新增回归测试 `internal/server/uiembed/freshness_test.go`（4 用例 PASS，已入 `check_parity`）。
3. 因此本 E2E 的 **PASS 运行使用“本树构建”的二进制**（任务允许），构建命令与 slot 一致
   （`-tags embed_ui -trimpath`），且验证了修复后的 slot 构建路径会重建并重新暂存前端。
   slot `20261122-14` 产物本身不会回溯更新；合并后 16:02 的 slot 将自动携带新 dashboard。

## 二、步骤与命令（实测）

```bash
# 1) 构建修复后的单二进制（worktree；embed_ui + trimpath，与 slot 一致）
cd /Users/mac/hive/.wt/dash-memory/go_hive/go-hive
ln -sfn /Users/mac/hive/core/frontend/node_modules frontend/node_modules   # 复用已装前端依赖
FORCE_FRONTEND_BUILD=1 GO_BUILD_FLAGS="-trimpath" OUT=$PWD/dist/hive-fixed \
  CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod bash ../scripts/embed_ui.sh

# 2) 启动 + 健康检查
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/hive-e2e-dash-memory-1015 \
  $PWD/dist/hive-fixed serve --host 127.0.0.1 --port 18787
curl -s http://127.0.0.1:18787/api/health     # → 200 {"status":"ok",...}

# 3) 缺陷复现（slot 二进制，18788）
HIVE_HOME=.../slot-home /Users/mac/hive/go_hive/dist/slots/20261122-14/hive serve --host 127.0.0.1 --port 18788
strings /Users/mac/hive/go_hive/dist/slots/20261122-14/hive | grep -c "How a queen's memory notes reach her"   # → 0
strings /Users/mac/hive/.wt/dash-memory/go_hive/go-hive/dist/hive-fixed | grep -c "How a queen's memory notes reach her"  # → 2

# 4) 回归测试
cd /Users/mac/hive/.wt/dash-memory/go_hive/go-hive
go test ./internal/server/uiembed/ ./internal/server/    # ok / ok
bash ../scripts/check_parity.sh                          # OK — 5709 个测试全部带注解
```

浏览器侧：
- 账号菜单（按钮 `title="账户"`）→「个人资料与设置」→ 设置弹窗 → 左侧「记忆」；
- Memory 页走 hash 路由：`http://127.0.0.1:18787/#/memory-library`（SPA 用 `#/` 路由，直连 `/memory-library` 只会落到首页）；
- 界面语言为中文（浏览器 locale 决定），下文断言用中文文案；
- 首次进入会自动弹「Welcome to Hive」导览层，截图前已点「Skip tour」（4 张通过截图均无遮挡）；
- 笔记用 `POST /api/memories/file?path=global/dash-memory-1015-e2e.md` 创建（任务允许 API 创建；
  Memory 页本身没有“新建”入口），其余动作（编辑/历史/恢复/删除）全部走 UI 点击。

## 三、断言表

| # | 断言 | 证据 | 结果 |
|---|------|------|------|
| A1 | `GET /api/health` → 200，`status=ok` | curl 输出；`api-evidence.json.health` | PASS |
| A2 | dashboard 显示版本号 v1.0.26-go.1（侧栏左下） | `01-dashboard-home.png`；DOM 提取 `{x:16,y:551,w:72,h:14}` | PASS |
| A3 | Settings → 记忆 渲染：标题「记忆」+ 说明文字 + 两个选择组（笔记如何到达她：3 项；写笔记的模型：2 项，含说明） | `02-settings-memory.png`；DOM：`radiogroup aria-label=["笔记如何到达她","写笔记的模型"]` | PASS |
| A4 | UI 改值 → `PUT /api/config/memory` 200 → `GET` 回读一致 → 落盘 `configuration.json`：`memory_recall.mode=wait`、`memory_reflection.model=main` | 页面内 fetch 记录（body `{"memory":{"recall_mode":"wait"}}` 与 `{"memory":{"reflection_model":"main"}}`，均 200）；`api-evidence.json.config_memory / configuration_json` | PASS |
| A5 | 创建 note（POST）→ Memory 页列表出现（标题 Dashboard memory E2E / `dash-memory-1015-e2e.md`） | POST 响应 `created:true`；页面文本 | PASS |
| A6 | UI 编辑保存 → `log.jsonl` 出现该变更（`op=write`,`reason=edited on the Memory page`，before/after 完整） | `api-evidence.json.memory_log[1]`（磁盘文件原样） | PASS |
| A7 | 历史列表渲染：每条含时间/作者/动作，最新为「当前」，旧版本带「恢复」按钮 | `03-memory-note-history.png`；`ul[aria-label=历史]` 5 条 | PASS |
| A8 | 恢复旧版本成功：点击历史「恢复」→ 内容回到 change 0（"Version two" 消失）→ 新增 `op=restore`；从「已移除」再恢复（change 3）同样成功 | `api-evidence.json.memory_log[2] / [4]`；恢复后 GET file 内容与 change 0 的 after 完全一致 | PASS |
| A9 | retired 列表可见：删除后「已移除 (1)」展开显示文件名/时间/原因/「恢复」按钮；`GET /api/memories/retired` 返回该条（n=5） | `04-memory-retired.png`；`api-evidence.json.retired` | PASS |
| A10 | 控制台错误 0（console.error / window.error / unhandledrejection，注入于文档加载前，覆盖 dashboard→Settings→Memory→恢复→删除全流程） | 页面 `window.__e2eErrors = []`；`api-evidence.json.console_errors=0` | PASS |
| F1 | slot 20261122-14 二进制的 Settings **没有「记忆」项**（导航：个人资料/AI 模型/速率限制/导览/开发者）；嵌入 JS `index-B_gOJ5GX.js` 无记忆面文案 | `00-slot-20261122-14-missing-memory-tab.png`；`strings`=0 | FAIL（构建缺陷，见 §四，已修复） |

补充一致性断言（均 PASS）：
- 删除后 `GET /api/memories` → `{"memories":[]}`；磁盘 `memories/` 下只剩 `global/log.jsonl`（笔记文件已回收）；
- `GET /api/memories/history` 6 条变更与 `log.jsonl` 6 行完全一致（write/write/restore/retire/restore/retire）；
- 修复前后服务端接口行为一致（`/api/config/memory` 只写 `memory_recall.mode` / `memory_reflection.model`，其余键保留）。

## 四、缺陷 F1：slot 嵌入过期 dashboard（已修复）

- **现象**：slot `20261122-14`（head `edfd590c`，2026-11-22 14:02 构建）运行后 Settings 无「记忆」；
  服务的内嵌资源入口为 `/assets/index-B_gOJ5GX.js`（= `internal/server/uiembed/dist` 里 2026-11-21 18:30 的旧构建）。
- **根因链**：
  1. `912056d1`（2026-11-22 08:36）前端同步 1.0.15 记忆面（Settings → Memory / 笔记历史与恢复）；
  2. `release_slot.sh` 用 `go build -tags embed_ui -trimpath` 打包，**从不**重新构建/暂存
     `internal/server/uiembed/dist`（gitignored 构建产物）；
  3. `embed_ui.sh` 仅在 `dist/index.html` **缺失**时才构建 —— 存量 dist 即使是旧的也会被原样嵌入。
  4. 结果：10/12/14 三个 slot 二进制全部嵌入旧 dashboard（`strings` 检查均为 0 处记忆面字符串）。
- **影响面**：只有 slot 交付产物的 dashboard 落后；Go 源码、前端源码的该功能本身正常（本树构建验证 PASS）。
- **修复（`5aea7e1a`）**：
  - `go_hive/scripts/embed_ui.sh`：按 mtime 判定前端新鲜度（`src/`、`public/`、`index.html`、
    `package.json`、`package-lock.json`、`vite.config.ts`、`tsconfig.json` vs `dist/index.html`），
    stale 即重建；`SKIP_FRONTEND_BUILD=1` 遇 stale 直接拒绝（不再静默嵌入过期 UI）；
    新增 `FORCE_FRONTEND_BUILD`、`GO_BUILD_FLAGS`、`--print-mode`（可查询）；暂存后 `diff -r`
    校验暂存树与构建产物一致。
  - `go_hive/scripts/release_slot.sh`：hive 构建改走 `embed_ui.sh`
    （`FORCE_FRONTEND_BUILD=1 GO_BUILD_FLAGS=-trimpath OUT=$out/hive`）。
  - 回归测试 `internal/server/uiembed/freshness_test.go`：dist 缺失 / dist 较新 / 源码较新 / 强制重建
    4 用例（`go test` PASS，`check_parity` 通过）。
- **修复验证**：本树重建（走的正是 slot 修改后的命令）→ `strings`=2、E2E 全断言 PASS；
  未整跑 `release_slot.sh`（避免扰动 2h cadence 与 `dist/slots` 台账），但其替换的那条命令已原样执行。

## 五、截图清单（`go_hive/docs/e2e-assets/dashboard-memory-1015/`）

| 文件 | 内容 | 字节 |
|------|------|------|
| `00-slot-20261122-14-missing-memory-tab.png` | F1 证据：slot 二进制 Settings 无「记忆」项 | 139,647 |
| `01-dashboard-home.png` | dashboard 首页，侧栏版本 `v1.0.26-go.1` | 169,403 |
| `02-settings-memory.png` | Settings → 记忆：两个选择组，`回答前挑选` + `蜂后自己的模型` 已选中 | 171,542 |
| `03-memory-note-history.png` | Memory 页：笔记内容（已恢复为 Version one）+ 历史列表（当前/恢复） | 83,539 |
| `04-memory-retired.png` | 「已移除 (1)」展开：文件名、删除时间/原因、「恢复」按钮 | 63,545 |

（5 张 PNG 全部经视觉核对无遮挡、内容正确；sha256 见 `api-evidence.json.screenshots`。）

## 六、备注（非缺陷）

- 首访自动导览层（「Welcome to Hive」）会遮挡页面；属产品既有行为，截图前点「Skip tour」即可，
  不视为缺陷（首轮未跳过的截图已全部重拍）。
- 测试脚本首轮误点「工作节点模型」选项：其描述文案「每位蜂后自己的模型」包含目标标题子串，
  属脚本选择器问题；改按选项标题精确匹配后通过 —— 与产品无关。
- `renderers` 计数等 `/api/health` 字段随环境变化（13→19），不影响状态断言。
