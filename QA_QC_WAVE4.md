# Wave4 QA/QC 独立审计报告

- 审计时间：2026-11-19 00:18 – 00:26 (CST)
- 审计对象：`/Users/mac/hive` 主树，HEAD 从 `64a0def2` 漂移到 `b43e8317`（审计窗口内 main 前进 **8 个提交**，含 `merge w4/t1`）
- 约束：只读。未修改任何代码、未提交、未动 git 历史与 worktree；仅新增本报告文件。§6 的实测命令跑在 `/tmp` 与隔离 HOME 下。
- 副作用说明：运行 `qa_audit.py` 会覆写受版本控制的 `go_hive/QA_REPORT.md`，已在审计结束后 `git checkout --` 还原；`TIME_EVENTS.csv` 的新增行来自同时在跑的 timegov 采集进程，非本次审计写入。
- 说明：任务书称「5 个新 worktree（w4-*）」，实际为 **9 个**；下文按实际数量审计。

## 0. 结论

**当前产物不能对外发布**：`dist/` 里的二进制在干净主机上找不到前端（`GET /` 返回 404），而现有 QA/QC 门禁在结构上无法发现这一点；作为内网候选版可以继续推进。

---

## 1. 两个审计脚本的运行结果

### 1.1 `qa_audit.py`（未加参数）

```
| 分支 | 提交 | 变更文件 | 新增测试 | parity 注解行 | 触碰共享文件 | 结论 |
| `fix/integration` | 0 | 0 | 0 | 0 | - | OK |
| `lane/a` ... `wave3/parityfix` | 0 | 0 | 0 | 0 | - | OK |
- 分支 29 个；共享文件违规 0 个。
```

29 个分支全部是「0 提交 / 0 变更文件 / OK」。这不是合规证据，是脚本失效（见 §2 第 1–3 条）：它用 `git log main..{branch}` 取差集，分支一旦合进 main，差集恒为空。反证：

```
$ git show --stat 3774b0c0 | grep app.go
 go_hive/go-hive/internal/server/app.go | 170 +++++++++++++++++++-
```

`merge wave3/int` 确实改了共享文件 `app.go`（+170 行），报告里该分支仍记「0 文件、无违规」。**「违规 0 个」不可证伪，不能当作放行依据。**

### 1.2 `qc_check.py`（未加 `--full` / `--smoke`）

```
| 检验 | 结果 | 证据 |
| C2 parity 引用 | PASS | check_parity: OK — 2755 个测试全部带有 parity 注解或豁免说明。 |
| C4 六平台产物 | PASS | 7 个产物；version=hive 1.0.14-go.1 |
| C5 无 cgo | PASS | import "C" 文件 0 个 |
| C6 文档口径 | PASS | 残留 811 口径：无 |
| C7 测试回归 | PASS | 当前 2755，采样峰值 2755 |
**5/5 PASS**
```

5 项全绿，但其中 3 项是自证或弱检验（§2 第 6–9 条），且**没有任何一项检查二进制能否在交付机器上跑起来**。

---

## 2. QA 违规清单（文件:行）

| # | 位置 | 问题 | 级别 |
| --- | --- | --- | --- |
| 1 | `go_hive/scripts/qa_audit.py:22` | `git log --format=%H "main..{b}"` 取不到已合并分支的任何提交；29/29 分支恒为 0 | P0（门禁失效） |
| 2 | `go_hive/scripts/qa_audit.py:38` | `if t and a < t`：新增测试为 0 时短路，WARN 分支永不可达 | P0 |
| 3 | `go_hive/scripts/qa_audit.py:41` | 共享文件违规依赖 `changed` 列表，恒空 → 违规永远为 0 | P0 |
| 4 | `go_hive/scripts/check_parity_targets.sh:97` | `grep ... --include='*_test.go'`：只校验测试文件。生产代码里 **772 条** parity 注解完全在门禁之外 | P1（取证缺口） |
| 5 | `go_hive/go-hive/internal/server/session.go:313` | `// Parity: framework/server/queen_orchestrator.py::_make_colony_pivot_sink` —— 该符号在 `core/` 全库不存在（`grep -rn _make_colony_pivot_sink core/` 无命中） | P1（悬空注解） |
| 6 | `go_hive/scripts/qc_check.py:33` | `annotated = re.search(...)` 算出后从未使用；C2 实际只判退出码 | P2（死代码误导） |
| 7 | `go_hive/scripts/qc_check.py:62-68` | C7 以 `TIME_EVENTS.csv` 为基线，该文件由同一批 agent 写入（仅 2 行，`tests` 恒 2755），等于自证 | P2 |
| 8 | `go_hive/scripts/smoke.sh:16` | `cd "$MOD"` 后启动二进制，`frontend/dist` 相对路径必然命中，掩盖「裸机无前端」缺陷 | P0（假绿） |
| 9 | `go_hive/scripts/qc_check.py:42-44` | C4 只数产物个数（≥6），不校验产物与 HEAD 的对应关系（无 commit stamp / 哈希） | P2 |
| 10 | `go_hive/DELIVERY.md:59` | 「2,436 条 parity 引用全量校验 100% 命中」与实测不符：实际 3,905 条引用，772 条生产注解中 1 条悬空、275 条无可校验符号 | P2（口径夸大） |
| 11 | 主树 `main`（00:21:29→00:26:36 连续 8 次提交、9 个不同 HEAD） | 审计期间无冻结点，HEAD 持续漂移（`64a0def2`→…→`b43e8317`，含 `merge w4/t1`），结果不可复现到单一 SHA | P1（过程） |

补充（不计违规，避免误伤）：
- `internal/server/judge.go:20`、`internal/maintenance/retention.go:23/41` 分别指向 `PROTOCOLS`、`KEEP_ALWAYS`、`KNOWN_ENTRIES`，三者**均真实存在**于上游（带类型注解的定义）。
- `internal/credentials/adapter.go:3`、`internal/attachments/attachments.go:3` 指向 `aden_tools`，文件在 `tools/src/...` 下真实存在，仅 `check_parity_targets.sh` 的搜索根未覆盖。
- 逐分支核对每个 merge 的第二父提交：**除 integrator 分支 `wave3/int` 外，无任何 lane 改动共享文件**（`app.go` / `go.mod` / `Makefile` / `registry.go`）。规则事实上被遵守，只是门禁证明不了。

---

## 3. QC PASS/FAIL 表

| 检验 | 结果 | 证据 |
| --- | --- | --- |
| C1 `make ci` | **NOT RUN** | 按要求不加 `--full`（约 2–3 分钟），本轮未执行 |
| C2 parity 注解覆盖率 | PASS | `check_parity.sh`：2755/2755 测试均有注解或豁免 |
| C2′ parity 目标可解析（本次新增） | **FAIL** | 772 条生产注解中 495 条符号命中，1 条悬空（`session.go:313`），275 条为文件级/说明性、无可校验符号 |
| C3 运行时冒烟 | **NOT RUN** | 按要求不加 `--smoke`；改用 §4 三项定点实测替代 |
| C4 六平台产物 | PASS（有条件） | 7 个产物，`hive 1.0.14-go.1`；`find -newer dist/hive -name '*.go'` = 0，`git diff 76e0de28..HEAD -- go_hive/go-hive` 为空 → **产物与当前源码一致**，无陈旧问题 |
| C5 无 cgo | PASS | `import "C"` 文件 0 个 |
| C6 文档口径 | PASS | 五份主文档无「811 action」残留 |
| C7 测试回归 | PASS（弱） | 当前 2755 = 台账峰值；基线自证，见违规 #7 |
| C8 编译 | PASS | `go build ./...` exit 0 |
| C9 静态检查 | PASS | `go vet ./...` exit 0 |
| C10 **前端可交付性** | **FAIL** | 见 §4.2：裸机 `GET /` = 404 |

---

## 4. 三个高风险点抽查

### 4.1 parity 注解是否指向真实上游 —— 基本成立，1 处悬空

独立重跑（不复用 `check_parity_targets.sh` 的结论），遍历全部 3,572 个带注解的 Go 文件、3,905 条引用：

```
prod annotation lines: 772
  file_level     63
  prose          212
  resolved_sym   495
  missing_file   2      # 均指向 tools/src/aden_tools，实际存在
  missing_sym    1      # internal/server/session.go:313
```

**结论：`// Parity:` 不是批量造出来的**，495 条能逐一命中上游 `def`/`class`/常量；唯一硬伤是 `session.go:313` 的 `_make_colony_pivot_sink`，上游根本不存在该函数。因为它落在生产代码而非 `*_test.go`，`check_parity_targets.sh` 永远扫不到。

### 4.2 frontend dist 是否真被服务 —— 在构建机上「是」，交付后「否」

代码路径：`internal/server/app.go:449 FrontendDistDir()` → 候选目录 `frontend/dist`（相对 CWD）与 `packageSourceDir()` 推导的源码路径（`app.go:481` 用 `runtime.Caller(0)`）；`app.go:196` 只有在 `distDir != ""` 时才注册 `GET /`。

实测 A（隔离 HOME、空 CWD、无 API key，端口 8877）：

```
GET /  → status=200 size=450 type=text/html
```

450 字节正是 `frontend/dist/index.html` 的大小。能返回，是因为二进制里的 `runtime.Caller` 指向 **构建机源码路径** `/Users/mac/hive/go_hive/go-hive/internal/server`，`../../frontend/dist` 恰好存在。

实测 B（同样的二进制，仅剥夺对仓库路径的读取）：

```
$ sandbox-exec -p '(version 1)(deny file-read* (subpath "/Users/mac/hive"))(allow default)' ./bin/hive serve --port 8878
GET /  → status=404 size=19
```

物证：`frontend/dist` 被 `.gitignore:6` 忽略，`git ls-files frontend/dist` = 0；二进制内 `strings | grep 'assets/index-'` = 0，即**没有 `go:embed`**。`DELIVERY.md:55` 也把「前端 `go:embed`」列为延期项。

**结论：交付给任何一台没有本仓库源码的机器，仪表盘 404。** `smoke.sh` 因为 `cd "$MOD"` 启动，永远测不到这个场景。

### 4.3 无 key 时 chat 是否真 503 —— 是

```
POST /api/sessions            → 201  session_20261119_002318_239478d4
POST /api/sessions/{id}/chat  → 503  {"error":"No LLM configured for this session"}
```

代码对应 `internal/cli/loader.go:556-564`（无 key 直接返回错误）→ `loader.go:452` 保持 `record.Chat = nil` → `chat_routes.go:115` 返回 503。**行为与声明一致，无伪造投递。**

---

## 5. w4-* worktree 状态（快照 00:26:15）

| worktree | 分支 | 领先 main | 未提交 | 创建时刻 | 判定 |
| --- | --- | ---: | ---: | --- | --- |
| `.wt/w4-t1` | `w4/t1` | 3 | 0 | 00:18 | 正常推进 |
| `.wt/w4-embed` | `w4/embed` | 0 | 6 | 00:21 | 在改前端 embed（`uiembed/`、`embed_ui.sh`），尚未提交 |
| `.wt/w4-img` | `w4/img` | 0 | 2 | 00:18 | 有改动未提交 |
| `.wt/w4-t2` | `w4/t2` | 0 | 3 | 00:18 | 有改动未提交 |
| `.wt/w4-qmail` | `w4/qmail` | 0 | 1 | 00:18 | 有改动未提交 |
| `.wt/w4-send` | `w4/send` | 0 | 1 | 00:18 | 有改动未提交 |
| `.wt/w4-x1` | `w4/x1` | 0 | 1 | 00:18 | 有改动未提交 |
| `.wt/w4-pdf` | `w4/pdf` | 0 | 0 | 00:18 | 无产出 |
| `.wt/w4-spec` | `w4/spec` | 0 | 0 | 00:18 | 无产出 |

**关于「卡住超过 30 分钟」**：9 个 w4 lane 创建于 00:18–00:21，审计时最长仅 8 分钟，**没有任何一个达到 30 分钟阈值**，故不列入卡住名单。其中 7 个已在写文件、`w4-t1` 已提交 3 次并在 00:26:36 被 `b43e8317 merge w4/t1` 合入 main，说明派工正在生效。

真正超期的是另一组：`.wt/agentloop`、`.wt/docs`、`.wt/eventbus`、`.wt/loopcore`、`.wt/parity`、`.wt/skills-installer`、`.wt/storage`、`.wt/tasks`、`.wt/tools`、`.wt/triggers` —— **10 个 `gohive/*` worktree 最后提交距今 11 天**，各自带一个未跟踪的 `go_hive/` 目录。`ALERTS.md` 里 00:16 / 00:20 两条 `[STALL] 10 个 worktree 有未提交改动` 指的正是它们，但看门狗把「11 天未提交」报成通用的「可能卡住」，没有暴露超期天数。

---

## 6. 复现命令

```bash
cd /Users/mac/hive
uv run python go_hive/scripts/qa_audit.py
uv run python go_hive/scripts/qc_check.py

# 4.2 的两个实测
mkdir -p /tmp/qa4 && cp go_hive/go-hive/dist/hive /tmp/qa4/
cd /tmp/qa4 && env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY -u HIVE_API_KEY \
  HOME=/tmp/qa4/home ./hive serve --port 8877 &
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8877/          # 200（借构建机源码路径）
sandbox-exec -p '(version 1)(deny file-read* (subpath "/Users/mac/hive"))(allow default)' \
  ./hive serve --port 8878 &
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8878/          # 404（等价于交付机器）

# 4.1 的独立复核
grep -rn "_make_colony_pivot_sink" core/ --include='*.py'                 # 无命中
```
