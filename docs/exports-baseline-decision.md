# exports/ 解析基准决策备忘（P0-G2-1 / blocked #4）

> 状态：**决策材料，未改产品行为**（基线 `7a14adf0`）。
> 范围：v1→v2 启动迁移读取 `exports/` 的基准；附 F4（`colonies/.migrations` 被 `hive colony list` 当 colony）的两侧复核。
> 证据：`go_hive/docs/e2e-assets/exports-baseline/`（本备忘 §2/§7 的每个数字都出自该目录），探针 `go_hive/scripts/emoh_exports_baseline.sh`。
> 既有登记：`docs/g2-lossless-migration.md` §P0-G2-1、`go_hive/docs/g2-lossless-replacement.html`、`docs/support-matrix.md:83`、`go_hive/docs/storage-contract-diff.md:92`。

## 0. 结论速览

- **差异实测成立**：Python `migrate_v2` 读 **进程 CWD 下**的 `exports/`；Go 读 **`$HIVE_HOME/exports`**。9 组启动场景 + 3 组 API 直调矩阵见 §2。
- **迁移是“复制”不是“移动”**（`copytree`/`copyTree`，源目录保留）；首跑即写 `<home>/.migrated-v2` 标记，**即使一个 colony 也没搬**（实测：Python `m4`）——首跑没找到 exports，之后启动永不重试（两侧同）。
- **需要拍板 2 个问题**（§6）：① 是否要求 Go 自动迁移“`exports/` 在检出根”的 v1 用户；② 若要求，回退锚点选 **CWD**（与上游逐行为一致）还是 **检出根**（与 agent allowlist 同锚、不引入 CWD 依赖）。
- 本备忘的推荐（供参考，不代替拍板）：**选项 A：保持 Go 现状 + 发布说明 + 首启诊断**；若 Q1 答“必须自动迁移”，选 **C′（home 优先、检出根回退，见 §5 备注）**，而不是 B（CWD 回退）。
- **F4 复核**：`colony list` 文本输出**逐字一致**（两侧 sha256 相同）；`--json` **语义一致**、仅对象键序不同（Go `encoding/json` 对 map 键排序，属仓库已登记的排版类残余）。登记：**行为对齐、无需修**。若产品决定过滤点目录，必须两侧联合改，单改 Go 破坏对齐。

## 1. 现状（代码基准）

| 环节 | Python（上游） | Go（本仓） |
|---|---|---|
| 启动调用点 | `core/framework/server/session_manager.py:403-408` → `run_v2()`（**不传** `exports_dir`） | `go_hive/go-hive/internal/cli/loader.go:290` → `runStorageStartupMigrations` |
| 迁移输入 | `core/framework/storage/migrate_v2.py:37` `_migrate_colonies(exports_dir or Path("exports"))` → **CWD 相对** | `go_hive/go-hive/internal/server/storage_startup.go:63` `steps.migrateV2(home.Root, filepath.Join(home.Root, "exports"))` → **$HIVE_HOME 锚定** |
| 库级 API 默认 | 同左（不传即 CWD） | `internal/storage/migrate_v2.go:24-26`：`exportsDir == ""` 时回退 `"exports"`（**CWD 相对**）；差异只在生产调用点 |
| 标记语义 | `migrate_v2.py:22,25-27`：`$HIVE_HOME/.migrated-v2`，存在即跳过 | `migrate_v2.go:12-19`：同文件同名同语义 |
| 复制语义 | `migrate_v2.py:48-66`：`shutil.copytree`；目标存在则跳过；跳过 `.` 前缀项 | `migrate_v2.go:38-59`：`copyTree`；同样的“目标存在跳过 / 跳过 `.` 前缀项” |
| 启动序列 | v2 → v3 → colony_sessions → tracker 回填 | 同序（`storage_startup.go:54-77`） |

结论：两侧的**函数能力**都支持显式 exports 目录（见 §2 API 行）；分歧只在 serve 启动时传什么。改基准的代码面很小（一个调用点），行为面很大（见 §3）。

## 2. 实测矩阵（探针 emoh）

**方法**：两侧构造同构的目录树；每个场景**同时**在两个候选位置各种一个 marker colony：

- `<CWD>/exports/probe_from_cwd`
- `$HIVE_HOME/exports/probe_from_home`

然后跑**真实入口的 `serve --port 1`**：启动迁移发生在监听 bind 之前，端口 1 随后 bind 失败、进程退出（Python `PermissionError`、Go `bind: permission denied`），不残留服务。跑完看 `$HIVE_HOME/colonies` 里出现了哪个 marker，即“迁移实际读了哪里的 exports”。

**启动场景**（数据与 `matrix.md` 一致；CWD 一列写的是目录形态）：

| side | mode | CWD | CWD 有 exports/ | $HIVE_HOME 有 exports/ | 启动方式 | exit | .migrated-v2 | 搬入 colonies | .migrations 目录 |
|---|---|---|---|---|---|---|---|---|---|
| py | m1_cwd_repo | 检出根形态目录 | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_cwd` | yes |
| py | m2_cwd_home | 用户 home 形态目录（沙箱） | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_cwd` | yes |
| py | m3_cwd_tmp | 临时目录 | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_cwd` | yes |
| py | m4_cwd_neutral | 无 exports 的中性目录 | 0 | 1 | 绝对路径 | 1 | yes | （无） | no |
| py | m4w_repo_wrapper | 无 exports 的中性目录 | 0 | 1 | 仓库 `./hive` 包装器（绝对路径） | 1 | **no** | （无） | no |
| go | m1_cwd_repo | 检出根形态目录 | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_home` | yes |
| go | m2_cwd_home | 用户 home 形态目录（沙箱） | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_home` | yes |
| go | m3_cwd_tmp | 临时目录 | 1 | 1 | 绝对路径 | 1 | yes | `probe_from_home` | yes |
| go | m4_cwd_neutral | 无 exports 的中性目录 | 0 | 1 | 绝对路径 | 1 | yes | `probe_from_home` | yes |

**库级 API 直调**（绕开 serve，验证“显式绝对路径”能力）：

| side | probe | 结果 |
|---|---|---|
| py | `run_migration(exports_dir=<绝对路径>)` | exit 0，搬入 `probe_from_abs` |
| go | `storage.RunMigrationV2(home, <绝对路径>)` | exit 0，搬入 `probe_from_abs` |
| go | `storage.RunMigrationV2(home, "")`，CWD 下有 `exports/` | exit 0，搬入 `probe_from_cwd`（**库默认是 CWD 相对**） |

**读法**：

1. Python 的模式 1/2/3 结果相同：**只要启动目录里有 `exports/` 就吃它**——与“是不是检出根/是不是 home”无关，纯粹绑定 CWD。
2. Python 模式 4：什么都没搬，但 `.migrated-v2` 已写——与 G2 的 `v1-rich-serve-cwd.json`（从 `core/` 启动）同形，属**一次性数据滞留**。
3. Python 模式 4w：仓库自带的 `./hive` 包装器从外部目录**拒绝启动**（`Error: hive must be run from the project directory.`）——v1 时代 CWD 相对语义“能用”，部分原因是这个包装器把 CWD 钉死在检出根。
4. Go 的 4 个模式**全部**读 `$HIVE_HOME/exports`，CWD 不参与；`.migrations` 也会随 colonies 出现（colony_sessions 步骤）。
5. Go 的库级默认（空串）与 Python 默认同为 CWD 相对；“home 锚定”只发生在 serve 调用点。

证据文件（全部落盘，可重放）：

```
go_hive/docs/e2e-assets/exports-baseline/
  matrix.{tsv,md,json}            # 上表 + 机器可读
  notes.tsv                       # 每次运行的退出原因（bind 失败 / wrapper 拒绝）
  commands.md                     # 探针实际执行的命令
  runs/<side>-<mode>/serve.log    # 每次真实启动的原始日志
  runs/go-api_*/run.log, runs/py-api_explicit_abs/run.log
  runs/go-probe-build.log
```

## 3. 用户场景影响（迁移兼容性）

| 场景 | Python | Go（现状） |
|---|---|---|
| **S1** v1 用户：`exports/` 在检出根，**从检出根启动**（v1 标准路径） | 迁移成功（矩阵 m1） | **不迁移**（矩阵 m1）——数据留在 `exports/`，`colonies/` 为空，应用看不到 colony |
| **S2** v1 用户：`exports/` 在检出根，**从 `core/` 或其他目录启动** | 不迁移且写 marker（G2 `v1-rich-serve-cwd.json`；矩阵 m4 同形） | 不迁移（同 S1） |
| **S3** 新布局：`exports/` 在 `$HIVE_HOME/exports` | 不迁移，除非 CWD 恰好是 `$HIVE_HOME` | 迁移成功（矩阵 m1–m4） |
| **S4** 任意目录碰巧有 `exports/`（误放 / 别的项目残留） | **会静默导入**（矩阵 m2/m3） | 不读 |

两点关键：

- **首跑是单向门**：迁移先写 `.migrated-v2` 再返回；首跑没找到 exports 后，后续启动不再重试。要补救只能删 `<home>/.migrated-v2`（并清掉已复制目标）或手动复制。
- **数据不丢**：两侧迁移都是复制、不删源。S1/S2 的问题是“看不见/没接上”，不是数据被删。

## 4. 同一名词的四处基准（现状不一致面）

`exports/` 在本仓库至少有四种解析方式，迁移只是其中之一：

| 面 | Python | Go | 两侧一致？ |
|---|---|---|---|
| v1→v2 迁移输入 | **CWD**/exports（`migrate_v2.py:37`） | **$HIVE_HOME**/exports（`storage_startup.go:63`） | ❌（本备忘主题） |
| `agent_path` 允许根 | `$REPO_ROOT/exports`（`app.py:61,124`） | `$REPO_ROOT/exports`（`routes_agents.go:45,49-55`） | ✅（同为源码锚定） |
| `hive mcp` agent 扫描候选 | `project_root/exports`、`core/exports`（源码锚定；`mcp_registry_cli.py:170-180`） | `cwd/exports`、`cwd/core/exports`（**CWD** 锚定；`mcpregistry/cli.go:128-138`） | ❌（反向漂移，未在本车道改动） |
| colony 发现/工具索引 | 需 `metadata.json` / worker json（`colony_metadata.py:85-95`、`discovery.py:161-170`） | 同条件（`routes_colony_tools.go:395-402`、`discovery.go:349-370`） | ✅ |

含义：**“迁到哪”无论选哪个选项，都应在存储契约/发布说明里写死一个基准**，否则下一个读 `exports/` 的功能还会再漂一次。

## 5. 选项与影响

### 选项 A：保持 Go 现状（`$HIVE_HOME/exports`）

- **迁移兼容**：S3 正常；S1/S2（v1 检出根用户）不自动迁移。
- **可发现性**：与启动目录无关，最可预测；不会误读任意 CWD 的 `exports/`。
- **与上游一致度**：不一致（仅此一处），但方向已在 `storage-contract-diff.md:92` 记录，G2 结论即“替换方向更安全”。
- **风险**：低。复制语义、不删源；失败非致命。残余风险 = v1 用户首启后“看起来没有 colony”。
- **回滚**：无代码变更。数据侧回滚 = 删除 `colonies/<被复制项>` + `<home>/.migrated-v2` 后重跑（迁移跳过已存在目标，幂等）。
- **配套（不改行为）**：发布说明写明“v1 `exports/` 请先放到 `$HIVE_HOME/exports`”；可选加首启诊断（`$HIVE_HOME/exports` 不存在、而 CWD 下有 `exports/` 时打一条 warning/迁移提示），只影响日志。

### 选项 B：复刻 Python 的 CWD 语义（调用点改为读 `Path("exports")` / 传 `""`）

- **迁移兼容**：S1 在“从检出根启动”时能迁移；S2 仍失败（Python 自己就是这样）。
- **可发现性**：绑定启动目录；任意 `./exports` 会被静默导入（矩阵 m2/m3 型风险）。
- **与上游一致度**：**最高**（逐行为一致）。
- **风险**：中。把“serve 行为不依赖启动目录”的既有契约打破；storage-compat 夹具/`e2e/cmd/storagecompat` 需同步改；用户在不同目录启动会得到不同结果。
- **回滚**：revert 调用点提交；已复制的 colonies 保留（目标存在即跳过），删 marker 可重放。

### 选项 C：双基准兼容（`$HIVE_HOME/exports` 优先；缺省时回退 CWD `exports/`）

- **迁移兼容**：覆盖 S1/S3；S2 在“检出根 + 其他 CWD”下仍不迁移（除非回退锚点改成检出根，见 D/备注）。
- **可发现性**：home 优先保证主要路径确定；但 home 缺省时仍继承 CWD 风险。
- **与上游一致度**：部分（并集，无法逐行为对齐）。
- **风险**：中低。两处都有 exports 时只取 home（需文档化“home 优先”）。
- **回滚**：revert；已复制数据保留。

### 选项 D：双侧统一（Python 也锚定 `$HIVE_HOME/exports`，可选源码根回退）

- **迁移兼容**：最彻底；上游行为会变，需要跨仓库/上游协调。
- **可发现性**：与启动目录无关。
- **与上游一致度**：改后一致（“新基准”成为双方契约）。
- **风险**：最大（影响所有 Python v1 用户）；超出 Go 移植车道。
- **回滚**：两仓同步回滚，成本最高。

> 备注（源码根回退）：`$REPO_ROOT/exports` 与 agent allowlist 同锚，不引入 CWD 依赖；但 `__file__`/`packageSourceDir()` 在发布二进制里指向构建机目录，只对“从源码检出运行”的用户有效。若 Q1 要求自动兼容 v1 用户，这是比 CWD 更干净的锚点（Go 可先做，Python 后续跟进）。

### 推荐（供参考）

**A**：保持 `$HIVE_HOME/exports` + 发布说明 + 首启诊断。理由：① 不把 Python 的 CWD 缺陷复制进新运行时；② 与已文档化的存储契约、G2 夹具一致；③ 迁移复制语义使“漏迁”可补救、无数据删除；④ 代码零改动。
若 Q1 的答案是“v1 检出根用户必须自动迁移”，优先选 **C′ = C 的回退锚点换成检出根**（home → `$REPO_ROOT/exports`，不引入 CWD 依赖；即“D 的 Go 先行版”）；只有在“逐行为一致优先于启动确定性”时才选 **B**（或 C 的 CWD 回退形态）。

## 6. 需要拍板的问题（不自行决定）

**Q1. Go 1.0.26 首启是否必须自动迁移“`exports/` 在检出根”的 v1 用户？**
- (a) 必须 → 实现回退基准（进入 Q2），并同步更新 storage 契约与夹具；
- (b) 不必须 → 保持现状（A），发布说明 + 手工迁移指引（把 `exports/*` 放到 `$HIVE_HOME/exports`，或删 `.migrated-v2` 后从目标目录重跑）+ 可选首启 warning。

**Q2.（若 Q1 选 a）回退锚点用哪个？**
- (a) **CWD `./exports`**：与 Python 逐行为一致，但把启动目录依赖带回 Go（S4 风险、S2 无解）；
- (b) **检出根 `$REPO_ROOT/exports`**：与现有 agent allowlist 同锚、不依赖 CWD，只对源码/检出部署有效，发布二进制下自动失效；
- (c) 两者都试（home → 检出根 → CWD），按顺序取第一个存在者（覆盖最广，歧义最大，需要写清优先级）。

## 7. `.migrations` 与 `colony list` 复核（F4）

**场景**：`$HIVE_HOME/colonies/` 下放 `.migrations/colony_sessions_v1`（真实启动迁移会生成的标记目录）、`.stray_dotdir/`（空点目录）、`alpha`（metadata + 2 个 worker json）、`beta`（仅 metadata）。两侧各跑 `hive colony list` 与 `--json`。

**实测结果**（`colony-list/compare.json`）：

| 检查 | 结果 |
|---|---|
| 文本输出 | **逐字一致**：两侧 sha256 同为 `30e73cca8add7a930cb8806115e4d07354aad25d3b66c9e77cc548df7a4dab37`，exit 均 0 |
| JSON 值 | **语义一致**：排序键后完全相同（`.migrations` 行两侧同为 `name=.migrations, queen_name="", queen_session_id="", workers=0, created_at="", path=<同一路径>`） |
| JSON 字面 | 不相同——**仅对象键序**：Python 按插入序（name, queen_name, queen_session_id, workers, created_at, path），Go 按字典序（created_at, name, path, queen_name, queen_session_id, workers）；`json.diff` 里除键序外无值差异 |
| `.migrations` 是否被当 colony | **是**，两侧都列入（`.stray_dotdir` 这类任意点目录同样被列入——两侧都没有点目录过滤） |
| 空 home | 两侧同输出：文本 `No colonies found.`、JSON `[]`（逐字一致） |
| 边带效应 | 两侧 tracker 回填也把 `.migrations` 当 colony（各建 `.migrations/tracker/tracker.db`，遍历逻辑同样无点过滤，见下列源码行） |

**两侧证据行（代码）**：

- Python `core/framework/loader/cli.py:634-655`：`for path in sorted(COLONIES_DIR.iterdir())` → 只判 `is_dir()`，**无点目录过滤**；`_RESERVED_JSON_STEMS`（`:866`）只影响 worker 计数，不影响是否列入。
- Go `go_hive/go-hive/internal/cli/loader.go:1337-1354`：`if !entry.IsDir() { continue }`，**无点目录过滤**；`countColonyWorkers`（`:1556-1568`）与 `reservedJSONStems`（`:41`）同样只影响计数。
- 两侧 tracker 回填：`core/framework/host/tracker_db.py:443-464`、`go_hive/go-hive/internal/tracker/tracker.go:294-316`——同样遍历 colonies 下每个目录、无点过滤。

**结论（登记）**：`.migrations` 被 `colony list` 当成 colony 的行为**两侧对齐、无需修**（文本逐字一致；JSON 仅键序，属已登记排版类残余：`docs/api-contract-diff.md:1261` NV-04、`go_hive/docs/g2-lossless-replacement.html` R2/R3/R4、`docs/g2-lossless-migration.md:109` R-G2-1）。
注意两点：① 该行为**没有测试钉住**（Go 侧 `internal/cli/loader_test.go:158` 标 `Parity-exempt: no pytest drives cmd_colony_list`），要防回归应两侧联合加冻结用例；② 若产品决定过滤点目录（F4），必须**两侧同改**（CLI + tracker 回填 + 发现面一起看），单改 Go 会破坏现有对齐。

### 7.1 “对应 API”面（同一 fixture）

| 面 | Python | Go | `.migrations` 结果 |
|---|---|---|---|
| `GET /api/colonies/tools-index` | `list_colony_ids()` 只收有 `metadata.json` 的目录（`colony_metadata.py:85-95`；实测输出 `['alpha','beta']`） | 同条件（`routes_colony_tools.go:395-402`） | 两侧都**排除** |
| `GET /api/discover` | `discover_agents()` 只收含 worker json 的目录（`discovery.py:161-170`；实测 `{"Your Agents": ["Alpha"]}`） | `agents.DiscoverAgents` 同条件（`discovery.go:349-370`；实测同输出，`discover_equal: true`） | 两侧都**排除** |

即：`colony list` CLI 是**唯一**把 `.migrations` 展示成 colony 的面向用户接口；API 面两侧都因 metadata/worker 条件天然排除。证据：`colony-list/api-surfaces-py.json`、`colony-list/api-discover-go.json`（compare.json `api_surfaces`）；其中 Go `tools-index` 处理器未导出、为源码核对，其余为实测。

## 8. 复现

```bash
# Go 二进制（基线 7a14adf0，工作树 .wt/exports-memo）
cd go_hive/go-hive
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
go build -o /tmp/emoh-hive ./cmd/hive

# 探针（Python 半用工作树的 core，pin 住被测代码）
EMOH_GO_BIN=/tmp/emoh-hive \
EMOH_PY_HIVE=<repo>/.venv/bin/hive \
EMOH_PY_CORE=<worktree>/core \
EMOH_REPO_WRAPPER=<repo>/hive \
EMOH_GO_MODULE=<worktree>/go_hive/go-hive \
bash go_hive/scripts/emoh_exports_baseline.sh
```

- Python 入口是 venv 里的 `hive` console script（与 `./hive` 包装器最终 exec 的 `uv run hive` 同一代码）；`EMOH_PY_CORE` 用 `PYTHONPATH` 把 `framework` 钉到被测工作树。
- `serve --port 1` 的 bind 失败是**故意的终止手段**：迁移在 bind 之前完成（`loader.go:290` / `session_manager.py:403` 早于监听），进程随后退出，不留后台服务。
- 证据重建后写到 `go_hive/docs/e2e-assets/exports-baseline/`，与本备忘数字一一对应。

## 9. 未验证项

- 只用**本机源码构建**的二进制与 venv 入口验证；未在干净机器/发布归档上复跑（`dist/` 里的既有二进制未用作证据）。
- Windows 未测（路径分隔符、权限、`Path("exports")` 语义理论上一致）。
- 未构造“真实 v1 用户 home + 真实升级流程”的端到端演练；S1/S2 是沙箱复刻 + G2 既有证据的组合结论。
- `hive mcp` agent 扫描的 CWD/源码锚点反向漂移（§4 第 3 行）只做了源码核对，未实测。
- JSON 键序为全局 `encoding/json` 行为，未逐命令统计影响面（本备忘只覆盖 `colony list`）。