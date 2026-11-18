# 00 · 产品目标与验收标准

> 上游参考：`aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）
> Go 模块：`github.com/aden-hive/hive-go`
> 本文定义「什么叫移植完成」，是所有实现与评审的共同口径。

## 1. 一句话目标

用**纯 Go、无 cgo** 重写 OpenHive，做到**功能对等、测试一对一、跨平台零妥协**：Python 版本能做的事，Go 版本以相同的数据格式、相同的外部行为、相同的边界语义完成，并为每一条 Python 测试找到对应的 Go 测试。

## 2. 三个不可妥协的目标

| 目标 | 含义 | 验收证据 |
| --- | --- | --- |
| **功能对等** | 覆盖 OpenHive 的全部对外能力：技能、记忆、会话、凭据、Tracker、AgentLoop、Colony、触发器、事件总线、工具、服务端、CLI | `11-product-requirements.md` 清单逐项勾选 |
| **质量对等** | 每条 Python 行为都有对应 Go 测试；测试通过 `// Parity:` 注解建立可审计的映射 | `scripts/check_parity.sh` 全绿；`15-test-parity-matrix.md` 可追溯 |
| **平台优势** | `CGO_ENABLED=0` 下在 darwin / linux / windows × amd64 / arm64 全部可编译、可测试；离线可复现 | `make cross` 六平台通过；`make test` 在 `GOPROXY=off` 下通过 |

三者冲突时，优先级是：**功能对等 > 质量对等 > 平台优势**。不允许为了「Go 风格更漂亮」而改变 Python 的外部行为——对等优先于惯用法。

## 3. 「功能对等」的判定标准

对等不是「看起来差不多」，而是以下四层同时成立：

1. **接口面**：导出的函数、结构体、工具名、HTTP 路由、CLI 子命令与上游一一对应。
2. **数据格式**：落盘文件可双向读取。已经锁定的契约包括：
   - `state.json` schema `1.1`（`session` 包）；
   - 会话对话 part 文件名 `%010d.json`、`cursor.json`、`meta.json`；
   - 凭据 `credentials/{id}.enc` + `metadata/index.json` 版本 `2.0`（Fernet 密文）；
   - Tracker SQLite schema `v3`、`_tracker_registry` / `_tracker_changes` 表和变更触发器；
   - 技能 `SKILL.md` YAML frontmatter 与 `.skill_overrides.json` schema `1`；
   - `~/.hive` 目录布局（`hivehome` 包）。
3. **边界语义**：错误分支、空值、越界、损坏文件的处理与上游一致。例如会话读取遇到损坏 part 是**跳过**而不是报错；凭据索引损坏时**重建**而不是失败；未知 JSON 字段**忽略**而不是拒绝。
4. **可观测文本**：面向模型/用户的提示词、错误消息、工具描述在需要一致的地方逐字对齐（例如 `ErrNoBinding` 的引导语、记忆召回的系统提示）。

## 4. 「质量对等」的判定标准

- 每一个 `func TestXxx` 必须在注释块中声明来源：

  ```go
  // Parity: core/tests/test_session_store.py::test_session_id_traversal
  func TestSessionIDAndTraversal(t *testing.T) { ... }
  ```

- 无法一一对应的测试（例如 Go 特有的编译期/跨平台验证）必须显式豁免：

  ```go
  // Parity-exempt: Go 特有；验证 6 平台交叉编译常量。
  func TestPlatformMatrix(t *testing.T) { ... }
  ```

- **注解是契约，不是装饰**。`// Parity:` 指向的 Python 测试必须真实存在；评审时按注解回溯上游用例，而不是凭测试名猜测。
- 不允许「一个 Go 测试覆盖十条 Python 用例却不写映射」；也不允许「复制 Python 测试名但没有断言对等行为」。
- 验收门槛：`bash go_hive/scripts/check_parity.sh` 退出码为 0。

## 5. 「平台优势」的判定标准

| 项 | 要求 |
| --- | --- |
| cgo | 全仓库 `import "C"` 数量为 0；`CGO_ENABLED=0` 是默认 |
| SQLite | `modernc.org/sqlite`（纯 Go 转译），驱动注册名 `sqlite` |
| 动态加载 | 需要时使用 `github.com/ebitengine/purego`，并以 build tag 隔离；当前尚无调用点 |
| 交叉编译 | `make cross` 产出 darwin/linux/windows × amd64/arm64 六个目标（有 main 包时产出二进制） |
| 离线 | `GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` 下可构建、可测试 |
| 路径与权限 | 使用 `filepath`，不硬编码 `/`；`0o600` 等权限在 Windows 上按 Go 语义降级处理，不 panic |

## 6. 非目标

- **不重新设计产品**：不新增上游没有的能力，不改 UI 交互，不改 Queen 人格与技能内容。
- **不做 Python 插件 ABI 兼容**：Go 版本不加载 Python 编写的工具/技能插件；对等的是**数据与协议**，不是进程内扩展机制。
- **不追求逐行翻译**：允许用 Go 的接口、泛型、`context` 改写内部结构，但外部行为必须一致。
- **不引入新依赖**：模块依赖锁定为 `modernc.org/sqlite`、`purego`、`yaml.v3`；新增依赖需要显式批准。
- **不在本阶段做性能优化**：正确性与对等优先，性能只做「不明显劣化」的底线要求。

## 7. 交付物与目录约定

所有 Go 代码与配套文档都在工作区的 `go_hive/` 下：

```
go_hive/
├── README.md                 # Go 项目说明（如何构建/测试/离线运行）
├── STATUS.md                 # 实时进度看板
├── 00-product-goals.md       # 本文
├── 11-product-requirements.md
├── 12-design.md
├── 13-cross-platform-and-purego.md
├── 14-quality-assurance.md
├── 15-test-parity-matrix.md
├── scripts/
│   ├── check_parity.sh       # parity 注解门禁
│   └── watchdog.sh           # 进度巡检
├── .github/workflows/go.yml  # CI（评审后并入根 .github）
└── go-hive/
    ├── go.mod
    ├── Makefile              # test / vet / fmt-check / build / cross / parity / ci
    ├── cmd/                  # CLI 入口（规划中）
    ├── internal/             # 全部实现包
    └── parity/fixtures/      # 预留的跨语言固定装置
```

上游分析文档 `01`–`10` 描述的是 Python 版本，作为移植参考保留，不代表 Go 实现进度。

## 8. 验收清单

| 门禁 | 命令 | 通过标准 | 当前状态（2026-11-18 19:25） |
| --- | --- | --- | --- |
| 格式 | `make fmt-check` | 无未格式化文件 | 通过 |
| 静态检查 | `make vet` | 无输出 | 通过 |
| 单元测试 | `make test` | 全部包 `ok` | 通过 |
| 交叉编译 | `make cross` | 6 平台全部编译通过 | 通过（`cmd/hive` 产出二进制） |
| 离线 | 上述命令带 `GOPROXY=off` | 不访问网络 | 通过 |
| parity 注解 | `make parity` | 退出码 0 | 通过：2,215/2,215 已注解，0 missing |
| parity 引用 | `make parity-targets` | 退出码 0 | 通过：2,035 条引用，0 missing |
| 竞态 | `make race` | 无 data race | 未纳入 CI 必须项（见 14 文档） |

本快照（2026-11-18 19:25）在 W3 agents 与 W4 create-session 两条并发车道合并后整树复跑 `make ci`，退出码 0：fmt-check、vet、全量测试、六平台交叉编译、parity 注解与引用门禁全部为绿，无在途车道。任何新增测试都不允许让未注解数上升，也不允许通过放宽门禁来「变绿」；缺口以 14、15 号文档为准。

## 9. 与 Python 上游的关系

- **同构而非分叉**：Go 版本是上游语义的另一个实现，不是新框架。上游 1.0.14 的行为是唯一事实来源。
- **兼容窗口**：Go 进程应能读取 Python 写下的会话、凭据与 Tracker；反向亦然（至少读）。这是 `state.json`、`.enc`、SQLite schema 必须逐字对齐的原因。
- **演进策略**：上游升级时，先更新 Python 参考测试，再更新 Go 测试与实现；不允许先改 Go 再改测试来「让测试变绿」。
