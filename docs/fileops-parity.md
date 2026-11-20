# file_ops 工具对齐说明（read_file / write_file / edit_file / search_files）

上游 `tools/src/aden_tools/file_ops.py` 通过 `files-tools` 组内向模型暴露 4 个文件工具：
`read_file`、`write_file`、`edit_file`、`search_files`。本仓库此前只移植了前两个，
`internal/tools/tier_defaults.go` 的 `code_editing` 还引用了不存在的 `edit_file`。
补齐后，四个工具都由 `internal/tools/builtin` 在进程内提供，无需子进程或 MCP。

残差补齐（本次）：`read_file` 的 `hashline`、stale-edit guard
（`aden_tools/file_state_cache.py`）、`write_safe_root` 列表。

`go_hive/docs/tool-parity-matrix.html` 是生成页，本次不手改；由 integrator 重跑矩阵。

## 1. 工具与 schema

| 工具 | 参数 | 必需 |
| --- | --- | --- |
| `read_file` | `path`、`offset`、`limit`、`hashline` | `path` |
| `write_file` | `path`、`content` | `path`、`content` |
| `edit_file` | `mode`（`replace`/`patch`，默认 `replace`）、`path`、`old_string`、`new_string`、`replace_all`、`patch_text` | 无（全部有默认值，与上游 FastMCP 签名一致） |
| `search_files` | `pattern`、`target`（`content`/`files`）、`path`、`file_glob`、`limit`、`offset`、`output_mode`（`content`/`files_only`/`count`）、`context`、`hashline`、`task_id` | `pattern` |

上游的 `session_cwd` 隐藏 context 参数不进入 schema：Go 侧每个 `Toolbox` 在构造时绑定
沙箱 root，由 `builtin.New(root)` 决定，模型无法覆盖。

## 2. read_file

### 输出格式

* 普通模式：`%6d\t<line>`（与上游 `f"{i + 1:>6}\t{line}"` 等价），单行超过
  `MAX_LINE_LENGTH`（2000 rune）截断并追加 `...`。
* `hashline=true`：每行输出 `N:hhhh|content`（`file_ops.py::read_file` 的
  `f"{i + 1}:{h}|{line}"`）。行号不补零；`hhhh` 是
  `crc32(line.rstrip(" \t")) % 65536` 的 4 位小写十六进制
  （`aden_tools/hashline.py::compute_line_hash`）。该模式**不做行截断**——截断会破坏
  哈希，因此超长行原样返回；空行输出 `N:0000|`。
* 行切分与 Python `str.splitlines()` 一致（`file_ops.py::read_file` 的
  `content.splitlines()`）：`\n`、`\r\n`、`\r`、`\v`、`\f`、`\x1c-\x1e`、`\x85`、
  `\u2028`、`\u2029` 都是行边界；`\r\n` 只算一次；末尾换行不产生额外空行；
  最后一行没有换行也仍然计一行。因此 CRLF 文件的行内容、行号与哈希都与上游相同。
* `offset`/`limit` 分页：`offset` 从 1 开始，`limit=0` 表示 `MAX_READ_LINES`（2000）；
  未显示完时追加 `(Showing lines A-B of N. Use offset=... to continue reading.)`。
* 50KB 字节预算（`MAX_OUTPUT_BYTES`）按“格式化后行字节 + 1”累计，超出时截断并追加
  `Truncated by byte budget.`；`hashline` 模式同样只受该预算限制。
* 每次成功读取都把**原始字节**记入本会话的 stale-edit guard 缓存（见 §3）。部分读取
  （offset/limit）也记录整文件哈希，保证之后编辑文件深处时仍能检测外部改动。

`Parity: tools/src/aden_tools/file_ops.py::read_file`、
`tools/src/aden_tools/hashline.py::compute_line_hash`、`hashline.py::format_hashlines`。

## 3. stale-edit guard（文件状态缓存）

`internal/tools/builtin/file_state.go` 是 `aden_tools/file_state_cache.py` 的 Go 版：

* 记录 `(size, mtime_ns, sha256)`。`size` 与 `mtime_ns` 都相同 → `FRESH`，不做哈希；
  任一不同 → 重新算 sha256：内容相同则刷新记录并仍判 `FRESH`（编辑器重写相同内容不算
  陈旧），不同则 `STALE`（`content changed on disk since the last read (sha256 differs)`）；
  stat 失败/文件被删 → `STALE`（`file has been deleted since it was read`、
  `stat failed: ...`）。
* `read_file` 记录；`write_file` 与 `edit_file` 的 `replace` 模式在写前校验：
  未读 → `UNREAD`，拒绝；陈旧 → `STALE`，拒绝。错误文案与上游逐字一致：
  * `Refusing to overwrite '<path>': call read_file('<path>') first so the harness can track its state before you replace it. If you intend to discard the current contents, read it first to acknowledge what you are overwriting.`
  * `Refusing to overwrite '<path>': <detail>. Re-read the file with read_file before writing.`
  * `Refusing to edit '<path>': call read_file('<path>') first so the harness can track its state before you edit it.`
  * `Refusing to edit '<path>': <detail>. Re-read the file with read_file before editing.`
* `write_file` 只对**已存在的普通文件**设防；新文件允许直接创建（没有可覆盖的内容）。
* 写成功后（`write_file`、`edit_file` replace、`edit_file` patch 的每个落盘文件）立即
  记录新状态，所以同一轮内的链式编辑不会自我失效。
* `edit_file` 的 `patch` 模式**不设 guard**（上游 `_patch_apply` 只 `record_read`，没有
  `check_fresh`），但会记录写入后的状态。
* 作用域：缓存挂在 `Toolbox` 上，一个 Toolbox 就是一个会话，两个会话不共享读状态。
  上游是进程级 `{scope: {path: record}}`，`scope` 取 agent_id（`file_ops.py` 传入
  `None`，实际等价 `"__global__"`）；Go 用 per-session 结构实现同一隔离意图。
  每个 scope 的 LRU 上限 256 条（`_MAX_ENTRIES_PER_SCOPE`），并发由 `sync.Mutex` 保护。
  包级 `builtin.Execute` 使用共享的 `defaultToolbox`，行为等价上游的 `"__global__"`。

`Parity: tools/src/aden_tools/file_state_cache.py::record_read`、
`file_state_cache.py::check_fresh`。

## 4. edit_file

### mode=replace

* 唯一匹配（`replace_all=false`）或全部替换（`replace_all=true`）；
  `old_string == new_string`、空 `path`/`old_string` 直接返回 `Error:`。
* 写前先过 §3 的 stale-edit guard（与上游 `_patch_replace` 同序：路径策略 →
  文件存在性 → guard → 匹配）。
* fuzzy 策略顺序与 `_fuzzy_find_candidates` 一致：
  1. exact
  2. line-trimmed
  3. whitespace-normalized
  4. indentation-flexible
  5. escape-normalized（字面 `\n`/`\t`/`\r`）
  6. trimmed-boundary
  7. unicode-normalized（弯引号、em/en dash、省略号、NBSP）
  8. block-anchor（首尾行锚定，单候选阈值 0.50，多候选 0.70）
  9. context-aware（逐行 ≥0.50 且均值 ≥0.65）
* 命中非 exact 策略时返回 `(matched via <strategy>)`；返回 unified diff（3 行上下文，
  2000 字符截断，与 `_compute_diff` 一致）。
* CRLF 文件在 LF 空间匹配、按文件行尾写回；原子写入复用 `internal/atomicfs`。
* 未命中时错误文案包含 `Use read_file ... or search_files` 提示与最多 3 条近似行。

### mode=patch

* 解析 V4A：`*** Begin/Patch` 可选；`Update/Add/Delete/Move File:`；
  `@@` / `@@ hint @@`；hunk 行前缀 ` `/`-`/`+`；容忍遗漏前缀的隐式上下文行、
  `\ No newline at end of file` 行。
* 两阶段 apply：先在内存副本上验证全部操作（含 fuzzy 唯一性、add 已存在、
  move 目标已存在、路径逃逸、write_safe_root），任一失败返回
  `Patch validation failed (no files were modified)` 且磁盘不变；全部通过后才写入。
* 写入阶段失败返回 `Apply phase failed (state may be inconsistent — run git diff ...)`。
* 返回 `Modified/Created/Deleted N file(s): <abs paths>` 摘要与各修改文件的 diff。
* 每个落盘文件写后记录状态（§3）。

## 5. search_files

### target=content

* 纯 Go 遍历 + `regexp` 匹配（RE2），不依赖 `rg` 子进程。
* `output_mode=content`：`rel:line:text`；`context>0` 时输出
  `rel:line:text`（命中行）与 `rel:line-text`（上下文行）分组，组间空行。
* `output_mode=files_only`：每行一个相对路径；`output_mode=count`：`rel:count`。
* `hashline=true`：命中行输出 `rel:line:hhhh|text`，
  `hhhh = crc32(line.rstrip(" \t")) % 65536`（对齐 `aden_tools/hashline.py`）。
* 分页：`limit` 默认 50（内容模式最小 1），`offset` 默认 0；未显示完时附
  `[Hint: showing N of M matches. Use offset=... ]`。
* 无结果显示 `No matches found.`；偏移越界显示 `No matches at offset N (total: M).`。

### target=files

* `pattern` 按 basename glob（`*` 或 `*.<ext>`）；`file_glob` 在 files 模式忽略。
* 跳过 `.git`、`__pycache__`、`node_modules`、`.venv`、`.tox`、`.mypy_cache`、
  `.ruff_cache` 以及任何以 `.` 开头的目录；files 模式额外跳过隐藏文件。
* 按 mtime 倒序（同 mtime 按路径倒序）；输出相对路径；附
  `[Hint: showing N of M files. ...]`。
* 非目录返回 `Error: Directory not found: <resolved>`。

### 防循环

进程级 tracker（按 `task_id` 或共享 bucket）对完全相同的查询连续计数：
第 3 次追加 `[Warning: ...]`，第 4 次返回 `BLOCKED: ...`，与上游一致。
查询 key 包含 target/pattern/path/file_glob/limit/offset/output_mode/context，
翻页不会误触发。

## 6. write_safe_root 与沙箱

上游 `_FilePolicy` 的语义：

* `write_safe_root` 是**可选的写上限**，接受单个路径或列表；`None` 表示只受 deny list
  约束。
* 只约束写；读完全不受影响（上游的读只受凭据 deny list 约束，绝对路径原样使用）。
* 每个 root 经 `realpath(expanduser(...))` 归一化，写路径必须落在**任一** root 之下。
* 越界文案：`write denied: '<original>' is outside the configured write_safe_root(s) (<roots>)`
  （roots 以 `, ` 连接）。

Go 侧实现：

* `Toolbox.SetWriteSafeRoots(roots ...string)` 配置上限（`~` 展开、绝对化、存在时
  realpath，等价 `_norm`）；空列表表示不额外设限。
* 写路径统一走 `resolveWritePath`：先按沙箱 root 解析；逃逸时再尝试任一配置 root；
  都不满足则返回上述上游文案（工具前缀沿用 `write_file:` / `Error:` 约定）。
  因此**配置后，root 外的绝对路径只要位于某个 write_safe_root 下就允许写**——与上游
  `test_write_safe_root_accepts_list` 的语义一致；未配置时行为不变（只有沙箱 root）。
* `write_file`、`edit_file`（replace 与 patch 两条路径）都经过同一检查。
* 读路径不查 `write_safe_root`（与上游“写上限不影响读”一致）。

与上游 `_FilePolicy` 的其余差异：

| 项 | 上游 | Go |
| --- | --- | --- |
| 相对路径 | 锚定 agent home | 锚定沙箱 root |
| 绝对路径（读） | 原样使用（仅凭据 deny list） | 必须在 root 内；`write_safe_root` 不放宽读 |
| 绝对路径（写） | 原样使用（仅 deny list + 可选上限） | root 内，或配置后位于某个 `write_safe_root` 内 |
| 读 deny list | 仅凭据文件（`~/.ssh`、`~/.aws`、`~/.gnupg` 等） | root 即边界；root 外一律拒绝（包括 `/etc/hosts`，比上游更严） |
| 写 deny list | 系统目录 + 凭据 + 可选 `write_safe_root` | root 即边界 + 可选 `write_safe_root`；未逐条复刻系统目录清单 |
| stale-edit guard | `read_file` 记录状态，未读/过期拒绝编辑 | 已实现（§3），per-session 缓存，文案逐字对齐 |

这些差异是沙箱模型差异，不是能力缺失；测试分别用 `Parity:` 或 `Parity-exempt:`
显式标注。

## 7. 组与分层接线

* `builtin.Defs()` 现在按 `read_file, write_file, edit_file, search_files,
  terminal_exec, ...` 顺序暴露 5 个文件/终端基础工具。
* `Toolbox.Execute` 新增 `edit_file`、`search_files` 分派，执行仍走沙箱。
* `code_editing` 分类（`read_file`, `edit_file`）全部可解析；会话目录
  `newSessionToolCatalogue` 同时把四个文件工具记录到 `files-tools` harness 组
  （`ToolRegistry.RecordBuiltinToolGroup`），因此 Tool Library 与
  `@server:files-tools` 分类能看到完整的 4 个工具，尽管它们由 builtin 执行而非注册表执行。
* 如果同名 `files-tools` MCP server 已经注册，记录逻辑不覆盖它。

## 8. 测试与 parity 注解

`internal/tools/builtin/file_ops_test.go` 逐条对拍
`tools/tests/tools/test_file_ops.py`；`file_ops_residual_test.go` 覆盖本次三个残差：

* read_file hashline：`N:hhhh|content` 逐字形状与固定哈希值（`a686`/`0000`/`2652`/
  `425e`/`1143`）、空行、offset/limit 范围、CRLF、最后一行无换行、超长行不截断。
* stale-edit guard：未读拒绝（write/edit）、读后放行、外部改动拒绝、新文件直接写、
  链式编辑不自失效、会话隔离、`(size, mtime, sha256)` 三种状态、同内容重写仍 FRESH、
  删除→STALE、LRU 上限、并发访问、patch 模式只记录不设防。
* write_safe_root：上限外拒绝（文件不落盘）、列表任一命中允许（含 root 外的绝对路径）、
  上限内允许、读不放宽、edit replace 与 patch 模式同样受约束。

既有覆盖：路径策略、fuzzy 策略、patch 两阶段、search 各模式与防循环。

每个 `func Test...` 都带 `// Parity: tools/tests/...py::test_...` 或
`// Parity-exempt: <原因>`，`make parity` / `make parity-targets` 均 0 缺口。

## 9. 未对齐项

* 读路径仍严格限定在沙箱 root 内（上游绝对路径原样可读）：这是 Go 沙箱的既定边界，
  `write_safe_root` 只补写侧语义。
* 因此 `edit_file` 的目标若在 root 外、仅因 `write_safe_root` 而可写，仍会先被 stale
  guard 拦下（guard 要求 `read_file`，而读不能越出 root）。`write_file` 不受此影响。
* 上游系统/凭据写的 deny list（`/etc`、`~/.ssh` 等）未逐条复刻：root 边界已覆盖，
  配置 `write_safe_root` 后仍以 root + 上限的交集为准。
* `before_write` 钩子、`session_cwd` 注入未移植；由 `Toolbox` 构造时绑定的 root 和
  原子写入替代。
* `write_file` 成功文案为 `Wrote N bytes to <path>`（上游 `Created/Updated <path>
  (N bytes, M lines)`）；错误与 guard 文案已对齐，成功文案属既有差异，本次未改。
* 内容搜索使用 RE2：不支持反向引用等 Python `re` 专有语法；非法模式返回
  `Error: invalid regex: ...`。
* 上游 rg 路径的 `.gitignore` 语义未复刻（Go 遍历使用固定 skip 集 + 隐藏目录规则）；
  上游自己的 Python fallback 同样不读 `.gitignore`。
