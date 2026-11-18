# 13 · 跨平台与无 cgo 设计

> 目标：`CGO_ENABLED=0` 下在 darwin / linux / windows × amd64 / arm64 六个目标全部可编译；本机测试全部通过；不依赖系统 SQLite、OpenSSL 或任何 C 库。

## 1. 为什么必须无 cgo

| 理由 | 说明 |
| --- | --- |
| 交叉编译 | cgo 交叉编译需要目标平台 C 工具链，Windows/macOS 矩阵会迅速失控 |
| 安装体验 | 用户应能下载单文件二进制直接运行，不需要 gcc、不需要系统 `libsqlite3` |
| CI 确定性 | 关闭 cgo 后，构建输入只有 Go 源码 + Go module cache |
| 静态链接 | 便于容器与精简镜像；不依赖 glibc 版本 |
| 安全面 | 少一层 C 代码与动态库加载，减少供应链与内存安全风险 |
| 可复现 | `GOPROXY=off` 时只要缓存完整即可离线重建 |

Go 标准库本身在 `CGO_ENABLED=0` 时会自动选择纯 Go 的 DNS 解析器等实现，行为可接受。

## 2. 依赖策略

| 依赖 | 版本 | 用途 | cgo |
| --- | --- | --- | --- |
| `modernc.org/sqlite` | v1.60.1 | Tracker 的 SQLite 实现 | 否（C 转译为 Go） |
| `github.com/ebitengine/purego` | v0.11.3 | 动态调用系统库（预留） | 否（运行时 dlopen/dlsym） |
| `gopkg.in/yaml.v3` | v3.0.1 | `SKILL.md` frontmatter | 否 |

间接依赖只有 `google/uuid`、`mattn/go-isatty`、`ncruces/go-strftime`、`remyoudompheng/bigfft`、`x/sys` 等纯 Go 包。

**新增依赖的硬性条件**：不得引入 cgo；不得在运行时下载可执行代码；必须有明确的上游对应物或 Go 特有的必要理由；需要显式批准。

## 3. SQLite：modernc.org/sqlite

- 驱动注册名是 `sqlite`，`database/sql` 直接使用：

  ```go
  db, err := sql.Open("sqlite", path)
  db.SetMaxOpenConns(1)
  ```

- 打开后立即设置：`journal_mode=WAL`、`synchronous=NORMAL`、`foreign_keys=ON`、`busy_timeout=5000`。
- `MaxOpenConns(1)` 是刻意选择：Tracker 是单写者、短事务场景，串行化避免 `SQLITE_BUSY` 抖动，也让变更触发器的顺序可预测。
- 不使用 SQLite 扩展（`load_extension`）。这与 Tracker 的 SQL 拒绝清单天然一致：既无 cgo 扩展可加载，也主动拒绝该函数。
- 已知取舍：纯 Go 实现的部分查询比 C 版慢；当前数据规模（每 Colony 一个库、变更日志上限 1 万条）不构成瓶颈。

## 4. purego 的定位与现状

**现状（必须如实说明）**：`purego` 出现在 `go.mod` 的 require 列表中，但**当前没有任何 `.go` 文件 import 它**。它是为后续「需要调用系统动态库但不想开 cgo」的场景预留的。

允许的使用方式：

1. 仅在确实没有纯 Go 替代时使用（例如特定平台的系统 API）。
2. 必须放在带 `//go:build` 约束的文件中，按 `darwin`/`linux`/`windows` 分别实现。
3. 必须有同签名的纯 Go 回退或明确的「不支持该平台」错误，不能因加载失败而 panic。
4. 不得用于加解密、SQLite、HTTP 等已有纯 Go 实现的领域。

禁止的使用方式：用 purego 包装 libsqlite3、OpenSSL、libc 的文件/网络函数——这些都有纯 Go 方案，引入动态库会破坏静态可移植性。

## 5. 验证命令与当前结果

所有命令在 `go_hive/go-hive/` 下执行：

```bash
# 本机测试（离线、无 cgo）
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./...

# 或直接使用 Makefile
make test
make cross
```

**2026-11-17 实测：**

```
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./...
ok   github.com/aden-hive/hive-go/internal/atomicfs    0.645s
ok   github.com/aden-hive/hive-go/internal/credentials 1.027s
ok   github.com/aden-hive/hive-go/internal/hivehome   1.270s
?    github.com/aden-hive/hive-go/internal/llm         [no test files]
ok   github.com/aden-hive/hive-go/internal/memory     1.725s
ok   github.com/aden-hive/hive-go/internal/session    2.137s
ok   github.com/aden-hive/hive-go/internal/skills     2.542s
ok   github.com/aden-hive/hive-go/internal/tracker    2.883s
?    github.com/aden-hive/hive-go/internal/version     [no test files]
```

```
make cross
== darwin/amd64 ==
== darwin/arm64 ==
== linux/amd64 ==
== linux/arm64 ==
== windows/amd64 ==
== windows/arm64 ==
cross: 全部平台编译通过（无 main 包时不产出二进制）
```

`dist/` 为空是预期结果：`cmd/hive/` 目前没有 main 包。等 CLI 落地后，`make cross` 会自动产出 `dist/hive-<os>-<arch>[.exe]`，无需改目标。

## 6. 交叉编译矩阵

| GOOS | GOARCH | 编译 | 测试 | 二进制 |
| --- | --- | --- | --- | --- |
| darwin | amd64 | 通过 | 仅本机可跑 | 待 CLI |
| darwin | arm64 | 通过 | 本机（darwin/arm64）全量通过 | 待 CLI |
| linux | amd64 | 通过 | 需 CI/QEMU | 待 CLI |
| linux | arm64 | 通过 | 需 CI/QEMU | 待 CLI |
| windows | amd64 | 通过 | 需 CI | 待 CLI |
| windows | arm64 | 通过 | 需 CI | 待 CLI |

「编译通过」指全部非测试包与测试包都能为目标平台构建；测试执行目前只在 darwin/arm64 本机与 CI 的 Linux 上验证。CI 的 cross job 只做六目标编译，**不在 Windows 上跑测试**（见第 9、10 节）；这是当前已知缺口。

## 7. `internal/platform`（规划）

目录已建但为空。目标是收拢所有平台差异，禁止业务包直接写 `runtime.GOOS` 分支。规划的子包：

| 包 | 职责 |
| --- | --- |
| `platform` | 路径规则、默认 shell、是否大小写敏感、可执行文件后缀 |
| `platform/term` | 终端能力探测（TTY、颜色、宽度）、进程组与信号 |
| 未来 `platform/fs` | 原子 rename 在 Windows 的替换语义、目录 fsync 的可用性 |

约束：平台包不得 import 领域包；所有平台差异必须在一处可枚举，便于测试矩阵覆盖。

## 8. 平台差异清单（实现时必须逐项考虑）

| 差异 | 处理 |
| --- | --- |
| 路径分隔符 | 只用 `filepath`，禁止字符串拼接 `/`；落盘 JSON 内的路径字段统一用 `/` 或仅存相对名 |
| 文件权限 | `0o600`/`0o644` 在 Windows 上被忽略，不视为错误；不要用权限位做安全边界 |
| 换行 | 文本比较统一 `\n`；`bufio.Scanner` 已按行剥离 `\r`，注意 `.env` 解析要 `TrimSpace` |
| 大小写 | Windows/macOS 默认大小写不敏感；技能名与文件名冲突检测不能只依赖 inode |
| 符号链接 | `filepath.EvalSymlinks` 在 Windows 需要权限；失败时回退到逻辑路径并告警 |
| 目录 fsync | Windows 不支持对目录 fsync；`atomicfs` 只对文件 fsync，rename 后不强制目录同步 |
| rename 覆盖 | Windows 的 `os.Rename` 不能覆盖已存在文件；原子写必须保证目标先被移走或使用 replace 语义（Go 1.26 的 `os.Rename` 在 Windows 已支持替换，但需测试） |
| 临时目录 | 一律 `os.MkdirTemp`，不硬编码 `/tmp` |
| Shell | 凭据写 shell 配置需要区分 bash/zsh/fish/PowerShell；检测失败要给出明确错误而非猜 |
| 信号 | `SIGTERM` 在 Windows 不存在；进程停止逻辑使用 `os.Interrupt` 兼容路径 |
| 行尾/编码 | `SKILL.md` 按 UTF-8 读取；BOM 需要容忍 |

## 9. CI 中的跨平台验证

`.github/workflows/go.yml`（当前在 `go_hive/.github/`，评审后并入根 `.github/`）包含：

- `quality` job：Ubuntu 上 `make ci`，`CGO_ENABLED=0`，先在线预热 module cache，再离线运行；
- `cross` job：矩阵 `darwin/linux/windows × amd64/arm64`，每个目标执行 `go build ./...`，有 main 包时上传二进制。

本地 `make cross` 覆盖同样的六目标，因此即使 CI 未运行，也能在开发机上得到相同结论。

## 10. 风险

1. **Windows 原子替换**：`os.Rename` 覆盖已存在文件的行为需要专门的 Windows 测试；当前 CI 只做编译，未跑测试。
2. **purego 的误用**：预留依赖容易被当成「可以调任意系统库」的许可，评审必须按第 4 节约束拦截。
3. **modernc SQLite 性能**：大批量 upsert 时可能明显慢于 C 版；若未来数据量增长，需要基准测试而不是直接引入 cgo。
4. **路径大小写**：技能同名判定在不同文件系统上可能不一致，需要用显式字符串比较兜底。
5. **无 main 包**：CLI 落地前，「交叉编译产出二进制」无法真正验收，只能验证库层可编译。