# 07 · 记忆与技能

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/memory`、`internal/soul`、`internal/skills`。
> 状态：已移植（LongMemEval 无 Go 分数，属外部验证缺口）；差异登记见 `docs/alignment-differences.md`。

## 1. 记忆：三层作用域（global / colony / agent）

Queen 的记忆全部是**纯文件**，没有向量数据库。作用域分三层：`global`（全局）、`colony`（Colony）、`agent`（具体 agent）；目录上 `agents/` 再细分为 `queens/{name}`（Queen）与 `{agent}`（Worker）。目录结构见 `agents/queen/queen_memory_v2.py`：

> 需求 / 设计 / 实现的完整规范：`docs/memory-management-spec.md`（含 Go 映射、API/配置表、已知差异）。

```
~/.hive/memories/
├── global/                    # 所有 Queen / Colony 共享
├── colonies/{name}/           # 某个 Colony 的记忆
└── agents/
    ├── queens/{name}/         # 某个 Queen 的记忆
    └── {agent}/               # 某个 Worker 的记忆
```

每个记忆是一个 `.md` 文件，可带 YAML frontmatter（`name`、`type`、`description`）。

硬约束（扫描与写入工具都执行）：

- 每个目录最多 `MAX_FILES = 200` 个文件：扫描按 mtime 取最新 200；`write_memory_file` 在目录已满时拒绝新建。
- 单文件最多 `MAX_FILE_SIZE_BYTES = 4 KB`：写入工具超限直接报错。
- 全局记忆的类别：`profile`、`preference`、`environment`、`feedback`（其它 type 由写入工具拒绝）。

真正进入 Queen 对话上下文的是 **global + 该 queen 自己的 `agents/queens/{name}`**：每回合 recall 对这两个作用域各发一次选择请求，memory 反思工具也只能读写这两个作用域。`colonies/{name}/` 与 `agents/{agent}/` 有磁盘布局、Memory API（`/api/memories`）可浏览/编辑，但 queen DM 的自动召回不会读取它们；上游代码里 `colony_memory_dir` / `agent_memory_dir` 只有布局定义，没有召回或反思消费者。把「Colony 的上下文」写成会自动注入的能力会高估现状。

## 2. 记忆的三种用法

### 2.1 Reflection（反思）

文件：`agents/queen/reflection_agent.py`、`queen_memory_v2.py`、`soul_reflection.py`

- 触发是**计数**，不是时间冷却：每个完成的 queen turn 计数，达到 `memory_reflection.every_turns`（默认 6）跑一次短反思；每 `long_every`（默认 4）次短反思后再跑一次长反思（合并/去重/清理）；反思进行中到达的触发直接跳过；会话停止时补跑一次短反思；上下文压缩后（`CONTEXT_COMPACTED`）跑一次长反思。
- Go 侧接线（lane `memory-compaction-reflect`，2026-11-23）：`internal/agentloop` 在 LLM 压缩成功路径发布 `context_compacted`（`level`/`usage_before`/`usage_after`/`tokens_before_k`/`tokens_after_k` 与上游 `log_compaction` 对齐，`ratio_before>=1.0` 才附 `message_inventory`），chat runtime 以 queen 流订阅并调用 `ReflectionScheduler.OnCompaction`；此前该事件无生产者、`OnCompaction` 无调用点。残余：Go 的完整压缩路径不含 microcompact/prune 阶段，proactive microcompaction 不发事件（与上游 `add_tool_result` 同）；`message_inventory` 的字符计数用 Go 字节长度，非 ASCII 与上游 Python 字符口径不同。
- 短反思把值得长期保留的结论写成 Markdown 记忆文件（`write_memory_file` / `delete_memory_file` 工具）；长反思只整理已有文件，不发明新事实。
- 写入是**按文件的增量更新**：每次只改相关文件，不会重写一份大记忆；单文件 4 KB、每个作用域 200 个文件的上限由工具强制。
- 反思失败只写 `.reflection_error.txt` 与日志，不阻塞主对话。

**为什么不是 queen 在对话里自己写：**

- 她的主工具注册表里**没有**记忆写工具——`write_memory_file` / `delete_memory_file` / `read_memory_file` / `list_memory_files` 是反思代理的内部工具（不在 queen 的 registry，schema 由工具契约测试冻结）。她能主动搜历史（`search_messages`），不能主动改记忆。
- 对话与策展分离：反思是独立 side agent（自己的提示词、工具集、模型槽 `memory_reflection.model`，默认 worker），fire-and-forget 运行；主回路保持快，`<soul>`/system prompt 的缓存前缀不被写操作扰动。
- 质量门都在反思侧：去重、`type` 白名单、4 KB/200 文件限额、跨回合证据；主回路若每轮可写，就要在用户可见回合里判断"值不值得记、记到哪、怎么合并"，既慢又容易把噪声写进长期记忆。
- 单一写入口 = 可审计：所有改动经 `memory_log.write/retire`，每作用域 `log.jsonl` 留 before/after + reason + evidence，删除可 `restore`；owner 写的文件她不能覆盖（`OwnerWritten`）。
- 用户说"记住这个"不需要工具：该消息会被下一轮反思吸收（最迟会话停止前的补跑）；要立刻生效则由 **owner 通道**直接写。

**owner 主动写通道**：`/api/memories`（list / file GET·POST·PUT·DELETE / history / restore / retired），写入以 `author="owner"` 记账，对应 Memory 页面。即"主动写入"有两种：反射代理的**自动主动**，和 owner 的**显式主动**；唯独没有"queen 对话中途直接写"。

**边界（诚实口径）**：代码里没有针对 `memories/` 的终端写保护（技能目录的沙箱同样是登记待办），所以严格说 queen 用 `terminal_exec` 硬写文件技术上可行——但这不在设计通道里，也没有提示词引导；正常路径不会发生。

### 2.2 Recall（召回）

文件：`agents/queen/recall_selector.py`

每回合开始前：

1. 扫描作用域目录（global 与 queen；每目录上限 200 个 `.md`）。
2. 读取 frontmatter 与前 30 行，生成清单。
3. 每个作用域一次 LLM 调用（结构化 JSON 输出），最多选 5 个（queen 对话路径每作用域上限 3 个）。
4. 注入为 `<system-reminder>` 用户消息（不是系统提示）：当前轮先用缓存块，后台刷新后内容变化再注入；内容没变不重复注入。

关键约束：

- **只看用户当前查询字符串**，不看完整对话，保持便宜和快。
- 选择失败时回退到 pinned 的 `type: profile` 记忆，任何错误都不阻塞主对话。

这是一种「低成本、可失败、不阻塞」的召回设计。

### 2.3 Past conversations（历史对话召回）

- 会话开始与每次用户输入时，最多 6 段相关历史会话摘要（带日期）被放入上下文（`<system-reminder>`）。
- 来源是按关键词检索的历史索引（`tools/src/memory_tools/recall.py`、`turn_index.py`）；窗口选「包含最多查询词」的片段，用户与助手预算都是 500 字符；一个已命中对话可再带回最多 2 个匹配回合（总数上限 4）。
- Queen 也可以主动调用 `search_messages` 搜索；最终 assistant 回复也会被索引。

### 2.4 Timeline（可选）

文件：`agents/queen/timeline.py`

- 由 `memory_timeline` 功能开关控制，**默认关闭**（`configuration.json` 的 `memory_timeline: true` 或 `HIVE_MEMORY_TIMELINE`）。
- 只读**用户消息**，用 Queen 自己的模型抽取事件/事实/计划，并解析相对日期（“昨天”“上周”）。
- 触发：会话进行中每新增 3 条用户消息或 300 秒冷却后（`HIVE_TIMELINE_BATCH` / `HIVE_TIMELINE_COOLDOWN`）、会话停止时 flush、启动时回填最近 10 个历史会话。
- 存储为：
  - `<session_dir>/timeline.jsonl`
  - `<session_dir>/timeline_meta.json`（抽取游标）
- 通过 `search_timeline` 工具查询。

它解决的是 `search_messages` 难以回答的「什么时候」「多少次」问题。

## 3. LongMemEval 基准

`docs/memory-benchmark.md` 给出了目前开源 Agent 项目里少见的、端到端的记忆评测。

### 3.1 结果

| 配置 | LongMemEval-S | LongMemEval-M |
| --- | --- | --- |
| **Hive V1** | **88.4%** | **82.0%** |
| V1 之前的 Hive | 77.0% | 未跑 |
| 同一模型读完整历史 | 96.8% | 不可行 |
| 同一模型只读「含答案的会话」 | 97.6% | 97.8% |

- S 约 115k tokens（约 48 个会话），M 约 1.1M tokens（约 476 个会话）。
- 全部 500 题，使用官方 judge（gpt-4o-2024-08-06），回答模型 `gpt-6.1-sol`。
- 超时/报错计为错误。

### 3.2 诚实的局限

官方文档主动披露：

- **S 上「读完整历史」比 Hive 高 8.4 分**（96.8% vs 88.4%）。当历史能装进上下文时，直接读更准。
- **偏好类问题最弱**：S 63.3%、M 30.0%。M 上 87% 的这类问题 Queen 搜索了，但含答案的会话只有 23% 出现在结果里。
- **「助手说过什么」约 77–79%**，而直接读会话是 100%；自动召回会截短助手回复、偏向用户措辞。
- **跨会话计数/合并**：S 82.0%、M 78.9%，部分检索会漏算。
- 回答模型影响很大：同版本换 `gpt-6-luna`，S 降到 73.0%。

### 3.3 评价

这套基准的价值不在于「88.4%」这个数字，而在于：

1. **端到端**：通过真实 Hive 存储与真实 Queen 回答，不是只测检索组件。
2. **给出上界参照**：读完整历史、只读答案会话，让读者知道差距在哪。
3. **公开失败模式**：偏好、助手原话、跨会话计数。

这是工程诚实的表现。风险是：基准是英文、单一模型、特定 judge，中文/多语言场景需要自行验证（1.0.8 修过中文召回问题，说明多语言曾有过实际缺陷）。

## 4. Skill：可复用的方法

### 4.1 格式

- 采用开放 [Agent Skills](https://agentskills.io) 规范。
- 一个技能是一个目录，核心文件 `SKILL.md`。
- `parser.py` 解析 YAML frontmatter + Markdown body，字段包括 `name`、`description`、`license`、`compatibility`、`metadata`、`allowed_tools`、`visibility`。
- 解析是宽松的：缺失 description 或 YAML 完全无法解析才跳过；常见 YAML 错误（未加引号的冒号）会尝试修复。

### 4.2 作用域

| 作用域 | 位置 | 可见性 |
| --- | --- | --- |
| framework/default | `skills/_default_skills/` | 所有 Agent |
| preset | `skills/_preset_skills/` | 预设场景 |
| queen | Queen 的技能目录 | 她的所有会话与 Colony |
| colony | `colonies/<name>/skills/` | 该 Colony 的 Worker |
| global | 共享技能目录 | 所有 Queen 与 Colony（需用户批准） |

内置技能数量：12 个 default + 8 个 preset。

**Go 注记（colony worker/overseer 目录）**：Go 侧的 colony 目录已按上游 `_build_default_skills_config` 组装，扫描顺序为 framework/default → preset → user（`~/.agents/skills`、external skills、`~/.hive/skills`）→ queen_ui → colony_ui（含旧版 `<colony>/.hive/skills`），后扫覆盖先扫（同名时 colony 覆盖 queen、queen 覆盖 user、user 覆盖 framework）。worker 与常驻 overseer 的目录提示都按 phase `colony` 过滤：`metadata.visibility` 缺省或包含 `colony` 才出现，`[independent]` / `[worker]` 不出现；`GET /api/sessions/{id}/colony/skills` 返回过滤后的完整 manager 目录，不做 phase 过滤，与上游读取 `_catalog._skills` 一致。preset 技能默认关闭，只有所属 queen 的 profile `default_preset_skills` 列出的才在内存启用（内置 13 个 queen 当前均为空；自定义 `profile.yaml` 可设置），未列出的 preset 保持关闭。

### 4.3 `write_skill`

1.0.14 的关键变化：

- 当 Queen 独自面对用户时也可用（以前只在 Colony 里）。
- 增加 `scope` 参数：`colony`（有 Colony 时默认）、`queen`（否则默认）、`global`。
- `global` 不会直接写入，而是：
  1. 在会话里保存一个安装请求；
  2. 该会话聊天页展示技能名称、描述、文件、正文以及覆盖警告；
  3. 用户批准后才写入共享目录；
  4. 关闭对话框不等于拒绝，请求持久化，重启后仍在。
- 不覆盖用户手写的技能；用户关闭过的技能在 Queen 重写后仍保持关闭。

实现：`skills/skill_writer.py`、`skills/install_requests.py`、`skills/overrides.py`、`server/routes_skills.py`。

### 4.4 信任与安全

`skills/trust.py` 处理项目级技能的信任门控：

- framework 与 user 作用域的技能始终可信。
- 来自不受信仓库的 project 技能，加载进系统提示前需要用户明确同意。
- 信任仓库记录在 `~/.hive/trusted_repos.json`。
- CI/无头环境可用 `HIVE_TRUST_PROJECT_SKILLS` 绕过（需显式开启）。
- `HIVE_OWN_REMOTES` 支持通配自己的远程仓库。

这是一个重要的安全设计：技能本质上是可执行指令，会进入系统提示，因此来源必须可审计。

### 4.5 社区注册表

`skills/registry.py` 从 GitHub 拉取技能索引，缓存 1 小时，任何失败都优雅降级为 `None`/`[]`。默认 registry 仓库可能尚不存在，因此这是「有则用之」的能力。

### 4.6 工具门控

`skills/tool_gating.py` 把技能与工具类别关联：技能可以声明需要的工具，未满足时可能被隐藏或提示。配合 [05](05-tools-and-integrations.md) 的 eager/searchable 分层，控制上下文与能力边界。

## 5. Soul：Queen 的自我叙事

1.0.12 引入（`agents/queen/soul.py`、`soul_probes.py`、`soul_reflection.py`）：

- 每个 Queen 维护一份简短的、第一人称的「她成为了谁」的记录。
- 每天根据聊天反思一次。
- 用户逐条批准修改。
- 配置在 `soul.queens.<id>`。

它与 memory 的区别：memory 是「关于用户/业务的事实」，soul 是「Queen 自己的价值观与风格演化」。这是一个有野心的产品设计，但也增加了「模型自我修改」的治理复杂度。

## 6. 评价

**优点**

- 纯文件 + 小文件上限，简单、可审计、可手动编辑。
- 三层作用域清晰（global / colony / agent；queen 与 worker 同属 agents 下细分）。
- 召回是「便宜、可失败、不阻塞」，工程上稳健。
- LongMemEval 端到端评测与诚实的局限披露，在开源项目中少见。
- 技能遵循开放规范，且有信任门控与审批流程。

**风险**

- 200 文件 × 4 KB 的上限在多 Colony、长周期场景可能不够；需要归档/压缩策略。
- 记忆召回只看当前查询字符串，多轮指代（“那个项目呢？”）可能召回失败。
- 偏好类问题在 M 上只有 30%，说明关键词检索在大历史下排序能力不足。
- Soul 的自动反思 + 用户逐条批准，交互成本高；如果批准流程被忽略，可能积累未审的自述。
- `write_skill(scope="global")` 的审批是行为规则而非硬墙，terminal 工具可绕过。

**建议**

- 为记忆增加「归档层」与按时间/主题的索引，缓解 200 文件上限。
- 召回时加入最近几轮对话的指代消解，或把「上一轮查询」作为额外信号。
- 对偏好类记忆做专门的强索引（例如按实体/属性），而不是通用关键词。
- 把 global 技能写入做成真正的沙箱约束（例如技能写入走专用工具，terminal 对技能目录只读）。
