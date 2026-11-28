# 17 · 文件系统与持久化契约

> Hive 是**严重依赖文件系统**的运行时：没有外部数据库，会话、计划、记忆、凭据、触发器、Playbook 日志全部落在 `HIVE_HOME` 下。
> 本章把这个“磁盘契约”完整写出来：**每个目录放什么、每个文件的格式与 schema、谁写谁读、何时创建/删除、如何保证原子性，以及它对应哪个内存对象。**
>
> 基线：`aden-hive 1.0.15`，v3 布局。所有路径以 `framework/config.py` 的 `HIVE_HOME` 为准。

---

## 1. 总则

### 1.1 三类数据

| 类别 | 例子 | 丢失后果 | 恢复方式 |
|---|---|---|---|
| **Canonical（真相）** | 会话 `parts/` + `cursor.json`、`tracker.db`、`events.jsonl`、凭据 `.enc`、`tasks.json` | 业务状态丢失，无法精确恢复 | 备份 `HIVE_HOME` |
| **Derived（派生）** | `summary.json`、`.message_index/`、`registry_cache/`、MCP 工具缓存 | 可重建，只影响性能/首屏 | 删除后自动重建 |
| **Diagnostic（诊断）** | `logs/`、`event_logs/`、`llm_logs/`、`debug_logs/`、`compaction_log/` | 不影响运行，丢失可观测性 | 重新产生 |

**判断一个文件能不能删**：先看它是不是 Derived。Canonical 不能随手删；Diagnostic 可按保留策略清。

### 1.2 通用约定

| 约定 | 说明 |
|---|---|
| 根目录 | `HIVE_HOME`（默认 `~/.hive`；桌面端由 Electron 传平台目录） |
| 编码 | UTF-8；JSON 文件多为 `indent=2`，部分 `ensure_ascii=False` |
| JSONL | 一行一个 JSON 对象；追加写；损坏行跳过而不是整文件失败 |
| 时间 | 混合使用：目录名/ID 用 `YYYYMMDD_HHMMSS`；JSON 多用 ISO 8601；`created_at` 有时是 epoch float |
| ID | `session_YYYYMMDD_HHMMSS_<8hex>`；`worker` 同形；`hop_<12hex>`；`cp_<type>_<node>_<ts>`；上传 `upload_id` 为 16 位 hex |
| 命名 | Colony/Queen ID 限制小写字母、数字、`_`、`-`；`_` 前缀在 Tracker 中保留给框架 |
| 权限 | `secrets/credential_key` 0600、目录 0700；`sentinel_secret` 0600；Claude Code 的 MCP token 文件 0600；其余文件默认 umask |

### 1.3 原子性等级

| 等级 | 实现 | 使用位置 |
|---|---|---|
| **A. tempfile + fsync + replace** | `utils/io.atomic_write` | 会话 parts、cursor、conversations meta、tasks、summary、soul、profile、MCP 注册表、notifications、escalations、colony tools |
| **B. tempfile + replace（无 fsync）** | 各模块自己的 `_write_json` | 部分配置/覆盖文件（soul、skills overrides、colony metadata、config） |
| **C. 直接 write** | `Path.write_text` / `open("w")` | `meta.json`、`triggers.json`、`custom_prompts.json`、`worker.json`、`attached_credentials.json`、`compaction_status.json`、`.enc` |
| **D. 追加 + flush** | `EventLogFile.write` | `events.jsonl`、`llm_logs/*.jsonl`、`mail.jsonl`、memory/soul 的 `log.jsonl` |
| **E. SQLite WAL** | `PRAGMA journal_mode=WAL` | `tracker.db`、`colony.db`、`social_rate_limits.db`、`.message_index/recall/*.sqlite3` |

> 等级 C 是**已知的崩溃窗口**：写一半会留下截断 JSON。读取方普遍用 `try/except` 降级为空，但这也意味着“文件存在但内容丢失”。需要强一致的数据应使用 A/D/E。

### 1.4 并发模型

- **单写者假设**：一个 session 只有一个 AgentLoop 在写 `parts/`/`cursor.json`；同进程内用 `asyncio` 串行化。
- **跨进程**：`atomic_write` 的临时文件名唯一，避免两个进程互相截断；SQLite 用 WAL + `busy_timeout`。
- **内存锁**：`TaskStore` 按 session 的 `threading.Lock`；`ConcurrentStorage` 有弱引用文件锁；`message_tools` 用按 session 的文件锁（POSIX `flock` / Windows `msvcrt`）。
- **没有全局写锁**：不同 session 可以并行写；同一 session 的跨进程并发**不在保证范围内**（桌面/CLI 同时操作同一 `HIVE_HOME` 是未定义行为）。

---

## 2. 根目录总览

```text
$HIVE_HOME/
├── configuration.json                 # 全局配置（模型/记忆/Sentinel/Soul/保留策略）
├── custom_prompts.json                # Prompt Library 自定义提示词
├── runtime_id                         # 本机 runtime 的稳定 ID（Inbox 路由）
├── sentinel_secret                    # Sentinel 升级 token 的 HMAC secret（0600）
├── trusted_repos.json                 # 技能信任仓库
├── own_remotes                        # 自有 git remote glob（每行一个）
├── .install_notice_shown              # 技能安装提示已展示
├── .skill_trust_notice_shown          # 技能信任提示已展示
├── .migrated-v2 / .migrated-v3        # 目录迁移标记
├── .janitor/                          # 保留给 Janitor 的顶层命名空间（当前仅 KNOWN_ENTRIES）
├── .message_index/                    # 记忆召回的派生索引（events/data/meta/recall）
├── desktop/                           # 桌面端私有状态（Janitor 永不清理）
├── workspace/                         # 桌面/工作区保留目录（Janitor 永不清理）
├── antigravity-accounts.json          # Antigravity OAuth 账号
├── social_rate_limits.db              # 社媒限速 SQLite
├── browser_default.json                # 浏览器桥“星标默认”profile（硬编码 ~/.hive）
├── browser_contexts.json               # 浏览器 tab group 持久化（硬编码 ~/.hive）
├── bridge_host.log                     # 浏览器桥 supervisor 日志（硬编码 ~/.hive）
│
├── secrets/
│   └── credential_key                 # Fernet key（纯文本，0600）
├── credentials/
│   ├── credentials/<id>.enc           # 加密的 CredentialObject
│   └── metadata/index.json            # 凭据索引（不含 secret）
│
├── mcp_registry/
│   ├── installed.json                 # 已安装 MCP server 注册表
│   ├── config.json                    # 注册表刷新配置
│   └── cache/
│       ├── registry_index.json        # 远程索引缓存
│       ├── last_fetched               # 上次抓取时间（新版）
│       └── last_fetched.json          # 上次抓取时间（legacy）
├── cache/mcp2cli/                     # mcp2cli 工具缓存
├── registry_cache/                    # 技能注册表缓存
│   ├── skill_index.json
│   └── metadata.json
│
├── queens/<queen_id>/
│   ├── profile.yaml                   # 人格
│   ├── avatar.*                       # 头像（可选）
│   ├── _profile_snapshots/            # profile.yaml 编辑快照（最多 30）
│   ├── tools.json                     # 每 Queen 工具允许列表
│   ├── skills_overrides.json          # 每 Queen 技能启停/来源
│   ├── skills/                        # 每 Queen 技能
│   ├── soul/                          # Soul 版本、提案、日志
│   └── sessions/<session_id>/         # DM 会话
│       ├── meta.json
│       ├── events.jsonl
│       ├── summary.json
│       ├── tasks.json
│       ├── reminder_state.json
│       ├── compaction_status.json     # 仅 fork compaction 期间
│       ├── skill_installs.json        # 全局技能安装请求
│       ├── timeline.jsonl
│       ├── timeline_meta.json
│       ├── .janitor.lock              # 冷会话重写/清理锁（1h TTL）
│       ├── .pruned.json               # Janitor 重写 events.jsonl 后的标记
│       ├── conversations/
│       │   ├── meta.json
│       │   ├── cursor.json
│       │   ├── parts/<seq>.json
│       │   └── partials/<seq>.json
│       ├── data/
│       │   ├── attachments/
│       │   ├── reports/               # 历史/兼容，报告的 bundle 实际写系统临时目录
│       │   └── *.txt / *.json        # 工具结果溢写
│       └── logs/                      # 可选：RuntimeLogStore 的 summary/details/tool_logs
│
├── colonies/
│   ├── .migrations/colony_sessions_v1 # Colony 会话迁移标记
│   └── <colony_id>/
│       ├── metadata.json              # provenance + worker profiles + 软删除
│       ├── worker.json                # 默认 Worker 模板
│       ├── profiles/<name>/worker.json # 命名 Worker 模板
│       ├── tools.json                 # Colony 工具允许列表
│       ├── skills_overrides.json      # 布局注释有，但当前代码不读（见 3.4）
│       ├── skills/                    # Colony 技能
│       ├── playbooks/<name>.play.py   # Playbook 脚本
│       ├── triggers.json              # 触发器定义
│       ├── notifications.json         # Sentinel 渠道配置
│       ├── escalations/<id>.json      # 升级记录
│       ├── tracker/
│       │   └── tracker.db（+ -wal / -shm）
│       ├── colony.db（+ -wal / -shm） # 框架簿记（playbook_runs）
│       ├── legacy_fork_snapshots/<sid>/ # 旧版 fork 快照（迁移归档）
│       ├── seed_conversation/         # fork 时 Queen 对话快照
│       ├── queens/<queen>/sessions/<sid>/ # Colony overseer 会话
│       └── workers/<sid>/             # Worker 运行目录
│           ├── meta.json
│           ├── tool_tiers.json        # 已加载的可搜索工具
│           ├── tasks.json
│           ├── result.json            # 可选/历史墓碑（当前 runtime 不写）
│           ├── conversations/{meta,cursor,parts,partials}
│           ├── data/
│           └── logs/                  # 可选：RuntimeLogStore（传统图/调试）
│
├── memories/
│   ├── global/                        # 跨 Queen/Colony
│   │   ├── *.md
│   │   ├── user-profile.md            # 用户身份（设置页同步）
│   │   ├── log.jsonl
│   │   └── .reflection_error.txt      # 反思失败时的 traceback（best-effort）
│   ├── colonies/<name>/               # Colony 作用域
│   └── agents/queens/<queen_id>/      # Queen 作用域
│
├── skills/                            # 用户级技能（全局共享）
├── sessions/<session_id>/
│   └── attached_credentials.json      # 会话级凭据 attach
│
├── channels/<name>/                   # Channel 插件私有状态
├── turn_context/<name>/               # Turn Context 插件私有状态
├── claude_code/<queen_id>/
│   ├── config/                        # CLAUDE_CONFIG_DIR
│   ├── workspace/                     # 她的工作目录
│   ├── conversations.json             # 通信者→Claude Code 会话
│   └── mcp/<session_id>.json          # 每轮 Hive MCP server 配置（0600）
│
├── event_logs/<ts>.jsonl              # HIVE_DEBUG_EVENTS 开启
├── llm_logs/<ts>.jsonl                # HIVE_ENABLE_LLM_LOGS 开启
├── debug_logs/llm_request_*.json      # DEBUG 级别 LLM 请求摘要
├── failed_requests/*.json             # 失败请求的 curl 复现包（最多 50）
├── compaction_log/*.md                # HIVE_COMPACTION_DEBUG 开启
├── tool-artifacts/                    # 浏览器/网页/图片生成的产物
├── charts/*.png                       # chart_render 输出
├── browser-logs/browser-YYYY-MM-DD.jsonl # 浏览器遥测（按日轮转）
├── senders/
│   ├── threads.json                    # 外发邮件 Message-ID → 会话
│   └── usage.json                      # 发件人每日用量
├── workdir/workspaces/default/<agent_id>/current/ # 文件工具沙箱
├── logs/
│   ├── hive-server.log                # 20MB × 3（.1/.2/.3 轮转）
│   └── sentinel.log                   # 5MB × 3（.1/.2/.3 轮转）
├── archive/janitor-<stamp>/*.tar.gz   # Janitor 归档
├── maintenance/
│   ├── server.lock                    # 运行中 server 的 PID/port
│   ├── janitor.lock                   # 全局清理锁（2h TTL）
│   └── last_janitor_report.json
├── tmp/colony_uploads/                # 无 /var/tmp 时的导入暂存
├── （系统临时）hive-tracker-snapshots/ # UI 读 tracker.db 的本地快照（/var/tmp，非 HIVE_HOME）
│
└── （legacy）agents/、queen/、.migrated-v2/v3
```

POSIX 上 Colony 导入的暂存目录实际是 **`/var/tmp/hive-colony-uploads/`**（本地磁盘、非 tmpfs、非 NFS）；Windows 或没有 `/var/tmp` 时才落到 `HIVE_HOME/tmp/colony_uploads/`。

### 2.1 核心实体对照表

| 实体 | 身份/配置 | 运行时状态 | 生命周期 | 恢复方式 | 关键不变量 |
|---|---|---|---|---|---|
| **Queen** | `queens/<id>/profile.yaml`、`tools.json`、`skills_overrides.json` | `sessions/<sid>/{meta,events,conversations,tasks,...}` | 首次选中物化；编辑重写；删除 Queen 时删除 | 按需冷恢复 | 一个 Queen 一个身份目录；profile 是人格唯一来源 |
| **Colony** | `colonies/<c>/metadata.json`、`worker.json`、`profiles/*`、`tools.json`、`triggers.json`、`notifications.json` | `queens/`、`workers/`、`tracker/`、`colony.db`、`playbooks/` | 建群创建；`deleted` 软删除；导入/导出 | 按需打开/`--colony` | Colony 目录自包含；binding 不可变 |
| **Session** | `meta.json`（queen/agent/phase/锁/标题） | `events.jsonl`、`conversations/`、`tasks.json`、`summary.json` | 创建→活跃→冷→删除 | 冷恢复（parts+cursor） | 一个 session 一个目录；`meta.json` 是恢复入口 |
| **Task / Plan** | 无独立配置；session 级 | `tasks.json`（meta + highwatermark + tasks）、`reminder_state.json` | 首次任务工具创建；归档/删除 | 会话加载即读 | ID 单调不复用；fork 不携带 |
| **Tracker** | `_tracker_registry`（表/写列/key/mode） | `tracker/tracker.db`（+WAL） | 建群 `ensure_tracker_db`；删除 Colony | SQLite WAL 恢复 | `_` 前缀保留；key 必须有唯一索引 |
| **Worker** | `worker.json` / `profiles/<name>/worker.json` | `workers/<sid>/{meta,tool_tiers,tasks,conversations,data}` | spawn→运行→终态；Janitor deep-clean | 手动 `resume_worker_ids` | 不自动恢复；一个 Worker 一个目录；不递归编排 |
| **Trigger** | `triggers.json` | `last_fired_at` / `next_due_at` / `enabled` | set→active→fired→removed | Colony 加载时自动启动 | 只在会话加载时运行；错过静默跳过 |
| **Sentinel** | `notifications.json` | `escalations/*.json`、`sentinel_secret` | 升级→通知→回复→resolved | 启动重建 token 索引 | 一个 session 一个 open 升级；token 不可伪造 |
| **Playbook** | `playbooks/<name>.play.py` | `colony.db.playbook_runs`、`tracker.db` | 运行→completed/failed/stopped | 手动重跑（Tracker 续跑） | runner 无状态；重跑即续跑 |
| **Memory** | 无独立配置；`memory_reflection` / `memory_recall` | `memories/**/*.md` + `log.jsonl` | 反思→确认→退休/恢复 | 会话加载时召回 | `author=owner` 不可覆盖；4KB/200 文件 |
| **Skill** | `SKILL.md` frontmatter | `skills_overrides.json`、`skill_installs.json` | 发现→信任→启用→写入/安装 | 热重载 | 作用域优先级；全局安装需批准 |
| **Credential** | `credentials/metadata/index.json` | `credentials/*.enc`、`secrets/credential_key` | 收集→加密→解析→刷新→删除 | 首次使用解析 | key 丢失即不可读；Agent 永不接触 secret |
| **MCP** | `mcp_registry/installed.json`、`config.json` | `cache/`、子进程 | add/install→enable/mode→remove | 会话加载重新 spawn | 自带组不受 max_tools 限制 |
| **Channel** | `channels/<name>/`（插件自管） | 插件内存映射 | 启动→open_session→send→close | 插件 `open_session` | 每个聊天一个 Queen session |
| **Soul** | 无独立配置；`soul.queens.<id>` | `soul/{soul.json,history,proposals,state.json,log.jsonl}` | 每日反思→审阅/应用→回滚 | scheduler 按 due 运行 | 版本只增；证据逐字校验 |
| **Queen Mail** | `queen_mail` 配置 | `queens/mail.jsonl`、`claude_code/*/conversations.json` | send→deliver→reply→drop | 会话加载 + 路由文件 | 深度/日限额；正文在 session |

---

## 3. 按领域详述

### 3.1 全局配置与身份

#### `configuration.json`

**写入者**：`routes_config._write_config_atomic`（设置页）、`quickstart`、credential 同步、`sync_email_senders_env`、`_update_user_profile_memory`。
**读取者**：`config.get_hive_config()` 及几乎所有子系统。
**格式**：JSON object。顶层 section 见 12 章。
**生命周期**：安装时创建；设置页每次保存整体重写；缺失等价于空配置（所有 getter 回退默认）。
**原子性**：tempfile + replace，**无 fsync**。崩溃可能丢最后一次保存，但不会读到半截 JSON。
**注意**：`user_profile` 也会同步写 `memories/global/user-profile.md`，两处要保持一致。

#### `custom_prompts.json`

**写入者**：`routes_prompts` 的 CRUD。
**格式**：

```json
[
  {
    "id": "custom_1712345678901",
    "title": "Weekly report",
    "category": "custom",
    "content": "…",
    "skills": ["hive.browser-automation"],
    "tags": ["report"],
    "custom": true
  }
]
```

**生命周期**：用户创建/编辑/删除；无自动清理。
**原子性**：直接 `write_text`（等级 C）。并发保存可能截断；单用户场景可接受。

#### `runtime_id`

**写入者**：`sentinel/runtime_identity.get_runtime_id()`，首次需要时。
**格式**：一行文本，`local-<32hex>`。
**用途**：Hive Inbox 把回复路由回正确的 runtime。
**生命周期**：永久；进程重启复用；云上可被 `HIVE_RUNTIME_ID` / `E2B_SANDBOX_ID` 覆盖。

#### `sentinel_secret`

**写入者**：`sentinel/token._load_or_create_secret()`，首次发升级时。
**格式**：base64 的 32 字节 HMAC secret。
**权限**：0600。
**用途**：生成/校验升级消息里的 `ref: <token>`；不可伪造。
**生命周期**：永久；丢失后旧 token 全部失效（已发消息的回复无法路由）。

#### `trusted_repos.json` / `own_remotes`

- `trusted_repos.json`：`{"version":1,"entries":[{"repo_key","added_at","project_path"}]}`。技能信任门禁的用户批准记录。
- `own_remotes`：纯文本，每行一个 fnmatch glob（`github.com/myorg/*`），`#` 开头为注释。命中即视为自有仓库，无需批准。

#### `.install_notice_shown` / `.skill_trust_notice_shown`

空标记文件；只用于“提示只展示一次”。

#### `antigravity-accounts.json`

Antigravity OAuth 账号池；由 `llm/antigravity.py` 读取，`~/.hive` 下持久化。

---

### 3.2 Queen 身份目录

```text
queens/<queen_id>/
├── profile.yaml
├── avatar.*
├── _profile_snapshots/
├── tools.json
├── skills_overrides.json
├── skills/
└── soul/
```

#### `profile.yaml`

**写入者**：`queen_profiles._materialize_default_queen`（首次）、`update_queen_profile`、`replace_queen_persona`。
**格式**：YAML。人格字段：

```yaml
name: Steve Jobs
title: Head of Growth
core_traits: |
  …多行…
hidden_background:
  past_wound: …
  deep_motive: …
  behavioral_mapping: …
psychological_profile:
  social_masks: …
  anti_stereotype: …
behavior_triggers:
  - trigger: …
    reaction: …
world_lore:
  habitat: …
  lexicon: …
skills: …
examples:
  - user: …
    internal: …
    response: …
```

能力字段（`default_tool_categories`、`default_preset_skills`）只在**默认 YAML** 中存在，物化到 `profile.yaml` 时被剥离；它们从 `queen_defaults/*.yaml` 读取。

**生命周期**：首次被选中/打开时惰性创建；Dashboard 编辑整体重写；旧版本保存在 `_profile_snapshots/profile.yaml.<ts>.pre-dashboard-edit`，最多 30 份。
**原子性**：tempfile + fsync + replace（等级 A）。

#### `tools.json`

每 Queen 的 MCP 工具允许列表：

```json
{
  "enabled_mcp_tools": ["pdf_read", "terminal_exec"] ,
  "updated_at": "2026-11-27T…",
  "saved_on_version": "1.0.15"
}
```

- 缺失 → 角色默认；
- `null` → 允许全部 MCP 工具；
- `[]` → 全部禁用；
- 非 MCP 的生命周期/合成工具始终绕过。

**原子性**：tempfile + fsync + replace（等级 A）。

#### `skills_overrides.json`

```json
{
  "version": 1,
  "all_defaults_disabled": false,
  "overrides": {
    "hive.browser-automation": {
      "provenance": "framework",
      "enabled": true,
      "trust": null,
      "param_overrides": {},
      "notes": null,
      "created_at": "2026-…",
      "created_by": "owner"
    }
  },
  "deleted_ui_skills": ["my-old-skill"]
}
```

**写入者**：`skills/overrides.SkillOverrideStore.save()`（tmp + replace，无 fsync）。
**用途**：启停、来源、参数覆盖、UI 删除墓碑。
**生命周期**：用户操作时保存；无自动清理。
**注意**：docstring 仍写 `agents/queens/...`，v3 实际路径是 `queens/<id>/skills_overrides.json`。

#### `skills/`

Queen 作用域技能；每个技能一个目录，含 `SKILL.md` 和可选 `scripts/`、`references/`、`assets/`。

#### `soul/`

```text
soul/
├── soul.json              # 当前版本
├── history/v<N>.json      # 每个版本
├── proposals/<id>.json    # 待审提案
├── log.jsonl              # 版本变更日志
├── state.json             # 每个会话读到哪
└── probes.json            # 语气探针（可选）
```

`soul.json`：

```json
{
  "version": 3,
  "updated_at": "2026-11-27T…",
  "sections": {
    "now": [{"text": "…", "date": "2026-11-27"}],
    "people": [{"text": "…", "date": "2026-11-20"}]
  }
}
```

`proposals/<id>.json`：

```json
{
  "id": "…",
  "created_at": "…",
  "base_version": 2,
  "edits": [
    {"op":"add|update|remove","section":"people","text":"…","old":"…","date":"…",
     "evidence":[{"quote":"…","where":"…"}],"reason":"…","expired":false}
  ],
  "summary": "…",
  "status": "pending|applied|dismissed",
  "applied": [0,2],
  "sources": ["…"],
  "bootstrap": false,
  "probes": [{"q":"…","anchor":"…","before":"…","after":"…"}]
}
```

`log.jsonl` 每行：`{version, at, author, note, edits[]}`。
**生命周期**：每次保存写新版本 + history + log；回滚也是新版本；提案由 review/auto 决定。
**原子性**：`soul.json`/history/proposal 用 tempfile + replace（无 fsync）；`log.jsonl` 追加。

#### `_profile_snapshots/`

每次 Dashboard 编辑前保存原文件；只保留最近 30 份。

---

### 3.3 Session 目录（DM / Colony Overseer / Worker 同构）

三种 session 的**内部结构相同**，只是父目录不同：

| 类型 | 路径 |
|---|---|
| Queen DM | `queens/<queen>/sessions/<session_id>/` |
| Colony overseer | `colonies/<colony>/queens/<queen>/sessions/<session_id>/` |
| Worker | `colonies/<colony>/workers/<session_id>/` |

#### `meta.json`

会话级元数据；**非**对话内容。

| 字段 | 类型 | 说明 |
|---|---|---|
| `queen_id` | string | 该会话的 Queen 身份 |
| `agent_name` | string | 人类可读名（Colony 名或 Worker 名） |
| `agent_path` | string | Colony/Agent 目录绝对路径 |
| `created_at` | float | epoch 秒；冷恢复时保留，不刷新 |
| `phase` | `"independent"` / `"colony"` | Queen 阶段 |
| `loaded_tools` | string[] | 通过 `search_tools` 加载的工具（顺序持久化） |
| `crm_setup` | bool | 是否 CRM 设置会话（可选包） |
| `channel` | string | 来源 Channel 名 |
| `colony_id` | string | 所属 Colony（仅 Colony 会话） |
| `colony_spawned` | bool | 该 DM 是否已建群并锁定 |
| `spawned_colony_id` | string | 建群后的 Colony 名 |
| `spawned_colony_at` | string | 建群时间 |
| `superseded_by` | string | 被哪个新会话取代 |
| `superseded_at` | string | 取代时间 |
| `forked_from` | object | `{session_id, before_input_at}` |
| `colony_fork` | bool | 是否 Colony overseer（用于从 DM 历史过滤） |
| `compacted_from` | string | compact-and-fork 的来源 session |
| `compacted_at` | string | 压缩时间 |
| `title` | string | 会话标题 |
| `title_source` | `"auto"` / `"user"` | 标题来源 |

**不持久化**：`Session.mode`（由 `colony_id` 推导）、`sse_client_count`、`active_trigger_ids`、`queen_executor`、`colony_runtime` 等纯运行时字段。

**写入者**：`SessionManager._start_queen`（创建/恢复）、`QueenPhaseState._persist_meta`（phase/loaded_tools/crm_setup）、`mark_colony_spawned`、`superseded_by` 写入、`session_titles.write_title`、fork 流程。
**原子性**：等级 C（直接 `write_text`）。读方对损坏降级为空。
**生命周期**：创建会话时写；每次阶段/标题/锁变化 merge 更新；删除会话时随目录删除。

#### `events.jsonl`

**写入者**：`EventLogFile.write()`（通过 `EventBus._write_session_log_event`）。
**格式**：每行一个 `AgentEvent`：

```json
{
  "type": "client_output_delta",
  "stream_id": "queen",
  "node_id": "queen",
  "execution_id": "session_…",
  "data": {"snapshot": "…", "iteration": 3, "inner_turn": 0},
  "timestamp": "2026-11-27T…",
  "correlation_id": "…",
  "colony_id": "…",
  "seq": 123,
  "run_id": "…"
}
```

**写入策略**：

- 流式 delta（`CLIENT_OUTPUT_DELTA` / `LLM_TEXT_DELTA` / `LLM_REASONING_DELTA`）在内存合并，`LLM_TURN_COMPLETE` 时落一条最终快照；保留**首条**时间戳，保证冷回放排序。
- `data.full_request` 在写盘前剥除（实时 SSE 仍有）；避免每 tick 280KB。
- Worker 的元事件同时写 queen log 与 worker log；worker chatter 只写自己的 log。
- `SUBAGENT_REPORT` 是 worker log 的关闭点。

**原子性**：追加 + flush；失败自动重开一次；持续失败会置 `broken` 并停写（不阻断 Agent）。Janitor 重写时会在同目录产生临时文件 `events.jsonl.janitor-tmp`，崩溃后由下次清理扫除。
**生命周期**：会话期间持续增长；Janitor 可按 `events_rewrite_min_bytes` 重写去遥测；删除会话时删除。
**注意**：append-only 是消息索引增量同步的前提；任何“原地重写”都会让索引按文件大小回退并重建。

#### `summary.json`

**写入者**：`FileConversationStore.write_part` → `session_summary.update_summary`（best-effort）。
**格式**：

```json
{
  "message_count": 42,
  "last_message": "最近一条 assistant 文本（≤120 字符）",
  "last_active_at": 1764…,
  "last_part_seq": 41
}
```

**用途**：冷会话列表侧栏，避免每次扫描全部 parts。
**原子性**：`atomic_write`（等级 A）。
**生命周期**：派生缓存；缺失/过期时 `rebuild_summary` 全量重建；删除无影响。

#### `tasks.json`

**写入者**：`TaskStore._write_doc_unsafe`（在 per-session `threading.Lock` 下）。
**格式**：

```json
{
  "meta": {
    "role": "session",
    "creator_agent_id": "queen",
    "goal": "…",
    "created_at": 1764…,
    "schema_version": 1
  },
  "highwatermark": 12,
  "tasks": [
    {
      "id": 3,
      "subject": "…",
      "description": "…",
      "active_form": "…",
      "owner": "queen",
      "status": "pending|in_progress|completed|abandoned|archived",
      "blocks": [4],
      "blocked_by": [2],
      "metadata": {},
      "created_at": 1764…,
      "updated_at": 1764…
    }
  ]
}
```

**原子性**：`atomic_write`（等级 A）。
**生命周期**：首次任务工具调用时创建；每次增删改整体重写；`highwatermark` 保证 ID 不复用；删除会话时删除；fork 时**不携带**（新会话任务列表为空）。
**注意**：`TaskStore` 通过扫描三种 session 布局定位目录；找不到时落到 `HIVE_HOME/_misc/<slug>/tasks.json`。

#### `reminder_state.json`

**写入者**：`tasks/reminders._save_state`。
**格式**：

```json
{
  "turns_total": 12,
  "turns_since_task_op": 3,
  "task_tool_ever_used": true,
  "turns_since_last_reminder": 4,
  "drift_reminders_sent": 1,
  "turns_since_snapshot": 2,
  "snapshots_sent": 5,
  "last_snapshot_fingerprint": "…"
}
```

**用途**：让任务提醒的冷却/软上限跨重启保持。
**原子性**：`atomic_write`。
**生命周期**：派生但“有状态”；fork 不携带；删除后从零开始。

#### `conversations/meta.json`

**写入者**：`NodeConversation._persist_meta`（首次持久化时一次）。
**格式**：

```json
{
  "system_prompt": "…",
  "max_context_tokens": 180000,
  "compaction_threshold": 0.8,
  "compaction_buffer_tokens": 8000,
  "compaction_buffer_ratio": 0.4,
  "compaction_warning_buffer_tokens": 12000,
  "output_keys": ["…"]
}
```

**原子性**：`atomic_write`。
**生命周期**：会话第一次写消息时创建；system prompt 变化后 `_meta_persisted=False` 会重写；恢复时用于重建 `NodeConversation`。

#### `conversations/cursor.json`

**写入者**：`cursor_persistence.write_cursor` / `NodeConversation._write_next_seq`。
**格式**：

```json
{
  "iteration": 12,
  "node_id": "queen",
  "outputs": {"key": "value 或落盘引用"},
  "tool_calls_used": 87,
  "recent_responses": ["…"],
  "recent_tool_fingerprints": [[["terminal_exec", "{\"cmd\":\"…\"}"]]],
  "pending_input": {
    "reason": "ask_user",
    "questions": [{"id": "q1", "prompt": "…", "options": ["…"]}],
    "emit_client_request": true
  },
  "user_stopped": false,
  "next_seq": 143
}
```

| 字段 | 作用 |
|---|---|
| `iteration` | 恢复时的起始迭代 |
| `node_id` | 哪个节点写的 |
| `outputs` | `OutputAccumulator` 的持久化 |
| `tool_calls_used` | 生命周期预算跨恢复累计 |
| `recent_responses` | 停顿检测窗口 |
| `recent_tool_fingerprints` | 死循环检测窗口 |
| `pending_input` | 停泊的问题/凭据表单/建群建议 |
| `user_stopped` | 用户停止跨重启保持 |
| `next_seq` | 下一条消息序号 |

**原子性**：`atomic_write`（等级 A）。
**生命周期**：每轮/停泊/压缩前写；恢复时读取；删除会话时删除。

#### `conversations/parts/<seq>.json`

**写入者**：`NodeConversation._persist`（每条消息）。
**格式**：`Message.to_storage_dict()`：

```json
{
  "seq": 41,
  "role": "user|assistant|tool",
  "content": "…",
  "tool_use_id": "…",
  "tool_calls": [{"id":"…","type":"function","function":{"name":"…","arguments":"…"}}],
  "is_error": false,
  "phase_id": "…",
  "is_transition_marker": false,
  "is_client_input": true,
  "image_content": [{"type":"image_url","image_url":{"url":"data:…"}}],
  "is_skill_content": false,
  "run_id": "…",
  "is_system_reminder": false,
  "truncated": false,
  "inherited_from": "…",
  "is_trigger": false,
  "thinking_blocks": [{"type":"thinking","thinking":"…","signature":"…"}],
  "reasoning_content": "…",
  "images": [{"type":"image_url","image_url":{"url":"hive-attachment://…"},"filename":"…"}],
  "spillover_path": "/abs/path/tool_…txt"
}
```

**只写非默认字段**（None/False 省略）；读取时 `from_storage_dict` 补默认。
**原子性**：`atomic_write`（等级 A，fsync）。
**生命周期**：追加；压缩时删除旧 seq；fork 时按切点复制；删除会话时删除。
**注意**：`created_at` 不在 part 中；`summary` 回退到文件 mtime。

#### `conversations/partials/<seq>.json`

**写入者**：`NodeConversation.checkpoint_partial_assistant`（流式过程中反复覆盖）。
**格式**：`{seq, role, content, truncated, tool_calls?}`。
**用途**：进程在流式中途崩溃时，恢复最后一条未完成 assistant 的已产出文本/工具调用。
**生命周期**：真实 `parts/<seq>.json` 写入后 `clear_partial` 删除；恢复时读取后也清理。
**原子性**：`atomic_write`。

#### `compaction_status.json`

**写入者**：`compaction_status.mark_in_progress/done/failed`。
**格式**：

```json
{"status":"in_progress","started_at":"…"}
{"status":"done","completed_at":"…","messages_compacted":12,"summary_chars":3456}
{"status":"failed","completed_at":"…","error":"…"}
```

**用途**：Colony fork 的后台压缩状态；会话加载时 `await_completion` 等待。
**原子性**：直接写（等级 C）。
**生命周期**：fork 前写入；完成/失败后保留；启动时 sweep 掉陈旧 `in_progress`。

#### `skill_installs.json`

**写入者**：`skills/install_requests._save`（tmp + replace）。
**格式**：

```json
[
  {
    "id": "…",
    "name": "hive.my-skill",
    "description": "…",
    "body": "…",
    "files": [{"path":"scripts/run.py","content":"…"}],
    "requested_at": "…",
    "status": "pending|approved|denied",
    "decided_at": "…"
  }
]
```

**用途**：`write_skill(scope="global")` 的待批准安装请求；批准后把这里的副本写入 `HIVE_HOME/skills`。
**生命周期**：请求创建→用户决定→保留记录；会话删除时删除。

#### `timeline.jsonl` / `timeline_meta.json`

`timeline.jsonl` 每行：

```json
{
  "session": "session_…",
  "turn": 3,
  "kind": "event|fact|plan",
  "text": "The user attended a wedding in Kyoto on 2026-11-12.",
  "start": "2026-11-12",
  "end": "2026-11-12",
  "aliases": ["wedding", "Kyoto", "travel"]
}
```

`timeline_meta.json`：

```json
{"version":1,"user_turns":42,"model":"…","updated_at":"…"}
```

**用途**：`search_timeline` 的日期检索；只从用户消息抽取。
**原子性**：items 追加；meta tmp + replace（无 fsync）。
**生命周期**：每 3 条新用户消息或 300s 冷却触发；会话停止时 flush；fork 不携带；默认关闭。

#### `data/attachments/`

用户上传与助手附件的落盘位置：

- 文件名：`sanitize_attachment_basename` + 内容类型扩展名 + 去重后缀；
- 大 PDF 只保留路径指针，不把整份文本塞进上下文；
- `attach_file` 从磁盘重新注入图片/PDF；
- 旧会话可能在顶层 `attachments/`（pre-D1），读取方两者都查。

**生命周期**：上传即写；随会话删除；Janitor 不单独清理（属于会话树）。

#### `data/*.txt` / `data/*.json`

工具结果溢写：

- `max_tool_result_chars`（默认 30,000）超出后写 `<tool>_<n>.txt`；
- 上下文保留预览 + 绝对路径；
- `OutputAccumulator` 的大值写 `output_<key>.json/txt`；
- 可被 Janitor 作为 orphan 清理。

#### `logs/`（可选）

`RuntimeLogStore` 的 session 级日志：

```text
logs/summary.json      # L1 汇总
logs/details.jsonl     # L2 节点完成
logs/tool_logs.jsonl   # L3 工具步骤
```

主要用于传统图执行/调试；Colony 主路径不依赖。

#### `sessions/<session_id>/attached_credentials.json`

**注意路径不同**：不在 Queen session 目录下，而在 `HIVE_HOME/sessions/<session_id>/attached_credentials.json`。

```json
[{"credential_id": "github", "account": "work"}]
```

**写入者**：`credential_tool.add_attachment/remove_attachment`（直接写）。
**用途**：把凭据固定到当前会话，每轮提示“已连接账号”。
**生命周期**：attach/detach；会话删除不自动清理（潜在残留）。

---

### 3.4 Colony 目录

#### `metadata.json`

**写入者**：`fork_session_into_colony`、`update_colony_metadata`、`_ensure_minimal_colony`、软删除。
**格式**：

```json
{
  "colony_id": "gtm_agency_leads",
  "queen_name": "queen_growth",
  "queen_session_id": "session_…",
  "source_session_id": "session_…",
  "created_at": "2026-11-27T…",
  "updated_at": "2026-11-27T…",
  "max_concurrent_workers": 8,
  "workers": {
    "worker": {"task": "…", "spawned_at": "…"}
  },
  "worker_profiles": [
    {
      "name": "slack-work",
      "task": "…",
      "skill_name": "…",
      "integrations": {"slack": "work"},
      "concurrency_hint": 2,
      "prompt_override": "…",
      "tool_filter": ["…"],
      "browser_profile": "work"
    }
  ],
  "scaffolded": false,
  "deleted": false,
  "icon": "…"
}
```

**生命周期**：建群时写；`update_colony_metadata` 原子 merge；软删除只置 `deleted=true`（目录保留，名字由 `vacate_soft_deleted_colony` 腾出）。
**原子性**：`update_colony_metadata` 用 tempfile + fsync + replace；fork 流程里直接 `write_text`（等级 C）。

#### `worker.json` / `profiles/<name>/worker.json`

**写入者**：`fork_session_into_colony` 用 `worker_definition.build_meta()`。
**格式**：

```json
{
  "name": "worker",
  "version": "1.0.0",
  "description": "Worker clone from queen session …",
  "input_data": {
    "binding": {
      "name": "colony",
      "dir": "/abs/colonies/colony",
      "tracker_db": "/abs/colonies/colony/tracker/tracker.db"
    },
    "task": "…"
  },
  "goal": {
    "description": "…",
    "success_criteria": [],
    "constraints": []
  },
  "system_prompt": "…",
  "tools": ["terminal_exec", "…"],
  "searchable_tools": [],
  "skills_catalog_prompt": "…",
  "protocols_prompt": "…",
  "skill_dirs": ["…"],
  "identity_prompt": "",
  "memory_prompt": "",
  "queen_phase": "colony",
  "queen_id": "queen_growth",
  "loop_config": {
    "max_iterations": 3,
    "grace_iterations": 1,
    "tool_call_budget": 30,
    "tool_call_hard_multiple": 3,
    "tool_call_lifetime_budget": 200,
    "max_context_tokens": 180000,
    "max_tool_result_chars": 30000
  },
  "spawned_from": "session_…",
  "spawned_at": "2026-11-27T…",
  "concurrency_hint": 8
}
```

**生命周期**：建群时写；`ensure_tracker_db` 会重写 `input_data.binding` 并清理 legacy 字段；删除 Colony 时删除。
**原子性**：直接 `write_text`（等级 C）。

#### `profiles/<name>/worker.json`

与默认 `worker.json` 同 schema；`WorkerProfile` 可覆盖 `task`、`skill_name`、`integrations`、`concurrency_hint`、`prompt_override`、`tool_filter`、`browser_profile`。
**注意**：`ensure_tracker_db._patch_worker_configs` 只 glob 顶层 `*.json`，**不会**补丁 `profiles/*/worker.json`；profile 的 binding 依赖 fork 时写入的正确值。

#### `tools.json`（Colony）

```json
{
  "enabled_mcp_tools": ["terminal_exec", "…"] | null,
  "updated_at": "…",
  "saved_on_version": "1.0.15"
}
```

**用途**：Colony 级 MCP 工具允许列表；`null` 允许全部，`[]` 全部禁用。
**原子性**：tempfile + fsync + replace。
**生命周期**：建群时从 Queen 当前 allowlist 复制；用户编辑；删除 Colony 时删除。

#### `skills_overrides.json`（Colony）

布局注释把它列在 Colony 下，但**当前代码没有读取 Colony 作用域的 override store**：`SkillsManager` 的 `queen_overrides_path` 指向 `queens/<queen>/skills_overrides.json`，Colony 技能继承所属 Queen 的覆盖。不要依赖 Colony 下的这份文件。

#### `skills/`

Colony 作用域技能；`write_skill(scope="colony")` 写入这里，之后 spawn 的 Worker 都能在 catalog 中看到。

#### `playbooks/<name>.play.py`

**写入者**：`run_playbook` 的 inline 脚本保存。
**格式**：Python 源码，必须有 `meta` 与 `async def run(args)`；`meta` 至少 `name`，可选 `description`、`concurrency`（默认 8，最大 32）。
**生命周期**：同名覆盖，无版本历史；删除 Colony 时删除。

#### `triggers.json`

**写入者**：`_save_trigger_to_agent` / `_remove_trigger_from_agent` / `_persist_active_triggers`。
**格式**：JSON 数组：

```json
[
  {
    "id": "morning_report",
    "name": "…",
    "trigger_type": "timer|webhook",
    "trigger_config": {"cron":"0 9 * * *"} ,
    "task": "…",
    "enabled": true,
    "last_fired_at": "2026-…",
    "next_due_at": "2026-…"
  }
]
```

**原子性**：直接 `write_text`（等级 C）。
**生命周期**：set/remove 时重写；会话关闭后 `enabled` 保留；加载时计算错过 tick（当前实现静默跳过并改写 `last_fired_at`）。

#### `notifications.json`

```json
{
  "sentinel_enabled": true,
  "channel": "hive|telegram|slack",
  "target": {"chat_id": 123, "channel": "C123"},
  "allowlist": ["user@example.com"],
  "thread": {"channel": "C123", "ts": "…"},
  "classify_after_seconds": 300,
  "updated_at": "…"
}
```

**原子性**：tempfile + fsync + replace。
**生命周期**：Sentinel 设置页写入；`thread` 由升级回复更新；删除 Colony 时删除。

#### `escalations/<id>.json`

```json
{
  "escalation_id": "…",
  "colony_id": "…",
  "session_id": "…",
  "correlation_token": "…",
  "park_reason": "…",
  "question_text": "…",
  "channel": "hive|telegram|slack",
  "thread_ref": {},
  "status": "open|resolved",
  "created_at": "…",
  "resolved_at": "…",
  "resolved_by": "…"
}
```

**原子性**：tempfile + fsync + replace。
**生命周期**：升级创建→回复后 `resolved`；保留可审计；启动时 `list_all_open` 重建 token 索引。

#### `tracker/tracker.db`

**Schema v3**（`host/tracker_db.py`）：

```sql
CREATE TABLE IF NOT EXISTS _tracker_registry (
    table_name TEXT PRIMARY KEY,
    write_columns TEXT NOT NULL,   -- JSON array
    key_columns TEXT NOT NULL,     -- JSON array
    mode TEXT NOT NULL CHECK (mode IN ('upsert','append')),
    registered_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_changes (
    id INTEGER PRIMARY KEY,
    table_name TEXT NOT NULL,
    pk TEXT NOT NULL,              -- JSON object
    op TEXT NOT NULL CHECK (op IN ('insert','update')),
    changed_at TEXT NOT NULL
);
CREATE INDEX _tracker_changes_table_id ON _tracker_changes(table_name, id);
```

业务表由 Queen 通过 `tracker_sql` 创建；`tracker_register_writable` 要求 key 列有 UNIQUE 索引，并安装 `AFTER INSERT/UPDATE` 触发器写 `_tracker_changes`。
**连接**：WAL、`synchronous=NORMAL`、`foreign_keys=ON`、`busy_timeout=5000`。
**生命周期**：建群时 `ensure_tracker_db`；每个 Colony 一份；导出/导入随目录；删除 Colony 时删除。
**限制**：单次 SQL ≤20 条语句；变更日志最多 10,000 条；`_` 前缀表受保护。

> Tracker 的完整机制（四个工具、注册门禁、SQL 安全、并发、变更日志、已知绕过）见 [19 Tracker DB 机制深入](19-tracker-db.md)。

#### Tracker 的 UI 读取快照（`/var/tmp/hive-tracker-snapshots/`）

Dashboard 的 Colony Data 页**不直接查 NFS 上的 tracker.db**，而是先做本地快照：

| 项 | 值 |
|---|---|
| 快照目录 | `/var/tmp/hive-tracker-snapshots/`（硬编码；不是 `HIVE_HOME`） |
| 快照方式 | `shutil.copyfile` 主 `.db` 文件（**不用** `sqlite3.backup()`，避免 NFS 上的 WAL/SHM 问题） |
| 打开方式 | `file:<snapshot>?mode=ro&immutable=1`（跳过 journal/WAL/lock） |
| TTL | 2s（同一 source 的并发读共享一份快照） |
| 并发 | 每 source 一把锁 + double-checked cache；同一时刻只允许一次 copy |
| 重试 | 最多 3 次；`sqlite_master` 探针失败则重试 |
| 读取超时 | 15s（`_COLONY_DATA_READ_TIMEOUT_S`），超时返回 503 `colony_data_timeout` |
| 行数上限 | 表格行 500/页；change log 2000 条/次 |
| 变化日志 | 读 `_tracker_changes`；`since<0` 只返回 cursor + covered；按 `(table,pk)` 去重，first op wins |

**一致性语义**：

- 快照反映的是**最后一次 WAL checkpoint** 的状态，通常落后几秒；浏览可以接受。
- 写路径（PATCH row）直接打开原 `tracker.db`（不是快照），并用主键校验；不能改主键列。
- 快照文件由缓存拥有，调用方不删除；过期后下一次刷新替换并 unlink 旧文件。
- Hive 重启后快照目录可能残留；不是 canonical 数据，可安全删除。

#### `colony.db`

```sql
CREATE TABLE IF NOT EXISTS playbook_runs (
    run_id TEXT PRIMARY KEY,
    name TEXT,
    status TEXT NOT NULL,           -- 'done' | 'error'
    dispatched INTEGER NOT NULL DEFAULT 0,
    dead_lettered INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    result_json TEXT,
    logs_json TEXT,
    created_at TEXT NOT NULL
);
```

**用途**：Playbook 运行日志；**不是**恢复权威（恢复靠 Tracker 行）。
**连接**：WAL；无 busy_timeout（短事务）。
**生命周期**：每次 run 结束 upsert；保留供 UI 查看；删除 Colony 时删除。

#### `seed_conversation/`

**写入者**：fork 流程把 Queen 的 `conversations/` 复制到这里。
**格式**：与 `conversations/` 相同（`meta.json`、`cursor.json`、`parts/`）。
**用途**：Worker 启动时的初始上下文；不是活跃会话，只读。
**生命周期**：建群时复制；压缩成功后是摘要，失败时是原始 transcript；删除 Colony 时删除。

#### `queens/` / `workers/`

- `queens/<queen>/sessions/<sid>/`：Colony overseer 的会话树，结构与 3.3 完全相同。
- `workers/<sid>/`：Worker 运行目录：

```text
workers/<sid>/
├── meta.json            # 血缘面包屑
├── tool_tiers.json      # {"loaded_tools": ["…"]}
├── tasks.json           # Worker 自己的任务列表
├── result.json          # 可选/历史墓碑；当前 runtime 不写
├── conversations/       # 与 3.3 相同
└── data/                # 溢写/附件
```

`workers/<sid>/meta.json`：

```json
{
  "worker_id": "session_…",
  "queen_session_id": "session_…",
  "queen_name": "queen_growth",
  "colony_id": "…",
  "spawned_at": 1764…,
  "task": "前 500 字符",
  "profile_name": "…",
  "batch_id": "rpw_…",
  "batch_index": 1,
  "batch_size": 4,
  "worker_seq": 0,
  "goal": "…"
}
```

**生命周期**：spawn 时写；`result.json` 若由桌面/历史写入则作为墓碑；Janitor deep-clean 保留 `meta.json`/`tasks.json`/`result.json`，删除 transcript。

---

### 3.5 记忆目录

#### `memories/<scope>/<file>.md`

**文件格式**：

```markdown
---
name: User Profile
description: User identity: Ann
type: profile | preference | environment | feedback
author: owner | queen | reflection
learned: 2026-11-09
confirmed: 2026-11-20
---

正文…
```

- `MAX_FILES=200`/作用域；`MAX_FILE_SIZE_BYTES=4096`；
- `author=owner` 的文件反思不可覆盖/删除；
- `confirmed` 是召回年龄的基准。

**写入者**：`memory_log.write/retire/restore`（直接写 `.md` + 追加 `log.jsonl`）。
**原子性**：`.md` 直接写（等级 C）；`log.jsonl` 追加。
**生命周期**：反思/用户编辑写入；退休删除文件但保留日志；恢复从日志写回；Janitor 保留 `memories/` 永不清理。

#### `memories/<scope>/log.jsonl`

每行：

```json
{
  "at": "2026-11-27T…",
  "op": "write|retire|restore",
  "file": "user-profile.md",
  "author": "owner|queen|reflection",
  "reason": "…",
  "sources": [{"quote":"…","where":"…"}],
  "before": "…",
  "after": "…"
}
```

**用途**：记忆变更历史、恢复、审计；可增长，无自动截断。

#### `user-profile.md`

由设置页写入（`routes_config._update_user_profile_memory`），保留 reflection 写的其他 section；`author=owner` 但 `claim=False`（不把整个文件归 owner）。

---

### 3.6 消息索引（Derived）

```text
$HIVE_HOME/.message_index/
├── events/<scope>/<owner>/<session>/<NNNNNN>.<role>.txt
├── data/<scope>/<owner>/<session>/<spill_filename>.txt
├── meta/<scope>/<owner>/<session>/cursor.json
├── meta/<scope>/<owner>/<session>/data_map.json
└── recall/<scope>/<owner>.sqlite3
```

`<scope>` 是 `queens` 或 `colonies`。镜像只保存**可检索内容**：

- user 消息正文；
- assistant 的最终 prose 快照；
- tool result 正文（大结果保存落盘副本）。

`cursor.json`：

```json
{
  "events_byte_offset": 123456,
  "next_ordinal": 42,
  "pending_assistant": {"iteration":3,"inner_turn":0,"snapshot":"…","ordinal":41}
}
```

`data_map.json`：`{"<spill_filename>": <turn_ordinal>}`。

`recall/<owner>.sqlite3`：

```sql
CREATE TABLE sessions (name TEXT PRIMARY KEY, events_size INTEGER NOT NULL, last_user INTEGER NOT NULL);
CREATE TABLE turns (
  id INTEGER PRIMARY KEY, session TEXT NOT NULL, ordinal INTEGER NOT NULL,
  user TEXT NOT NULL, reply TEXT NOT NULL, length INTEGER NOT NULL,
  UNIQUE(session, ordinal)
);
CREATE TABLE postings (term TEXT NOT NULL, turn INTEGER NOT NULL, tf INTEGER NOT NULL,
                       PRIMARY KEY(term, turn)) WITHOUT ROWID;
```

**生命周期**：完全派生；删除后下次查询重建；`events.jsonl` 缩短（被重写）时游标重置；Janitor 的 Queen hygiene 会清 meta/cursor 但保留索引可重建。
**并发**：SQLite WAL + 30s timeout；每个 session 的镜像同步用文件锁。

---

### 3.7 技能目录

| 作用域 | 路径 | 说明 |
|---|---|---|
| framework | 包内 `skills/_default_skills/` | 只读，12 个 |
| preset | 包内 `skills/_preset_skills/` | 只读，8 个，默认关 |
| user cross-client | `~/.agents/skills/` | 跨客户端 |
| user Hive | `HIVE_HOME/skills/` | 用户安装/全局批准 |
| queen_ui | `queens/<q>/skills/` | Queen 自己的 |
| colony_ui | `colonies/<c>/skills/` | Colony |
| project cross-client | `<project>/.agents/skills/` | 仓库 |
| project Hive | `<project>/.hive/skills/` | 仓库 |

每个技能：

```text
<skill-dir>/
├── SKILL.md          # YAML frontmatter + markdown body
├── scripts/          # 可选
├── references/       # 可选
└── assets/           # 可选
```

`SKILL.md` frontmatter：`name`、`description` 必需；`license`、`compatibility`、`metadata`、`allowed-tools` 可选。
**生命周期**：安装/写入即生效（热重载监听 `SKILL.md` 与 overrides）；UI 删除会 rm-tree + 写墓碑；全局安装需用户批准。

`registry_cache/`：

```text
registry_cache/skill_index.json   # 社区索引缓存
registry_cache/metadata.json      # 抓取时间等
```

**Derived**；网络失败时使用过期缓存。

---

### 3.8 凭据与 MCP

#### `credentials/credentials/<id>.enc`

**写入者**：`EncryptedFileStorage.save()`。
**格式**：Fernet 加密后的 `CredentialObject` JSON：

```json
{
  "id": "github_oauth",
  "credential_type": "oauth2",
  "keys": {
    "access_token": {"name":"access_token","value":"…","expires_at":"…","metadata":{}},
    "refresh_token": {"name":"refresh_token","value":"…","expires_at":null,"metadata":{}},
    "_integration_type": {"name":"_integration_type","value":"github",…},
    "_identity_email": {"name":"_identity_email","value":"…",…}
  },
  "provider_id": "oauth2",
  "last_refreshed": "…",
  "auto_refresh": true,
  "last_used": "…",
  "use_count": 12,
  "description": "…",
  "tags": ["work"],
  "created_at": "…",
  "updated_at": "…"
}
```

**原子性**：直接 `open("wb")` 写（等级 C）；不是原子替换。写入中断会留下不可解密的 `.enc`。
**生命周期**：保存/刷新/删除；`HIVE_CREDENTIAL_KEY` 丢失则永久不可读（服务端会自愈删除不可读文件）。

#### `credentials/metadata/index.json`

**格式**：

```json
{
  "version": "2.0",
  "store_path": "/abs/credentials",
  "generated_at": "…",
  "credentials": {
    "github_oauth": {
      "credential_type": "oauth2",
      "provider": "github",
      "alias": "work",
      "identity": {"email": "…"},
      "key_names": ["access_token","refresh_token"],
      "created_at": "…",
      "updated_at": "…",
      "last_refreshed": "…",
      "expires_at": "…",
      "auto_refresh": true,
      "tags": ["work"]
    }
  }
}
```

**不含 secret**；仅供枚举/人读。`.enc` 是权威，索引可重建。
**原子性**：直接写（等级 C）。

#### `secrets/credential_key`

纯文本 Fernet key；目录 0700、文件 0600。用于解密 `.enc`。
**生命周期**：首次启动生成；`HIVE_CREDENTIAL_KEY` 优先；从环境变量得到时会回写文件。

#### 外部订阅凭据源（不在 `HIVE_HOME` 内）

| 路径/存储 | 读取者 | 写入者 | 生命周期 |
|---|---|---|---|
| `~/.claude/.credentials.json` | `loader/agent_loader.py`、`config.get_api_key` | Claude Code / Hive 刷新 token 时回写 | Claude Code 管理；Hive 只读/刷新 |
| macOS Keychain `Claude Code-credentials` | 同上（Darwin 优先） | Hive 刷新时回写 | 系统 Keychain |
| `~/.codex/auth.json` | `loader/agent_loader.py`、`config.get_api_key` | Hive 刷新时回写 | Codex CLI 管理；Hive 只读/刷新 |
| macOS Keychain `Codex Auth` | 同上（Darwin 优先） | Codex CLI 管理 | 系统 Keychain |
| `~/.kimi/config.toml` | `get_kimi_code_token` | Kimi Code CLI | 用户 `kimi /login` 创建 |
| `~/Library/Application Support/Antigravity/User/globalStorage/state.vscdb`（macOS） | `llm/antigravity.py` | Antigravity IDE | IDE 管理 |
| `~/.config/Antigravity/User/globalStorage/state.vscdb`（Linux） | 同上 | Antigravity IDE | IDE 管理 |
| `~/.hive/antigravity-accounts.json` | 同上 | Hive OAuth 实现 | Hive 管理 |

**风险**：

- 这些文件**不在 `HIVE_HOME`**，备份 `HIVE_HOME` 不会包含它们；换机/恢复订阅登录需要单独处理。
- Hive 可能刷新并回写 Claude Code / Codex 的 token；与 CLI 同时使用会竞争。
- 订阅用于无人值守 Agent 可能违反条款（产品已明示）。
- Keychain 条目受 OS 用户/钥匙串权限保护；Hive 进程以同一用户运行即可读取。

#### `mcp_registry/installed.json`

```json
{
  "servers": {
    "comfy": {
      "source": "local|registry",
      "manifest_version": "1.0.0",
      "manifest": {
        "name": "comfy",
        "transport": {"supported": ["stdio"], "default": "stdio"},
        "stdio": {"command": "uvx", "args": ["…"], "env": {}}
      },
      "installed_at": "…",
      "installed_by": "cli",
      "transport": "stdio",
      "mode": "tools|cli",
      "enabled": true,
      "pinned": false,
      "auto_update": false,
      "resolved_package_version": "…",
      "overrides": {"env": {}, "headers": {}},
      "last_health_check_at": "…",
      "last_health_status": "ok",
      "last_error": null,
      "last_used_at": "…",
      "last_validated_with_hive_version": "1.0.15"
    }
  }
}
```

**原子性**：tempfile + fsync + replace。
**生命周期**：add/install/remove/enable/disable/mode 时重写；健康检查更新字段；Hive 自带组不再写入此文件。

#### `mcp_registry/config.json`

```json
{"index_url":"https://…/registry_index.json","refresh_interval_hours":24}
```

#### `mcp_registry/cache/registry_index.json`

远程 registry 的缓存；`last_fetched` 记录抓取时间（旧版为 `last_fetched.json`）。

#### `cache/mcp2cli/`

`mcp2cli` 的工具列表缓存；`MCP2CLI_CACHE_DIR` 可覆盖。

---

### 3.9 自治与入口

#### `triggers.json`

见 3.4。**只在会话加载时运行**；`last_fired_at`/`next_due_at` 是错过 tick 计算的依据（当前实现静默跳过）。

#### `notifications.json` / `escalations/<id>.json`

见 3.4。`notifications.json` 是 per-Colony Sentinel 配置；`escalations/*.json` 是升级记录。

#### `queens/mail.jsonl`

**写入者**：`QueenMail._log`（追加）。
**格式**：

```json
{
  "id": "hop_…",
  "at": "2026-11-27T…",
  "kind": "message|reply",
  "from": "queen_a",
  "to": "queen_b",
  "from_session": "…",
  "to_session": "…",
  "caused_by": "hop_…",
  "depth": 2,
  "chars": 123,
  "dropped": "max_depth|disabled|writer_session_gone|locked"
}
```

**用途**：Queen Mail 的链审计；正文不落此文件（在 session 里）。
**生命周期**：永久追加；无自动清理。

#### `claude_code/<queen_id>/conversations.json`

```json
{
  "conversations": {
    "<session_id>": {"peer": "queen_b", "reply_to": "session_…", "started": true}
  }
}
```

**原子性**：tmp + replace。
**用途**：Claude Code Queen 的“通信者→会话”映射；重启后恢复回信路径。

#### `claude_code/<queen_id>/mcp/<session_id>.json`

```json
{
  "mcpServers": {
    "hive": {
      "type": "stdio",
      "command": "/abs/python",
      "args": ["-m", "framework.server.queen_mail_mcp"],
      "env": {
        "HIVE_QUEEN_MAIL_URL": "…",
        "HIVE_QUEEN_MAIL_TOKEN": "…",
        "HIVE_QUEEN_MAIL_FROM": "…"
      }
    }
  }
}
```

**权限**：0600；token 只对本次会话有效，运行结束删除。

#### `channels/<name>/` / `turn_context/<name>/`

插件私有状态目录；Hive 只保证目录存在，内容由插件定义。

---

### 3.10 日志与诊断

| 路径 | 开启方式 | 格式 | 保留 |
|---|---|---|---|
| `event_logs/<ts>.jsonl` | `HIVE_DEBUG_EVENTS=1` 或目录 | `AgentEvent.to_dict()` 每行 | 7 天（Janitor） |
| `llm_logs/<ts>.jsonl` | `HIVE_ENABLE_LLM_LOGS=1` | `_kind=session_header/turn` | 7 天 |
| `debug_logs/llm_request_*.json` | `DEBUG` 日志级别 | 消息摘要数组 | 手动/无自动 |
| `failed_requests/*.json` | 请求失败时 | 请求 + curl | 最多 50 个 |
| `compaction_log/*.md` | `HIVE_COMPACTION_DEBUG=1` | Markdown 表格 + 预览 | 7 天 |
| `tool-artifacts/` | 工具写入 | 浏览器/网页/图片产物 | 14 天 |
| `charts/*.png` | `chart_render` | PNG | 不自动清理（保留） |
| `logs/hive-server.log` | 默认（可 `HIVE_NO_FILE_LOG=1` 关） | 文本/JSON | 20MB×3 |
| `logs/sentinel.log` | `HIVE_SENTINEL_LOG=1` | 文本 | 5MB×3 |

`llm_logs` 的 `turn` 记录：

```json
{
  "_kind": "turn",
  "timestamp": "…",
  "execution_id": "…",
  "node_id": "…",
  "stream_id": "…",
  "iteration": 3,
  "header_hash": "…",
  "message_hashes": ["…"],
  "new_messages": {"<hash>": {…}},
  "assistant_text": "…",
  "tool_calls": [],
  "tool_results": [],
  "token_counts": {}
}
```

`session_header` 记录 system prompt 与 tools，只在变化时写。

#### `social_rate_limits.db`

```sql
CREATE TABLE actions (
  id INTEGER PRIMARY KEY,
  platform TEXT NOT NULL,
  account_id TEXT NOT NULL,
  action_type TEXT NOT NULL,
  target_id TEXT,
  performed_at REAL NOT NULL,
  session_id TEXT
);
CREATE INDEX idx_actions_lookup ON actions(platform, account_id, action_type, performed_at);
```

WAL；用于社媒限速的滑动窗口与最小间隔；不自动清理历史（可按时间归档）。

---

#### 3.10.1 浏览器桥、GCU 与文件沙箱

| 路径 | 写入者 | 格式 | 生命周期 | 原子性 |
|---|---|---|---|---|
| `~/.hive/browser_default.json` | `gcu/browser/bridge.py` | `{"label": "…", "extension_id": "…"}` | 侧栏“星标默认 profile”时写；重启恢复 | 直接写 |
| `~/.hive/browser_contexts.json` | `gcu/browser/tools/lifecycle.py` | `{profile: {groupId, name, browser_profile, activeTabId}}` | 每次 tab group 变化；gcu 重启 rehydrate；无 live tab 的组丢弃 | 直接写 |
| `~/.hive/browser-logs/browser-YYYY-MM-DD.jsonl` | `gcu/browser/telemetry.py` | 每行工具调用/动作 | 按 UTC 日轮转；无自动清理 | 追加 |
| `~/.hive/bridge_host.log` | `gcu/bridge_host.py` | 文本 supervisor 日志 | 桥启动/重启；无轮转 | 追加 |
| `GCU_LOG_FILE` / `HIVE_GCU_MEMTRACE_FILE` | gcu CLI/server | 外部指定的日志/内存追踪文件 | 用户环境变量 | 外部 |
| `~/.hive/workdir/workspaces/default/<agent_id>/current/` | `aden_tools/file_system_toolkits/security.py` | 文件沙箱 | 工具首次使用创建；无自动清理 | 目录 |

**重要路径差异**：这些浏览器/沙箱路径使用 **`Path.home() / ".hive"` 硬编码**，不是 `HIVE_HOME`。桌面端把 `HIVE_HOME` 指到 `~/Library/Application Support/Hive/...` 时，浏览器桥状态仍写在 `~/.hive/`。这是运维/备份必须知道的偏差。

**浏览器生命周期**：

- 桥是独立 supervisor 进程；Hive 重启不必然杀死它；
- `browser_contexts.json` 让新 gcu 进程重新附着到仍存活的 tab group，避免孤儿/重复组；
- 组内 tab 全关时条目在 rehydrate 时丢弃；
- `browser_default.json` 决定“未指定 profile 时路由到哪个已连接 Chrome”；
- Hive 不持久化“哪个 Worker 开了哪些 tab”；崩溃后 Worker 的 tab group 不保证被回收。

#### 3.10.2 邮件发件人状态

| 路径 | 格式 | 用途 | 原子性 |
|---|---|---|---|
| `~/.hive/senders/threads.json` | `{token: {token, message_id, colony_id, sender_id, from_email, to_email, subject, sent_at, replies, last_reply_at, references[]}}` | 外发邮件 Message-ID token → 会话；回信路由 | tmp + replace |
| `~/.hive/senders/usage.json` | `{date: "YYYY-MM-DD", usage: {sender_id: count}}` | 每发件人每日用量；跨重启保持，UTC 日重置 | tmp + replace |

**云侧发送日志**：`senders/sendlog.py` 的 `reserve_send` 把“预留槽位”写云端（`team_sender_sends`），本地不保留发送记录；离线时，有 `daily_limit` 的发件人**拒绝发送**（避免烧域名），无限制的发件人继续。

**生命周期**：

- `threads.json` 只增不减；无 TTL；删除后回信无法路由；
- `usage.json` 每日 UTC 重置；并发下可能少计（注释承认）；
- 两者都是 `HIVE_HOME`（或 `~/.hive` fallback），不是 `Path.home()` 硬编码。

#### 3.10.3 文件工具沙箱

`aden_tools/file_system_toolkits/security.py`：

- 允许根：`~/.hive`、`~/aden/hive/exports`；
- 旧 API 的沙箱：`~/.hive/workdir/workspaces/default/<agent_id>/current`；
- `get_sandboxed_path` 把绝对路径视为相对 agent 沙箱根，拒绝 `..` 逃逸；
- 目录按需创建；无自动清理；Janitor 不清理 `workdir`（不在已知顶层列表中，属于“unknown”）。

**风险**：

- 允许根是 `~/.hive`（硬编码），不是 `HIVE_HOME`；桌面端自定义 `HIVE_HOME` 时，文件工具仍可能访问 `~/.hive`。
- 沙箱是路径拼接 + `realpath` 检查，不是 OS 级隔离；符号链接/竞态需要额外防范。
- 没有文件锁；多个 Worker 同时写同一文件会 last-write-wins。

### 3.11 运维、归档与临时目录

#### `maintenance/`

- `server.lock`：`{pid, port, started_at}`；服务启动写入，正常退出删除；CLI Janitor 用它判断“有没有 live server 拥有这个 HIVE_HOME”。
- `janitor.lock`：`{pid, started_at}`；`O_CREAT|O_EXCL`，2h TTL；防并发清理。
- `last_janitor_report.json`：

```json
{
  "started_at": "…",
  "finished_at": "…",
  "dry_run": true,
  "mode": "archive|delete",
  "tiers": [1,2,3],
  "total_bytes": 123,
  "total_files": 4,
  "targets": [
    {"name":"…","tier":1,"files":4,"bytes_freed":123,"archived_files":0,"skipped":0,"errors":[]}
  ],
  "manifest_path": "…",
  "archive_path": "…",
  "error": ""
}
```

#### `archive/janitor-<stamp>/`

每个被清理目标一个 `NNNNN-<name>.tar.gz`；成员路径相对 `HIVE_HOME`，解包可还原布局。**先写并 fsync 归档，再删除源**；归档失败不删源。

#### `tmp/colony_uploads/` 或 `/var/tmp/hive-colony-uploads/`

分块导入：

```text
<upload_id>.data        # 追加的原始 tar 字节
<upload_id>.meta.json   # {filename,total_bytes,sha256,replace_existing,name_override,created_at,received_bytes}
```

**原子性**：meta 用 tmp + replace；data 用 `flush + fsync`；每个 chunk 先按 `received_bytes` 截断再追加，保证断点续传不重复。
**生命周期**：24h TTL；`/init` 时 GC 过期/孤儿；finalize 后解包到 `HIVE_HOME`。

#### 会话报告包

`user_report.json` 写在 session 根：

```json
{
  "label": "bad",
  "label_source": "user_reported",
  "description": "…",
  "severity": "low|medium|high|critical",
  "reported_at": "…",
  "reporter": "desktop"
}
```

`tar.gz` 写到系统临时目录 `…/hive-session-reports/report_<ts>_<sid>.tar.gz`（**不在** session 树内）；文本文件做凭据脱敏，截图/二进制在预算内保留。

---

### 3.12 Legacy 图执行路径（仍存在，但不在 Colony 主路径）

这些文件服务传统 `AgentLoader` / `GraphExecutor` / `ExecutionManager`，与 Colony 的 `conversations/` 是**两套**存储模型。不要把它们混为一谈。

```text
HIVE_HOME/agents/<agent_name>/sessions/<session_id>/
├── state.json                 # SessionState（图执行状态）
├── checkpoints/
│   ├── index.json             # CheckpointIndex
│   └── cp_<type>_<node>_<ts>.json
├── runs/<run_id>.json         # ConcurrentStorage Run
├── summaries/<run_id>.json    # RunSummary
└── logs/
    ├── summary.json           # L1
    ├── details.jsonl          # L2
    └── tool_logs.jsonl        # L3
```

`state.json`（`schemas/session_state.py`）：

```json
{
  "schema_version": "1.1",
  "session_id": "session_…",
  "stream_id": "…",
  "correlation_id": "…",
  "status": "active|paused|completed|failed|cancelled",
  "goal_id": "…",
  "agent_id": "…",
  "entry_point": "start",
  "timestamps": {"started_at":"…","updated_at":"…","completed_at":null,"paused_at_time":null},
  "progress": {
    "current_node": "…", "paused_at": "…", "resume_from": "…",
    "steps_executed": 3, "total_tokens": 0, "total_latency_ms": 0,
    "path": ["…"], "total_retries": 0, "nodes_with_failures": [],
    "retry_details": {}, "had_partial_failures": false,
    "execution_quality": "clean", "node_visit_counts": {}
  },
  "result": {"success": null, "error": null, "output": {}},
  "data_buffer": {},
  "metrics": {
    "decision_count": 0, "problem_count": 0,
    "total_input_tokens": 0, "total_output_tokens": 0,
    "nodes_executed": [], "edges_traversed": []
  },
  "problems": [],
  "decisions": [],
  "input_data": {},
  "current_run_id": null,
  "pid": 12345,
  "isolation_level": "shared",
  "checkpoint_enabled": false,
  "latest_checkpoint_id": null,
  "active_triggers": [],
  "trigger_tasks": {},
  "worker_configured": false
}
```

`checkpoints/index.json`（`schemas/checkpoint.py`）：

```json
{
  "session_id": "…",
  "checkpoints": [
    {
      "checkpoint_id": "cp_node_complete_planner_20261127_101500",
      "checkpoint_type": "node_start|node_complete|loop_iteration",
      "created_at": "…",
      "current_node": "…",
      "next_node": "…",
      "is_clean": true,
      "description": "…"
    }
  ],
  "latest_checkpoint_id": "…",
  "total_checkpoints": 1
}
```

> 另有 `framework/testing/test_storage.py` 的 `TestStorage`：`tests/<goal_id>/*.json`、`indexes/by_*/*.json`、`results/<test_id>/*.json`、`suites/<goal_id>_suite.json`，由调用方给 `base_path`（测试框架/评测用），不在 Hive 运行时主存储中。

`cp_*.json`（`Checkpoint`）：

```json
{
  "checkpoint_id": "…",
  "checkpoint_type": "…",
  "session_id": "…",
  "run_id": "…",
  "created_at": "…",
  "current_node": "…",
  "next_node": "…",
  "execution_path": ["…"],
  "data_buffer": {},
  "accumulated_outputs": {},
  "metrics_snapshot": {},
  "is_clean": true,
  "description": "…"
}
```

**生命周期**：

- 只在传统图执行路径创建/读取；
- `state.json` 由 `ExecutionManager._write_session_state` 写，带 `pid` 用于跨进程 stale 检测；
- `checkpoints/` 用于暂停/恢复/失败重试；
- Janitor 会清理旧 session 的 `state.json`/checkpoints（作为冷会话的一部分）；
- Colony 主路径**不使用**这些文件；Colony 的恢复完全靠 `conversations/` + `tracker.db`。

**Standalone Agent 目录**（`exports/<agent>/`，不在 `HIVE_HOME` 下）：

```text
exports/<agent>/
├── agent.json          # {id,name,goal,nodes,edges,...}
├── config.py           # CONFIG 字典（model/max_tokens/tools/storage_path）
├── mcp_servers.json    # 该 Agent 的 MCP server 列表
└── mcp_registry.json   # 该 Agent 的 MCP 选择/覆盖
```

它们由 `AgentLoader` 加载，属于“独立/导出 Agent”格式；Colony 不读取。

### 3.13 前端持久化（浏览器存储）

Hive 的 Dashboard 不使用 IndexedDB；前端持久化全部在 `localStorage` / `sessionStorage`。

| 存储 | Key | 作用域 | 内容 | 生命周期 |
|---|---|---|---|---|
| localStorage | `theme` | 全局（跨用户） | `light` / `dark` | 用户切换主题；清除站点数据重置 |
| localStorage | `theme-explicit` | 全局 | 是否用户显式选过主题 | 一次写入 |
| localStorage | `density` | 全局 | `spacious` / `compact` | 跟随设置 |
| localStorage | `density-explicit` | 全局 | 是否显式选过密度 | 一次写入 |
| localStorage | `motion` | 全局 | 动效偏好 | 跟随设置 |
| localStorage | `showRuntimeLogs` | 全局 | Developer 的 Runtime logs 抽屉开关 | 用户切换 |
| localStorage | `hive.queenPrefs` | 全局（旧） | Queen 偏好覆盖（`MeQueenOverride` map） | 旧版；新代码逐步迁到服务端/用户命名空间 |
| localStorage | `hive:u:<userKey>:colony-panel-selected-tab` | 每用户 | Colony 面板当前 tab | 用户切换 tab |
| localStorage | `hive:u:<userKey>:colony-panel-width` | 每用户 | Colony 抽屉宽度 | 拖动 resize |
| localStorage | `hive:u:<userKey>:promptLibrarySort.my` | 每用户 | Prompt Library 排序 | 用户切换 |
| localStorage | `hive:u:<userKey>:promptLibrarySort.community` | 每用户 | 社区 prompt 排序 | 用户切换 |
| localStorage | `hive:u:<userKey>:promptCopyCounts` | 每用户 | prompt 复制次数 | 每次复制 |
| localStorage | `hive:u:<userKey>:tutorial-seen-v1` | 每用户 | 教程是否看过 | 完成/跳过 |
| sessionStorage | `hive-boot-seen` | 每 tab | 启动动画是否已播 | 关闭 tab |
| sessionStorage | `hive:u:<userKey>:*` | 每 tab/用户 | `userStorage.session` 包装的临时状态 | 关闭 tab |

**命名空间**：`userStorage` 把 raw key 包装为 `hive:u:<activeUserKey>:<rawKey>`；未登录时 `<activeUserKey>` 为 `_`。旧版裸 `hive:*` key 会在首次 `setActiveUser` 时迁移到当前用户命名空间（`theme` 除外，保持全局）。

**与服务器状态的关系**：

- 用户资料（displayName/about）、Prompt Library 的自定义 prompt、Queen 偏好、主题/密度的服务端副本都在 `configuration.json` / `custom_prompts.json` / 后端 API；
- 浏览器存储只是**渲染层缓存/设备偏好**，清掉不会丢服务端数据；
- 反之，服务端数据是跨设备真相，但 `theme` / `density` / `motion` 是**每设备**偏好，不同步。

**生命周期**：

- 每个浏览器 Profile 独立；
- 清除站点数据 → 全部重置；
- 切换账号 → 因命名空间不同而互不覆盖；
- `sessionStorage` 关闭 tab 即清；
- 没有 IndexedDB，也没有 Service Worker 持久缓存（构建产物由浏览器 HTTP 缓存管理）。

### 3.14 覆盖审计：有没有遗漏

按“文件型存储”逐类核对：

| 类别 | 已覆盖的路径/文件 | 状态 |
|---|---|---|
| 全局配置 | `configuration.json`、`custom_prompts.json`、`runtime_id`、`sentinel_secret`、`trusted_repos.json`、`own_remotes`、通知标记、`antigravity-accounts.json`、`social_rate_limits.db` | ✅ |
| Queen 身份 | `profile.yaml`、`avatar.*`、`_profile_snapshots/`、`tools.json`、`skills_overrides.json`、`skills/`、`soul/` | ✅ |
| Session | `meta.json`、`events.jsonl`、`summary.json`、`tasks.json`、`reminder_state.json`、`compaction_status.json`、`skill_installs.json`、`timeline.*`、`.janitor.lock`、`.pruned.json`、`conversations/`、`data/`、`logs/` | ✅ |
| Colony | `metadata.json`、`worker.json`、`profiles/*`、`tools.json`、`skills_overrides.json`、`skills/`、`playbooks/`、`triggers.json`、`notifications.json`、`escalations/`、`tracker/`、`colony.db`、`legacy_fork_snapshots/`、`seed_conversation/`、`queens/`、`workers/` | ✅ |
| Worker | `workers/<sid>/{meta,tool_tiers,tasks,result}.json`、`conversations/`、`data/` | ✅ |
| Tracker | `tracker/tracker.db` + `_tracker_registry/_tracker_meta/_tracker_changes` + WAL；`/var/tmp/hive-tracker-snapshots/`（UI 只读快照） | ✅ |
| Memory | `memories/{global,colonies,agents/queens}/**/*.md`、`log.jsonl`、`user-profile.md`、`.reflection_error.txt` | ✅ |
| Message index | `.message_index/{events,data,meta,recall}/**` | ✅ |
| Skills | 包内 `_default_skills/_preset_skills/`、`~/.agents/skills/`、`HIVE_HOME/skills/`、`queens/<q>/skills/`、`colonies/<c>/skills/`、项目 `.agents/skills` / `.hive/skills`、`registry_cache/`、`skill_installs.json` | ✅ |
| Credentials | `credentials/credentials/*.enc`、`credentials/metadata/index.json`、`secrets/credential_key` | ✅ |
| 外部订阅凭据 | `~/.claude/.credentials.json`、macOS Keychain、`~/.codex/auth.json`、`~/.kimi/config.toml`、Antigravity IDE `state.vscdb`、`~/.hive/antigravity-accounts.json` | ✅（不在 HIVE_HOME） |
| MCP | `mcp_registry/{installed,config}.json`、`mcp_registry/cache/{registry_index,last_fetched,last_fetched.json}`、`cache/mcp2cli/` | ✅ |
| Autonomy | `triggers.json`、`notifications.json`、`escalations/*.json`、`queens/mail.jsonl`、`claude_code/*/conversations.json`、`claude_code/*/mcp/*.json` | ✅ |
| Channel / Turn Context | `channels/<name>/`、`turn_context/<name>/`（插件私有） | ✅（schema 由插件定义） |
| Browser / GCU | `~/.hive/browser_default.json`、`~/.hive/browser_contexts.json`、`~/.hive/browser-logs/*.jsonl`、`~/.hive/bridge_host.log`、`GCU_LOG_FILE` / `HIVE_GCU_MEMTRACE_FILE`（外部） | ✅ |
| Senders | `~/.hive/senders/threads.json`、`~/.hive/senders/usage.json`、云端 `team_sender_sends` | ✅ |
| 文件沙箱 | `~/.hive/workdir/workspaces/default/<agent_id>/current/` | ✅ |
| 终端运行时 | 作业/PTY/output 缓冲（内存，无文件） | ✅（明确非文件） |
| 测试框架 | `TestStorage`（调用方提供 `base_path`；`tests/indexes/results/suites`） | ✅（非运行时存储） |
| 可选/商业包 | `commercial`、`framework.crm`、`framework.cloud_sync` | ✅（本 OSS 工作区不存在；远端/插件自管） |
| Logs | `logs/hive-server.log(.1-.3)`、`logs/sentinel.log(.1-.3)`、`event_logs/`、`llm_logs/`、`debug_logs/`、`failed_requests/`、`compaction_log/`、`tool-artifacts/`、`charts/` | ✅ |
| Maintenance | `maintenance/{server.lock,janitor.lock,last_janitor_report.json}`、`archive/janitor-*/` | ✅ |
| Import temp | `/var/tmp/hive-colony-uploads/`（或 `HIVE_HOME/tmp/colony_uploads/`）、`hive-session-reports/` | ✅ |
| Legacy | `agents/**/sessions/**`、`queen/`、`.migrated-v2/v3`、`colonies/.migrations/`、`legacy_fork_snapshots/`、`checkpoints/`、`state.json`、`runs/`、`summaries/`、`logs/{summary,details,tool_logs}`、`exports/` | ✅ |
| Frontend | `localStorage` / `sessionStorage` 键（见 3.13） | ✅ |

**明确“不是文件”或“不在 Hive 进程内”的**：

| 对象 | 说明 |
|---|---|
| 正在运行的 asyncio 任务/流/工具 | 内存；重启丢失 |
| `EventBus` 环形历史 | 内存；磁盘只有 `events.jsonl` 回放 |
| Worker 队列/状态 | 内存；Worker 会话在磁盘，状态不自动恢复 |
| 自适应预算样本 / Key 池健康 / 资源采样 / 上游健康 | 内存；重启重置 |
| 浏览器扩展的 `chrome.storage` / 标签状态 | 扩展自己管理；Hive 不持久化 |
| MCP server 自己的状态 | server 自己管理；Hive 只持久化注册表 |
| Channel / Turn Context 插件的内部状态 | 插件自己的 `state_dir`；schema 由插件定义 |
| Claude Code 自己的 `config/projects/*.jsonl` | Claude Code 管理；Hive 只持久化映射和 MCP token 文件 |
| 云侧（Hive Inbox / global DB / cloud sync） | 远端；本地只有缓存/凭据 |
| OS 临时报告包 | 系统临时目录；不保证保留 |

**仍存在不确定性的路径（明确标注）**：

1. `HIVE_HOME/desktop/`、`HIVE_HOME/workspace/`、`HIVE_HOME/.janitor/`：Janitor 的 `KEEP_ALWAYS`/`KNOWN_ENTRIES` 列出，但当前 OSS 代码没有写入方；可能是桌面端/未来预留。报告按“保留目录”处理，不假设 schema。
2. `HIVE_HOME/queens/<q>/sessions/<sid>/data/reports/`：报告包实际写到系统临时目录；该目录仅历史/兼容，当前不主动写。
3. `HIVE_HOME/colonies/<c>/skills_overrides.json`：布局注释有，当前代码不读（Colony 继承 Queen 的 override store）。
4. `HIVE_HOME/agents/**`：v2 legacy；v3 只读迁移，不作为主存储。
5. `HIVE_HOME/queens/<q>/sessions/<sid>/logs/`：仅传统 `RuntimeLogStore`/调试使用，Colony 主路径不依赖。
6. 浏览器扩展的存储：未在 `core/frontend` 中定义；属于 `tools/browser-extension` 的实现细节，本报告不展开。

**复核命令**：

```bash
# 所有 HIVE_HOME 拼接
grep -rhoE 'HIVE_HOME / "[^"]+"' core/framework tools/src --include='*.py' | sort -u

# 所有写入文件（write_text / open(...,"w") / atomic_write）
grep -rnoE '(write_text|atomic_write|open\([^)]*["'"']w)' core/framework tools/src --include='*.py' | head -200

# 所有 SQLite
grep -rn 'sqlite3.connect' core/framework tools/src --include='*.py'

# 所有 JSONL
grep -rn '\.jsonl' core/framework tools/src --include='*.py'

# 前端存储键
grep -rhoE '(localStorage|sessionStorage)\.(getItem|setItem|removeItem)\([^)]*' core/frontend/src | sort -u
```

---

## 4. 生命周期总表

| 文件 | 创建 | 更新 | 读取 | 删除/保留 | 原子性 |
|---|---|---|---|---|---|
| `configuration.json` | 安装/首次保存 | 设置页整体重写 | 所有 getter | 手动 | B |
| `profile.yaml` | 首次选中 Queen | 编辑整体重写 | 提示/UI | 删除 Queen | A |
| `_profile_snapshots/*` | 编辑前 | 每次编辑新增 | 回滚 | 保留 30 | A |
| `tools.json`（Queen/Colony） | 首次保存 allowlist | 保存 | 每次 spawn | 删除 Queen/Colony | A |
| `skills_overrides.json` | 首次启停 | 每次操作 | 技能加载 | 删除 Queen | B |
| `soul.json` | 首次反思 | 每日反思/回滚 | 每轮提示 | 永久 | B |
| `soul/history/*` | 每次版本 | — | 回滚 | 永久 | B |
| `soul/proposals/*` | 反思 | 批准/拒绝 | UI | 永久（状态） | B |
| `meta.json`（session） | 建会话 | 阶段/标题/锁 | 冷恢复/列表 | 删除会话 | C |
| `events.jsonl` | 首事件 | 追加/合并 | 回放/索引 | Janitor 重写/删除 | D |
| `summary.json` | 首 part | 每 part | 列表 | 可重建 | A |
| `tasks.json` | 首次任务工具 | 每次增删改 | 任务工具/UI | 删除会话；fork 不携带 | A |
| `reminder_state.json` | 首次提醒 | 每回合 | 提醒源 | 删除会话；fork 不携带 | A |
| `conversations/meta.json` | 首消息 | prompt 变化 | 恢复 | 删除会话 | A |
| `cursor.json` | 首轮 | 每轮/停泊 | 恢复 | 删除会话 | A |
| `parts/*.json` | 每条消息 | 一次写入 | 恢复/回放 | 压缩/删除会话 | A |
| `partials/*.json` | 流式开始 | 每次快照 | 崩溃恢复 | 真实 part 写入后 | A |
| `timeline.jsonl` | 首次抽取 | 追加 | `search_timeline` | 删除会话；fork 不携带 | D |
| `compaction_status.json` | fork | 完成/失败 | 会话加载等待 | 启动 sweep | C |
| `skill_installs.json` | 全局写技能 | 决定 | 聊天页 | 删除会话 | A |
| `attached_credentials.json` | attach | attach/detach | 每轮提示 | 手动 | C |
| `worker.json` | 建群 | tracker 补丁 | spawn | 删除 Colony | C |
| `metadata.json`（Colony） | 建群 | 设置/软删除 | 列表/加载 | 删除 Colony | A（fork 为 C） |
| `tools.json`（Colony） | 建群 | 编辑 | spawn | 删除 Colony | A |
| `triggers.json` | set_trigger | set/remove | 会话加载 | 删除 Colony | C |
| `notifications.json` | Sentinel 设置 | 回复线程 | 停泊判定 | 删除 Colony | A |
| `escalations/*.json` | 升级 | 回复 | 启动重建 | 永久 | A |
| `tracker.db` | 建群 | Worker/Queen 写 | 查询/Playbook | 删除 Colony | E |
| `/var/tmp/hive-tracker-snapshots/*.db` | UI 读取 | 每 2s 刷新 | Data 页 | 过期替换/可删 | 只读快照 |
| `colony.db` | 首次 Playbook | 每次 run | 状态/列表 | 删除 Colony | E |
| `seed_conversation/` | 建群 | 只读 | Worker 启动 | 删除 Colony | 复制 |
| `workers/<sid>/meta.json` | spawn | 一次 | resume/UI | Janitor | C |
| `workers/<sid>/tool_tiers.json` | 首次加载工具 | 每次 promote | 恢复 | Janitor | C |
| `memories/*.md` | 首次反思/编辑 | 反思/编辑 | 每轮 recall | 退休（保留日志） | C |
| `memories/*/log.jsonl` | 首次变更 | 追加 | 历史/恢复 | 永久 | D |
| `.message_index/**` | 首次查询 | 增量同步 | 召回/搜索 | 可重建 | E/D |
| `credentials/*.enc` | 保存 | 刷新/更新 | 解析 | 删除/自愈 | C |
| `credentials/metadata/index.json` | 保存 | 每次保存 | 枚举 | 可重建 | C |
| `mcp_registry/installed.json` | 首次 add | add/remove/mode/健康 | spawn | 手动 | A |
| `queens/mail.jsonl` | 首封信 | 追加 | 审计 | 永久 | D |
| `claude_code/conversations.json` | 首封信 | 每封信 | 重启恢复 | 永久 | B |
| `claude_code/mcp/<sid>.json` | 每轮 | 覆盖 | 该轮 Claude Code | 运行结束删除 | 0600 |
| `event_logs/*.jsonl` | 开启 | 追加 | 调试 | 7 天 | D |
| `llm_logs/*.jsonl` | 开启 | 追加 | 调试 | 7 天 | D |
| `failed_requests/*.json` | 失败 | 一次 | 复现 | 50 个上限 | C |
| `logs/hive-server.log` | 启动 | 轮转 | 诊断 | 3×20MB | 库 |
| `logs/sentinel.log` | Sentinel 启动 | 追加/轮转 | 诊断 | 3×5MB | 库 |
| `.janitor.lock`（session） | 冷会话重写 | 一次 | 恢复前等待 | 1h TTL | 锁文件 |
| `.pruned.json`（session） | events 重写 | 一次 | 冷却判断 | 手动 | A |
| `mcp_registry/cache/last_fetched` | 拉取索引 | 每次 | 刷新判断 | 手动 | A |
| `legacy_fork_snapshots/` | 迁移 | 一次 | 人工恢复 | 手动 | 复制 |
| `agents/**/state.json` | 图执行 | 暂停/完成 | 恢复 | 手动/Janitor | B |
| `checkpoints/index.json` + `cp_*.json` | 图执行 | 每 checkpoint | 恢复 | 手动 | A |
| `runs/*.json` / `summaries/*.json` | 图执行 | 每次 run | 审计 | 手动 | 库 |
| `logs/{summary,details,tool_logs}`（legacy） | RuntimeLogger | 每步 | 调试 | 手动 | D |
| `localStorage` / `sessionStorage` | 前端 | 每次交互 | 渲染 | 浏览器清理 | 浏览器 |
| `~/.hive/browser_default.json` | 侧栏星标 | 一次 | 桥路由 | 手动 | C |
| `~/.hive/browser_contexts.json` | tab group 变化 | 每次 | gcu rehydrate | 无 live tab 丢弃 | C |
| `~/.hive/browser-logs/*.jsonl` | 工具调用 | 追加 | 遥测 | 无自动清理 | D |
| `~/.hive/bridge_host.log` | 桥启动 | 追加 | 诊断 | 无轮转 | D |
| `~/.hive/senders/threads.json` | 发信 | 每次 | 回信路由 | 无 TTL | B |
| `~/.hive/senders/usage.json` | 发信 | 每次 | 轮换/限额 | UTC 日重置 | B |
| `~/.hive/workdir/workspaces/default/**` | 文件工具 | 每次 | 工具 | 手动 | 目录 |
| `maintenance/*` | 启动/清理 | 每次 | CLI/API | 手动 | A |
| `archive/*.tar.gz` | 清理 | 一次 | 恢复 | 手动 | A |

---

## 5. 内存对象 ↔ 磁盘映射

| 内存对象 | 磁盘 | 说明 |
|---|---|---|
| `SessionManager.Session` | `meta.json` + `events.jsonl` + `conversations/` + `tasks.json` + `triggers.json` | 运行时字段（`queen_executor`、`event_bus`、`sse_client_count`、`active_trigger_ids`、`colony`）**不持久化** |
| `AgentLoop` | `conversations/` + `cursor.json` + `events.jsonl` | 循环计数器、预算、流任务在内存；`iteration`/`tool_calls_used`/`recent_*` 在 cursor |
| `NodeConversation` | `parts/*.json` + `conversations/meta.json` + `cursor.next_seq` | `_messages` 是 parts 的内存镜像；`_next_seq` 写 cursor |
| `OutputAccumulator` | `cursor.outputs` | 大值落 `data/output_*` |
| `ReminderState` | `reminder_state.json` | 任务提醒计数器 |
| `ToolTierState`（Worker） | `workers/<sid>/tool_tiers.json` | 已加载的 searchable 工具 |
| `Worker` | `workers/<sid>/meta.json` + `conversations/` + `data/` | `WorkerStatus`/`WorkerResult` 不落盘；`result.json` 仅历史/桌面写入 |
| `ColonyRuntime` | `metadata.json` + `tracker.db` + `colony.db` | `_workers`、`_pending_queue`、`_budget_applied` 是内存 |
| `TaskStore` | `tasks.json` | 每个 session 一份；锁是进程内 |
| `ColonyBinding` | `worker.json.input_data.binding` + 执行上下文 | 不单独落盘；由 Colony 目录推导 |
| `MemoryFile` | `memories/**/*.md` | 文件即对象 |
| `Soul` | `soul/soul.json` + `history/` + `proposals/` | 版本化 |
| `CredentialObject` | `credentials/*.enc` | Fernet 加密；索引是投影 |
| `MCPRegistry` | `installed.json` | 内存 `servers` 是文件镜像 |
| `TriggerDefinition` | `triggers.json` | 激活状态在 Session |
| `EscalationRecord` | `escalations/*.json` | token 索引在 Manager 内存 |
| `PlaybookRun` | `colony.db.playbook_runs` + Tracker | 运行循环在内存；重启丢失循环 |
| `SkillEntry` | `SKILL.md` | 文件即对象 |
| `QueenProfile` | `profile.yaml` | 默认人格物化 |
| `QueenMail` 链状态 | `queens/mail.jsonl` + 各 session | 深度/回信地址在内存 + conversations.json |
| `Channel` | `channels/<name>/` | 插件私有 |
| `TurnContext` | `turn_context/<name>/` | 插件私有 |
| `ClaudeCodeQueen` 会话 | `claude_code/<id>/conversations.json` + `mcp/<sid>.json` | Claude 自己的 jsonl 在 `config/projects/` |
| `custom_prompts` | `custom_prompts.json` | 列表即文件 |
| `user_profile` | `configuration.json` + `memories/global/user-profile.md` | 两处同步 |
| `message_index` | `.message_index/**` | 完全派生 |
| `RuntimeLogStore` | `sessions/<sid>/logs/*` | 传统图/调试 |

---

## 6. 一致性、崩溃与恢复

### 6.1 崩溃恢复顺序

```text
进程崩溃
  → 重启 hive serve
  → 打开 session 目录
  → conversations/meta.json（没有 → 新会话）
  → parts/*.json（按 seq 排序）
  → cursor.json（iteration / outputs / pending_input / user_stopped / tool_calls_used）
  → partials/*.json（悬空 assistant turn）
  → events.jsonl（前端回放）
  → tasks.json（计划）
  → 若 pending_input：恢复停泊
  → 若 user_stopped：保持停泊
  → 若最后回合未完成：COLD_INTERRUPTED
```

### 6.2 悬空工具调用

如果 assistant part 有 `tool_calls` 但没有对应 tool part，恢复时补一条中断结果，避免 provider 400。这是 `NodeConversation.restore` 的职责。

### 6.3 压缩与重写

- 压缩会删除旧 parts、写 summary part、更新 cursor；
- `events.jsonl` 可能被 Janitor 原地重写（去遥测），消息索引通过“文件大小回退”检测并重建；
- 会话目录的 `mtime` 不可靠（parts 在子目录），所以 `summary.is_stale` 比较 `parts/` 目录 mtime。

### 6.4 多进程

- 同一 `HIVE_HOME` 同时跑两个 `hive serve`：**不支持**；`server.lock` 只是 Janitor 的安全信号，不是互斥锁。
- 桌面 + CLI 同时写：`atomic_write` 保证单文件不截断，但业务层可能 last-write-wins。
- SQLite：WAL 允许多读单写；`busy_timeout` 处理短竞争。

### 6.5 原子性缺口（需要知道）

| 文件 | 风险 |
|---|---|
| `meta.json` | 写一半会损坏；读方降级为空，可能丢阶段/标题/锁 |
| `triggers.json` | 同上；丢触发器定义 |
| `custom_prompts.json` | 同上；丢自定义提示词 |
| `worker.json` | 同上；spawn 读到坏文件会回退空工具/默认 |
| `attached_credentials.json` | 同上；丢 attach |
| `compaction_status.json` | 同上；会话加载可能误判无压缩 |
| `.enc` | 写一半永久不可解密；只能删除重存 |
| `events.jsonl` | 最后一行可能截断；读取方跳过损坏行 |
| `profile.yaml` | 原子写，但无 fsync；断电可能丢最后一次编辑 |

---

## 7. 版本与迁移

| 标记/版本 | 作用 |
|---|---|
| `.migrated-v2` | v1→v2 目录迁移完成 |
| `.migrated-v3` | v2→v3 目录迁移完成 |
| `conversations/meta.json.schema`? | 无显式版本；靠字段缺省 |
| `tasks.json.meta.schema_version` | 1 |
| `tracker.db PRAGMA user_version` | 3 |
| `soul.json.version` | 整数递增 |
| `skills_overrides.json.version` | 1 |
| `credentials/metadata/index.json.version` | 2.0 |
| `.message_index` `PRAGMA user_version` | 1 |
| `timeline_meta.json.version` | 1 |

v3 迁移（`storage/migrate_v3.py`）：

```text
agents/queens/<q>/            → queens/<q>/
agents/queens/<q>/sessions/   → queens/<q>/sessions/<sid>/ 或
                                colonies/<c>/queens/<q>/sessions/<sid>/
agents/<colony>/worker/conversations/ → colonies/<c>/seed_conversation/
agents/<colony>/<worker>/     → colonies/<c>/workers/<worker>/
colonies/<c>/data/tracker.db  → colonies/<c>/tracker/tracker.db
```

迁移幂等；只迁移真实旧布局；`memories/` 不变。

---

## 8. 可移植性与备份

### 8.1 必须备份

```text
HIVE_HOME/
├── configuration.json
├── credentials/          # 没有它，.enc 不可读
├── secrets/credential_key
├── queens/               # profile、sessions、soul、skills
├── colonies/             # metadata、worker.json、tracker.db、playbooks、sessions
├── memories/
├── skills/
├── custom_prompts.json
└── mcp_registry/installed.json
```

### 8.2 可重建/可不备

```text
.message_index/  summary.json（各 session）  registry_cache/
logs/  event_logs/  llm_logs/  debug_logs/  failed_requests/
tool-artifacts/  charts/  archive/  tmp/
```

### 8.3 Colony 可移植性

Colony 目录自包含：

- `metadata.json` 记录来源；
- `worker.json` 带 binding；
- `tracker/tracker.db` 是业务事实；
- `queens/`、`workers/`、`seed_conversation/` 是会话；
- 导入时重新绑定 `HIVE_HOME` 路径（`worker.json.input_data.binding` 会被 `ensure_tracker_db` 重写）。

但**Queen 的身份**（`queens/<id>/profile.yaml`、soul、skills）不在 Colony 目录里；跨机器导入 Colony 时，Queen 人格需要另外存在或导入多根归档。

### 8.4 备份一致性

- 备份时最好停 `hive serve`，或至少接受“最后几条消息可能未 flush”；
- SQLite 用 `.backup` 或先 `PRAGMA wal_checkpoint(TRUNCATE)`；直接复制 `.db` 而不复制 `-wal` 会丢最近事务；
- `secrets/credential_key` 与 `credentials/` 必须一起备份。

---

## 9. 不变量与设计评价

### 9.1 文件系统不变量

1. **一个 session 一个目录**；目录名就是 `session_id`。
2. **parts 的 `seq` 单调递增**；`cursor.next_seq` 是下一条。
3. **`events.jsonl` 只追加**；任何重写都会让派生索引重建。
4. **Tracker 的 binding 不可变**；`worker.json` 的 binding 必须指向真实 `tracker/tracker.db`。
5. **`_` 前缀在 Tracker 中保留**给框架。
6. **`HIVE_CREDENTIAL_KEY` 是 `.enc` 的唯一钥匙**；丢失即数据丢失。
7. **`memories/*.md` 的 `author=owner` 不可被反思覆盖**。
8. **`soul.json` 的版本只增不减**；回滚产生新版本。
9. **`conversations/meta.json` 是恢复的入口**；没有它视为新会话。
10. **Canonical 与 Derived 分离**；删除 Derived 永远安全。

### 9.2 设计评价

**优点**

- 目录结构高度自解释，人类可直接 `ls`/`cat` 调试。
- 会话、Colony、记忆、技能、凭据、触发器各自边界清晰。
- 写穿 + 游标 + partials + 事件日志，构成完整的崩溃恢复链。
- 原子写工具统一了大部分关键文件；SQLite WAL 处理并发。
- Colony 自包含，导入导出/迁移可行。

**风险**

- **原子性不一致**：`meta.json`、`triggers.json`、`worker.json`、`.enc` 等仍是直接写；崩溃窗口真实存在。
- **单进程假设**：`server.lock` 不是互斥锁；多实例会破坏内存索引/事件日志。
- **`events.jsonl` 无界增长**；Janitor 是手动触发，不是自动调度。
- **派生缓存与真相的失效逻辑复杂**：`summary` 用 mtime、消息索引用文件大小、`compaction_status` 用标记；任何一处失效都可能造成“看起来没数据”。
- **权限不一致**：只有少数 secret 文件显式 0600；`.enc` 依赖 umask。
- **文档漂移**：`agents/queens`、`data/tracker.db`、Colony `skills_overrides.json` 等旧路径仍出现在 docstring 中，容易误导。

### 9.3 一句话

> **Hive 的“数据库”就是 `HIVE_HOME` 这棵树：会话是 parts + cursor，Colony 是目录 + tracker.db，Queen 是 profile + soul + memory，入口是 triggers/notifications/mail。**
> 理解这棵树，就理解了 Hive 的状态、恢复与可移植性；它也暴露了 Hive 最需要工程纪律的地方——**哪些文件是真相，哪些只是缓存，以及哪些写入还没有原子性保证。**
