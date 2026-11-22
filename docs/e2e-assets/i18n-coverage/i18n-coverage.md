# i18n coverage — 静态盘点（Go vendored frontend A vs upstream B）

- 生成：`2026-11-23T01:21:05+08:00`
- HEAD：`62e92f3b10376c5f6a1e5212b932fcfbbbcd77e5`（worktree `/Users/mac/hive/.wt/i18n-coverage`）
- A：`go_hive/go-hive/frontend`；B：`core/frontend`
- 复现：`uv run --no-project python go_hive/scripts/i18n_coverage.py`

## 1. 核心数字

> 口径：key = 英文源文案；字典按 `index.tsx` spread 顺序合并（后 spread 覆盖）；
> 引用仅统计产品代码（`src/**`、排除 `src/i18n/zh`/`index.tsx`/测试文件）且文件必须经 `useI18n()` 取 `t`；
> 模板 `${…}` 内的调用、三元分支字面量计入；比较操作数（`=== "x"`）、拼接与嵌套数据字面量不计。

| 指标 | 数值 |
| --- | ---: |
| A 字典文件条目（去重前） | 1290 |
| A 字典唯一键（按 index.tsx spread 顺序合并） | 1238 |
| A 被 `t()` 静态字面量引用的唯一键 | 1140 |
| A 静态调用点（含重复） | 1482 |
| A 表达式参数中的显式字面量键（三元等） | 8（4 个调用） |
| A 明确引用键合计（静态 ∪ 表达式字面量，含字典缺的 8 条） | 1148 |
| A 动态 `t(expr)` 调用：已解析/未解析 | 29 / 21 |
| **A 缺失翻译（明确引用但字典无）** | **8** |
| A 静态未引用字典条目（= 下列三层之和） | 98 |
| ├ 仅字典出现（强 dead-key 候选） | 1 |
| ├ 文本在别处作为字面量出现 | 45 |
| └ 仅由动态候选覆盖（低置信） | 52 |
| A 翻译值 == 英文键 | 15 |
| A 占位符集合不一致 | 0 |
| B 字典键 / 被引用键 / 缺失 | 0 / 0 / 0 |
| A/B 键集合：A 有 B 无 / B 有 A 无 / 值不同 | 1238 / 0 / 0 |

- tokenizer 自检：产品文件 raw `t(` 计数 1539 vs token 识别 1539（0 个文件有差），保证模板插值 `${…}` 内/嵌套调用不被漏计。

## 2. A 缺失翻译（按文件分组，列表上限 200）

| 文件 | 缺失条数 |
| --- | ---: |
| `src/components/QueenSoulSection.tsx` | 6 |
| `src/components/SentinelConnectorsSection.tsx` | 2 |

| 文件:行 | key |
| --- | --- |
| `src/components/QueenSoulSection.tsx:159` | Her changes apply at once |
| `src/components/QueenSoulSection.tsx:159` | Her changes wait for your approval |
| `src/components/QueenSoulSection.tsx:203` | Her first draft |
| `src/components/QueenSoulSection.tsx:203` | Her reflection |
| `src/components/QueenSoulSection.tsx:271` | Try her with these changes ({count} question) |
| `src/components/QueenSoulSection.tsx:271` | Try her with these changes ({count} questions) |
| `src/components/SentinelConnectorsSection.tsx:276` | Connected as {account}. |
| `src/components/SentinelConnectorsSection.tsx:276` | Connected to {account}. |

动态解析候选缺失（低置信，单列）：**43** 条。

| key | 引用点 |
| --- | --- |
| 200+ models, any provider | `src/pages/models.tsx:405` |
| Athena | `src/pages/home.tsx:525` |
| Catherine | `src/pages/home.tsx:525` |
| Claude 4 family - Fast, balanced, and most capable | `src/pages/models.tsx:405` |
| Cleopatra | `src/pages/home.tsx:525` |
| Customer Success & Support | `src/pages/home.tsx:525` |
| Data & Analytics | `src/pages/home.tsx:525` |
| DeepSeek Chat and Reasoner | `src/pages/models.tsx:405` |
| DeepSeek, Qwen3, GPT-OSS, Llama | `src/pages/models.tsx:405` |
| Describes images for a model that can't see them. | `src/pages/models.tsx:405` |
| Diana | `src/pages/home.tsx:525` |
| Elena | `src/pages/home.tsx:525` |
| Every queen thinks with it, unless she has her own. | `src/pages/models.tsx:405` |
| Executive & Administrative | `src/pages/home.tsx:525` |
| Finance & Accounting | `src/pages/home.tsx:525` |
| Freya | `src/pages/home.tsx:525` |
| GPT-5.4 family - Best intelligence and value | `src/pages/models.tsx:405` |
| Gemini 3 family - Fast and best quality | `src/pages/models.tsx:405` |
| Grace | `src/pages/home.tsx:525` |
| HR & People Operations | `src/pages/home.tsx:525` |
| IT & Security | `src/pages/home.tsx:525` |
| Ivy | `src/pages/home.tsx:525` |
| Legal & Compliance | `src/pages/home.tsx:525` |
| Liz | `src/pages/home.tsx:525` |
| Marketing & Growth | `src/pages/home.tsx:525` |
| Mary | `src/pages/home.tsx:525` |
| MiniMax M2.7 and M2.5 - Best coding quality | `src/pages/models.tsx:405` |
| Mistral Large, Medium, Small, Codestral | `src/pages/models.tsx:405` |
| None | `src/pages/models.tsx:410` |
| Nora | `src/pages/home.tsx:525` |
| Operations & Supply Chain | `src/pages/home.tsx:525` |
| Pearl | `src/pages/home.tsx:525` |
| Pick a model | `src/pages/models.tsx:410` |
| Product & Engineering | `src/pages/home.tsx:525` |
| Ruby | `src/pages/home.tsx:525` |
| Sales & Business Development | `src/pages/home.tsx:525` |
| Same as the main model | `src/pages/models.tsx:410` |
| Sophia | `src/pages/home.tsx:525` |
| The workers a colony sends out. | `src/pages/models.tsx:405` |
| Ultra-fast inference, GPT-OSS and Llama | `src/pages/models.tsx:405` |
| Ultra-fast inference, GPT-OSS and Z.ai GLM | `src/pages/models.tsx:405` |
| Victoria | `src/pages/home.tsx:525` |
| Vision fallback | `src/components/ProviderManager.tsx:328`, `src/components/ProviderManager.tsx:483`, `src/pages/models.tsx:404` |

## 3. A 未引用字典条目（dead keys）

静态未引用合计 **98** 条（= 下列三层，互不重叠）。口径：字典 1238 键中，被明确引用命中 1140 个；另有 8 个被引用但字典没有（见 §2）。

| 层级 | 条数 | 含义 |
| --- | ---: | --- |
| 仅字典出现 | 1 | 产品源码中没有任何同文字面量；最可能是死键（运行时后端值仍可能命中） |
| 文本另有字面量 | 45 | 产品源码别处存在同文字面量，可能经数据表/动态 t() 命中 |
| 动态候选命中 | 52 | 仅由本脚本的动态解析候选覆盖（低置信） |

### 仅字典出现（1）

- `Delete this memory?`

### 文本另有字面量（45）

- `CRM (HubSpot)`
- `Calendar`
- `Campaign email`
- `Charts & diagrams`
- `Email`
- `Files & documents`
- `Framework skills`
- `GitHub`
- `Google Docs & Sheets`
- `Her own notes about herself, revised once a day from her chats.`
- `How she thinks`
- `Image generation`
- `Notion`
- `Other`
- `Other tools`
- `Project skills`
- `Read & edit code`
- `Reflection`
- `Security scanning`
- `Sends this session's data so we can investigate.`
- `Slack`
- `Spreadsheets & data`
- `Terminal & commands`
- `Terminal (advanced)`
- `The colonies she runs.`
- `The model behind her voice.`
- `The notes she keeps across chats.`
- `The text every chat with her starts from, in the chapters it is written in.`
- `Time & account info`
- `Time, account & memory`
- `User skills`
- `Web browsing & automation`
- `Web research`
- `What she can do`
- `What she has become`
- `What she is good at, in her own profile's words, and the skills and tools she may use.`
- `What she remembers`
- `Where she leads`
- `Who she is`
- `daily`
- `hourly`
- `hours`
- `minutes`
- `stopped`
- `weekly`

### 动态候选命中（52）

- `A model picks the notes for each message before she answers, adding a couple of seconds to each reply.`
- `Captions images when the main model has no vision.`
- `Colony workers. Cleared → workers use the main LLM.`
- `Craft`
- `Critical`
- `Daily`
- `Every day`
- `Friday`
- `Full`
- `Get pinged in Slack when a colony stalls — reply in-thread to keep it moving.`
- `Get pinged on Telegram when a colony stalls — reply to keep it moving.`
- `Hidden background`
- `High`
- `Idle`
- `Interval`
- `LLM (main)`
- `Low`
- `Medium`
- `Mind`
- `Monday`
- `Most copied`
- `Moves her, and is never shown to anyone`
- `Newest`
- `Not a chat model: checks queen turns for Developer → Stall check.`
- `Note list`
- `Parked`
- `Persona`
- `Pick before answering`
- `Pick while answering`
- `Powers every queen session. Always set.`
- `Psychological profile`
- `Queen's own model`
- `Reduced`
- `Saturday`
- `She sees each note's name and one-line description, and opens the ones she needs. No extra model calls.`
- `Sunday`
- `The masks she wears, and what she is not`
- `The same pick, made while she answers, so the notes often arrive a message late.`
- `Thursday`
- `Triggers`
- `Tuesday`
- `Wednesday`
- `Weekdays`
- `Weekends`
- `Weekly`
- `What sets her off, and how she answers`
- `Where she lives, and the words she uses`
- `Worker model`
- `World lore`
- `removed`
- `restored`
- `wrote`

## 4. 值体检

- 值 == 英文键（多为品牌/技术名，属故意不译）：15 条
  - `App-Level Tokens`、`Basic Information →`、`Bot Token Scopes`、`Bot User OAuth Token`、`Create New App → From scratch`、`GitHub`、`Install to Workspace`、`Markdown`、`Notion`、`OAuth`、`OAuth & Permissions`、`Slack`、`Socket Mode`、`URL *`、`new-skill-name`
- 空值：0 条
- 占位符集合不一致：0 条

## 5. A vs B（key 集合 / zh 值）

| 项 | 条数 |
| --- | ---: |
| B 有 A 无 | 0 |
| A 有 B 无 | 1238 |
| 值不同 | 0 |

B 侧证据：

- `src/i18n/` 目录存在：False
- 含 `useI18n` / locale / i18n import 标记的产品文件：0
- 原始 `t(` 调用点：3（均为 DebugPanel 的数值格式化局部函数，不是 i18n）
- 字典键：0

A 键英文文案在 B 产品源码中的启发式命中（仅 presence，非语义断言）：

- 精确子串命中：1069；占位符通配命中：150；未命中：19（基数：A 字典 1238 键）

未命中清单（Go 侧新增文案或已改写；启发式，非断言）：

- `Claude Code · {n} chat`
- `Claude Code · {n} chats`
- `Connected to {team}.`
- `Copied {count} times`
- `Finish signing in to {name} in the Aden tab that just opened, then click "I've authorized" to sync.`
- `Hey there, what can I help you with?`
- `Scheduler fired`
- `She has no soul yet. Turn it on in configuration.json under soul.queens.{id}.`
- `Uploading {count} files…`
- `Uploading {count} file…`
- `Webhook fired`
- `{count} archived task, grouped by goal.`
- `{count} archived tasks, grouped by goal.`
- `{count} custom · `
- `{count} dirs · {skills} skills`
- `{count} skills found`
- `{n} chapter · {count} characters`
- `{n} chapters · {count} characters`
- `{providers} not connected — {count} tools unavailable until you authorize.`

值差异（上限 100）：
无（B 无 zh 字典，不存在可比的值）。

## 6. 与既有冻结盘点的校准（frontend-surface-diff.json）

- 基线：`a1d537df`（2026-11-21T15:53:35+08:00）
- 基线数字：字典 1209 / 引用 1118 / 动态调用 51 / 未引用 91 / 缺失 0
- 当前：字典 1238 / 明确引用 1148 / 动态调用 50 / 静态未引用 98 / 缺失 8
- 与基线白名单：仍在 1209；新增 29；移除 0
- 基线未引用键：91；当前仍未引用 85
- 旧方法回放（`T_CALL_RE` 正则，含测试、跳过 `src/i18n/**`）：引用 1134 键；与当前 tokenizer 口径：仅旧方法 0、仅新方法 6

## 7. 局限（无法静态判定）

- 运行时拼接/后端下发字符串作为键：动态 `t(expr)` 未解析 21 处（完整清单见 JSON）；其中若运行时值与字典键相同则仍会命中。
- 字典键的英文文案可能在 JSX 纯文本节点中直接书写而未包 `t()`（不属于本盘点口径；本脚本只核 t() 引用）。
- 动态解析的 prop/named pool 是超集启发：用于 dead-key 降级与低置信缺失清单，不进入头条缺失数字。
- B 无 i18n 层，A/B 差异仅能作结构结论；值级对照不可能（B 无 zh 值）。

证据 JSON：`go_hive/docs/e2e-assets/i18n-coverage/i18n-coverage.json`
