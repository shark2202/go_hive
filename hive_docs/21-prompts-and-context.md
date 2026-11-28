# 21 · 提示词与上下文的构成（全场景穷举）

> 目的：回答“Hive 的提示词到底由什么拼成”，覆盖初始系统提示词、每个角色的 system prompt、每轮注入的上下文，以及所有后台辅助 LLM 的提示词。
> 基线：`aden-hive` / `aden-tools` **1.0.15**，源码 HEAD `8ad4dbf23`（自报告基线 `f195ebaf3` 以来，本文涉及的 `core/framework/agents/queen/`、`agent_loop/`、`queen_orchestrator.py`、`colony_runtime.py` 无代码变更）。
> 证据：以源码符号与实际运行结果为准。文中的字符数是本机 2026-11-27 实测（`python` 直接 import 模块后取 `len()`），随 profile、技能数量、凭据数量变化。

---

## 1. 一句话结论

**Hive 没有“一份系统提示词”。** 它在每个回合现场拼装五类文本，并分别放进请求的不同位置：

1. **system 静态块**：人格身份 + Soul + 阶段角色 + 工具说明 + 行为规则 + 环境块（凭据、CLI MCP、协议）。Queen 的这块约 **2.1–2.6 万字符**（≈5–6.5k tokens）。
2. **system 动态尾**：`narrative`。当前 Queen 路径刻意留空；时间戳已移到消息里，避免破坏缓存。
3. **tools 数组**：工具名、description、JSON Schema。**大量行为规则实际写在这里**（`ask_user`、`report_to_parent`、`run_playbook`、`tracker_*`）。
4. **conversation**：首条用户消息、`<system-reminder>` 块、外部事件、工具结果、压缩摘要。技能目录、可搜索工具清单、召回记忆、任务计划、Worker 报告都在这里，而不是 system prompt。
5. **后台辅助提示词**：Queen 路由、记忆召回、反思、Soul 反思、时间线、会话标题、Sentinel、压缩、judge、视觉回退等十余个独立 LLM 调用。

最重要的实测发现：**在本基线的 live 服务路径上，并行 Worker 的 `AgentSpec.system_prompt` 是空字符串**。`worker_definition.WORKER_SYSTEM_PROMPT`（7,613 字符的 Worker 规则）只写进了 `worker.json`，而 live 的 `ColonyRuntime` 从不读取它；Worker 实际只拿到“技能目录 + 绑定/账号块 + task 首条消息”。详见 §7.5。

---

## 2. 请求里到底有什么

一次 Queen 回合发出的 chat completion 请求，结构如下（`core/framework/llm/litellm.py`）：

```text
POST /chat/completions
├─ tools[]                          ← 工具 schema（也是提示词）
├─ messages[0] = system
│    ├─ block 0  "cache_control": ephemeral   ← 静态前缀（Queen 的全部常驻提示）
│    └─ block 1                                ← 动态后缀（narrative；Queen 当前为空）
├─ messages[1] user                 ← [2026-11-27 12:00 CST] user_request: ...
├─ messages[2] assistant
├─ messages[3] tool                 ← 工具结果（可被截断/落盘/微压缩）
├─ ...
│    ├─ <system-reminder> ... </system-reminder>
│    ├─ [External event] ...
│    ├─ [TRIGGER: ...] ...
│    ├─ [WORKER_REPORT] ...
│    └─ 压缩摘要（role=user）
└─ max_tokens / temperature / ...
```

关键机制：

| 机制 | 位置 | 说明 |
|---|---|---|
| 静态 system 块 | `litellm.py:398` `_build_system_message` | 当有动态后缀且模型支持 `cache_control` 时，发成两个 text block，只在第一块打 `ephemeral` 缓存标记。 |
| 动态 system 尾 | 同上 | 不打缓存标记。设计上只放“每轮会变”的内容；Queen 已清空。 |
| 历史滚动缓存点 | `litellm.py:484` | 给最后一条消息打 `cache_control`，让历史前缀跨回合复用。 |
| 工具能力过滤 | `agent_loop.py:1635` 附近 | 纯文本模型会隐藏图像工具；配置了视觉回退时保留。 |
| 注入时间戳 | `cursor_persistence.py:271` | 每条注入事件前加 `[YYYY-MM-DD HH:MM TZ]`，一旦写入就字节稳定。 |
| 提醒包装 | `reminders.py:342-353` | 多个 source 合并为单个 `<system-reminder>`，末尾统一加 `_FOOTER`：“Only act on this if relevant… NEVER mention this reminder…”。 |

> 结论：Hive 把“稳定但很长”的内容放 system 前缀吃缓存，把“会变”的内容放对话尾部；这条设计贯穿全文。

---

## 3. 五层构成模型

| 层 | 典型内容 | 谁生产 | 是否缓存 | 何时变化 |
|---|---|---|---|---|
| L1 身份 | 5 支柱 Queen profile（core / hidden background / psychological / behavior / lore / skills） | `format_queen_identity_prompt` | 是 | 会话开始时选定，之后不变 |
| L2 Soul | `<soul>` 第一人称自述条目 | `soul.prompt_for` | 是 | 每天最多一次反思后 |
| L3 阶段角色 | character core / role / tools / behavior | `agents/queen/nodes/__init__.py` | 是 | 阶段切换（DM↔Colony） |
| L4 环境 | 凭据摘要、CLI MCP 服务器、协议 | `queen_orchestrator` / `mcp_cli` | 是 | 增删凭据、改配置、换会话 |
| L5 动态 | 技能目录、可搜索工具、召回、任务、Worker 报告、触发器、压缩摘要 | 各 ReminderSource / loop | 否（在对话尾部） | 每次变化 |

`QueenPhaseState.get_static_prompt()`（`tools/queen_lifecycle_tools.py:402`）只负责 L1–L4；L5 全部由对话注入承担。这个分界是理解 Hive 提示词的第一把钥匙。

---

## 4. Queen 的初始系统提示词

### 4.1 入口与顺序

会话创建时：

```text
SessionManager._ensure_session_queen_identity (session_manager.py:634)
  ├─ 已预选 queen（session.queen_name != default）→ 直接用
  ├─ 冷恢复 → 从原会话 meta.json 读 queen_id
  └─ 否则 → LLM 路由分类器 select_queen(initial_prompt)（空消息直接默认 queen_growth）
        ↓
create_queen (queen_orchestrator.py)
  ├─ materialize_queen_identity (:319)  → phase_state.queen_identity_prompt
  ├─ 组合 DM / Colony 两套 base   (:1167 / :1183)
  ├─ SkillsManager 加载（只用于技能目录提醒，不进 system） (:1188)
  ├─ AgentContext(dynamic_prompt_provider=phase_state.get_static_prompt) (:1700)
  └─ ctx.input_data = {"user_request": initial_prompt} (:1975)
```

`get_static_prompt()` 的拼接顺序是固定的，用 `"\n\n"` 连接：

```text
1. queen_identity_prompt        ← 5 支柱人格
2. <soul> ... </soul>           ← 如非空
3. base_prompt                  ← 阶段角色 + 工具 + 行为（DM 或 Colony 各一套）
4. credentials_block            ← “Connected integrations” 摘要 + 本会话固定凭据
5. mcp_cli_prompt               ← 可从终端运行的 MCP 服务器清单
6. protocols_prompt             ← 当前为空（见 §4.6）
```

### 4.2 base_prompt 的内部顺序

DM（independent）：

```text
_queen_character_core
+ _queen_role_independent
+ _queen_tools_independent
+ _queen_behavior_always
+ _queen_behavior_independent
```

Colony：

```text
_queen_character_core
+ _queen_role_colony
+ _queen_tools_colony
+ _queen_behavior_colony
+ _queen_behavior_always
```

差异不只是“换了角色段”，**顺序也被刻意调换**：Colony 把 `_queen_behavior_colony`（委派五步 + 殖民地运行规则）放在通用 `_queen_behavior_always` **之前**，让“第一步先建 tracker 表”压过通用的“先 task_create”。DM 则保持 always → independent。

`_queen_behavior_always` 本身又是五块拼成（`nodes/__init__.py:750`）：

```text
ask_user 规则
+ [CRM 计划规则，仅 CRM_IN_THIS_BUILD 时]
+ task_plan 其余规则（跳过条件、顺序、粒度、心跳、verification_step、pivot、file_creation、sharing_files、draft_outbound_message）
+ Communicating with the user
+ Your Memory
```

### 4.3 实测体积（本基线，CRM 关闭）

| 段 | 字符数 | 说明 |
|---|---:|---|
| `_queen_character_core` | 529 | 关系/语气校准、不用破折号、记忆人 |
| `_queen_role_independent` | 2,012 | DM 模式：直接执行、suggest_colony、write_skill |
| `_queen_role_colony` | 631 | Colony 模式：tracker.db 已预置、以委派为主 |
| `_queen_tools_independent` | 1,788 | 文件 I/O、浏览器、handoff |
| `_queen_tools_colony` | 1,162 | 委派循环、trigger、凭据 CLI |
| `_queen_behavior_always` | 13,404 | 最大单块（约 3.3k tokens） |
| `_queen_behavior_independent` | 1,087 | 大规模任务/建群前问清 scope |
| `_queen_behavior_colony` | 5,143 | 五步委派 + 殖民地规则 |
| **DM base 合计** | **18,820** | |
| **Colony base 合计** | **20,869** | |
| `WORKER_SYSTEM_PROMPT` | 7,613 | 见 §7，但 live 未生效 |

身份块（`format_queen_identity_prompt(..., max_examples=0)`）实测 2,500–3,590 字符，13 个默认 Queen 在 2.5–3.6k 之间（`queen_sales` 最大 3,590）。因此：

- DM 初始 system prompt ≈ **21.3k–22.4k 字符** + 凭据/CLI 块；
- Colony 初始 system prompt ≈ **23.4k–24.5k 字符** + 凭据/CLI 块；
- 粗略按 4 字符/token 估算：**5.3k–6.5k tokens**，不含 tools schema。

### 4.4 身份块：5 支柱人格

`format_queen_identity_prompt`（`queen_profiles.py:533`）从 YAML profile 生成：

| 支柱 | 标签 | 内容 | 是否给用户看 |
|---|---|---|---|
| 1 | `<core_identity>` | “You are a queen agent, your name is X, Title.” + core_traits | 间接 |
| 2 | `<hidden_background>` | past_wound / deep_motive / behavioral_mapping | 明确“Strictly hidden” |
| 3 | `<psychological_profile>` | social_masks、anti_stereotype（第三人称自动改第二人称） | 否 |
| 4 | `<behavior_rules>` | 每次回复前的内部评估 + trigger→reaction 列表 | 否 |
| 5 | `<negative_constraints>` | 禁 corporate filler、禁止出戏解释 | 否 |
| 附 | `<world_lore>` / `<core_skills>` | habitat、lexicon、功能技能 | 部分 |
| 附 | `<roleplay_examples>` | few-shot 的“内部思考 + 外部回复” | live 不渲染 |

live 调用固定 `max_examples=0`（`queen_orchestrator.py:340`、`:1398`），所以**示例块永远不会出现在真实系统提示里**；它只用于 profile 编写和评测（`max_examples=None` 时渲染全部 4 条，约 2.4–3.0KB）。

Queen 的选择本身也是一次 LLM 调用：`_queen_selector_system_prompt`（`queen_profiles.py:667`，约 2,378 字符）把 13 个 Queen 的一行领域描述给分类器，要求只输出 `{"reason","queen_id"}`；空消息直接返回 `queen_growth`（`_DEFAULT_QUEEN_ID`，旁边的 reason 文案仍写“queen_technology”，属过期注释），失败也回退同一个默认值。带建群的选择器 `_colony_selector_system_prompt`（:796）在同一提示上追加 `colony_name` 要求。

### 4.5 Soul

`soul.render`（`soul.py:291`）输出：

```text
<soul>
## <section title>
- <item text> (m/d)
...
</soul>
```

`prompt_for`（`soul.py:308`）按 `soul.md` 与 `configuration.json` 的 mtime 缓存，因此每次迭代重建不产生 I/O。Soul 每天最多由 `soul_reflection` 改一次，所以放在缓存前缀里代价很低。

### 4.6 环境块

**凭据块**（`queen_orchestrator._build_credentials_provider:237`）：

- 默认只渲染**紧凑摘要**：`# Credentials` + provider 名称与数量，不列每个 alias；
- 只有女王通过 `credentials` 工具**显式附加**到本会话的凭据，才渲染完整 per-account 明细（`Pinned to this session:`）；
- 账号快照有 TTL 缓存，附加/解除每轮读取，新增凭据下一轮可见。

**CLI MCP 块**（`loader/mcp_cli.py:120`）：列出“可从 terminal 运行、但不在工具列表里”的 MCP 服务器，以及 `hive mcp run <server> --list` 的用法。

**协议块**：`phase_state.protocols_prompt` 来自 `SkillsManager.protocols_prompt`；但 `_do_load` 现在显式把它设为空：

```python
self._protocols_prompt = ""  # all skills use progressive disclosure now
```

（`skills/manager.py:205`）

因此在本基线，**Queen 的系统提示里没有独立的“Operational Protocols”大段**；默认技能全部退化为 `<available_skills>` 目录条目，按需 `cat SKILL.md`。

### 4.7 不在 system prompt 里的三样

这三样曾经在 system prompt，现在全部搬到对话尾部，原因是“放在 system 里会让整个消息历史的缓存前缀在每次变化时失效”：

| 内容 | 生产者 | 投递方式 |
|---|---|---|
| 技能目录（`<available_skills>`） | `QueenPhaseState.render_skills_catalog` | `SkillsCatalogReminderSource`（`tool_skill_reminders.py:153`），在 `SESSION_START` / `USER_PROMPT_SUBMIT` / `POST_TOOL_USE` / `POST_COMPACT` 变化时发 |
| 可搜索工具清单（未加载 schema 的工具名） | `phase_state.get_searchable_tools` | `SearchableToolsReminderSource`（:64），只发新增/移除的增量 |
| 召回记忆 | `recall_selector` / `memory_reminders` | 编排器直接 `inject_event("<system-reminder>…")`（`queen_orchestrator.py:1290`） |

技能目录的体量值得单独说：框架自带 12 个技能时目录全文 **5,527 字符**；本机技能总数 187 个（含 12 个框架技能）时目录变成 **28,419 字符**（超过 5,000 字符后自动切 compact 变体，丢弃 `<description>` 只保留名字）。它作为首轮提醒注入，等价于 7k tokens 的上下文成本。

### 4.8 视觉过滤

`finalize_queen_prompt`（`nodes/__init__.py:18`）会按模型能力裁剪 `<!-- vision-only -->…<!-- /vision-only -->` 块。当前源码中已没有实际标记（只有正则和调用），所以这一步在本基线是空操作，但机制保留给未来。

---

## 5. 每轮重建与缓存

`AgentLoop` 每次迭代都会：

```python
if ctx.dynamic_prompt_provider is not None:
    _new_prompt = ctx.dynamic_prompt_provider()      # 女王 = phase_state.get_static_prompt
    _new_suffix = ""                                 # 女王没有 suffix provider
    if 组合结果 != conversation.system_prompt:
        conversation.update_system_prompt(_new_prompt, dynamic_suffix=_new_suffix)
```

（`agent_loop.py:2017-2054`）

因此：

- **每个 iteration 都会重新调用一次 `get_static_prompt()`**，但只有字节变化才写回会话；
- 稳定性来自各段的缓存：身份在会话内不变、Soul 按 mtime、凭据按 TTL、CLI MCP 在启动时固定；
- **禁止**把分钟级时间、每轮召回、每轮技能目录放进 system；它们会破坏缓存前缀。代码注释在 `agent_loop/types.py` 与 `queen_orchestrator.py:1695` 明确写了这一点；
- 恢复（cold resume）时也会重建并刷新 system（`agent_loop.py:1463-1475`），所以升级提示词后旧会话会在下一次恢复时用上新版。

两套实现的关键差异：

| 项 | `agent_loop/prompting.py`（Queen / Worker / SDK） | `orchestrator/prompting.py`（传统图节点） |
|---|---|---|
| `focus_prompt` 位置 | 静态前缀 | 动态后缀（`--- Current Focus ---`） |
| `EXECUTION_SCOPE_PREAMBLE` | 无 | 有输出键时加入后缀 |
| 日期戳 | 无（`stamp_prompt_datetime` 注释说明无调用者） | 后缀末尾加 **天级** `Current date: ...` |
| narrative | 后缀 | 后缀，包在 `--- Context (what has happened so far) ---` |
| 召回记忆前置语 | 直接拼 memory | 先加“Relevant recalled memories…verify stale details”再拼 |

图节点把 focus 放在后缀、并每天改一次日期戳，意味着这条路径的缓存命中率天然低于 Queen 路径。

---

## 6. 用户消息、事件与“伪消息”

### 6.1 首条消息

Queen 的首条 user message 由 `_build_initial_message`（`agent_loop.py:6293`）生成：

```text
<key>: <value>          ← ctx.input_data 的全部键
...
Goal: <goal_context>    ← 仅当 Goal 有 criteria/constraints/context
```

Queen 的 `input_data` 只有一个键 `user_request`（`queen_orchestrator.py:1975`），`queen_goal` 没有成功标准/约束，所以首条实际是：

```text
[2026-11-27 12:00 CST] user_request: <initial_prompt>
```

如果会话创建时没有 `initial_prompt`，则首条消息为空，循环等待 `/chat`；恢复模式同样不造“Hello”。

### 6.2 后续用户消息

`/chat` → `inject_event` → `drain_injection_queue`（`cursor_persistence.py:186`）：

- 真实用户输入：`[stamp] <content>`，`is_client_input=True`（压缩时会逐字保留）；
- 外部事件：`[stamp] [External event] <content>`；
- 触发器批量：`[stamp]\n[TRIGGER: type/source]\nTask: ...\n{payload JSON}`，多个 trigger 合并为一条（`drain_trigger_queue`）；
- 图片：data URI 图片块随消息发送；纯文本模型走视觉回退，先由子模型转写成文字（`vision_fallback.py`）。

### 6.3 不是用户、但以 user 角色进入的内容

| 文本 | 生产者 | 说明 |
|---|---|---|
| `<system-reminder>…</system-reminder>` | ReminderHub | 15 个 source，穷举见 [20 Reminder 机制穷举](20-reminder-mechanism.md) |
| `[WORKER_REPORT]` + XML 字段 | `queen_orchestrator._on_worker_report:1783` | worker_id/status/summary/data/batch_remaining 等 |
| `[PLAYBOOK_COMPLETE]` | `tools/playbook_tools.py:899` | run_id/status/错误行 |
| `[PHASE CHANGE] Switched to COLONY/INDEPENDENT` | `queen_lifecycle_tools.py:231` / `:540` | 非工具触发的阶段切换 |
| `[SYSTEM] <doom loop / 预算 / stop>` | `agent_loop.py:6512` / `:5935` / `:5984` | 死循环告警、预算耗尽、宽限收尾 |
| `[Colony 'x' is ready and you are its queen…]` | `routes_sessions.py:488` | 建群后无 goal 时的开场 |
| `[Session handoff]\n<brief>` | 兄弟建群路径 | 新殖民地女王的第一条种子消息 |
| `[Name(queen_id)]: text` | `queen_mail.py:461` | 女王之间的信件，伪装成聊天应用消息 |
| 渠道前缀 | 外部 channel 插件 / `turn_context` | 时间、发送者、群聊最近上下文等，在进入会话前加 |
| 压缩摘要 | `conversation.py` 压缩 | `role=user` 的摘要消息，可能带 `PRESERVED VALUES` 头；客户端原始消息逐字保留 |

> 关键点：Hive 的“提示工程”很大一部分不是改 system prompt，而是**决定在对话的哪个位置插入哪段文本**。

### 6.4 建群 fork 的种子

从 DM 创建一个殖民地时，新女王的提示词并不是“复制旧系统提示”，而是 **Colony 系统提示 + 一段被处理过的历史**：

- **普通建群**：`fork_session_into_colony` 先把父会话的 conversations/events 复制到新会话目录，再在后台调用 LLM 压缩（`_compact_inherited_conversation`，超时 180s），用一条 `inherited_from` 摘要消息替换原始 parts；压缩失败/超时会回退到原始 transcript。新女王拿到的是 **Colony 系统提示 + 压缩摘要历史**。
- **`task_create(new_colony=true)` 的 pivot**：不跑 LLM 压缩，只写 `[Session handoff]\n<brief>` 作为种子，外加 `tasks.json` 里预置的计划。
- 两条路径都会把父会话的“目标/已完成/待办/约束”带过去，但摘要的保真度不同；这也是 pivot 的 `handoff` 必须“self-contained”的原因（`nodes/__init__.py:615` 的 `<pivot>` 块）。

---

## 7. Worker 的提示词（含 live 缺口）

### 7.1 设计上的 Worker 提示

`worker_definition.py` 是 Worker 身份与磁盘格式的唯一来源：

```text
build_system_prompt(task) = WORKER_SYSTEM_PROMPT + "\nTask: " + task
```

`WORKER_SYSTEM_PROMPT`（7,613 字符）包含：

1. 身份与终局通道：你是女王派的专注 Worker，最后必须 `report_to_parent(status, summary, data)`；
2. 并行 Worker 九条硬规则：不委派、不问问题、不发散、scope 取最窄、先存值再被裁剪、报告一次即停；
3. 输出效率：极简、先给动作/结果；
4. 出站消息写作规则（与女王同一套“像真人写信”的要求）；
5. 任务跟踪：`task_create` 拆步骤、每步心跳；
6. tracker 共享状态、凭据 CLI 用法；
7. Fail-fast 分类：瞬时/结构性可修/结构性不可修；失败先落 tracker 再 report。

`build_meta`（:292）把它写进 `worker.json`，同时写入 `identity_prompt=""`、`memory_prompt=""`、技能目录、协议、工具列表、循环配置。设计意图很明确：Worker 不是女王，不继承人格，只继承技能与工具说明。

### 7.2 PromptSpec 层（如果 AgentSpec.system_prompt 有值）

`agent_loop/prompting.py:125` 的静态顺序：

```text
identity_prompt
binding_prompt（Tracker DB 路径）
accounts_prompt
skills_catalog_prompt（+ 工具门控预激活的整段技能正文）
protocols_prompt
memory_prompt
focus_prompt（= AgentSpec.system_prompt）
report_schema_instruction
```

动态后缀只有 `narrative`。Worker 的 `identity_prompt`、`memory_prompt` 按设计为空。

### 7.3 技能目录会“膨胀”

`build_prompt_spec` 会对技能目录调用 `augment_catalog_for_tools`（`skills/tool_gating.py:79`）：只要当前 eager 工具名以某个前缀开头，就把对应技能的**完整正文**拼进 system prompt：

| 工具前缀 | 预激活技能 | 正文大小 |
|---|---|---:|
| `browser_` | `hive.browser-automation` | 48,905 字符 |
| `terminal_` | `hive.terminal-tools-foundations` | 10,613 字符 |
| `chart_` | `hive.chart-creation-foundations` | 9,275 字符 |

所以一个带浏览器工具且有终端的 Worker，system prompt 可能在技能目录之外再多 **约 60k 字符（≈15k tokens）**。分层 Worker（keep-set）只按当前 eager 集合判断，未 `search_tools` 加载的浏览器工具不会立刻拉入 49k 正文。

### 7.4 首条消息

Worker 的 `input_data` 通常包含 `task` 与 `binding`（`build_input_data`），所以首条 user 消息是：

```text
task: <任务文本>
binding: {name, dir, tracker_db, ...}
```

有 Goal 时再追加 `Goal: ...`。Worker 的 `task` 也会出现在 `worker.json` 的 system_prompt 尾部，但这取决于 live 是否读取该文件（见下）。

### 7.5 实测：live Worker 没有 Worker 系统提示

在本基线实际构造一个 `ColonyRuntime` 并 `spawn` 一个 Worker，检查其 `AgentContext.agent_spec`：

```text
system_prompt = ''
spec id = queen_colony
input_data = {'task': 'do the thing'}
static prompt chars = 28,419
has worker rules: False
```

原因链条：

1. `SessionManager._start_unified_colony_runtime`（`session_manager.py:1654`）构造的 `colony_spec` 是：
   ```python
   AgentSpec(id="queen_colony", ..., system_prompt="", tools=[...], tool_access_policy="all")
   ```
2. `ColonyRuntime.spawn` 使用 `spawn_spec = agent_spec or self._agent_spec`（`colony_runtime.py:1694`），`run_worker` / `spawn_batch` 不传 `agent_spec`；
3. `worker.json` 只被 `AgentLoader.load`（`loader/agent_loader.py:1337`）与 profile 序列化读取，live 的 `ColonyRuntime` 没有任何 `worker.json → AgentSpec` 的读取路径；
4. `WorkerProfile.prompt_override` 也只在写 `worker.json` 时拼接（`routes_execution.py:2016`），live 路径同样忽略。

**后果**：live Worker 的实际 system prompt = 技能目录（本机 28,419 字符）+ [绑定/账号/report schema，如果传入]；`WORKER_SYSTEM_PROMPT` 的九条并行规则、fail-fast 分类、输出规范全部缺席。Worker 只能从以下面获得行为约束：

- `report_to_parent` / `tracker_*` / `write_skill` 等**工具 description**；
- task 文本本身；
- 宽限回合的强制 wind-down（代码层，不依赖提示）；
- 技能目录里的 `hive.worker-delegation` 等条目（仍需模型主动 `cat`）。

这解释了为什么“同一套 Worker 规则”在代码与运行时表现之间可能存在落差。**建议**：在 `_build_worker` 构造 `AgentContext` 时，若 `spawn_spec.system_prompt` 为空，则用 `worker_definition.build_system_prompt(task)` 兜底；或在 `spawn` 入口显式传入 worker spec。修复前，任何依赖 Worker 提示词行为的评测都应显式注入该提示。

> 注：`AgentLoader` 路径（把殖民地当独立 agent 运行、SDK/CLI 导出运行）仍然会读 `worker.json` 的 system_prompt，因此 §7.1 的提示并非完全死代码，只是不在 live 服务路径上。

---

## 8. 独立 Agent、SDK 与传统图节点

### 8.1 SDK / 独立 `AgentLoop`

调用方提供 `AgentSpec` 与 `AgentContext`：

- `AgentSpec.system_prompt` → `PromptSpec.focus_prompt`，进入**静态前缀**；
- `AgentContext.identity_prompt` → 最前面的身份块；
- `AgentContext.narrative` → 动态后缀；
- `AgentContext.memory_prompt` / `accounts_prompt` / `skills_catalog_prompt` / `protocols_prompt` → 各自段；
- 初始消息 = `input_data` 全部键 + `Goal:`。

这条路径没有女王/Worker 的特殊逻辑，完全由调用方决定这五层放什么。

### 8.2 传统图节点（`orchestrator/`）

`NodeSpec.system_prompt` 是 focus 块，但放在**动态后缀**：

```text
静态: identity → binding → accounts → skills → protocols → memory（带“verify stale details”提示）
动态: --- Context (what has happened so far) --- narrative
      + EXECUTION_SCOPE_PREAMBLE（当 event_loop 且有 output_keys）
      + --- Current Focus --- focus
      + Current date: YYYY-MM-DD TZ
```

`EXECUTION_SCOPE_PREAMBLE`（`orchestrator/prompting.py:23`）要求节点“只做本节点任务，为每个 output key 调 set_output，然后停”。阶段切换另发一条 `build_transition_message`（:425）：上一阶段完成摘要、可用输出、数据文件、累计工具、下一阶段任务与 output keys，最后要求“先反思上一阶段”。

`transition` 与 phase compactor 是这条路径独有的提示面；Queen 路径已不再使用。

### 8.3 `AgentLoader` 读取的 `worker.json`

`AgentLoader.load` 从殖民地目录取第一个 `worker.json`，构造单节点 `GraphSpec`：`system_prompt`、`identity_prompt`、工具、`loop_config` 全部来自该文件。这是“把殖民地当独立 agent 跑”的场景，与 live 服务不同。

---

## 9. 其它 Queen 形态

### 9.1 Claude Code Queen

`claude_code_queens.py:374` 以 `--append-system-prompt` 注入：

```text
<note> + "\n\n" + config.system_prompt
```

`_system_prompt(name)`（:546）说明“你是 Hive 的众女王之一，别人写信给你，格式 `[Name(queen_id)]: text`，用 list_queens / message_queen 回信”。用户消息就是信件正文，Hive 侧不拼五层结构。

### 9.2 Queen Mail

`queen_mail.py:461` 把信件格式化为 `[Name(queen_id)]: text`，与聊天应用消息同形，以便记忆与 Soul 正确归属作者。收信方是一个 `channel="queens"` 的普通 Queen 会话，所以仍走 §4 的完整系统提示。

### 9.3 Channel / Turn Context

- Channel 插件在进入会话前可加前缀（发送者、时间、群聊最近上下文等）；`turn_context` 包则在每轮前返回一段文本，作为 `<system-reminder>` 插在用户消息之前（5 秒超时，失败跳过）。
- `chat_app_provider` 还会影响行为：例如 QQ 等聊天应用里，任务面板不可见，`task_create` 规则会放宽（`tasks/reminders.py`）。

---

## 10. 后台辅助 LLM 提示词穷举

这些调用不参与主对话，但同样是“提示词系统”的一部分：

| 场景 | System prompt 位置 | 长度/要点 | 输入 | 输出 |
|---|---|---|---|---|
| Queen 路由 | `queen_profiles.py:667` | 2,378 字符；CEO 视角选最匹配女王 | 用户首条消息 | JSON `{reason,queen_id}` |
| 建群路由 | `queen_profiles.py:796` | 同上 + 生成 colony_name | 用户消息 | JSON + slug |
| 记忆召回选择 | `recall_selector.py:42` | 731 字符；只选“确定有用”的 ≤5 个文件 | 用户 query + 记忆清单 | JSON 文件名列表 |
| 短反思 | `reflection_agent.py:511` | 2,904 字符；把用户持久知识写入 global/queen | 最近对话 | 工具调用写文件 |
| 长反思/整理 | `reflection_agent.py:576` | 1,499 字符；跨作用域去重、搬家、删冗余 | 全部记忆文件 | 工具调用 |
| 时间线抽取 | `timeline.py:52` | 1,742 字符；从会话抽取带日期的事件/事实 | 批量会话文本 | JSON 条目 |
| 会话标题 | `session_titles.py:19` | 193 字符；≤6 词、用户语言 | 首条用户消息 + 最新回复 | 纯文本标题 |
| Soul 反思 | `soul_reflection.py:182` | 2,577 字符模板；第一人称日记、证据引用、最多改 N 条 | 当日对话 + 现有 Soul | 工具调用 add/update/remove |
| Soul 探针 | `soul_probes.py:93` | 复用身份 + Soul 作为 system，逐题问两遍（改前/改后） | 固定问题 + 相关记忆 | 文本回答 |
| Sentinel 分类 | `sentinel/classifier.py:118` | 2,344 字符；continue / needs_human / done | goal、任务、停泊原因、最后回复、用户消息、错误、在飞 Worker | JSON verdict |
| 压缩摘要 | `compaction.py:751` | 7 段结构化摘要（意图/概念/文件/错误/解决/用户消息/待办） | 格式化后的历史 | 摘要文本 |
| 压缩 system | `compaction.py:609/619` | “用户消息逐字保留”或“保留规则/约束/身份偏好” | — | — |
| 图节点质量 judge | `orchestrator/conversation_judge.py:62` | “按成功标准评，不评风格” | 阶段名/描述/标准/输出/最近对话 | ACCEPT/RETRY |
| 阶段压缩 | `orchestrator/orchestrator.py:404` | 面向下一阶段保留上下文 | 历史 + 下一阶段 | 摘要 |
| 视觉回退 | `vision_fallback.py:79` | 1,297 字符；描述意图相关元素并给视口分数坐标 | 意图 + 图片 | ≤600 词纯文本 |
| 停顿检查问题 | `stall_check.py:33` | 404 字符问题 + 636 字符 true/false 标准 | 用户消息、回复、工具、上一轮 | 概率（judge 协议） |
| 图边路由 | `orchestrator/edge.py:221` | “是否沿边继续” | 状态 | JSON |
| Skill CLI 对话 | `skills/cli.py:1113` | 直接拿 skill.body 当 system | 用户输入 | 文本 |

两个“非文本”的 judge 面：

- `server/judge.py` 的 `typesafe` 协议把问题、`criteria`、`state` POST 到 `/systemone`，返回 `noul` 概率；
- `judge_pipeline.py` 的内置 judge 不给 LLM 提示，而是按 output_keys 与工具结果做确定性判定；`SubagentJudge` 只生成 `RETRY` 反馈（“URGENT: 只剩 N 轮，立即 set_output”）。

---

## 11. 上下文注入面（提醒之外）

除了 [20 Reminder 机制穷举](20-reminder-mechanism.md) 的 15 个 source，以下内容也直接改变模型看到的上下文：

| 注入 | 触发 | 形态 | 生命周期 |
|---|---|---|---|
| 工具结果 | 每次工具执行 | `role=tool`，可截断到 30k 字符并落盘 | 回合内 |
| 微压缩占位 | 老工具结果被清理 | “Old tool result (N chars) at /path” | 直到压缩 |
| 压缩摘要 | 上下文超阈值 | `role=user` 摘要 + `PRESERVED VALUES` | 直到下次压缩 |
| 视觉回退说明 | 纯文本模型遇图片 | 工具结果替换为 caption + 坐标 | 回合内 |
| 召回记忆 | 用户消息后（background/wait/index） | `<system-reminder>` | 内容变化才重发 |
| Turn context | 每轮前 | `<system-reminder>` | 每次返回新文本 |
| 任务计划/漂移 | 任务状态变化 | `<system-reminder>` | 变化才发 |
| 技能目录/可搜索工具 | 目录或工具集变化 | `<system-reminder>` | 变化才发；压缩后全量重发 |
| Worker 报告 | Worker 终止 | `[WORKER_REPORT]` user 消息 | 永久 |
| Playbook 完成 | 收敛结束 | `[PLAYBOOK_COMPLETE]` | 永久 |
| 阶段切换 | 工具/前端 | `[PHASE CHANGE]` | 永久 |
| 死循环/预算 | 检测器 | `[SYSTEM] …` | 永久（但通常伴随停泊） |

这些文本一旦写入 `parts/<seq>.json` 就进入历史，后续请求字节稳定，因此不会破坏缓存；这与“把变化塞进 system”形成鲜明对比。

---

## 12. 体积、预算与截断

| 项 | 值 | 位置 |
|---|---:|---|
| Queen DM system prompt | ≈21–24k 字符（5–6.5k tokens） | 实测 |
| Queen Colony system prompt | ≈23–26k 字符 | 实测 |
| Queen 行为规则单块 | 13.4k 字符 | `_queen_behavior_always` |
| Worker 设计提示 | 7.6k 字符 | `WORKER_SYSTEM_PROMPT` |
| 框架技能目录（12 个） | 5.5k 字符 | `SkillCatalog.to_prompt` |
| 本机 187 个技能目录 | 28.4k 字符（compact） | 实测 |
| 浏览器预激活正文 | 48.9k 字符 | `tool_gating.py` |
| 终端预激活正文 | 10.6k 字符 | 同上 |
| 图表预激活正文 | 9.3k 字符 | 同上 |
| 技能描述上限 | 300 字符/条 | `catalog.py:26` |
| 目录 compact 阈值 | 5,000 字符 | `catalog.py:21` |
| 单条工具结果上限 | 30k 字符（可配） | `LoopConfig.max_tool_result_chars` |
| 微压缩保留最近 | 6 条结果 | `MICROCOMPACT_KEEP_RECENT` |
| 压缩目标 | 上下文窗口的一部分 | `compaction_target_chars` |

Queen 的系统提示本身并不夸张；真正随部署膨胀的是**技能目录**与**预激活技能正文**。一个装了很多用户技能、又给 Worker 开了浏览器工具的部署，Worker 的静态提示可以到 60–90k 字符。

---

## 13. 提示词的可覆盖点

| 想改什么 | 改哪里 | 生效范围 | 备注 |
|---|---|---|---|
| 女王人格/标题/触发反应 | `agents/queen/queen_defaults/*.yaml` 或女王 UI | 该 Queen | 5 支柱；live 不渲染 examples |
| 女王自我认知 | `<HIVE_HOME>/queens/<id>/soul.md` + config | 该 Queen | 每天反思后可改 |
| 用户长期记忆 | `<HIVE_HOME>/memory/…`、queens/<id>/memory | global / queen | 召回以 reminder 注入 |
| 技能 | 框架/用户/共享/queen/colony 五级作用域 | 目录可见性 | 写 `SKILL.md`；`visibility` 可按阶段过滤 |
| 默认技能协议 | `configuration.json` 的 skills 配置 | 全局 | 但当前 `protocols_prompt` 恒空 |
| 单 Queen 工具允许列表 | `queens/<id>/tools.json` | 该 Queen | 影响工具 schema 与 searchable 清单 |
| Worker profile | `worker_profiles` / `prompt_override` | 仅写入 worker.json | live 路径不读（§7.5） |
| Loop hooks | `LoopConfig.hooks` | 该 AgentLoop | 可替换 system prompt / 注入消息 |
| Turn context 包 | entry point `hive.turn_context` | 该服务器 | 每轮 `<system-reminder>` |
| Channel 插件 | entry point `hive.channel` | 该渠道 | 入站前缀、出站格式 |
| 视觉回退模型 | `configuration.json` | 全局 | 决定 caption 提示词由谁执行 |

注意：**没有**一个“全局提示词文件”。大多数“改提示词”的需求实际落在 YAML profile、Soul、技能或提醒源上。

---

## 14. 缺口与风险（按影响排序）

1. **live Worker 丢失 Worker 系统提示**（§7.5）。这是最严重的一处：7.6k 字符的并行规则、fail-fast、输出规范在服务端不生效。根因是 Phase 2 统一 `ColonyRuntime` 后，`worker.json` 的 `system_prompt` 没有接进 `AgentSpec`。修复成本低（spawn 时兜底 `build_system_prompt(task)`），但影响面大。
2. **`queen_node.system_prompt` 是死字段**。`queen_node`（`nodes/__init__.py:759`）仍然拼了一份完整独立阶段提示，但 live 女王用 `phase_state.get_static_prompt()`；`queen_orchestrator.py:1502` 的 `_orig_node.model_copy(update=...)` 结果被丢弃。两份提示目前内容一致，但未来改一份忘另一份会导致“测试/文档与实际行为不一致”。
3. **`_queen_identity_hook` 是死代码**（`queen_orchestrator.py:1368`）。它包含完整的“选择身份 → 返回 system prompt”逻辑，但从未注册到 hooks；实际身份选择在 `SessionManager._ensure_session_queen_identity`。删掉或接线，二者择一。
4. **`protocols_prompt` 恒空**（`skills/manager.py:205`）。默认技能的“操作协议”不再常驻 system，全部走渐进披露。好处是省 token，代价是 Worker 在没读技能前缺少基础约定（尤其叠加缺口 1）。
5. **DM 女王不加载角色预设技能**。`queen_orchestrator.py:1201` 构造 `SkillsManagerConfig` 时没有传 `default_preset_skills`，而 `ColonyRuntime._build_default_skills_config:722` 传了。结果是同一个女王在 DM 与 Colony 的技能目录可能不同；profile 里的 `default_preset_skills` 在 DM 侧不生效。
6. **`max_examples=0` 让 few-shot 永不进入 live 提示**。这是有意为之（省 token），但意味着“人格示例”只影响作者，不影响运行时；调人格时不要指望 examples 起作用。
7. **技能目录的规模风险**。187 个用户技能 → 28k 字符的首轮提醒；再叠加浏览器预激活 49k，Worker 的上下文在第一次 LLM 调用前就被占掉一大块。需要按部署做技能裁剪或提高 compact 阈值策略。
8. **两套 prompting 实现的漂移**。`agent_loop/prompting.py` 与 `orchestrator/prompting.py` 是双胞胎，但 focus 位置、日期戳、记忆前置语不同；`stamp_prompt_datetime` 在 agent_loop 侧已无调用者。修改提示布局时必须同时检查两处。
9. **`vision-only` 标记机制空转**。正则与裁剪函数都在，但源码无标记；保留成本低，容易让人误以为有视觉专属提示。
10. **缓存稳定性依赖“自律”**。代码没有强制禁止把时间/召回写进 system；一旦有人加回 `dynamic_prompt_suffix_provider` 或时间戳，缓存会静默失效。建议在 `build_system_prompt_dynamic_suffix` 加断言或测试。

---

## 15. 生效矩阵（一页看懂每个角色拿到什么）

| 角色 / 场景 | system 静态 | system 动态 | tools | 首条 user | 特殊上下文 |
|---|---|---|---|---|---|
| Queen DM | 身份 + Soul + DM base + 凭据 + CLI MCP + 协议(空) | 空 | DM 阶段生命周期工具 9 个（并集 24）+ MCP eager；searchable 走提醒 | `user_request: …` | 技能目录、可搜索工具、召回、任务、turn context |
| Queen Colony | 身份 + Soul + Colony base + 凭据 + CLI MCP + 协议(空) | 空 | Colony 阶段生命周期工具 23 个 + MCP；searchable 走提醒 | 同上 / `[Session handoff]` | Worker 报告、Playbook 完成、fleet 快照 |
| Worker（live） | **仅技能目录（+绑定/账号/report schema）** | 空 | 继承女王工具（去掉 queen-only）；可分层 | `task: …` / `binding: …` | Worker 专属提醒（tracker、batch 余量等） |
| Worker（AgentLoader / worker.json） | `WORKER_SYSTEM_PROMPT + Task:` + 目录 + 协议 | 空 | worker.json 工具列表 | `task/binding` | 同 live 的提醒（若走 runtime） |
| 独立 SDK Agent | 调用方给的 identity/accounts/skills/protocols/memory + `AgentSpec.system_prompt` | narrative | 调用方工具 | `input_data` + `Goal:` | 由调用方决定 |
| 传统图节点 | identity/binding/accounts/skills/protocols/memory | narrative + scope + focus + 日期 | NodeSpec 工具 | `input_data` + 过渡消息 | 阶段过渡、图 judge |
| Claude Code Queen | `--append-system-prompt` 的固定说明 + config.system_prompt | — | Claude Code 自身 | 信件文本 | queen_mail 通道 |
| 辅助 LLM（标题/召回/反思/…） | 各自专用 system | — | 部分带工具 | 专用输入 | 无 |

---

## 16. 源码地图与复核命令

| 关注点 | 文件:符号 |
|---|---|
| 请求结构与缓存 | `core/framework/llm/litellm.py:398` `_build_system_message`；`:484` `_apply_history_cache_breakpoint` |
| PromptSpec 与静态/动态拆分 | `core/framework/agent_loop/prompting.py:62`、`:125`、`:154` |
| 女王 system 拼装 | `core/framework/tools/queen_lifecycle_tools.py:402` `get_static_prompt` |
| 女王阶段提示段 | `core/framework/agents/queen/nodes/__init__.py:151`、`:168`、`:206`、`:264`、`:300`、`:327`、`:465`、`:473`、`:703`、`:730`、`:750` |
| 女王编排 | `core/framework/server/queen_orchestrator.py:319`、`:1167`、`:1183`、`:1668`、`:1700`、`:1975` |
| 女王身份 | `core/framework/agents/queen/queen_profiles.py:533`、`:667`、`:796` |
| Soul | `core/framework/agents/queen/soul.py:291`、`:308` |
| 技能目录 | `core/framework/skills/catalog.py:92`；`core/framework/skills/manager.py:148`、`:205` |
| 工具门控预激活 | `core/framework/skills/tool_gating.py:34`、`:79` |
| 技能/工具提醒 | `core/framework/agent_loop/tool_skill_reminders.py:64`、`:153` |
| 记忆提醒 | `core/framework/agent_loop/memory_reminders.py` |
| Worker 身份 | `core/framework/agents/queen/worker_definition.py:79`、`:229`、`:245`、`:292` |
| Worker live 构造 | `core/framework/host/colony_runtime.py:353`、`:1390`、`:1568`、`:1694` |
| Worker 磁盘格式写点 | `core/framework/server/routes_execution.py:1955`、`:2016` |
| AgentLoader 读 worker.json | `core/framework/loader/agent_loader.py:1337` |
| 传统图提示 | `core/framework/orchestrator/prompting.py:23`、`:263`、`:306`、`:425` |
| 注入与时间戳 | `core/framework/agent_loop/internals/cursor_persistence.py:186`、`:271`、`:317` |
| 提醒包装 | `core/framework/agent_loop/reminders.py:342`、`:346` |
| 压缩提示 | `core/framework/agent_loop/internals/compaction.py:751`、`:799` |
| judge | `core/framework/agent_loop/internals/judge_pipeline.py`；`core/framework/orchestrator/conversation_judge.py:62`；`core/framework/server/judge.py` |
| Sentinel | `core/framework/sentinel/classifier.py:118`、`:152` |
| 标题/时间线/反思 | `core/framework/server/session_titles.py:19`；`core/framework/agents/queen/timeline.py:52`；`core/framework/agents/queen/reflection_agent.py:511`、`:576` |
| Claude Code Queen | `core/framework/server/claude_code_queens.py:374`、`:546` |
| Queen Mail 格式 | `core/framework/server/queen_mail.py:461` |
| Turn context | `core/framework/server/turn_context.py:1-14` |

复核示例：

```bash
# 女王各段体积
uv run python -c "import sys; sys.path.insert(0,'core'); from framework.agents.queen import nodes as n; print(len(n._queen_character_core), len(n._queen_behavior_always))"

# 女王初始提示（需要 profile 与 SkillsManager）
uv run python - <<'PY'
import sys; sys.path.insert(0,'core')
from framework.agents.queen.queen_profiles import load_queen_profile, format_queen_identity_prompt
p=load_queen_profile('queen_technology')
print(len(format_queen_identity_prompt(p, max_examples=0)))
PY

# live Worker 实际 system prompt（验证 §7.5）
uv run python - <<'PY'
import sys, asyncio, tempfile; sys.path.insert(0,'core')
from pathlib import Path
from framework.host.colony_runtime import ColonyRuntime
from framework.agent_loop.types import AgentSpec
from framework.schemas.goal import Goal
from framework.llm.mock import MockLLMProvider
from framework.agent_loop.prompting import build_system_prompt_parts_for_context
async def main():
    rt=ColonyRuntime(agent_spec=AgentSpec(id="queen_colony",name="Q",description="d",system_prompt=""),
        goal=Goal(id="g",name="g",description="g"),storage_path=Path(tempfile.mkdtemp()),
        llm=MockLLMProvider(),tools=[],tool_executor=None,stream_id="s")
    await rt.start()
    try:
        ids=await rt.spawn("do the thing")
        ctx=rt._workers[ids[0]]._context
        static,_=build_system_prompt_parts_for_context(ctx)
        print(repr(ctx.agent_spec.system_prompt), len(static), "worker rules:", "focused worker agent" in static)
    finally:
        await rt.stop()
asyncio.run(main())
PY
```

---

## 17. 设计评价

**做得好的地方：**

- **缓存意识贯穿设计**：静态前缀 / 动态后缀 / 对话尾部三分，且每个“迁移”都有注释解释为什么（时间戳、召回、技能目录、工具清单）。
- **提示词有单一来源**：女王提示按段命名、按阶段组合；Worker 提示集中在 `worker_definition`；技能走标准 `<available_skills>`。
- **行为规则与工具描述同源**：`ask_user`、`report_to_parent`、`run_playbook` 的关键约束同时写在 system prompt 与工具 description 里，模型从两个通道都能读到。
- **辅助提示词可观测**：Sentinel 记录 `classify_decision`、压缩落盘、记忆变更日志，提示工程不是黑箱。

**代价与风险：**

- **行为大量写在提示里**，而提示的“哪一份真正生效”并不直观（§7.5 就是例子）。需要一张类似本文的生效矩阵，最好再加一个启动时的 prompt dump 自检。
- **技能目录成为新的体量中心**：技能越多，首轮上下文越贵；compact 只丢描述，不丢条目。
- **两套 prompting 实现**增加了维护成本；`queen_node`、`_queen_identity_hook`、`stamp_prompt_datetime` 等死代码说明迁移尚未收尾。
- **Worker 提示缺口**会影响并行任务质量与失败行为，应优先修复并加回归测试（断言 spawn 出的 Worker system prompt 包含 `WORKER_SYSTEM_PROMPT` 的关键句）。

---

> 相关章节：Reminder 的 15 个 Source / 9 个 Point 穷举见 [20 Reminder 机制穷举](20-reminder-mechanism.md)；压缩与 judge 的执行细节见 [03 执行内核](03-agent-loop.md)；技能作用域与 Playbook 契约见 [07 技能与 Playbook](07-skills-and-playbooks.md)；记忆与 Soul 见 [06 记忆与 Soul](06-memory-and-soul.md)。
