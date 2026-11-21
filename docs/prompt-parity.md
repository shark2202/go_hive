# 首轮 Prompt / Skill Catalog / turn_context 逐字对照（Python 参考 vs Go 目标）

> 状态：**已实测**（2026-11-21，双跑探针 + 本地 recording stub LLM；不触外网）
> 探针：`go_hive/scripts/prompt_catalog_parity.py`（渲染 golden）、`go_hive/scripts/prompt_system_parity.py`（live 双跑）、
> `go_hive/scripts/prompt_system_sections.py`（分段归因）
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
   语义文本逐字保留。

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

## 3. 首轮 system prompt：分段归因（未改内容，列为结构差异）

live 双跑（同一 queen `queen_growth`、同一 fixture）：Python **21,584 chars**，Go **62,196 chars**。
分段归因见 `system-prompt.sections.{json,txt}`：

| 段 | chars | 归属 | 说明 |
| --- | ---: | --- | --- |
| queen identity + soul（`<core_identity>` … `<core_skills>`） | 2,578 | **两侧逐字一致** | `FormatQueenIdentityPrompt` + `soul.prompt_for/PromptFor` 渲染完全对齐（含 trigger 反应转写、负向约束、world lore） |
| `queen_character_core` | 529 | Python 独有 | 校准/标点/记忆总纲 |
| `queen_role_independent` | 2,013 | Python 独有 | INDEPENDENT 模式职责、`suggest_colony`、`write_skill` 契约 |
| `queen_tools_independent` | 1,788 | Python 独有 | `# Tools`（planning / file I/O / browser / colony handoff） |
| `queen_system_rules` | 11,050 | Python 独有 | `# System Rules`（`ask_user` / `task_create` / pivot / 文件 / 分享 / 起草规则） |
| `queen_communication` | 1,332 | Python 独有 | `## Communicating with the user` |
| `queen_memory_instructions` | 2,110 | Python 独有 | `## Your Memory` |
| `credentials_block` | 184 | Python 独有 | `# Credentials`（`_render_credentials_block`） |
| `pre_activated:hive.browser-automation` | 48,944 | Go 独有 | 整篇 SKILL.md body 被 tool-gating 预激注入静态提示词 |
| `pre_activated:hive.terminal-tools-foundations` | 10,674 | Go 独有 | 同上 |
| CJK 字符 | 0 / 0 | — | 两侧系统提示词均无中文，i18n 不是本差异来源 |

**归因**：

- Python 的 queen 系统提示词由 `QueenPhaseState.get_static_prompt()` 组装（`dynamic_prompt_provider`）：
  identity + soul + 相位基础提示词 + credentials + `mcp_cli_prompt` + protocols。当前版本
  `protocols_prompt == ""`（progressive disclosure），`mcp_cli_prompt` 为空。
- Go 的 chat queen 走通用 `agentloop.BuildSystemPrompt`：只有 identity（含 soul），再叠加通用 builder 的
  **tool-gated pre-activation**（`skills.AugmentCatalogForTools` 对 `AvailableTools` 里匹配前缀的 skill
  追加整篇 body）。
- 因此两边都不是「同一段文本的渲染差异」：Python 独有的是**尚未移植的 queen 基础提示词与 credentials 段**；
  Go 独有的是**通用 builder 的预激注入**（Python 的 queen 静态提示词路径不跑这段）。
  这两项都直接改变模型可见文本，属结构性差异，不在「保持模型行为不变」的渲染修复范围内。

**建议（后续车道，需产品/提示词评审）**：

1. 移植 queen 基础提示词（`core/framework/agents/queen/nodes/__init__.py` 的
   `_queen_character_core` / `_queen_role_independent` / `_queen_tools_independent` / `_queen_behavior_*`
   / `_queen_communication` / `_queen_memory_instructions`）与 `_render_credentials_block`，在 Go 侧实现
   等价 `QueenPhaseState.get_static_prompt`（identity + soul + 相位基础段 + credentials + protocols）。
2. 迁移后让 chat queen 走 queen 专用静态提示词构建器，并**对 queen 路径跳过
   `AugmentCatalogForTools`**（通用 worker/agent 路径保留），避免与 catalog 的按需加载重复
   （browser-automation / terminal-tools-foundations 已在 catalog 中，可 `cat` 读取）。
3. 在完成 1 之前，Go 当前内容全部保留（不为了缩短而删减模型必需的工具指南）；本车道只登记数值与证据。

## 4. reminder 与 searchable-tools manifest

- `<system-reminder>` 包装逐字一致：`<system-reminder>\n{joined}\n\n{footer}\n</system-reminder>`，
  footer 为 `Only act on this if relevant to the current work. NEVER mention this reminder to the user.`
  （golden `reminder.block`，222 chars；两侧 hub 的 join/trim 语义一致）。
- 开场轮 messages 的差异**仅**来自 searchable-tools manifest（`first-turn-messages.diff` 只有新增工具名）：
  - Python：307 chars，首轮只播报 2 个非 eager 的 allowlisted 工具（`chart_render`、`web_scrape`）。
    运行时日志：`allowlist=25, always_enabled=13, gateable=28, independent=35 -> allowed=22, eager=20`，
    searchable = allowed − eager = 2。
  - Go：1,029 chars，播报 51 个 flat directory 工具（含 `csv_*`、`excel_*`、`edit_file`、`read_file`、
    `terminal_job_*`、`tracker_*` 等）。这是 `tool_surface_flat.go` 的既定 lazy 层设计：
    「everything else the build can execute is deferred」。
- 结论：**结构差异（工具面策略）**，不是渲染 bug。收敛为逐字需要把 Go 的 lazy 层收缩到 Python 的
  per-queen allowlist（会删掉 49 个当前可 load/可执行的工具路由），与「不删减模型必需内容」冲突；
  建议单独以工具面车道决策（若要对齐，应在 `LazyModelDefs` 前套用与 Python 相同的
  `enabled_mcp_tools ∩ phase_tools − eager` 过滤，并补 queen profile 类别→allowlist 的同一映射）。
- 工具**调用面**（首轮 `tools[]`）两侧 24 个 name 已在既有 `tool-flat-surface.md` 对齐；本文只覆盖 reminder 文本。

## 5. turn_context：配置键、注入正文与位置

两侧 `configuration.json` 使用同一 section：

```json
{"turn_context": {"probe": {"enabled": true}}}
```

- **配置键口径**：`turn_context.<name>.enabled`（1 个键路径）；section 必须是对象且 `enabled` 为真值才启动。
  两侧读取/启动语义一致（`server/turn_context.go` ↔ `framework/server/turn_context.py`），既有
  `configuration.md` 已列为 aligned。
- **安装面差异（结构）**：Python 通过 `hive.turn_context` entry point 发现并启动（本次探针在
  `PYTHONPATH` 放了一个 fake dist-info 包，日志 `turn context 'probe' started`）；Go 无 entry point 机制，
  用 `RegisterTurnContextProvider` 在编译期注册。本次 Go 二进制未链接任何 package → live 不注入
  （`go_injected=false`，且 runtime 保持空、不阻塞回合）；Python live 注入成功。
- **注入正文与位置（逐字一致）**：
  - Python live 抓包：用户轮注入独立 `<system-reminder>`，正文
    `Turn-context probe body for the parity probe.`，位置在 `[timestamp] PARITY_PROBE_MESSAGE` 之前
    （`user-turn-messages.python.txt`）。开场轮因 `user_text` 为空值守卫不注入（两侧同规则）。
  - Go：`TestTurnContextBodyParityGolden` 走真实启动路径
    （`configuration.json` → `StartTurnContexts` → `Runtime.Context`），正文与 Python `context_for` 输出
    逐字一致（golden 45 chars；`prompt_catalog_parity.py` 的 `turn-context` case）。
  - 包装：两侧都是同一 reminder footer（§4），Go 的 `fireReminderPoint` 把 block 作为
    user message（`IsSystemReminder`）放在用户消息前，与 Python `_fire_reminder` 的位置语义一致
    （既有 `agentloop/turn_context_reminder_test.go` 钉住 points/applies/render；Python 侧
    `framework/server/tests/test_turn_context.py`）。

**例外**：Go 的 overseer（colony 会话）catalog 仍走 `relaySkillsCatalogBlock`（手写
`<system-reminder>\nAvailable skills catalog: …`，无 footer），与 Python overseer 的
「静态 prompt 段 + 标准 reminder」路径不同。这是 `reminders-coverage.md` §7 已登记的
「实现形态差异」，本文复核后维持登记，不在本车道改。

## 6. 例外清单（本车道不改）

| ID | 差异 | 类型 | 处理 |
| --- | --- | --- | --- |
| P-01 | Python queen 基础提示词（18,822 chars：character core / INDEPENDENT role / tools / System Rules / communication / memory）未移植 | 结构（内容缺失） | 登记 + §3 建议；不删 Go 现有内容 |
| P-02 | Python `# Credentials` 段（184 chars）未移植 | 结构（内容缺失） | 同上；Go 的 credentials 走工具 + CLI |
| P-03 | Go tool-gated 预激注入 2 个整篇 skill body（59,618 chars） | 结构（装配路径不同） | 保留；建议随 P-01 迁移后对 queen 路径关闭 |
| P-04 | searchable-tools manifest：Python 2 个非 eager 工具 vs Go 51 个 lazy 工具 | 结构（工具面策略） | 登记；按 §4 建议单独立项 |
| P-05 | turn_context 安装面：Python entry point vs Go 编译期 registry | 结构（平台机制） | 正文/位置已验证一致；live Go 未链接 package 属预期 |
| P-06 | Go overseer catalog 走 relay 包装（`Available skills catalog:`，无 footer） | 结构（已登记 `reminders-coverage.md` §7） | 维持登记 |

## 7. 复现与门禁

```bash
uv run --offline python go_hive/scripts/prompt_catalog_parity.py      # 期望 10/10 identical，exit 0
uv run --offline python go_hive/scripts/prompt_system_parity.py       # 期望 catalog 段 IDENTICAL
uv run --offline python go_hive/scripts/prompt_system_sections.py \
    --python go_hive/docs/e2e-assets/prompt-catalog/system-prompt.python.txt \
    --go go_hive/docs/e2e-assets/prompt-catalog/system-prompt.go.txt \
    --out go_hive/docs/e2e-assets/prompt-catalog

cd go_hive/go-hive
gofmt -l internal/cli internal/skills internal/server internal/agentloop
go vet ./internal/cli ./internal/skills ./internal/server ./internal/agentloop
go test ./internal/cli ./internal/skills ./internal/server ./internal/agentloop -count=1
```

正常路径（默认测试）无需任何外部服务：catalog / reminder / turn-context 的 golden 是固定 fixture +
逐字文本；live 探针是 opt-in，不纳入 `make ci`。

## 8. 结论

- Skill catalog（含相位过滤、description 截断、symlink location、compact 回退、XML 转义、排序、分隔符）
  已**逐字一致**：渲染 golden 10/10，live catalog 段 3,985 chars 逐字一致。
- turn_context 的配置键、正文、包装与位置逐字一致；安装面为已知平台差异。
- 首轮 system prompt 的公共前缀（identity + soul，2,578 chars）逐字一致；其余差异按 §3 分段归因为
  「Python 未移植段」与「Go 预激注入段」两类结构差异，附后续迁移建议，本车道未删改模型必需内容。