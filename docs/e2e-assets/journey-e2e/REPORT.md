# API 旅程门禁（journey-e2e）运行报告 —— Q1 质量保障改造

> 背景与门禁定位见 `docs/qa-postmortem-20261123.md`。本报告记录门禁脚本在**同一个缺陷的修复前/修复后**两次真实运行：修复前 c 步 404（exit 1），修复合并、二进制重建后六个步骤全过（exit 0）。**红 → 绿**是本门禁有效性的直接证据。

## 1. 交付物

| 文件 | 说明 |
|---|---|
| `scripts/journey_e2e.py` | 门禁脚本（Python 标准库；`uv run --no-project python` 可跑） |
| `docs/e2e-assets/journey-e2e/pre-fix-table.txt` | **修复前**运行的人类可读步骤表（stderr 捕获；c 步 404） |
| `docs/e2e-assets/journey-e2e/pre-fix-result.json` | 修复前的机器可读结果（`--json-out`） |
| `docs/e2e-assets/journey-e2e/pre-fix-stdout.json` | 修复前 `--json` 的 stdout 捕获（与 result.json 字节一致） |
| `docs/e2e-assets/journey-e2e/pre-fix-serve.log.txt` | 修复前的 serve 日志（访问日志可见 `POST /api/sessions -> 404`） |
| `docs/e2e-assets/journey-e2e/post-fix-table.txt` | **修复后**运行的步骤表（六步全 PASS） |
| `docs/e2e-assets/journey-e2e/post-fix-result.json` | 修复后的机器可读结果（含磁盘字段与业务字段） |
| `docs/e2e-assets/journey-e2e/post-fix-stdout.json` | 修复后 `--json` 的 stdout 捕获（与 result.json 字节一致） |
| `docs/e2e-assets/journey-e2e/post-fix-serve.log.txt` | 修复后的 serve 日志（`POST /api/sessions -> 201`，触发器 200） |

> 仓库 `.gitignore` 忽略 `*.log`，因此 serve 日志以 `.log.txt` 入库（内容为脚本 `--save-server-log` 的原样输出）。

## 2. 时间线与两次运行的环境

本 lane 工作期间产品侧发生了修复合并，dist 二进制被重建，因此同一脚本先后测到两个二进制：

| 事件 | 时间（2026-11-23, +08:00） | 证据 |
|---|---|---|
| main HEAD = `a73b45ee`（本 lane 基线） | 01:06 | `git log` |
| 修复前二进制（`dist/hive-embed`，mtime 00:55） | 00:55 | sha256 `cf0db63cd944a27399c05ee98bdab493f097803321d8b0bee58dd4e47bcd8317` |
| **修复前运行**（c 步 404，exit 1） | 01:17:26 | `pre-fix-*` |
| 修复提交 `2e57f1bc`（colony-bootstrap lane，01:16:30） | 01:16:30 | `git show 2e57f1bc` |
| 修复合并入 main `9ffd2c70`，dist 重建（mtime 01:17） | 01:17:34 | sha256 `e79ce6b60f583ce000231477589ef478b7aa3e1f4aa51af6e7a83d383aee7015` |
| **修复后运行**（六步全 PASS，exit 0） | 01:19:11 | `post-fix-*` |

两次运行共同的环境：

| 项 | 值 |
|---|---|
| worktree / 分支 | `/Users/mac/hive/.wt/journey-e2e` @ `journey-e2e`（产品基线 `a73b45ee`；门禁改动见本 lane 提交 `5e79351d`、`761cbe65` 及本次证据提交） |
| 二进制路径 | `/Users/mac/hive/go_hive/go-hive/dist/hive-embed`（既有构建；worktree 内 `dist/` 被 gitignore、无产物，故以 `--binary` 指向主 checkout 的已有构建） |
| 版本 | 两次均为 `hive 1.0.27-go.1`；差异在 sha256（见上表） |
| HIVE_HOME | 临时目录（运行后删除）：`…/cache/scratch/journey-<uuid>`（repo 外） |
| 端口 | 每次随机空闲端口（修复前 `53578`，修复后 `53818`） |
| 模型 | 主槽指向不可达 `http://127.0.0.1:9/v1`；本旅程不发 LLM 请求 |
| 浏览器桥 | `HIVE_BROWSER_BRIDGE=0`（本旅程不测浏览器扩展，避免固定端口与其他 lane 冲突） |

修复后运行的命令（与修复前相同，仅输出文件名不同）：

```bash
cd /Users/mac/hive/.wt/journey-e2e
uv run --no-project python go_hive/scripts/journey_e2e.py \
  --binary /Users/mac/hive/go_hive/go-hive/dist/hive-embed \
  --scratch-root /Users/mac/.hermes/profiles/omh-tui/cache/scratch \
  --save-server-log go_hive/docs/e2e-assets/journey-e2e/post-fix-serve.log.txt \
  --json --json-out go_hive/docs/e2e-assets/journey-e2e/post-fix-result.json \
  > go_hive/docs/e2e-assets/journey-e2e/post-fix-stdout.json \
  2> go_hive/docs/e2e-assets/journey-e2e/post-fix-table.txt
# exit code: 0
```

## 3. 修复前结果（2026-11-23 01:17:26，二进制 `cf0db63c…`，exit 1）

| 步骤 | 结果 | HTTP | 实测 |
|---|---|---|---|
| a 建 queen DM 会话 | PASS | 201 | `session_id=session_20261123_011726_539de298`，`colony_id=null` |
| b 列 queen profiles（UI 前置） | PASS | 200 | 13 个 queen，首个 id `queen_brand_design` |
| **c 建新 colony（弹窗路径）** | **FAIL** | **404** | `{"error":"Colony not found: journey-colony"}`；`session_id` 为空，`colony_id`/`has_worker` 缺失 |
| d 磁盘 + tools-index + sessions | SKIP | — | 依赖 c |
| e SPA 页面（`/`、`/colony/journey-colony`） | PASS | 200 | 均为 `text/html; charset=utf-8` |
| f timer 触发器建/停 | SKIP | — | 依赖 c |
| g 清理 | OK | 200 | DELETE 会话；SIGTERM 停 serve（rc=0）；删除临时 home |

`result: FAIL — failed: c`，`exit code: 1`。与复盘记录一致：**门禁抓住了该缺陷**。`pre-fix-serve.log.txt` 的访问日志逐条对应：`POST /api/sessions -> 201`、`GET /api/queen/profiles -> 200`、`POST /api/sessions -> 404 (17ms)`、`GET / -> 200`、`GET /colony/journey-colony -> 200`、`DELETE … -> 200`。

## 4. 修复后结果（2026-11-23 01:19:11，二进制 `e79ce6b6…`，exit 0）

主 checkout 于 01:17:34 合并 `2e57f1bc`（`merge(colony-bootstrap)` = `9ffd2c70`）并重建 `dist/hive-embed`（mtime 01:17）；同一脚本复跑：

| 步骤 | 结果 | HTTP | 实测 |
|---|---|---|---|
| a 建 queen DM 会话 | PASS | 201 | `session_id=session_20261123_011911_77744a8c`，`colony_id=null` |
| b 列 queen profiles | PASS | 200 | 13 个 queen，首个 id `queen_brand_design` |
| **c 建新 colony** | **PASS** | **201** | `session_id=session_20261123_011911_157d8194`；`colony_id=journey-colony`、`has_worker=true` |
| d 磁盘 + tools-index + sessions | PASS | 200 | `metadata.json={name:"journey-colony", created_at:1795367951.3976939, queen_name:"queen_brand_design"}`；`worker.json={name:"Journey-Colony", description:"Colony journey-colony", tools:[], goal:{…}, queen_name:"queen_brand_design"}`；`/api/colonies/tools-index` 与 `/api/sessions` 均列出该 colony |
| e SPA 页面 | PASS | 200 | `/` 与 `/colony/journey-colony` 均 `text/html; charset=utf-8` |
| f timer 触发器建/停 | PASS | 200 | 建：`trigger_id=user-probe`；停：`{"status":"deactivated","trigger_id":"user-probe"}` |
| g 清理 | OK | 200 | 两个会话 DELETE 200；SIGTERM 停 serve（rc=0）；删除临时 home |

`result: PASS — 6 steps, cleanup ok`，`exit code: 0`。`post-fix-serve.log.txt` 访问日志：`POST /api/sessions -> 201 (35ms)`、`GET /api/queen/profiles -> 200`、`POST /api/sessions -> 201 (68ms)`、`POST …/triggers -> 200 (17ms)`、`POST …/triggers/user-probe/deactivate -> 200 (7ms)`。

结论：Q1 门禁在真实缺陷上完成 **红 → 绿**；修复后的磁盘字段与上游 `_ensure_minimal_colony` 的字段一致（`metadata.json` 的 `name/created_at/queen_name`，`worker.json` 的 `name/description/tools/goal/queen_name`）。

## 5. 复跑方法

**修复后（当前 main，期望 exit 0）：**

```bash
make -C go_hive/go-hive embed-ui            # 产物 go_hive/go-hive/dist/hive-embed
uv run --no-project python go_hive/scripts/journey_e2e.py \
  --json --json-out /tmp/journey.json      # 默认找 <repo>/go_hive/go-hive/dist/hive-embed
echo $?                                     # 期望 0
```

**复现修复前（红）**（旧二进制未保留；需在修复前代码上重建）：

```bash
git -C /Users/mac/hive worktree add /tmp/hive-prefix a73b45ee
make -C /tmp/hive-prefix/go_hive/go-hive embed-ui
uv run --no-project python go_hive/scripts/journey_e2e.py \
  --binary /tmp/hive-prefix/go_hive/go-hive/dist/hive-embed
echo $?                                     # 期望 1（c 步 404）
```

## 6. 门禁脚本要点

- 6 个评分步骤（a–f）+ 清理（g）：每步断言 HTTP 状态和 UI 会读取的业务字段；任一步失败即非 0。
- 退出码：`0` 全过；`1` 旅程失败；`2` 环境错误（缺二进制、serve 起不来等）。缺二进制时明确提示先 `make build && make embed-ui`。
- 临时 HIVE_HOME 在 repo 外（`--scratch-root` / `JOURNEY_E2E_SCRATCH` / `$TMPDIR`），内含 `secrets/credential_key` 与最小 `configuration.json`；`--keep` 可保留现场。
- 模型槽指向不可达的本机端口：**本旅程不发 LLM 请求**；若误发会以请求失败暴露，而不是被真实 provider 掩盖。
- 启动就绪用轮询等待 `GET /api/health` 返回 200（macOS 无 `timeout(1)`）；端口随机，`address already in use` 时自动换端口重试（最多 3 次），仍失败按环境错误退出。
- `--json` 时 JSON 走 stdout、人类表走 stderr；`--json-out` 另存一份；`--save-server-log` 在删除临时 home 前拷贝 serve 日志。
- 结果 JSON 含：`schema/ok/exit_code/duration_ms/environment`（二进制 sha256、版本、commit、端口等）、`steps[]`（id/title/expect/status/http_status/duration_ms/actual/checks/fields/request/response）、`cleanup[]`、`failed[]`、`notes[]`。

## 7. 观察（非本次验收项）

- 修复前的 404 分支会在 `<HIVE_HOME>/queens/<queen>/sessions/<session_id>/` 留下已写入 `meta.json` 的会话目录（修复前 serve 日志里可见第二个 `session_...` 流）：会话身份在 colony 绑定失败之前就已落盘。修复后该目录属于正常创建，不再有孤儿；本次门禁不对该失败路径单独断言。
- `trigger_id` 由 name slug 化（实测 `user-probe`）；脚本按响应返回的 `trigger_id` 停用，不断言具体拼写。

## 8. 门禁自检与改动范围

- `bash go_hive/scripts/merge_postcheck.sh` → **exit 0**（gofmt clean；wiring_check 21 个接线文件、68 项全部命中；7 个生成物与 `model.yaml` + `go list` 一致）。
- 本次仅新增 `go_hive/scripts/journey_e2e.py` 与 `go_hive/docs/e2e-assets/journey-e2e/**`；未改产品代码、Makefile、go.mod、contracts。按任务要求未跑 `make ci`。