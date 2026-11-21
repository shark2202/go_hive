# 首轮 Prompt / Skill Catalog / turn_context 逐字对照（Python 参考 vs Go 目标）

> 状态：**已实测**（2026-11-21，双跑探针 + 本地 recording stub LLM；不触外网）
> 探针：`go_hive/scripts/prompt_catalog_parity.py`（渲染 golden）、`go_hive/scripts/prompt_system_parity.py`（live 双跑）、
> `go_hive/scripts/prompt_system_sections.py`（分段归因）
> 生成器：`go_hive/scripts/gen_queen_prompt_sections.py`（从上游模块生成 queen 静态提示词段 + golden）
> 证据：`go_hive/docs/e2e-assets/prompt-catalog/`
> 关联：`skills-api.md`（catalog 渲染）、`reminders-coverage.md`（reminder 矩阵）、`sse-payload-parity.md` §4.6
> （`system_prefix_sha` 的值级差异即本文 §3 的系统提示词文本差）

## 1. 范围与方法

同一剧本、同一 fixture、同一配置下抓两侧：

1. 独立临时 `HIVE_HOME` + `HOME`；`configuration.json` 的 `llm` 指向各自的 recording stub（OpenAI 兼容，
   记录完整请求体），`turn_context.probe.enabled=true`（见 §5）。
2. 两侧同一技能 fixture：`go_hive/go-hive/internal/skills/testdata/prompt-catalog/fixture.json`。
   含 NBSP 截断、300/304 字符边界、首句规则、XML 转义、YAML colon 修复、`visibility` 相位、
   跨 scope 重名、symlink 目录等用例；`small`/`edge` 为磁盘 profile，`caps`/`caps_compact` 为合成表。
3. Python：`uv run --offline hive serve`；Go：`hive serve`。`POST /api/sessions {"queen_name":"queen_growth"}`
   → 抓开场轮请求；再发一条用户消息 → 抓用户轮请求。
4. 归一化：`<HIVE_HOME>` / `<HOME>` / `<BUNDLED>` / `<REPO>` / `<SESSION_ID>` / `<TIMESTAMP>` / `<PORT>`，
   语义文本逐字保留。macOS 上探针根目录在 `/var/folders` 下，运行时可能持 `/private/var/...` 拼写，
   两种拼写都先归一（否则 capture 会随 `TMPDIR` 漂移 112 chars 的 `/private` 前缀）。

渲染 golden（不经服务器）：按 `queen_orchestrator` 同款 `SkillsManagerConfig`（queen_id、queen overrides、
`queen_ui` extra scope、无 project root、skip community）渲染 `none / independent / colony` 三个相位；
外加 description-cap 合成表（full 与 compact 两种渲染）、`<system-reminder>` 包装、turn-context 正文。

## 2. Skill catalog：逐字一致（10/10）

`prompt_catalog_parity.py` 当前 **10/10 identical**（canonical 后逐字）：

| case | Python chars | Go chars | 覆盖点 |
| --- | ---: | ---: | --- |
| `small.none` | 3,615 | 3,615 | 磁盘发现 + compact 渲染（17 skills，仅 name/location） |
| `small.independent` | 4,394 | 4,394 | 相位过滤（去掉 worker/colony 专属）+ full 渲染 |
| `small.colony` | 4,808 | 4,808 | colony 相位（含 `worker-delegation`） |
| `edge.none` | 4,987 | 4,987 | 全部边界 skill（compact） |
| `edge.independent` | 3,893 | 3,893 | 相位过滤 + 重名/scope/symlink |
| `edge.colony` | 4,169 | 4,169 | colony 相位 |
| `caps.none` | 3,966 | 3,966 | description cap 表（首句/NBSP/300/304/XML 转义） |
| `caps.compact` | 3,088 | 3,088 | >5000 字符的 compact 回退（两侧同阈值） |
| `turn-context` | 45 | 45 | turn-context 正文（§5） |
| `reminder.block` | 222 | 222 | `<system-reminder>` 包装 + footer |

live 抓包里的 **skills catalog 段**（independent 相位，20 skills）：两侧各 **3,985 chars**，逐字一致
（`reminder-catalog.{python,go}.txt`；bytes 数 3,997 因 fixture 含中文名）。

> compact 判定口径：两侧都对 `<available_skills>` XML 本体做 `<= 5000`（Go 用 rune 数，Python 用 code point 数，
> 等价）；XML 内含 `<location>` 绝对路径，因此**同一技能集合在路径更长时会翻到 compact**——这是上游行为，
> 两侧一致。fixture 因此把各 profile 放在阈值两侧的安全距离，并用 `/fixture/skills` 固定根的 `caps*` 用例
> 专门验证阈值两侧。

### 2.1 本车道修复的渲染差异

| # | 问题 | 证据 | 修复 | 测试 |
| --- | --- | --- | --- | --- |
| 1 | **会话 catalog 未做相位过滤**：Go 把未过滤 catalog 交给 `skills_catalog` reminder；Python 用 `skills_catalog_prompt_for_phase(live phase)` | 修复前 independent 相位 Go 多播 `quality-monitor` / `error-recovery` / `note-taking` / `context-preservation` / `colony-progress-tracker` / `worker-delegation` 6 个 skill | `queenSkillsCatalog.CatalogForPhase`；`loader.go`、`loader_preloaded_session.go` 用 `SessionPhaseLabel(record)` 闭包（每轮读 live phase，fork 后自动换 colony 视图） | `TestQueenSkillsCatalogForPhaseFiltersVisibility`（cli） |
| 2 | **description 截断的尾随空白**：Go 用 `TrimRight(cutset " \t\n\r\f\v")`，Python `str.rstrip()` 按 Unicode White_Space（含 NBSP、U+001C–001F） | `caps.none` 的 `cap-nbsp-at-cut`：Python 输出 `…n…`，Go 残留 `\u00a0` 后接 `…` | `pyStrip/pyRStrip/pyIsSpace`（与 Python `str.strip/rstrip` 同 whitespace 集合），`CapDescription` 改用 | `TestPromptCatalogParityGolden`（skills） |
| 3 | **symlink 技能 location**：Python 存 `Path.resolve()`，Go 存 `filepath.Abs`（保留链接路径，且不解析 `/var → /private/var`） | 真实用户目录 `~/.agents/skills/ego-browser → app bundle`：Python `<app>/.../SKILL.md`，Go `~/.agents/skills/ego-browser/SKILL.md` | `resolveSkillPath`（`EvalSymlinks` → `Abs`）用于 `Location`/`BaseDir`；fixture 增加 `symlink-view -> symlink-target` | 同上 + `edge.*` golden |
| 4 | **bundled 提取根过长把默认 catalog 顶过 compact 阈值**：compact 判定数的是含绝对路径的 XML 字符数；macOS `TMPDIR=/private/var/folders/…` 且 resolve 后再 +8 字符/技能，13 技能时 full XML 5039 > 5000 → 整个 bundled catalog 变 compact（描述全丢），而 Python 用包内短路径仍为 full | `TestManagerReloadRefreshesCatalogPrompt` 在 main 4935（full）→ 修复后 5039（compact）翻转 | 提取根前缀 `hive-bundled-skills-` → `hive-sk-`（临时目录名，仅缩短路径），full XML 回到 ~4,894（余量 ~106） | `TestManagerReloadRefreshesCatalogPrompt`（skills） |

修复只改渲染口径，不合并不删减技能、不改 catalog 内容。

## 3. 首轮 system prompt：逐段逐字一致（P-01/P-02/P-03 已修复）

live 双跑（同一 queen `queen_growth`、同一 fixture）：Python **21,584 chars**，Go **21,584 chars**，
**整段逐字一致**——分段归因显示 `common prefix = 21,584`（即全文），Python-only 段 0 chars / Go-only 段 0 chars，
`system-prompt.diff` 0 字节。分段归因见 `system-prompt.sections.{json,txt}`。

| 段 | Python | Go before | Go after | 状态 |
| --- | ---: | ---: | ---: | --- |
| queen identity + soul（`<core_identity>` … `<core_skills>`） | 2,576 | 2,576 | 2,576 | 逐字一致 |
| `queen_character_core`（对齐/标点/记忆总纲） | 529 | 0 | 529 | 逐字一致 |
| `queen_role_independent`（INDEPENDENT 职责、`suggest_colony`、`write_skill`） | 2,012 | 0 | 2,012 | 逐字一致 |
| `queen_tools_independent`（`# Tools`：planning / file I/O / browser / colony handoff） | 1,788 | 0 | 1,788 | 逐字一致 |
| `queen_behavior_always`（`# System Rules` + task plan + `<pivot>` + file/share/draft + `## Communicating with the user` + `## Your Memory`） | 13,404 | 0 | 13,404 | 逐字一致 |
| `queen_behavior_independent`（large-scale 规则 + `<ask_user_for_colony_scope>`） | 1,087 | 0 | 1,087 | 逐字一致 |
| `credentials_block`（`# Credentials`，provider 计数摘要） | 184 | 0 | 184 | 逐字一致 |
| `pre_activated:hive.browser-automation` | 0 | 48,944 | 0 | 已移除（上游从不注入） |
| `pre_activated:hive.terminal-tools-foundations` | 0 | 10,674 | 0 | 已移除（同上） |
| **合计** | **21,584** | **62,196** | **21,584** | **逐字一致** |

> 分项之和 21,580，加 identity / base / credentials 之间的两个 `\n\n` 分隔符（4 chars）= 21,584。
> 段名与 `system-prompt.sections.txt` 对应：文中的 `queen_system_rules` / `queen_communication` /
> `queen_memory_instructions` 三段合起来就是 Go 常量 `queenBehaviorAlways`（上游在模块层把它们拼成
> `_queen_behavior_always` 后整体拼进相位提示词，Go 保留同一粒度）。
> 本 build 的 `CRM_IN_THIS_BUILD=False`（`framework.crm` 不在构建内），因此系统提示词不含 CRM 段；
> 生成器直接从上游模块取值，构建开关翻转时重新生成即同步。

### 3.1 装配实现

1. **段文本（逐字来源）**：`go_hive/scripts/gen_queen_prompt_sections.py` 导入上游
   `framework.agents.queen.nodes` 与 `framework.orchestrator.prompting`，把每个相位段写成 Go 源码
   `go_hive/go-hive/internal/queen/prompt_sections.go`（逐行字面量，文件头标注 `Code generated … DO NOT EDIT`）。
   生成器还会：
   - 断言上游段里**没有** `<!-- vision-only -->` 标记（出现即报错，要求先实现 `finalize_queen_prompt(has_vision)`）；
   - 把 Python 渲染的相位提示词与 credentials 段冻结成 golden（`internal/queen/testdata/prompt/`）；
   - **重新推导**上游整段首轮提示词（profile identity + `\n\n` + 相位段 + `\n\n` + credentials），与
     `system-prompt.python.txt` live capture 逐字比对后写出 `internal/server/testdata/prompt/queen_growth.independent.python.txt`。
     `--check` 可判定这些产物是否过期。
2. **相位拼装**：`internal/queen/static_prompt.go` 移植
   `QueenPhaseState.get_static_prompt` + `queen_orchestrator.create_queen` 的两段拼装：
   `PhasePrompt(phase)`（INDEPENDENT = character core + role + tools + always + independent；
   COLONY = character core + role + tools + behavior_colony + always，共享规则在后）、
   `StaticPrompt`（identity / soul / base / credentials / mcp_cli / protocols，空段丢弃，`\n\n` 连接；base 永不省略）。
   `mcp_cli_prompt` 已接线（`ChatRuntimeDeps.MCPCLI` ← `mcpregistry.RegistryCLIServersPrompt`，
   上游 `registry_cli_servers_prompt` 的移植）：本 build 的 fixture 未配 CLI-mode server，两侧都为空段；
   `protocols_prompt` 在当前版本为空（progressive disclosure），Go 侧以空输入保留位次。
3. **queen chat 会话改走该拼装**：`internal/server/chat_runtime.go::chatQueenStaticPrompt` 取代通用
   `agentloop.BuildSystemPromptForContext`。通用 builder 会执行 tool-gated 预激（`AugmentCatalogForTools`
   把整篇 skill body 追加到 catalog 段），而**上游 queen 路径从不经过它**：`get_static_prompt` 作为
   `dynamic_prompt_provider` 直接绕过 spec 构造（`agent_loop.py` 有 provider 即不重建 spec）。
   通用 worker/colony 路径的预激**保留不动**（`internal/agentloop/prompting.go`、`internal/skills/tool_gating.go`）。
   工具能力不变：首轮 `tools[]` 两侧仍是 24 个 name（`live-diff.json` meta 一致），
   browser/terminal 指南改为上游位置——`# Tools` 段的 `hive-browser open <url> --json` 入口 +
   `MUST Follow the browser-automation skill protocol` 指针，技能本体留在 catalog 里按需 `cat` 读取。
4. **credentials 段**：`internal/queen/static_prompt.go::CredentialsSummaryBlock` 逐字移植
   `build_credentials_summary`（provider 计数、`sorted` 排序、多账户 `name (n)`、无账户文案）；
   loader 注入真实账户快照（`ChatRuntimeDeps.Credentials` ← `buildSessionCredentialAdapter`，与凭据路由同一 store）。
   上游该段每轮 TTL 缓存重算，Go 在会话构建/恢复时快照一次（见 §6 P-07）。
5. **测试**：`internal/queen/static_prompt_test.go`（golden 逐字、段序、无预激 body、credentials 两种形态、
   拼装次序）与 `internal/server/chat_queen_prompt_test.go`（**整段首轮提示词 == Python capture 逐字**、
   相位切换、真实 `NewChatRuntime` + `browser_*`/`terminal_*` 工具下无预激 body、连接账户摘要）。

### 3.2 colony 相位

colony 相位 base prompt 同样由生成器逐字生成（20,869 chars：character core + COLONY role + COLONY tools +
delegation loop/operating rules + 共享规则），由 golden 与段序测试钉住；但本车道未跑 colony 相位的 live 抓包
（探针只建 DM 会话），登记为 P-07。

## 4. reminder 与 searchable-tools manifest

- `<system-reminder>` 包装逐字一致：`<system-reminder>\n{joined}\n\n{footer}\n</system-reminder>`，
  footer 为 `Only act on this if relevant to the current work. NEVER mention this reminder to the user.`
  （golden `reminder.block`，222 chars；两侧 hub 的 join/trim 语义一致）。
- 首轮 messages 的 searchable-tools manifest **逐字一致**（307 / 307 chars；`reminder-searchable.diff`
  与 `first-turn-messages.diff` 均 0 字节）：
  - Python：首轮只播报 2 个非 eager 的 allowlisted 工具（`chart_render`、`web_scrape`）。
    运行时日志：`allowlist=25, always_enabled=13, gateable=28, independent=35 -> allowed=22, eager=20`，
    searchable = allowed − eager = 2。
  - Go：lazy 层**仍可加载** 51 个 flat 工具，但**播报集**按同一规则收窄——per-queen allowlist
    （`tools.json` sidecar 优先，否则 role-default 分类经 MCP catalog 展开，`@server:chart-tools` →
    `chart_render`）∩ deferred − eager = 同样的 2 个。
  - 收敛点在**清单文本层**，不在工具面：`search_tools` 的 `select:` / keywords / `inventory` 仍覆盖全部
    51 个 deferred 定义，「schema 未加载」守卫也仍对每个 deferred 工具生效。Go-only 的 49 个 flat 目录工具
    从「首轮播报」变成「可搜索加载」，能力没有删减。
- 工具**调用面**（首轮 `tools[]`）两侧 24 个 name 已在既有 `tool-flat-surface.md` 对齐；本文只覆盖 reminder 文本。

### 4.1 manifest 过滤实现

| 层 | 位置 | 作用 |
| --- | --- | --- |
| 解析 per-queen allowlist | `internal/cli/tool_surface_manifest.go::SearchableManifestNames` → `server.QueenToolsAllowlist`（复用 `load_queen_tools_config`） | `tools.json` sidecar 优先，否则 role-default 分类 × MCP catalog；未知 queen 按上游 allow-all 从 catalog 解析；共享默认 queen 无 role default → 不过滤 |
| 收窄播报 | `internal/server/chat_tool_tiers.go::chatManifestFilter`（`chatToolTier.searchableTools`） | 只过滤 reminder 的 searchable 视图；`ToolTierState` 与 `GetSearchableTools` 不变 |
| 注入 | `internal/cli/loader.go` / `loader_preloaded_session.go` → `ChatRuntimeDeps.ManifestToolNames` / `ManifestFiltered` | 创建会话与 preload 会话同一口径；`Filtered` 区分「空 allowlist（不播报）」与「不过滤（全量播报）」 |
| golden / 回归 | `internal/server/testdata/prompt/searchable-manifest.python.txt`、`internal/server/chat_manifest_filter_test.go`、`internal/cli/tool_surface_manifest_test.go` | 逐字钉住 307 chars；并断言 inventory 仍列全量 deferred、`select:csv_read` 仍可加载并进入下一轮 `tools[]`、未加载直调仍被拒 |

## 5. turn_context：配置键、注入正文与位置

两侧 `configuration.json` 使用同一 section：

```json
{"turn_context": {"probe": {"enabled": true}}}
```

- **配置键口径**：`turn_context.<name>.enabled`（1 个键路径）；section 必须是对象且 `enabled` 为真值才启动。
  两侧读取/启动语义一致（`server/turn_context.go` ↔ `framework/server/turn_context.py`），既有
  `configuration.md` 已列为 aligned。
- **安装面（entry-point 等价物）**：Python 通过 `hive.turn_context` entry point 发现并启动（本次探针在
  `PYTHONPATH` 放了一个 fake dist-info 包，日志 `turn context 'probe' started`）；Go 无 entry point 机制，
  以编译期注册表承载安装面——二进制内置 `probe` 包（`internal/server/turn_context_probe.go`，
  `init()` 调 `RegisterTurnContextProvider`），同一份 `turn_context.probe.enabled=true` 配置在两侧都启动
  一个同名包。未配置/未启用时内置包不启动、不注入（`TestChatRuntimeTurnContextUnconfiguredDoesNotInject`
  钉住，启动日志按上游口径报 `installed but off`）。
- **注入正文与位置（逐字一致）**：
  - Python live 抓包：用户轮注入独立 `<system-reminder>`，正文
    `Turn-context probe body for the parity probe.`，位置在 `[timestamp] PARITY_PROBE_MESSAGE` 之前
    （`user-turn-messages.python.txt`）。开场轮因 `user_text` 为空值守卫不注入（两侧同规则）。
  - Go live 抓包（本次）：同样在用户消息前注入同一正文（`turn-context-live.go.txt` 45 chars；
    `user-turn-messages.diff` 0 字节）。两条连续 user 消息（reminder 块 + 带时间戳的用户文本）由
    `sanitizeForAPI` 以 `\n` 合并，与 Python `_sanitize_for_api` 的合并口径一致。
  - Go 真实启动路径：`configuration.json` → `StartTurnContexts` → `Runtime.Context` 的正文与 Python
    `context_for` 输出逐字一致（golden 45 chars；`prompt_catalog_parity.py` 的 `turn-context` case）。
  - 包装：两侧都是同一 reminder footer（§4），Go 的 `fireReminderPoint` 把 block 作为
    user message（`IsSystemReminder`）放在用户消息前，与 Python `_fire_reminder` 的位置语义一致
    （`agentloop/turn_context_reminder_test.go` 钉住 points/applies/render；`turn_context_parity_test.go`
    的 `TestChatRuntimeBuiltinProbeInjectsBeforeUserMessage` 钉住完整消息体与位置）。

**例外**：Go 的 overseer（colony 会话）catalog 仍走 `relaySkillsCatalogBlock`（手写
`<system-reminder>\nAvailable skills catalog: …`，无 footer），与 Python overseer 的
「静态 prompt 段 + 标准 reminder」路径不同。这是 `reminders-coverage.md` §7 已登记的
「实现形态差异」，本文复核后维持登记，不在本车道改。

## 6. 残余例外清单

| ID | 差异 | 类型 | 处理 |
| --- | --- | --- | --- |
| P-01 | Python queen 基础提示词（组合文本 18,820 chars：character core / INDEPENDENT role / tools / System Rules / communication / memory）未移植 | 结构（内容缺失） | **已修复**：`gen_queen_prompt_sections.py` 逐字生成 + `internal/queen/static_prompt.go` 拼装；live 抓包 21,584 chars 逐字一致 |
| P-02 | Python `# Credentials` 段（184 chars）未移植 | 结构（内容缺失） | **已修复**：`CredentialsSummaryBlock` + loader 注入账户快照；两种形态（无/有账户）都有 Python golden |
| P-03 | Go tool-gated 预激注入 2 个整篇 skill body（59,618 chars） | 结构（装配路径不同） | **已修复**：queen chat 会话改走 `chatQueenStaticPrompt`，不再经过 `BuildSystemPromptForContext`；worker/colony 路径的预激保留（上游同款） |
| P-04 | searchable-tools manifest：Python 2 个非 eager 工具（307 chars）vs Go 51 个 lazy 工具（1,029 chars） | 结构（工具面策略） | **已修复**：`internal/cli/tool_surface_manifest.go::SearchableManifestNames` 按上游规则解析 per-queen allowlist（`tools.json` sidecar 优先，否则 role-default 分类 × MCP catalog），`chatToolTier` 的 `chatManifestFilter` 只收窄**播报文本**；tier 仍持有全部 51 个 deferred 定义，`search_tools` 的 select/keywords/inventory 与「schema 未加载」守卫不变。live 双跑 manifest 307/307、opening messages 4,344/4,344 逐字一致 |
| P-05 | turn_context 安装面：Python entry point vs Go 编译期 registry | 结构（平台机制） | **已修复**：Go 二进制内置 `probe` 包（`internal/server/turn_context_probe.go`，init 注册 = entry-point 等价物），同一 `turn_context.probe.enabled=true` 配置两侧都启动并注入；live 双跑 `go_injected=true`、正文 45/45、`user-turn-messages.diff` 0 字节；未配置时不启动不注入 |
| P-06 | Go overseer catalog 走 relay 包装（`Available skills catalog:`，无 footer） | 结构（已登记 `reminders-coverage.md` §7） | 维持登记；colony 会话（非 chat queen）路径，不在本轮 queen 静态提示词范围内 |
| P-07 | 静态提示词的 credentials 段在 Go 侧是**会话构建/恢复时快照**（上游每轮 TTL 重算，attach/detach 下一轮可见） | 结构（构建时机） | **新增登记**：段文本逐字一致，差异只在刷新时机。Go 的 system prompt 目前整段在 `NewChatRuntime`/resume 时构建一次（上游靠 `dynamic_prompt_provider` 每轮重建；Go 的 loop 尚无该钩子，恢复它需要同时解决 cached prefix 失效）。下一 lane：给 `ChatRuntimeDeps` 增加每轮提示词重建时，credentials 段随其它动态段一起接上 |
| P-08 | colony 相位 base prompt（20,869 chars）无 live 抓包 | 证据缺口 | 文本由同一生成器逐字生成并有 golden/段序测试；探针只建 DM 会话，未覆盖 colony fork 后的 system prompt。下一 lane：给 `prompt_system_parity.py` 加 colony 剧本（`POST /api/sessions` with `colony_id`）后按本节口径归因 |

> 编号沿用 `prompt-parity.md` 既有 P-01…P-06（P-04 = searchable manifest，P-06 = overseer relay）；
> `skills-parity-report.json` 的 `structural_exceptions` 已同步为剩余项。

## 7. 复现与门禁

```bash
# 生成/校验 queen 静态提示词段与 golden（从上游 Python 模块取值）
uv run --offline python go_hive/scripts/gen_queen_prompt_sections.py
uv run --offline python go_hive/scripts/gen_queen_prompt_sections.py --check   # 期望 exit 0

uv run --offline python go_hive/scripts/prompt_catalog_parity.py      # 期望 10/10 identical，exit 0
uv run --offline python go_hive/scripts/prompt_system_parity.py       # 期望 system prompt + catalog 段 IDENTICAL
uv run --offline python go_hive/scripts/prompt_system_sections.py \
    --python go_hive/docs/e2e-assets/prompt-catalog/system-prompt.python.txt \
    --go go_hive/docs/e2e-assets/prompt-catalog/system-prompt.go.txt \
    --out go_hive/docs/e2e-assets/prompt-catalog

cd go_hive/go-hive
gofmt -l internal/cli internal/skills internal/server internal/agentloop internal/queen
go vet ./internal/cli ./internal/skills ./internal/server ./internal/agentloop ./internal/queen
go test ./internal/cli ./internal/skills ./internal/server ./internal/agentloop ./internal/queen -count=1
```

提示词相关的新测试（默认路径，无需外部服务）：
`internal/queen/static_prompt_test.go`（段 golden、段序、无预激 body、credentials 两种形态、拼装次序）、
`internal/server/chat_queen_prompt_test.go`（整段首轮提示词逐字对齐 Python capture、相位切换、真实
`NewChatRuntime` 下 `browser_*`/`terminal_*` 工具不再拉入整篇 skill body）、
`internal/skills/catalog_prompt_parity_test.go`（catalog 渲染 golden）、
`internal/server/chat_manifest_filter_test.go`（searchable manifest 307-char golden，且 inventory /
`select:` 加载 / 未加载守卫不复位）、
`internal/cli/tool_surface_manifest_test.go`（per-queen allowlist 解析：role-default 分类、`tools.json`
sidecar 优先、空 allowlist、allow-all 回退）、
`internal/server/turn_context_parity_test.go`（内置 `probe` 包经真实启动路径的正文 golden；
`TestChatRuntimeBuiltinProbeInjectsBeforeUserMessage` 钉住注入块逐字与「用户消息前」位置；
`TestChatRuntimeTurnContextUnconfiguredDoesNotInject` 钉住未配置时不注入）。

正常路径（默认测试）无需任何外部服务：catalog / reminder / turn-context / queen 提示词的 golden 是固定 fixture +
逐字文本；live 探针是 opt-in，不纳入 `make ci`。

## 8. 结论

- Skill catalog（含相位过滤、description 截断、symlink location、compact 回退、XML 转义、排序、分隔符）
  已**逐字一致**：渲染 golden 10/10，live catalog 段 3,985 chars 逐字一致。
- **首轮 system prompt 整段逐字一致**：21,584 / 21,584 chars，`system-prompt.diff` 0 字节
  （identity+soul 2,576 + queen 基础提示词 18,820 + credentials 184，再加 2 个 `\n\n` 分隔符）。
  Python 独有的 queen 段已移植，
  Go 独有的 59,618 chars 整篇 skill 预激注入已从 queen 路径移除（通用 worker/colony 路径保留）。
  工具调用面不变（首轮 `tools[]` 24 个 name 一致）。
- turn_context 的配置键、正文、包装与位置逐字一致；**live 注入两侧一致**（`go_injected=true`）：
  Go 二进制内置 `probe` 包承载 entry-point 安装面，`turn-context-live.{python,go}.txt` 45/45 chars，
  `user-turn-messages.diff` 0 字节；未配置时不启动不注入。
- **首轮 opening messages 整段逐字一致**：4,344 / 4,344 chars，`first-turn-messages.diff` 0 字节。
  首轮非 system 内容 = 单一 `<system-reminder>`（searchable-tools manifest 307 chars + skills catalog
  3,985 chars），两段都逐字对齐。
- 残余：credentials 段的刷新时机（P-07）、colony 相位无 live 抓包（P-08）、overseer catalog relay（P-06）。
  它们都不影响独立相位首轮 system prompt / opening messages 的逐字一致性；用户轮文本已无残余
  （turn_context 注入两侧逐字一致，`user-turn-messages.diff` 0 字节）。