# 08 · 自治与入口：Trigger、Sentinel、Channel、Queen Mail、Turn Context

> Hive 的“无人值守”不是后台全量扫描，而是**显式加载 + 停泊时判定 + 外部消息回流**。
> 这一章讲清楚四条入口和一条回执路径。

## 1. Trigger：定时与 Webhook

### 1.1 定义

`host/triggers.py`：

```python
@dataclass
class TriggerDefinition:
    id: str
    trigger_type: str          # "timer" | "webhook"
    trigger_config: dict
    description: str = ""
    task: str = ""
    enabled: bool = False
    last_fired_at: str | None = None
    next_due_at: str | None = None
```

触发器属于一个 Colony（或 Queen 会话），持久化在 `triggers.json`。

### 1.2 两种类型

| 类型 | 配置 | 行为 |
|---|---|---|
| `timer` | `{"cron": "*/5 * * * *"}` 或 `{"interval_minutes": 5}` | 用 `croniter` 计算下一次触发 |
| `webhook` | `{"path": "/hooks/github", "methods": ["POST"], "port": 8090}` | 起一个本地 HTTP 端点，收到请求即注入任务 |

`set_trigger` 会校验：

- cron 表达式合法；
- interval > 0；
- webhook path 以 `/` 开头，method 合法；
- 必须有 `task`（触发时注入的任务文本）。

### 1.3 只在会话加载时运行

触发器**不会**在 Hive 未加载该会话时自行运行。会话加载时：

1. 读 `last_fired_at` / `next_due_at`；
2. 用 `compute_missed` 计算关闭期间应触发但错过的 tick（cron 或 interval）；
3. **当前实现：静默跳过**——把每个 trigger 的 `last_fired_at` 设为 now 并保存，不补跑、不弹窗。

**设计意图 vs 实现**：`MISSED_TRIGGERS` 事件类型、`resolve_missed` 端点、前端握手 UI 都存在，但当前源码中**没有 `MISSED_TRIGGERS` 的生产者**（`session_manager._load_worker_core` 只做静默改写）。因此“错过 N 次，让你选补跑/跳过/重排”是**设计描述，不是当前行为**。要补跑需要手动触发或重跑任务。

### 1.4 触发后发生什么

- 定时器到点：向 Queen 会话注入一条任务消息（`inject_trigger`）；
- Webhook：请求体被包装成触发器 payload，注入同一路径；
- Queen 像收到用户消息一样开始一个回合；她可以选择直接做、派 Worker、或写 Playbook。

### 1.5 另一层 TriggerSpec

`ColonyRuntime` 里还有 `TriggerSpec`（`webhook` / `api` / `timer` / `event` / `manual`），用于“自动 spawn Worker”的更低层触发器。它与 Queen 的 `set_trigger` 不是同一抽象：前者是 ColonyRuntime 的调度规格，后者是 Queen 可调用的工具。生产主路径是后者。

## 2. Sentinel：停泊时的三选一

### 2.1 它解决什么

Queen 经常在目标未完成时停泊：

- “要继续吗？”式的请示；
- 真正需要人的阻塞（登录过期、浏览器崩溃、只有用户知道的信息）；
- 已经完成但没有明确收尾。

`ParkReason` 只能告诉你是 `TURN_DONE` 还是 `ASK_USER`，不能区分“该继续”与“该找人”。Sentinel 用一个便宜的 LLM 分类器补上这一层。

### 2.2 开关与作用域

- **按 Colony  opt-in**：`notifications.json` / Colony 设置里的 Sentinel 开关；
- 全局参数在 `configuration.json` 的 `sentinel` 块；
- 只对 Colony Queen 的停泊生效；普通 DM Queen 不走 Sentinel；
- 与 `IdleNudgeSource` 互斥：Sentinel autopilot 开启时，idle nudge 自动让位，避免双重 nudge。

### 2.3 三个判定

`sentinel/classifier.py`：

| verdict | 含义 | 动作 |
|---|---|---|
| `continue` | 只是请示/汇报，工作还能推进 | 注入 nudge，唤醒 Queen 继续 |
| `needs_human` | 真阻塞：凭据过期、工具崩溃、只有人能提供的信息、重大分叉决策 | 升级给人，Queen 保持停泊 |
| `done` | 目标已完成，有收尾报告 | 记录完成，停泊（终态），不必回复 |

判定依据：

- 目标；
- 未完成任务；
- Queen 最后一条消息；
- 用户最近一条消息（**用户说“停/等等我”会覆盖 Queen 的继续权**）；
- 待回答问题；
- 最近工具错误；
- 仍在运行的 Worker 快照（有 Worker 在跑时，空闲是健康等待，不是停滞）。

分类器失败或没有 LLM 时，保守回退到 `continue`，但标记 `errored=True`；会“采取行动”的调用方可以据此跳过，避免因一次 LLM 抖动而误唤醒。

### 2.4 硬阻塞先于分类器

在调用分类器之前，`EscalationSource` 已做确定性判断：

- broken park（`LLM_ERROR` / `EMPTY_RESPONSES` / `DOOM_LOOP`）；
- 检测到的认证/崩溃错误。

这些直接升级，不浪费一次分类调用。

### 2.5 节奏与上限

| 参数 | 默认 |
|---|---:|
| `classify_after_seconds` | 300s（5 分钟） |
| `max_nudges_before_escalate` | 5 |
| `escalate_when_ui_attached` | false |
| 每 Colony 可覆盖 `classify_after_seconds` | 是 |

连续 nudge 达到上限后，升级为 `heartbeat`（更醒目的检查点），而不是无限 nudge。

### 2.6 四种报告

| kind | 含义 | 走哪些渠道 |
|---|---|---|
| `blocker` | 真阻塞，需要回复 | 所有已配置渠道 |
| `heartbeat` | 自动推进 N 次后的检查点 | 所有已配置渠道 |
| `done` | 完成报告 | 所有已配置渠道 |
| `progress` | “还在做”的 FYI + 内部 nudge | **仅 Hive Inbox feed**，不发 Telegram/Slack |

这是很务实的设计：进度噪音只留在应用内，外部渠道只收需要人注意的事。

### 2.7 三个渠道

`sentinel/notifier.py`：

| 渠道 | 凭据 | 说明 |
|---|---|---|
| **Hive Inbox** | `HIVE_CLOUD_BASE` + `HIVE_CLOUD_JWT`（账号登录） | 第一方、无需 per-channel token |
| **Telegram** | `TELEGRAM_BOT_TOKEN` | Bot API；长轮询收回复 |
| **Slack** | `SLACK_BOT_TOKEN` + `SLACK_APP_TOKEN` | Socket Mode；线程回复 |

监听器（`sentinel/listeners.py`）都是**出站连接**，可穿过 NAT：

- Telegram：`getUpdates` 长轮询；
- Slack：`apps.connections.open` + WebSocket；
- Hive Inbox：SSE 流。

### 2.8 回复如何回到 Queen

- 每条升级消息带一个 `ref: <token>` 页脚；
- token 是 `HMAC(escalation_id)`，每安装一个 0600 secret，**不可伪造、不可推断**（`sentinel/token.py`）；
- 用户回复 → 监听器解析 token → 找到 `EscalationRecord` → `_resume` 注入原会话；
- 如果会话是冷的，先 `_cold_restore` 再注入；
- 回复成功后标记 escalation 为 resolved，并通知 Inbox 关闭。

### 2.9 持久化

- 升级记录：`colonies/<c>/escalations/<id>.json`；
- 通知配置：`colonies/<c>/notifications.json`；
- Sentinel 日志：`HIVE_SENTINEL_LOG=1` 时写 `~/.hive/logs/sentinel.log`，其中 `classify_decision` 是稳定调试锚点。

### 2.10 安全含义

Sentinel 把“外部聊天消息”变成“可以唤醒 Agent 的输入”。因此：

- token 必须不可伪造（HMAC + 常量时间比较）；
- Telegram/Slack 的 bot 权限应最小化；
- 群聊里任何能看到消息的人都能回复——如果 Agent 有高危工具，需要限制渠道/人；
- `escalate_when_ui_attached=false` 意味着“用户正开着页面时，不要往外部渠道推”。

## 3. Channel：聊天应用接入

### 3.1 插件模型

Channel 是安装在 Hive 旁边的 Python 包，在 entry point 组注册：

```toml
[project.entry-points."hive.channels"]
mychat = "mychat:MyChatChannel"
```

`hive serve` 读取 `configuration.json`：

```json
{"channels": {"mychat": {"enabled": true, "token": "..."}}}
```

启用则调用工厂，传入 `ChannelHost`，启动返回对象。加载/启动失败的 Channel 只记日志，不会拖垮服务器。

### 3.2 会话映射

**每个聊天（群/私聊）映射为一个 Queen session。**

- `open_session(queen_id, session_id=..., title=...)`：恢复旧会话或新建；
- 如果 Queen 把对话移到了新 session（`SESSION_FORKED`），Channel 会跟随；
- Dashboard 不会抢占或停止 Channel 的会话；
- Channel 的会话在 Dashboard 里照常可见。

### 3.3 `ChannelHost` 能力

| 方法/属性 | 作用 |
|---|---|
| `config` | 该 Channel 的配置段 |
| `state_dir` | `$HIVE_HOME/channels/<name>` |
| `open_session` | 打开/恢复会话 |
| `send(session_id, text, images=...)` | 发送用户消息；自动恢复停止的会话；图片落盘 |
| `subscribe(session_id, handler)` | 订阅 Queen 输出 |
| `unsubscribe` / `set_title` / `close` | 生命周期 |
| `judge(question, state, criteria=...)` | 用 judge 槽位做 yes/no 概率判断 |

### 3.4 五种输出

`QueenOutput.kind`：

| kind | 内容 |
|---|---|
| `message` | 一个完成的模型回合文本 |
| `question` | `ask_user` 的问题列表（含选项） |
| `error` | 模型调用失败 |
| `files` | `attach_file` 交出的文件副本 |
| `idle` | 她完成了，等待下一条消息（可忽略） |

顺序保证：

- 文本在工具调用开始时就发出，而不是等整个回合结束；
- `CLIENT_INPUT_REQUESTED` 发 `question` 或 `idle`；
- `attach_file` 完成后发 `files`。

内部是一个 `asyncio.Queue` + 单独投递协程；handler 异常只记日志，输出丢失（**至多一次**语义）。慢聊天应用不会阻塞 Queen 的发布路径。

### 3.5 Channel 的边界

- Channel 拥有平台连接、鉴权、conversation→session 映射；
- Host 只暴露 6 个会话方法 + 5 种输出；
- Channel 不能直接操作 Colony、Tracker、工具；它只能通过 Queen 的消息面。

## 4. Queen Mail：Queen 之间写信

### 4.1 能力

开启 `queen_mail` 后，Queen 获得：

- `list_queens`：列出可通信的 Queen；
- `message_queen(to, text)`：发一封信。

### 4.2 投递语义

- 每个“写信人”在收信 Queen 那里有一个**固定会话**（以写信人命名的 session）；
- 信件以 `[Name(queen_id)]: ...` 的形式到达，像聊天应用消息，因此记忆与 Soul 会把功劳记给正确的人；
- 回复沿原路返回，唤醒写信人；
- 支持附件、问题、失败回执；
- 跨重启保持：会话恢复后回信路径还在。

### 4.3 链与限额

```json
"queen_mail": {
  "enabled": true,
  "queens": ["queen_ava", "queen_ivy"],
  "daily_limit": 100,
  "max_depth": 8,
  "max_parallel_turns": 2
}
```

| 键 | 默认 | 含义 |
|---|---:|---|
| `daily_limit` | 100 | 每个 Queen 每天最多发多少封（回复不计） |
| `max_depth` | 8 | 一封信/回复比“开启它的最深一封信”深一层；超过拒绝/丢弃 |
| `max_parallel_turns` | 2 | Claude Code Queen 同时运行的回合数 |

链的规则：

- 用户消息、定时器、Worker 报告会**重置**链深度；
- 两个 Queen 互相回信最多 `max_depth` 次；
- 每一跳写 `queens/mail.jsonl`：id、时间、kind、from/to、两个 session、caused_by、depth、chars、dropped。

这是防止“两个 Agent 无限客套”的必要护栏。

### 4.4 Claude Code Queen

`queen_mail.claude_code.<id>` 可以把一个“Queen”配置成 **Claude Code 本身**：

```json
"claude_code": {
  "queen_claude": {"name": "Claude", "api_key": "sk-ant-...", "permission_mode": "acceptEdits"}
}
```

- 其他 Queen 通过 `list_queens` 找到她；
- 给她的信会在一个固定 Claude Code 会话里以 `claude -p` 运行一轮；
- 同一通信者的信会合并为下一轮；
- 她通过每次运行注入的 `hive` MCP server 使用 `list_queens` / `message_queen`；
- Dashboard 用虚线卡片只读展示她的对话；
- 她需要本机安装 Claude Code 与 Anthropic API key（或显式选择订阅，风险自负）。

配置项包括 `command`、`auth`、`api_base`、`config_dir`、`cwd`、`permission_mode`、`allowed_tools`、`disallowed_tools`、`add_dirs`、`model`、`timeout_minutes`、`system_prompt`、`env`。

安全：

- 她的运行只继承必要环境变量，不继承 Hive 的凭据/密钥；
- 她用自己的 key；
- `bypassPermissions` 意味着“一个能给她写信的外部人，可能间接执行本机命令”；`queens` / `max_depth` / `daily_limit` 是必要收窄。

## 5. Turn Context：外部包在每轮前注入上下文

### 5.1 插件模型

```toml
[project.entry-points."hive.turn_context"]
mycontext = "mycontext:MyContext"
```

```json
{"turn_context": {"mycontext": {"enabled": true}}}
```

### 5.2 契约

```python
class TurnContext(Protocol):
    async def start(self) -> None: ...
    async def stop(self) -> None: ...
    async def context(self, queen_id: str, message: str) -> str | None: ...
```

- 每条**回答用户消息**的回合前调用；
- 返回文本会被包成 `<system-reminder>` 放在用户消息前；
- 超时 **5s** 或抛异常 → 本轮跳过，不阻塞；
- 加载/启动失败只记日志，不拖垮服务器；
- 状态目录 `$HIVE_HOME/turn_context/<name>`。

### 5.3 用途

- 接入企业知识库/工单系统；
- 注入“当前用户是谁、有什么权限”；
- 在 Agent 回答前提供外部检索结果；
- 与 Channel 互补：Channel 管“消息怎么进来”，Turn Context 管“回答前看到什么”。

## 6. 入口的统一收敛

所有入口最终都变成**一次输入**：

```text
Dashboard  ─┐
Channel    ─┤
Trigger    ─┼─► inject_event / inject_trigger / send ─► AgentLoop 的注入队列
Sentinel   ─┤
Queen Mail ─┘
```

区别只在语义：

- **真实用户输入**：`inject_event(is_client_input=True)` 清除 `user_stopped`、重置 idle nudge、通知 Sentinel 本地恢复、记录 pending user text，并触发 `CLIENT_INPUT_COMMITTED`。
- **触发器**：`inject_trigger` 把事件放入独立队列并唤醒停泊；它**不**经过 `inject_event`，因此不直接清除 `user_stopped`。但源码的 trigger drain 没有额外的“已停止”闸门——这是需要在使用中留意的实现细节，而不是“触发器绝不会在 Stop 后运行”的保证。
- **Worker 报告 / 外部事件**：走 `inject_event(is_client_input=False)`，作为用户回合进入（带 `[WORKER_REPORT]` 等标记）；它们**同样**会清除 `user_stopped`（源码行为，与 `mark_user_stopped()` 的注释措辞不完全一致）。
- **提醒**：只有标记 `energizes=True` 的提醒才唤醒停泊；普通提醒等到下一次输入。

这条“入口 → 队列 → 迭代边界 drain”的统一路径，是 Hive 能同时支持 Dashboard、聊天应用、定时器、邮件的根本原因。

## 7. 评价

**优点**

- 触发器的设计意图是“错过后握手”而不是偷偷补跑；但当前实现是静默跳过，握手端点尚未接线（见 1.3）。
- Sentinel 把“停泊”从死等变成可分类、可升级、可回复的异步流程；token 设计正确。
- Channel/Turn Context 的 entry-point 插件模型清晰，宿主暴露面小。
- Queen Mail 的深度/日限额是对多 Agent 通信的必要治理。

**风险**

- Sentinel 的远程渠道是“外部输入可唤醒 Agent”的通道；权限与 token 管理必须严格。
- Channel 输出是至多一次，网络失败会丢消息；没有持久重放。
- 触发器只在会话加载时运行，意味着“无人值守”仍需要一个常驻 `hive serve` 并显式 `--colony` 预加载。
- Queen Mail 的链深度限制能防无限往返，但不能防“内容层面的无效对话”。
