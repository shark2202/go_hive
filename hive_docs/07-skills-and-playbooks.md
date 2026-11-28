# 07 · 方法与系统化：Skills 与 Playbooks

> 关键文件：
> `core/framework/skills/`（18 个模块）
> `core/framework/tools/playbook_tools.py`
> `core/framework/host/playbook/runner.py`
> `docs/playbook-spec.md`（本仓库自带、与代码一致的契约说明，可作为补充阅读）

## 1. Skill：可复用的方法

### 1.1 格式

Skill 遵循开放的 **Agent Skills** 标准（agentskills.io），与 Claude Code / Cursor 等兼容：

```text
my-skill/
├── SKILL.md          # 必需：YAML frontmatter + markdown 正文
├── scripts/          # 可选：可执行脚本
├── references/       # 可选：参考资料
└── assets/           # 可选：模板、数据
```

`SKILL.md` frontmatter：

| 字段 | 必需 | 说明 |
|---|---|---|
| `name` | 是 | 小写字母/数字/连字符，需与目录名一致，≤64 |
| `description` | 是 | 做什么、何时用；≤1024；这是模型匹配的主要依据 |
| `license` | 否 | 许可 |
| `compatibility` | 否 | 环境要求 |
| `metadata` | 否 | 任意键值（author/version 等） |
| `allowed-tools` | 否 | 预批准工具列表；用于工具门控 |

### 1.2 作用域与优先级

发现顺序（后者覆盖前者，同名去重）：

| 优先级 | 作用域 | 路径 | 说明 |
|---:|---|---|---|
| 0 | `framework` | `skills/_default_skills/` | Hive 自带基础设施技能 |
| 1 | `preset` | `skills/_preset_skills/` | 能力包，默认关闭，按角色/Colony 开启 |
| 2 | `user` | `~/.agents/skills/`、`~/.hive/skills/`、`external_skills` 配置目录 | 个人技能 |
| 3 | `queen_ui` | `queens/<id>/skills/` | 某个 Queen 自己的技能 |
| 4 | `colony_ui` | `colonies/<name>/skills/` | 某个 Colony 的技能 |
| 5 | `project` | `<project>/.agents/skills/`、`<project>/.hive/skills/` | 仓库内技能 |

同一作用域内，Hive 专用路径覆盖跨客户端路径（`discovery.py:33`）。

### 1.3 自带技能

**框架默认 12 个**（`_default_skills/`）：

```text
browser-automation, colony-progress-tracker, context-preservation,
error-recovery, image-generation, note-taking, pdf, quality-monitor,
slack-notifications-setup, telegram-notifications-setup,
worker-delegation, writing-hive-skills
```

**预设能力包 8 个**（`_preset_skills/`）：

```text
chart-creation-foundations, linkedin-automation,
terminal-tools-foundations, terminal-tools-fs-search,
terminal-tools-job-control, terminal-tools-pty-sessions,
terminal-tools-troubleshooting, x-automation
```

预设包默认关闭；每个 Queen 的 YAML 可用 `default_preset_skills` 为自己的角色默认开启。

### 1.4 渐进披露

技能不是全量塞进系统提示：

- 目录只给 `name + description`；
- Agent 判断相关后，调用工具/读取 `SKILL.md` 加载全文；
- `SkillsCatalogReminderSource` 在会话开始、用户消息、工具后、压缩后维护目录的**增量**；
- 加载后的技能正文在对话里标记 `is_skill_content`，压缩时受保护。

这使 100+ 技能的目录仍可控。

### 1.5 信任门禁

项目作用域技能来自不受信仓库时，默认需要用户明确同意：

- 信任判断基于 git remote（`trust.py`）；
- 可信仓库持久化到 `~/.hive/trusted_repos.json`；
- localhost remote 永远可信；
- `HIVE_TRUST_PROJECT_SKILLS=1` 可在 CI/headless 中跳过；
- UI 作用域（queen_ui / colony_ui）与框架/preset 默认可信。

### 1.6 工具门控

`allowed-tools` 与 `tool_gating.py` 允许技能声明“我只需要这些工具”。这既能缩小暴露面，也能避免技能调用不该用的工具。门控是**建议性**的：真正的硬边界仍是 Queen/Worker 的允许列表与凭据。

### 1.7 `write_skill`

Queen 在运行中写技能：

```python
write_skill(scope="colony" | "queen" | "global", ...)
```

| scope | 写入位置 | 生效 |
|---|---|---|
| `colony` | `colonies/<c>/skills/` | 当前 Colony 的所有 Worker 下一次 spawn 可见 |
| `queen` | `queens/<id>/skills/` | 该 Queen 自己的目录立即可见 |
| `global` | 不直接写共享库，而是生成 install request | 用户在会话页面批准后进入共享库 |

写入后强制同步重载 catalog，避免“本轮 spawn 的 Worker 看不到刚写的技能”的竞态。

### 1.8 热重载与覆盖

- `SkillsManager` 监听 `SKILL.md` 与 `skills_overrides.json`，变更即增量重载；
- 每个 Queen / Colony 有覆盖文件（`skills_overrides.json`），可启停/覆盖；
- 预设技能默认关闭，`default_preset_skills` 或 UI 开启；
- 技能目录作为 `allowlisted_dirs` 暴露给工具，允许脚本引用技能内资源。

### 1.9 技能注册表与 CLI

`hive skill` 命令族：

```text
list / install / remove / info / init / validate / doctor / update /
search / fork / test / trust
```

社区注册表：

- 默认索引 `https://raw.githubusercontent.com/hive-skill-registry/.../skill_index.json`；
- `HIVE_REGISTRY_URL` 可覆盖；
- 缓存到 `~/.hive/registry_cache`；
- 网络不可用时降级到过期缓存；
- 安装时按 tier（official / verified / community）提示信任等级。

## 2. Playbook：把方法批量重放

### 2.1 它是什么

Playbook 是一段 Python 编排脚本：

```python
meta = {
    "name": "fix-ci-failures",
    "description": "每个失败的 CI job 派一个 worker",
    "concurrency": 4,
}

async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT id, job FROM ci_failures WHERE status='open'"),
        dispatch=lambda row, i: worker(
            task=f"复现并修复 CI job {row['job']}",
            data={"id": row["id"]},
            retries=1,
            backoff="exp",
            timeout=600,
        ),
        max_rounds=3,
    )
    return {"remaining": tracker_count("SELECT id FROM ci_failures WHERE status='open'")}
```

核心原则：

- **Runner 不持有业务状态**；Tracker 是唯一事实源。
- **重跑即续跑**：done 行不会再次出现在 pending 查询里。
- **控制流确定，Worker 输出不确定**：脚本决定顺序/并发，Worker 仍是 LLM。
- **脚本是编排，不是替代 Worker 的业务判断**。

### 2.2 运行模型

```text
Queen 调用 run_playbook
  → 解析脚本（inline / path / saved name）
  → 同步 load：compile + exec + 校验 meta/run
  → 短跑（≤ 1.5s）：直接返回 completed / failed
  → 长跑：返回 started + run_id，后台 converge
      → 每轮查 pending()
      → 每个 unit 派一个 Worker
      → 等报告、重试、退避
      → 再查 pending()，直到空 / max_rounds / circuit breaker
      → 未解决行进入 deadletter
  → 完成时注入 [PLAYBOOK_COMPLETE]
```

### 2.3 脚本契约

**`meta`**

| 字段 | 必需 | 默认 | 说明 |
|---|---|---|---|
| `name` | 是 | — | 保存为 `playbooks/<sanitized>.play.py`，也是重跑名 |
| `description` | 否 | — | 说明 |
| `concurrency` | 否 | **8** | 同时运行 Worker 上限；硬上限 **32** |

环境变量：`HIVE_DEFAULT_PLAYBOOK_CONCURRENCY`、`HIVE_MAX_PLAYBOOK_CONCURRENCY`。

**`run(args)`**

- 必须存在且可调用，约定 `async def run(args)`；
- 模块级代码在 `load()` 时同步执行；
- 返回值必须 JSON 可序列化；
- 缺失 `meta`/`name`/`run` 或编译错误 → `failed` + traceback。

### 2.4 注入 API

| API | 同步/异步 | 契约 |
|---|---|---|
| `tracker_query(sql)` | 同步 | 返回 `list[dict]`；最多 100,000 行 |
| `tracker_count(sql)` | 同步 | 返回 `int` |
| `worker(task, data, profile, lane, skill, retries, backoff, timeout, schema, goal)` | **必须 await** | 返回 `{status, receipt, attempts, error}` |
| `converge(pending, dispatch, max_rounds=1, circuit_breaker=None, chunk=None)` | await | 返回 `{rounds, converged, remaining, dead_lettered}` |
| `lane(name, concurrency=1, rate_per_min=None)` | 同步 | 限速车道 |
| `deadletter.list()` / `.size` / `len()` | — | 未解决行 |
| `log(msg)` / `phase(title)` | 同步 | 运行日志/阶段标签 |

`worker` 的重试只针对**生命周期失败**（超时、崩溃），不会因为回执形状不对而盲目重试，避免重复副作用。

### 2.5 并发与限速

三层收窄：

```text
meta["concurrency"] / converge chunk    本次运行并发
  → lane concurrency / rate_per_min    每条车道的并发与启动速率
    → colony max_concurrent_workers     下限，不是上限
```

`run_playbook` 会把 Colony 的 `max_concurrent_workers` 临时抬到 `max(原值, meta 并发)`，结束后恢复。因此 Colony cap 是**下限**，不是 Playbook 的上限。

### 2.6 状态与持久化

| 状态 | 含义 |
|---|---|
| `started` | 后台运行，返回 `run_id` |
| `completed` | 结束，含 `result` / `dispatched` / `dead_lettered` |
| `failed` | load 或运行失败，含 `error` / `traceback` |
| `stopped` | 被 `stop_playbook` 取消 |

| 内容 | 位置 |
|---|---|
| 脚本 | `colonies/<c>/playbooks/<name>.play.py` |
| 运行日志 | `colonies/<c>/colony.db` 的 `playbook_runs` |
| 业务状态 | `colonies/<c>/tracker/tracker.db` |

**关键限制**：runner 不保存“做到哪一行”；恢复完全依赖 Tracker 查询。Hive 重启会丢失正在运行的 Playbook 循环（已完成的 run log 保留），需要手动用 `playbook_name` 重跑——因为 done 行不会再次派发，所以这仍然是“续跑”。

### 2.7 查询与停止

- `get_playbook_status(run_id)`：运行中给 `dispatched` / `dead_lettered` / `phase` / `recent_log`；结束后给 run-log 行；找不到返回 `not_found`。
- `stop_playbook(run_id)`：取消收敛循环并停止在飞 Worker；已标记 done 的行保留。
- `list_playbook_runs(limit=20)`：按创建时间倒序。

### 2.8 错误与死信

| 阶段 | 行为 |
|---|---|
| 编译/模块级异常 | `failed`，带 traceback |
| 缺 meta/run | `failed`，明确提示 |
| `run()` 异常 | `failed`，定位到 `<playbook>` 行号 |
| Worker 失败 | `worker()` 返回 failed；`converge` 决定重试或 dead-letter |
| 0 次派发 | 返回 `hint`，提示常见原因（pending 用了 `COUNT(*)`，或 worker 没经过 converge） |
| 不可序列化返回 | 工具结果序列化失败 |

### 2.9 安全模型：不是沙箱

`runner.py` 在 Hive 进程内用 `exec` 执行脚本：

- 完整 Python 标准库；
- 已安装第三方包；
- 不限制 `__builtins__`；
- 可访问文件系统、网络、环境变量。

设计假设是 **Queen 是可信作者**。因此：

- 谁能让 Queen 写 Playbook，谁就能以 Hive 进程用户身份执行任意 Python；
- 生产环境应把 Playbook 当代码审，而不是当配置审；
- 不要把不可信输入直接拼进 Playbook；
- 工具白名单、凭据隔离、分支保护仍要单独做。

脚本在事件循环上运行；同步阻塞调用会卡住整个 loop，阻塞 IO 应使用 `asyncio.to_thread`。

### 2.10 非目标

- 不是可视化工作流编辑器；没有 DAG/BPMN。
- 不是通用工作流引擎；没有跨 Playbook 依赖、版本、回滚。
- 不会自动优化自己；Queen 改脚本、重跑，是“改一版、再跑一次”。
- 不校验 Worker 回执 schema（schema 只在 Worker 侧引导）。
- 没有 mid-run escalation；Worker 不确定时把行标成 `needs_review`。
- 没有脚本版本历史；同名覆盖。

## 3. Skill 与 Playbook 的关系

| | Skill | Playbook |
|---|---|---|
| 形态 | 说明文档 + 可选脚本 | Python 编排脚本 |
| 作用 | 告诉 Agent“怎么做” | 决定“做哪些行、并发多少、失败怎么办” |
| 状态 | 无 | 无（Tracker 是状态） |
| 粒度 | 方法 | 批量执行 |
| 谁写 | Queen / 用户 / 社区 | Queen |
| 典型组合 | `write_skill("enrich-lead")` 写协议 | `run_playbook` 每行派 Worker，任务文本引用该技能名 |

最佳实践（来自 `run_worker` 的工具说明）：

- 90% 相同的协议写进 Skill；
- 每个 Worker 的任务文本只带**差异**（哪 5 家公司、哪些行 ID）；
- 不要为每个小单元派一个 Worker：每个 Worker 有约 2–3k token 的“定位税”，5–10 个同质单元/Worker 更经济。

## 4. 评价

**优点**

- Skill 采用开放标准，与外部生态互通；渐进披露 + 信任门禁 + 作用域覆盖，工程上完整。
- Playbook 的“无状态 + Tracker 续跑”极简且正确；重试/退避/车道/死信覆盖了批量作业的主要失败模式。
- `write_skill` 让“试点→沉淀→批量化”闭环能在一次会话里完成。

**风险**

- Playbook 是任意代码执行，安全边界等于宿主；多租户场景不可直接暴露。
- 技能是提示词注入面：不受信项目技能若被信任，可引导 Agent 调用危险工具；信任门禁是必要的但不够。
- Playbook 没有版本历史与运行恢复；生产上需要外部编排/版本管理兜底。
- `concurrency` 与 lane 的语义容易被误用（Colony cap 是下限，不是上限）。
