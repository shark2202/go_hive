# go-hive 依赖清单与离线可重现证据

本文件回答四个问题：依赖有哪些（模块 / 版本 / 直接或间接）、无外网时能否从零构建并通过测试（命令与退出码）、`go.sum` 是否完整可信、以及本文件**没有**做的那件事——在线漏洞库比对（§5 声明）。

## 0. 记录环境

| 项 | 值 |
| --- | --- |
| 工作树 | `/Users/mac/hive/.wt/w5-dep`（分支 `w5/dep`） |
| 提交 | `49c56b0bc7365d9f2c3dbb63c6af9b047a826192` |
| 模块 | `github.com/shark2202/go_hive/go-hive`，`go 1.26.5` |
| 工具链 | `go version go1.26.5 darwin/arm64` |
| 模块缓存 | `~/.vfox/sdks/golang/packages/pkg/mod`（预置；28 个外部模块均已解包） |
| 外网 | 不可达。`curl https://proxy.golang.org/...` 与 `curl https://sum.golang.org/...` 均 6s 超时（curl exit 28，http=000），且无 `*_proxy` 环境变量 |
| 记录时间 | 2026-11-19 00:44 CST |
| 依赖变更 | 无。`go.mod` / `go.sum` 与 HEAD 逐字节一致（sha256 见 §7） |

## 1. 摘要

- 外部模块 **28 个** = 4 直接 + 8 间接（`go.mod` 中带 `// indirect`）+ 16 仅图内（只参与最小版本选择，不编译进产物）。
- 真正进入编译闭包的 **11 个**；其余 17 个不产生任何代码。测试闭包与编译闭包完全一致，即没有「只为测试引入」的模块。
- `go build ./...` 与 `go test ./...` 在 `GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` + **全新 `GOCACHE`** 下均退出码 0（37 个包 `ok`、0 个 `FAIL`、6 个包无测试文件）。
- `go mod verify` → `all modules verified`（退出码 0）。
- 未做在线漏洞库比对：`govulncheck` 因无外网不可用；本文件对漏洞状态**不作任何断言**（§5）。

## 2. 依赖清单

生成命令（离线、退出码 0）：

```bash
cd go_hive/go-hive
GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go list -m all
# 退出码 0，输出 29 行：本模块 1 行 + 外部模块 28 行
```

分类口径：

- **直接**：`go.mod` 首个 `require` 块中的条目（不带 `// indirect`）。
- **间接**：`go.mod` 第二 `require` 块中带 `// indirect` 的条目——被依赖的传递依赖，但本模块代码不直接 import。
- **仅图内**：出现在 `go list -m all` 的完整模块图中、但不在 `go.mod` 里的模块（通常是依赖的构建工具或测试依赖），只影响版本选择结果，不参与编译。
- **编译闭包**：`go list -deps ./...` 输出中出现该模块的标记；`—` 表示不编译。

| 模块 | 版本 | 类别 | 编译闭包 | 模块自身 `go` 指令 |
| --- | --- | --- | --- | --- |
| `github.com/ebitengine/purego` | `v0.11.3` | 直接 | — | 1.25.0 |
| `github.com/mattn/go-isatty` | `v0.0.24` | 直接 | ✓ | 1.20 |
| `gopkg.in/yaml.v3` | `v3.0.1` | 直接 | ✓ | — |
| `modernc.org/sqlite` | `v1.60.1` | 直接 | ✓ | 1.26.0 |
| `github.com/dustin/go-humanize` | `v1.0.1` | 间接 | ✓ | 1.16 |
| `github.com/google/uuid` | `v1.6.0` | 间接 | ✓ | — |
| `github.com/ncruces/go-strftime` | `v1.0.0` | 间接 | ✓ | 1.17 |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | 间接 | ✓ | 1.12 |
| `golang.org/x/sys` | `v0.48.0` | 间接 | ✓ | 1.26.0 |
| `modernc.org/libc` | `v1.77.1` | 间接 | ✓ | 1.26.0 |
| `modernc.org/mathutil` | `v1.7.1` | 间接 | ✓ | 1.21 |
| `modernc.org/memory` | `v1.12.1` | 间接 | ✓ | 1.23.0 |
| `github.com/google/pprof` | `v0.0.0-20260802141513-ef3492d7dac3` | 仅图内 | — | 1.25.0 |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | 仅图内 | — | 1.18 |
| `golang.org/x/mod` | `v0.41.0` | 仅图内 | — | 1.26.0 |
| `golang.org/x/sync` | `v0.23.0` | 仅图内 | — | 1.26.0 |
| `golang.org/x/tools` | `v0.50.0` | 仅图内 | — | 1.26.0 |
| `gopkg.in/check.v1` | `v0.0.0-20161208181325-20d25e280405` | 仅图内 | — | — |
| `modernc.org/cc/v4` | `v4.29.7` | 仅图内 | — | 1.25 |
| `modernc.org/ccgo/v4` | `v4.36.1` | 仅图内 | — | 1.26.0 |
| `modernc.org/fileutil` | `v1.4.0` | 仅图内 | — | 1.24 |
| `modernc.org/gc/v2` | `v2.6.5` | 仅图内 | — | 1.21 |
| `modernc.org/gc/v3` | `v3.1.5` | 仅图内 | — | 1.23.0 |
| `modernc.org/goabi0` | `v0.2.0` | 仅图内 | — | 1.23.0 |
| `modernc.org/opt` | `v0.2.0` | 仅图内 | — | 1.21 |
| `modernc.org/sortutil` | `v1.2.1` | 仅图内 | — | 1.21 |
| `modernc.org/strutil` | `v1.2.1` | 仅图内 | — | 1.21 |
| `modernc.org/token` | `v1.1.0` | 仅图内 | — | — |

合计：直接 4、间接 8、仅图内 16，共 28 个外部模块（另有本模块 `github.com/shark2202/go_hive/go-hive` 自身）。

`gopkg.in/yaml.v3`、`github.com/google/uuid`、`gopkg.in/check.v1`、`modernc.org/token` 的 `go.mod` 未声明 `go` 指令（上表记为 `—`），属老模块写法，不影响构建。

## 3. 离线构建与测试验证

所有用例均在**全新的空 `GOCACHE`** 下执行，等价于「冷编译」：不复用任何已编译的目标文件，仅复用只读的 module cache。因此成功即说明源码可在离线状态下从零编译，而不是命中旧产物。

| # | 用例 | 关键环境 | 退出码 | 耗时 | 结果 |
| --- | --- | --- | --- | --- | --- |
| A | `go build ./...`（任务指定三个 flag，CGO 默认） | `GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod`，空 `GOCACHE` | 0 | 8.86s | 无输出（成功时 go 不打印） |
| B | `go test ./...`（同上） | 同上 | 0 | 29.80s | 37 `ok` / 0 `FAIL` / 6 `no test files` |
| C | `go build ./...`（加 `CGO_ENABLED=0`，对齐 Makefile 的 `OFFLINE`） | 追加 `CGO_ENABLED=0` | 0 | 8.77s | 无输出 |
| D | `go test ./...`（加 `CGO_ENABLED=0`） | 追加 `CGO_ENABLED=0` | 0 | 31.66s | 与 B 逐包结果相同 |
| E | `go test -tags e2e -c -o /tmp/w5dep_e2e.test ./e2e/` | 同 A | 0 | — | 仅编译 e2e 包（不执行），产出 21.6 MB 测试二进制 |
| F | `go mod verify` | `GOPROXY=off GOSUMDB=off` | 0 | — | `all modules verified` |

A 的完整现场：

```bash
$ C=$(mktemp -d /tmp/w5dep-gocache.XXXXXX)
$ time env GOCACHE="$C" GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./... > /tmp/w5dep_build_exact.log 2>&1
real    0m8.858s
$ echo $?
0
$ wc -c /tmp/w5dep_build_exact.log
0
```

B 的输出摘要（37 个 `ok` + 6 个 `no test files`，0 个 `FAIL`）。逐包耗时取自 D（`CGO_ENABLED=0`）那次运行；B 与 D 在去掉耗时列后逐行一致（已 `diff` 验证），故合并展示：

```text
?   github.com/shark2202/go_hive/go-hive/cmd/hive        [no test files]
?   github.com/shark2202/go_hive/go-hive/cmd/hive-cred   [no test files]
ok  github.com/shark2202/go_hive/go-hive/internal/agentloop       2.324s
ok  github.com/shark2202/go_hive/go-hive/internal/agents          0.478s
ok  github.com/shark2202/go_hive/go-hive/internal/atomicfs        2.262s
ok  github.com/shark2202/go_hive/go-hive/internal/attachments     1.655s
ok  github.com/shark2202/go_hive/go-hive/internal/charts          1.243s
ok  github.com/shark2202/go_hive/go-hive/internal/cli             2.711s
ok  github.com/shark2202/go_hive/go-hive/internal/colony          3.765s
ok  github.com/shark2202/go_hive/go-hive/internal/credentials     3.158s
ok  github.com/shark2202/go_hive/go-hive/internal/eventbus        3.987s
ok  github.com/shark2202/go_hive/go-hive/internal/globaldb        3.710s
ok  github.com/shark2202/go_hive/go-hive/internal/hivehome        3.611s
ok  github.com/shark2202/go_hive/go-hive/internal/host            3.945s
ok  github.com/shark2202/go_hive/go-hive/internal/llm             3.987s
ok  github.com/shark2202/go_hive/go-hive/internal/llmjudge        3.734s
ok  github.com/shark2202/go_hive/go-hive/internal/maintenance     4.645s
ok  github.com/shark2202/go_hive/go-hive/internal/mcpregistry     6.360s
ok  github.com/shark2202/go_hive/go-hive/internal/memory          4.552s
ok  github.com/shark2202/go_hive/go-hive/internal/orchestrator    3.762s
?   github.com/shark2202/go_hive/go-hive/internal/platform        [no test files]
ok  github.com/shark2202/go_hive/go-hive/internal/playbook        4.859s
ok  github.com/shark2202/go_hive/go-hive/internal/qmail           4.559s
ok  github.com/shark2202/go_hive/go-hive/internal/queen           4.247s
ok  github.com/shark2202/go_hive/go-hive/internal/ratelimit       4.254s
ok  github.com/shark2202/go_hive/go-hive/internal/runtimehealth   4.671s
ok  github.com/shark2202/go_hive/go-hive/internal/schemas         4.672s
ok  github.com/shark2202/go_hive/go-hive/internal/senders         4.445s
ok  github.com/shark2202/go_hive/go-hive/internal/sentinel        3.987s
ok  github.com/shark2202/go_hive/go-hive/internal/server          5.487s
?   github.com/shark2202/go_hive/go-hive/internal/server/uiembed  [no test files]
ok  github.com/shark2202/go_hive/go-hive/internal/session         3.950s
ok  github.com/shark2202/go_hive/go-hive/internal/sessionreport   3.900s
ok  github.com/shark2202/go_hive/go-hive/internal/skills          4.633s
?   github.com/shark2202/go_hive/go-hive/internal/soul            [no test files]
ok  github.com/shark2202/go_hive/go-hive/internal/storage         4.677s
ok  github.com/shark2202/go_hive/go-hive/internal/tasks           6.185s
ok  github.com/shark2202/go_hive/go-hive/internal/tools           6.401s
ok  github.com/shark2202/go_hive/go-hive/internal/tools/builtin   7.498s
ok  github.com/shark2202/go_hive/go-hive/internal/tools/integrations 4.461s
ok  github.com/shark2202/go_hive/go-hive/internal/tracker         4.794s
ok  github.com/shark2202/go_hive/go-hive/internal/triggers        4.155s
?   github.com/shark2202/go_hive/go-hive/internal/version         [no test files]
```

边界说明：

- `e2e/` 目录下两个文件带 `//go:build e2e`，**不在** `go test ./...` 范围内。用例 E 只证明其可离线编译，未执行（执行可能依赖真实凭据/外部服务）。
- `GOPROXY=off` 只约束模块解析；为排除测试隐式联网的可能，先做了外网探测（§0：两个域均超时），且 B/D 全程无网络重试或超时日志。
- 未运行 `go vet`、`make ci`、`make cross`（不在本次任务范围）。

## 4. `go.sum` 完整性

| 检查 | 命令 | 退出码 | 结果 |
| --- | --- | --- | --- |
| 内容一致性 | `GOPROXY=off GOSUMDB=off go mod verify` | 0 | `all modules verified` |
| 条目行数 | `wc -l go.sum`（HEAD 版本） | — | 24 行 |

结论与限度：

- `go mod verify` 通过说明：缓存中每个已下载模块的 zip 与其解包内容，均与 `go.sum` 记录的哈希一致——即依赖内容未被替换或篡改。这是**完整性**证据，不是**安全性**证据（§5）。
- HEAD 的 `go.sum` 并非「全图完整」：16 个仅图内模块中，15 个没有 `/go.mod` 哈希行（`gopkg.in/check.v1` 已有）。这不影响构建——`-mod=readonly` 下 `go build ./...`、`go test ./...`、`go list -m all` 均退出码 0（已实测）——但会带来一个副作用，见 §6-F1。

## 5. 声明：未做在线漏洞库比对

原文（可整段引用）：

> 本次交付**未做在线漏洞库比对**：`govulncheck` 因无外网而不可用。本文件及随附验证仅覆盖依赖清单、离线构建/测试可完成性，以及 `go.sum` 的完整性校验；**不包含**对任何模块或 Go 标准库已知漏洞（CVE / GO-xxxx-xxxxx）的查询与可达性分析。因此本文件不得作为「无已知漏洞」的证据。

不可用的事实依据（可复现）：

```bash
$ command -v govulncheck          # 无输出
$ echo $?
1
$ ls "$(go env GOPATH)/bin"       # protoc-gen-go  protoc-gen-go-grpc  wails —— 无 govulncheck
$ grep -c golang.org/x/vuln go.mod go.sum
go.mod:0
go.sum:0
$ GOPROXY=off GOBIN=/tmp/w5dep-bin go install golang.org/x/vuln/cmd/govulncheck@latest
go: golang.org/x/vuln/cmd/govulncheck@latest: module lookup disabled by GOPROXY=off
$ echo $?
1
```

### 原因

1. 本机未安装 `govulncheck`，`golang.org/x/vuln` 也不在模块图中。
2. 安装需要经 proxy 拉取模块：`GOPROXY=off` 直接拒绝；放开 proxy 也不可行——`proxy.golang.org` 与 `sum.golang.org` 均 6 秒超时（curl exit 28）。
3. 漏洞扫描本身需要联网查询漏洞库（`vuln.go.dev`）并下载模块元数据，离线状态下即使二进制已在手也无法完成。

### 影响范围

- 覆盖对象：本模块 28 个外部模块 + Go 标准库（工具链 go1.26.5）+ 本模块自身代码，**全部无漏洞结论**（既未确认存在，也未确认不存在）。
- 风险优先级（恢复联网后按此顺序核对）：
  1. 进入编译闭包的 11 个模块，尤其处理不可信输入的 `modernc.org/sqlite`（SQLite 解析面）、`gopkg.in/yaml.v3`（YAML 解析面）、`golang.org/x/sys` 与 `modernc.org/libc`（系统调用/libc 绑定）。
  2. 仅图内 16 个模块：不产生代码，直接风险低；但仍影响版本选择，且未来若被提升为编译依赖需重新评估。
- 不受影响的部分：`go.sum` 完整性（已由 §4 覆盖）；依赖版本集合（已由 §2 固定并记录）。

### 恢复联网后的建议命令

```bash
# 1) 安装（无外网机器上先在联网机执行，或把 GOBIN 一起归档）
GOBIN="$HOME/go/bin" go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck -version            # 记录实际版本，随后固定为 @vX.Y.Z 重装

# 2) 源码级扫描：本模块代码 + 全部依赖（默认 -mode=source）
cd go_hive/go-hive
govulncheck ./...               # 退出码 3 表示发现漏洞（按版本而定，以 -h 为准）

# 3) 归档 JSON，便于与本次提交比对、纳入 CI 门禁
govulncheck -format=json ./... > govulncheck-$(git rev-parse --short HEAD).json

# 4) 对发布产物做符号级核对（可选；先 make cross 产出 dist/）
govulncheck -mode=binary dist/hive-darwin-arm64

# 5) 顺带查看是否有可用小版本（只读，不改 go.mod；需要 proxy 可达）
GOPROXY=https://proxy.golang.org,direct go list -m -u all
```

## 6. 观察项（均**未**改动依赖）

- **F1 — `-mod=mod` 会补写 `go.sum`。** 在 HEAD 的 `go.sum`（24 行）上执行 `go list -m all`（`GOFLAGS=-mod=mod`）后，`go.sum` 变为 39 行：新增 15 行，全部是仅图内模块的 `/<version>/go.mod` 哈希（`google/pprof`、`hashicorp/golang-lru/v2`、`golang.org/x/{mod,sync,tools}`、`modernc.org/{cc/v4,ccgo/v4,fileutil,gc/v2,gc/v3,goabi0,opt,sortutil,strutil,token}`）。**模块集合与版本均未变化**，仅哈希条目补齐。副作用：Makefile 的 `OFFLINE` 变量用的正是 `-mod=mod`，因此 `make test` / `make build` / `make cross` 在干净检出上会弄脏 `go.sum`。本次已将其还原（§7），是否采纳这 15 行由仓库所有者决定；采纳命令：`GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go list -m all && git diff go.sum`。
- **F2 — `github.com/ebitengine/purego` 是声明未使用。** 全树 `.go` 文件对其零 import（`grep -rn purego --include=*.go` 无结果），也不在编译闭包中。`go mod tidy -diff` 提议把它从 `go.mod` 删除。按仓库规则（依赖变更需批准）**未执行**；若后续要接入 purego，需先落地实际调用点，否则它只是噪音。
- **F3 — `e2e/` 不受 `./...` 覆盖。** 两个文件带 `//go:build e2e`，需显式 `-tags e2e`。离线只验证了编译（用例 E），未执行。
- **F4 — `go mod tidy -diff` 非空**（退出码 1）：既包含 F2 的删除建议，也包含为 15 个仅图内模块补 `h1:` 哈希。属「不整洁」而非「不可构建」——HEAD 状态下构建/测试/校验全部通过。
- **F5 — 主模块 `go` 指令为 1.26.5**，与 `modernc.org/sqlite@v1.60.1`、`golang.org/x/sys@v0.48.0` 要求的 `go 1.26.0` 相容；但工具链下限被拉到 1.26.5。本机 `GOTOOLCHAIN=auto` 且安装的正是 go1.26.5，离线无法下载更高工具链，因此在更低版本工具链的机器或 CI 上构建会直接失败——不要依赖 `GOTOOLCHAIN` 自动下载。

## 7. 未变更证明

```text
# sha256（本次工作结束时，与 HEAD 的 blob 一致）
c313c5434995d02369b86b5bf12feeb1aed90d83487ef9f2ea2a3b3e4b95b9ea  go_hive/go-hive/go.mod
84bd45ac417153d0d59eff190dd09fc6a59e2236e678822b2d5b2f4dcef9e8b8  go_hive/go-hive/go.sum

# git status --porcelain（依赖文件无变更，仅本文件为新增）
?? go_hive/DEPENDENCIES.md
```

未新增、未升级、未降级、未替换、未 vendor 任何依赖；未修改 `go.mod` 与 `go.sum`；未创建 `vendor/` 目录。

## 8. 一键复现

```bash
cd go_hive/go-hive
export GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod

# 清单（29 行）
go list -m all

# 完整性
go mod verify                       # 期望 "all modules verified"；退出码 0

# 冷构建 / 冷测试（空 GOCACHE，验证不依赖旧产物）
C=$(mktemp -d); GOCACHE="$C" go build ./...   # 期望退出码 0
C=$(mktemp -d); GOCACHE="$C" go test ./...    # 期望 37 ok / 0 FAIL，退出码 0

# e2e 仅编译（需 tag）
C=$(mktemp -d); GOCACHE="$C" go test -tags e2e -c -o /tmp/hive-e2e.test ./e2e/
```
