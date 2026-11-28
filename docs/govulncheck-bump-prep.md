# govulncheck stdlib CVE：go.mod 升版记录（已批准 · 已落地）

> 本页是 `pending-decisions.md` §D1 **选项 A**（升 `go.mod` 到 go1.26.11）的**已批准落地记录**。
> 改动已合并进 `main` 并通过全量复验；本页保留批准时的工作树证据与操作记录。
> 项目纪律：`go.mod` 属依赖变更，必须显式批准后才能进 `main`。

- 基线：`main` `f0a1899e`；工作树 `.wt/prep-govuln`，分支 `prep-govuln`
- 改动面：**1 个文件 1 行**（`go_hive/go-hive/go.mod`），`go.sum` 未动，`Makefile` 未动
- 工具链：vfox 安装的 `go1.26.11`，隔离路径，**不改本机全局默认**（全局仍 1.26.5）
- 证据：本页内联了每条命令与退出码；报告目录在会话 scratch（不随仓库提交）

---

## 1. 结论

| 项 | 结论 | 关键证据 |
| --- | --- | --- |
| 本机能否装到 ≥ 1.26.6 | **能**：vfox 有 1.26.6 / 1.26.7 / 1.26.8 / 1.26.9 / 1.26.10 / 1.26.11（及 1.27.x） | §3.1；`vfox search golang` |
| 隔离安装 | **已装** `~/.vfox/cache/golang/v-1.26.11/golang-1.26.11`，`VERSION=go1.26.11` | §3.2；全局 `vfox current golang` 仍为 1.26.5 |
| `make ci` | **PASS**，`rc=0`（9 道门禁全绿） | §5.1 |
| QC `7/11` | **失败项与本次升版无关**（缺 dist 产物 + `uv run` 污染的 `python3`），已用基线同条件复现 | §5.2 |
| govulncheck | **FAIL（23 OSV / 1423 events）→ PASS（0 / 0）** | §6 |
| `go.sum` / 其它依赖 | **未变**（`git diff` 只有 go.mod 一行） | §4 |

---

## 2. 现状（before）

`main` `f0a1899e` 上：

```text
$ grep -E '^go ' go_hive/go-hive/go.mod
go 1.26.5

$ go version
go version go1.26.5 darwin/arm64      # ~/.vfox/sdks/golang/bin/go

$ bash go_hive/scripts/vulncheck.sh --report <dir> --no-install
vulncheck: FAIL: reachable vulnerabilities found
```

基线扫描（govulncheck `v1.10.0`，DB `2026-11-11T02:51:11Z`，source/symbol）：

| 模块 | go_version | unique OSV | finding events |
| --- | --- | --- | --- |
| `go-hive` | go1.26.5 | 23 | 815 |
| `browserbridge` | go1.26.5 | 23 | 608 |
| 合计（去重） | — | **23** | **1423** |

23 条**全部是 stdlib**（net/http、crypto/tls、crypto/x509、encoding/asn1、encoding/xml、
html/template、net/url、net/textproto 等），没有任何第三方依赖命中；修复版本分布：

| 修复于 | 条数 | OSV |
| --- | --- | --- |
| v1.26.6 | 8 | GO-2026-5026, 5942, 5972, 6088, 6089, 6090, 6091, 6218 |
| v1.26.9 | 13 | GO-2026-6599, 6600, 6603, 6604, 6605, 6607, 6608, 6609, 6610, 6611, 6612, 6613, 6617 |
| v1.26.10 | 1 | GO-2026-6809（crypto/x509 不再信任被攻破的中间 CA） |
| v1.26.11 | 1 | GO-2026-6859（net/http `MaxBytesReader` 头竞争导致崩溃） |

**为什么选 1.26.11 而不是最小修复 1.26.6**：`GO-2026-6859` 只在 v1.26.11 修好，
`GO-2026-6809` 在 v1.26.10。只升到 1.26.6 仍剩 15 条，门禁仍红。

---

## 3. 工具链：可用性、安装与隔离

### 3.1 渠道核查

| 渠道 | 结果 |
| --- | --- |
| `vfox search golang` | 可用：`1.26.6` … `1.26.11`、`1.27.0` … `1.27.4`（本机已装 1.26.5） |
| `brew list --versions go` | 未安装 go 包（本次不采用 brew 路径） |
| 网络 | `go.dev` 200、`proxy.golang.org` 200、`vuln.go.dev` 200 |

结论：**无需阻塞，vfox 可直接装到 1.26.11**。

### 3.2 安装（隔离，不动全局）

```text
$ vfox install golang@1.26.11
$ vfox list golang
-> 1.26.5 <— current        # 全局默认未变
-> 1.26.11

$ cat ~/.vfox/cache/golang/v-1.26.11/golang-1.26.11/VERSION
go1.26.11
time 2026-11-02T20:36:21Z

$ readlink ~/.vfox/sdks/golang
/Users/mac/.vfox/cache/golang/v-1.26.5/golang-1.26.5   # 仍指向 1.26.5
```

**隔离 Go 根**：`/Users/mac/.vfox/cache/golang/v-1.26.11/golang-1.26.11`
（`bin/go` = `go version go1.26.11 darwin/arm64`）

### 3.3 使用环境

仓库门禁要求离线（`GOPROXY=off`），所以复用已填充的 module cache，只把工具链换成 1.26.11：

```bash
NEWROOT="$HOME/.vfox/cache/golang/v-1.26.11/golang-1.26.11"
export GOROOT="$NEWROOT"                 # 必须显式设，见 §3.4
export PATH="$NEWROOT/bin:$PATH"
export GOPATH="$HOME/.hermes/profiles/omh-tui/cache/scratch/gopath-go1126"      # 隔离 bin
export GOMODCACHE="$HOME/.vfox/cache/golang/v-1.26.5/golang-1.26.5/packages/pkg/mod"  # 复用缓存
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local
```

验证（每次换 shell 必查这一行）：

```text
$ command -v go; go version; go env GOROOT
/Users/mac/.vfox/cache/golang/v-1.26.11/golang-1.26.11/bin/go
go version go1.26.11 darwin/arm64
/Users/mac/.vfox/cache/golang/v-1.26.11/golang-1.26.11
```

### 3.4 两个会伪造结果的陷阱（本次实测踩到）

1. **继承的 `GOROOT` 会让 1.26.11 的编译器配 1.26.5 的标准库**。本机 shell 已导出
   `GOROOT=/Users/mac/.vfox/sdks/golang`（指向 1.26.5 树）。此时直接跑 1.26.11 的二进制：

   ```text
   $ GOROOT=/Users/mac/.vfox/sdks/golang <1.26.11>/bin/go env GOROOT GOVERSION
   /Users/mac/.vfox/sdks/golang           # ← 仍旧 1.26.5 的树
   go1.26.11
   ```

   版本号看着是 1.26.11，实际用的是 1.26.5 的 stdlib 源码——正好就是要修的那批 CVE。
   **结论：用哪个工具链，就必须把 `GOROOT` 指到它自己的根。**

2. **`source env 文件` 不一定在同一 shell 生效**。第一次复验时 `source` 没落地，
   日志里 `TOOLCHAIN:` 行仍是 `go1.26.5 / GOROOT=~/.vfox/sdks/golang`，`make vet` 立刻失败：

   ```text
   go: go.mod requires go >= 1.26.11 (running go 1.26.5; GOTOOLCHAIN=local)
   make: *** [vet] Error 1        # rc=2
   ```

   这条失败同时**证明了 `go` 指令是硬下限**：升版后 1.26.5 的 go 会直接拒绝构建，不会静默降级。
   修法：用显式 `bash` 包装脚本（或直接内联 export），跑门禁前先打印
   `command -v go; go version; go env GOROOT`。

---

## 4. 变更内容

```diff
--- a/go_hive/go-hive/go.mod
+++ b/go_hive/go-hive/go.mod
 module github.com/aden-hive/hive-go
 
-go 1.26.5
+go 1.26.11
```

```text
$ git diff --stat
 go_hive/go-hive/go.mod | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
```

- 未加 `toolchain` 行：`go 1.26.11` 本身就表达了最低工具链要求，多一行只会增加维护面。
- `go.sum` 无变化：本变更不引入、不升级任何依赖。
- `browserbridge/go.mod` 保持 `go 1.22` **未动**——见 §6.3（升版后它同样扫干净）。

---

## 5. 复验（工具链 go1.26.11 + go.mod 1.26.11）

### 5.1 `make ci` — PASS

```text
TOOLCHAIN: .../v-1.26.11/golang-1.26.11/bin/go | go version go1.26.11 darwin/arm64
GO_MOD_DIRECTIVE: go 1.26.11
gofmt: OK
go vet ./...                                          # 0 error
go test -count=1 ./...                                # 5709 tests, all ok
== darwin/amd64 == ... == windows/arm64 ==            # cross 6 平台全过
check_parity: OK — 5709 个测试全部带有 parity 注解或豁免说明
test parity refs: 4496 checked / prod 1695 checked；parity refs checked: 6191, missing: 0
check_parity_targets: OK
wiring_check: OK —— 20 个接线文件、67 项要求全部命中
compat-check: pass=12 fail=0 skip=0 verdict=PASS
一致：7 个生成物与 model.yaml + go list 完全一致（commit 7b77d047e82e）
POST_CI_RC=0
```

同一 worktree 的**基线**（go.mod 1.26.5 + go1.26.5）`make ci` 也是绿（9 道门禁跑到 arch-check），
所以本次升版**没有引入门禁回归**。

### 5.2 QC `--full --smoke` — 7/11，4 项失败均与升版无关

| 检验 | 结果 | 证据 |
| --- | --- | --- |
| C2 parity 引用 | PASS | annotation_exit=0；5709 个测试；refs 0 missing |
| C4 六平台产物 | PASS | 6 个产物；version=hive 1.0.26-go.1 |
| **C4b UI 可分发性** | **FAIL** | 磁盘 dist=无；embed 脚本=有；hive-embed=未构建 |
| **C4c 归档含四个二进制** | **FAIL** | 未找到归档：`…/go-hive/dist/release` |
| C5 无 cgo | PASS | `import "C"` 文件 0 个 |
| C6 文档口径 | PASS | 残留 811 口径：无 |
| C7 测试回归 | PASS | 当前 5709，台账峰值 4160 |
| C8 表面审计（强制） | PASS | 前端缺失 0/Python 缺失 0/卫生 0；Go 多余 11（仅提示） |
| C9 证据审计锚点 | PASS | 8/8 anchors PASS (5 sites) |
| **C1 make ci** | **FAIL** | `make: *** [test] Error 1` |
| **C3 运行时冒烟** | **FAIL** | `SMOKE: 2 FAILED` |

逐项归因（都有复现证据，不是猜测）：

- **C4b / C4c**：全新工作树里 `frontend/dist` 与 `dist/release` 从未构建过。
  `main` 上已提交的 `QC_REPORT.md` 是 9/9 PASS（磁盘 dist=有、6 个归档齐），说明这两项只在
  "没有产物的干净检出"里红。
- **C3**：`smoke.sh` 的 13 个探针只失败 2 个，且正是依赖前端产物的两个：

  ```text
  PASS  health 200 ... PASS  events SSE 200
  FAIL  dashboard /        got 404 want 200
  FAIL  spa deep route     got 404 want 200
  SMOKE: 2 FAILED
  ```

  同一个原因：没有 `frontend/dist`，`/` 与 SPA 深路由没有可服务资源。
- **C1**：qc 里 `make ci` 在 `test` 挂掉，但同一 worktree、同一工具链、干净 PATH 下
  `make ci` 单独跑 **rc=0**（两次），`make test` 也 **rc=0**。差异来自 `uv run`：

  ```text
  # qc 进程内环境（uv run 注入）
  VIRTUAL_ENV= /Users/mac/hive/.venv
  PATH0= ['/Users/mac/hive/.venv/bin', ...]
  RC= 2
  --- FAIL: TestStorageStartupMigrationsV2Home (0.07s)
      storage_startup_test.go:306: post-migration files differ from storage-compat reference
          missing: [colonies/alpha/colony.db-shm colonies/alpha/colony.db-wal]
  ```

  该测试用 PATH 上的 `python3` 生成 v2 夹具
  （`internal/server/storage_startup_test.go:49` → `scripts/storage_compat_gen_fixture.py`），
  并断言文件清单与录制基准一致。而 PATH 被 venv 前置后：

  | python3 | 来源 | sqlite3 |
  | --- | --- | --- |
  | `/usr/bin/python3`（默认） | Xcode | 3.51.0 |
  | `/Users/mac/hive/.venv/bin/python3`（uv 注入） | 主树 venv | **3.53.1** |

  不同 SQLite 对 WAL 边车文件（`-wal`/`-shm`）的落盘行为不同，夹具清单随之变化 → 断言失败。

  **与升版无关的证明**（三重）：
  1. 同工具链（1.26.11）下，干净 PATH **20/20 全过**；只要 PATH 前置 venv 就稳定失败。
  2. 恢复**基线**（go.mod `1.26.5` + `go1.26.5`）在同一 venv 条件下跑同一个用例：
     `BASELINE_1_26_5_GO_TEST_RC=1`，报错文字**完全一致**。
  3. 该用例根本不做 Go 版本相关逻辑，只比对 python 生成的夹具清单。

  即：这是**既有的环境敏感测试**（`uv run` 会把主树 `.venv` 带进 PATH），本次未修，也不该由本 lane 修。

**等效结论**：在有 `frontend/dist` + `dist/release` 的环境（如主树）里，把 PATH 清干净后
QC 应回到 9/9 的同构状态；本 lane 的 4 项红与 `go 1.26.11` 无因果关系。

### 5.3 `vulncheck` — PASS

```text
vulncheck: using Go: go1.26.11 at ~/.cache/hive-vulncheck/bin/govulncheck
vulncheck: scanning go-hive      → no vulnerabilities found; scanner=v1.10.0 2026-11-11T02:51:11Z
vulncheck: scanning browserbridge → no vulnerabilities found; scanner=v1.10.0
# Dependency vulnerability scan
- verdict: **PASS**
- unique OSV ids: 0; finding events: 0
- browserbridge: 0 unique / 0 events, 2 SBOM module(s),  go=go1.26.11
- go-hive:       0 unique / 0 events, 20 SBOM module(s), go=go1.26.11
VULN_RC=0
```

---

## 6. 前后对比

### 6.1 总账

| 指标 | before（1.26.5） | after（1.26.11） |
| --- | --- | --- |
| verdict | **FAIL** | **PASS** |
| unique OSV | 23 | **0** |
| finding events | 1423（815 + 608） | **0** |
| `config.go_version` | go1.26.5 | go1.26.11 |
| 第三方依赖命中 | 0（全是 stdlib） | 0 |
| scanner / DB | v1.10.0 / 2026-11-11 | 同（同一 DB，排除"DB 变新"的解释） |

**同一份 vuln DB、同一个 scanner** 下从 23 条降到 0 条，说明差异来自 stdlib 版本本身，
不是数据库更新。

### 6.2 逐条消化

23 条按修复版本全部落在 1.26.6 / 1.26.9 / 1.26.10 / 1.26.11 四个 patch 里（见 §2 表）。
升到 1.26.11 后 23 条**全部消失**：`0 unique / 0 events`。

### 6.3 一个反直觉但重要的发现：`browserbridge` 的 `go 1.22` 不用改

`go_hive/browserbridge/go.mod` 仍是 `go 1.22`，但升版后它同样扫出 `0 unique`。
原因是 govulncheck 判定 stdlib 版本看的是**执行扫描的工具链**（报告里 `config.go_version`
= go1.26.11），不是模块的 `go` 指令。

推论（合并时要注意）：**门禁绿的前提是"用 ≥1.26.11 的 go 跑扫描"**，而不只是"go.mod 写了
1.26.11"。在本仓库这条被 `ci.yml` 自动满足——见 §7。

（把 `browserbridge` 从 `go 1.22` 抬到 1.26 是另一件事：那会把模块的语言下限一次抬 4 个小版本，
影响面远大于本 lane，建议单独立项，不在本次批准范围。）

---

## 7. 合并步骤与回滚

### 7.1 合并（批准后）

本分支两个提交：

| # | 提交 | 内容 |
| --- | --- | --- |
| 1 | `chore(deps): go-hive 升 go 1.26.11（stdlib CVE 修复，D1）` | `go_hive/go-hive/go.mod` 一行 |
| 2 | `docs(govulncheck): D1 升版预实现报告` | 本页 |

```bash
git checkout main
git merge --no-ff prep-govuln          # 或 cherry-pick 上面两个提交
```

合并后不需要改 CI：`.github/workflows/ci.yml:55` 与 `release.yml:209` 都是
`go-version-file: go_hive/go-hive/go.mod`，**会自动装 1.26.11**。

合并后建议再跑一遍（带产物的环境）：

```bash
cd go_hive/go-hive && make ci
bash ../scripts/vulncheck.sh          # 期望 verdict PASS / rc=0
uv run --no-project python ../scripts/qc_check.py --full --smoke
```

### 7.2 回滚（一条 revert）

```bash
git revert <提交1的sha>               # 只回滚 go.mod 那一行，回到 go 1.26.5
```

- 提交 2 只是文档，保留即可；如需彻底回到原状再 `git revert <提交2的sha>`。
- 回滚后行为面与今天完全一致（`go.sum` 从未变化），**无数据面影响**。
- 代价：回滚即恢复那 23 条 stdlib CVE，`vulncheck` 重新转红。

---

## 8. 风险与遗留

| # | 风险 | 评估 / 处置 |
| --- | --- | --- |
| R1 | **工具链下限变硬**：`GOTOOLCHAIN=local` 的机器用 1.26.5 会直接失败 | 实测报错 `go.mod requires go >= 1.26.11 (running go 1.26.5; GOTOOLCHAIN=local)`。CI 走 `go-version-file` 自动满足；开发机需装 1.26.11，或保留默认 `GOTOOLCHAIN=auto`（有网时自动拉取） |
| R2 | **stdlib 补丁带来行为变化** | 均为安全修复，但 `GO-2026-6809`（x509 不再信任被攻破的中间 CA）、`6605/6613`（net/http CONNECT 后的连接失步）、`6604`（Windows `os.Root` 可跟随 junction）可能改变边界行为。发布前建议在真实凭据/TLS 场景抽查一次 |
| R3 | **门禁绿依赖扫描工具链** | 见 §6.3：govulncheck 的 stdlib 版本取自运行它的工具链。CI 已由 `go-version-file` 保证；本地用旧 go 扫仍会报 23 条，别误判成"升版没生效" |
| R4 | `TestStorageStartupMigrationsV2Home` 环境敏感 | 既有问题（§5.2），由 `uv run` 把主树 `.venv` 注入 PATH 触发；本次不修，建议单独修（例如测试内固定 `python3` 或显式传入解释器） |
| R5 | 本 lane **未跑真实 CI** | 只做了本地复验；`cross` 6 平台编译通过，但归档/发布产物未重建（`qc` 的 C4b/C4c 依赖它们） |
| R6 | 未做 `go.mod` 之外的清理 | `browserbridge` 仍是 `go 1.22`（§6.3，单独决策）；`Makefile`/依赖/`go.sum` 一律未动 |

---

## 9. 复现命令

```bash
# 0) 工具链（隔离）
vfox install golang@1.26.11
NEWROOT="$HOME/.vfox/cache/golang/v-1.26.11/golang-1.26.11"
export GOROOT="$NEWROOT" PATH="$NEWROOT/bin:$PATH"
export GOMODCACHE="$HOME/.vfox/cache/golang/v-1.26.5/golang-1.26.5/packages/pkg/mod"
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local
go version && go env GOROOT          # 必须都是 1.26.11

# 1) 门禁
cd go_hive/go-hive && make ci

# 2) 漏洞门禁（before/after 用同一条命令，用不同 --report 目录留证据）
#    不给 --report 时默认落在 go_hive/dist/vulncheck/<utc 时间戳>/
bash ../scripts/vulncheck.sh --no-install

# 3) QC
uv run --no-project python ../scripts/qc_check.py --full --smoke
```

- 依赖已全部在本地 module cache，`GOPROXY=off` 可离线复现（本机实测通过）。
- `govulncheck` 不在 PATH 时脚本会 `go install` 到 `~/.cache/hive-vulncheck/bin`（需网络，
  不改仓库 `go.mod`）；本机已存在 `v1.10.0`，用 `--no-install` 即可。
