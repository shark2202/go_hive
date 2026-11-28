# Go 静态检查规范（go-hive）

> 适用模块：`go_hive/go-hive`（`github.com/shark2202/go_hive/go-hive`）
> 约束：`CGO_ENABLED=0`、`GOPROXY=off`、`GOSUMDB=off`；不新增 Go 依赖。

## 1. 工具选型

| 工具 | 作用 | 是否门禁 |
|---|---|---|
| `gofmt` | 语法级格式化 | `make fmt-check` |
| `go vet` | Go 官方静态检查（printf、copylocks、nil 等） | `make vet` |
| `staticcheck` | Go 长期静态分析器：`SA*` 正确性、`S*` 简化、`ST*` 风格、`U*` 未使用 | `make staticcheck-check` |
| `golangci-lint` | 多 linter 聚合器 | 本仓库不采用；避免再引入一个未固定版本的二进制 |

`staticcheck` 是本仓库的 lint 规范；不把 `golangci-lint` 作为隐藏依赖，也不靠 IDE 插件替代 CI。

## 2. 本地命令

```bash
cd go_hive/go-hive

make lint                  # gofmt + go vet + staticcheck 基线门禁
make staticcheck-check     # 只跑 staticcheck 门禁
make staticcheck-update    # 有意接受当前债务时重建基线（默认禁止）
```

`staticcheck` 未安装时，门禁明确输出 `BLOCKED` 并退出 2，不会把“没扫”伪装成绿色：

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck -version
```

当前基线用 **staticcheck 2026.2.2 (0.8.2)** 生成；升级 staticcheck 版本后应重新审阅基线，而不是直接 `--update` 掩盖差异。

## 3. 配置

### `.staticcheck.conf`

```toml
checks = ["all"]
```

启用全部检查。**禁止**通过全局 `checks = ["-U1000", ...]` 关闭整类规则；存量债务由基线文件冻结，不靠关闭规则。

### `internal/quality/staticcheck_baseline.json`

- 记录当前存量的 `code | 模块相对文件 | message` 计数；
- 不记录行号/列号：代码移动、插入注释不会让基线失效；
- 同一文件重复 message 用计数保留；
- 门禁规则：
  - 当前 findings 中出现了基线没有的键 → **FAIL**；
  - 基线中的 findings 被修掉 → **FAIL（stale baseline）**，提示运行 `make staticcheck-update` 下调上限；
  - 基线只能下降，不能通过 `--update` 扩大。

### 什么时候可以 `staticcheck-update`

只有以下情况允许：

1. 升级 staticcheck 后确认新规则是**有意的**，逐条审阅差异；
2. 接受一笔明确的迁移债务，并在 PR/提交说明中写出原因和清理计划；
3. 生成物或平台差异导致静态工具误报，且已确认不是代码缺陷。

不允许为了“先让 CI 过”而批量 `--update`，也不允许用 `//lint:ignore` 静默新告警；确需忽略时必须写 `//lint:ignore <check> <理由和清理条件>`。

## 4. 与 `make ci` 的关系

`make ci` 现在按以下顺序执行：

```text
fmt-check
vet
staticcheck-check
test
cross
parity
parity-targets
wiring-check
compat-check
arch-check
```

`staticcheck-check` 使用冻结基线，所以当前全量 CI 仍应保持 RC=0；新增代码如果引入新 findings，会在进入测试阶段前失败。

## 5. 修复顺序

存量 **372** 条 findings（317 个唯一键）不应一次性清理。按以下顺序逐 lane 收敛：

1. **正确性**：`SA*`（尤其 `SA1012` nil context、`SA4006` 未使用赋值、`SA4023` 永不成立比较）；
2. **资源/生命周期**：`U1000` 未使用函数/字段，先确认不是测试注入或反射使用；
3. **简化**：`S*`（`append` 循环、冗余 return、nil 检查）；
4. **风格**：`ST1005` 错误字符串大小写/标点，按包批量修；
5. 每修一组，运行 `make staticcheck-update` 下调基线并提交 baseline 变化；不要在同一提交混入无关重构。

## 6. 验证证据

```bash
cd go_hive/go-hive
make staticcheck-check
make lint
make ci
```

- `make staticcheck-check` 输出当前 findings 数与 baseline 数；
- `make lint` 应同时通过 `gofmt`、`go vet`、`staticcheck`；
- `make ci` 必须 RC=0；若 staticcheck 缺失，按 §2 安装后重跑，不得用 `|| true` 绕过。
