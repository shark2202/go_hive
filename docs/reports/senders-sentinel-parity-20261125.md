# Senders / Sentinel 机制一致性深度审计

- 时间：2026-11-25
- 基线：仓库 HEAD `f293fa108`
- Python 面：`tools/src/aden_tools/senders/**`、`tools/src/aden_tools/tools/senders_tool/**`、`core/framework/sentinel/**`（aden-tools/aden-hive 1.0.15）
- Go 面：`go_hive/go-hive/internal/senders/**`、`internal/sentinel/**`、`internal/server/{adapters_senders,sentinel_runtime,adapters_sentinel,routes_sentinel,routes_config}.go`
- 方法：逐模块对照配置、状态文件、动作/流程、生命周期、错误路径；引用两侧 `file:line`。未运行真实 Telegram/Slack/邮件投递。
- 标记：✅ 一致；◐ 一致但带已登记/边界差异；⚠ 未闭环缺口。

> **2026-11-25 晚更新（w170）：** Slack Socket Mode `SocketDialer` 与 Sentinel `SessionRestorer` 已落地并有 fake server/假会话测试；N1/N2 的代码链路已修。真实 Slack 凭据/网络 E2E 仍未跑，下方审计时的“未修”描述保留为历史证据。

---

## 0. 结论

| 面 | 判定 | 一句话 |
|---|---|---|
| **Senders** | ◐ 本地子集一致，云能力有意缺失 | 9 个 action、轮换/日限/抑制/发送日志/SMTP 语义对齐；云端发送器池、ESP/OAuth 投递、团队级配额、`threads.json` 回复路由未移植；另有 2 处安全姿态分叉。 |
| **Sentinel** | ◐ 引擎高度一致，2 个链路缺口已修（fake 验证） | 配置、决策、分类器、报告、存储、token、路由、工具一致；Slack Socket Mode 入站与冷会话恢复代码已接线；真实 Slack E2E 未跑。 |

---

# 一、Senders

## 1.1 架构分界

| 层 | Python（上游） | Go |
|---|---|---|
| 发送器池 | **Aden cloud**；60s TTL 缓存；OAuth 行只带 `integration_id`（`registry.py:3-6,24,30,90`） | **本地** `$HIVE_HOME/senders/senders.json`（`sender.go:4-6`、`store.go:4,19`） |
| 投递 | 云端连接器：Gmail/HubSpot OAuth、SendGrid/Mailjet API、SMTP 邮箱（`providers.py`） | **仅 SMTP 邮箱**（`mailer.go`）；ESP/OAuth 返回 `needs_user`（`engine.go:464-470`） |
| 配额/发送日志 | 云端事务 + 团队级计数；本地兜底（`sendlog.py:40,59-128`） | 本地 `sends.json` / `usage.json`，**设备级**（`sendlog.go:1-12,31`） |
| 抑制名单 | 云端优先，本地兜底（`sendlog.py:171-191`） | 本地 `suppressions.json`（`suppress.go:6,20`） |
| 回复线程 | `threads.json`：Message-ID token → colony；`record_send/record_reply/extract_token`（`threads.py:25-200`） | 只 mint Message-ID + `conversation_token` 写进 `sends.json`；**无 `threads.json`、无 `record_reply/extract_token`**（`mailer.go:154-163`） |
| 工具形态 | 9 个独立 MCP 工具（`senders_tool.py:266-745`） | 1 个 action-grouped `senders`（9 actions，`tool.go:16,31-41`） |

## 1.2 配置与启用

| 项 | Python | Go | 判定 |
|---|---|---|---|
| 开关解析 | `HIVE_EMAIL_SENDERS` env > `configuration.json.email_senders` > 默认 **off**（`config.py:972-997`） | 同解析（`config_features.go:53-60`） | ✅ |
| env 桥 | `sync_email_senders_env` 写入进程 env，MCP 子进程继承（`config.py:1366-1386`） | `SyncEmailSendersEnv`（`config_features.go:69-76`） | ✅ |
| 注册 | `_email_senders_enabled()` 真时注册 9 工具（`tools/__init__.py:163-177`） | `RegisterSenderTools` 真时注册 1 个工具（`adapters_senders.go:26-34`） | ◐ 形态不同 |

## 1.3 状态文件与生命周期

| 文件 | Python | Go | 生命周期 |
|---|---|---|---|
| 发送器池 | 云端（本地无） | `senders.json` | 首次 `Open` 读取；原子写；无 TTL |
| 轮换游标/用量 | `usage.json`（`rotation.py:8-9,33-35`） | `usage.json`（`rotation.go:21,36`） | 每次 pick 保存 cursor |
| 发送日志 | 云端 `reserve/complete` | `sends.json`（append-only，`sendlog.go:31`） | 无 TTL/清理，随发送量增长 |
| 抑制名单 | 云端/本地 | `suppressions.json` | 持久，无清理 |
| 回复线程 | `threads.json`（`threads.py:26,51`） | 无 | Go 只有 `sends.json` 行内 `conversation_token` |

## 1.4 动作与流程

9 个 action 名称、参数两侧一致：

`list_senders`、`send_from_sender`、`setup_email_sender`、`adjust_sender`、`pick_sender`、`send_campaign`、`suppress_recipient`、`list_suppressed`、`sender_history`。

核心发送流程两侧同构：

```
resolve sender
  → resolve body (text ↔ html, legacy html_content)
  → reserve slot (daily limit / duplicate / suppression)
  → deliver (provider)
  → complete (status / provider_message_id / error)
  → record conversation token (if trackable)
```

轮换策略 `round_robin` / `weighted` / `least_used`、pool/tags 过滤、`daily_limit` 跳过、cursor 持久化：**逐项一致**（`rotation.py:107-170` ↔ `rotation.go:100-186`）。

Body 语义（text→HTML 段落/换行、HTML→text、legacy `html_content`）由 `body_test.go` 逐条对齐上游测试。

## 1.5 Senders 差异

| # | 严重度 | 差异 | 影响 | 证据 |
|---|---|---|---|---|
| **S1** | 中高 | Go 无云端发送器池，不能看到/同步团队发送器 | 多设备/团队共享发送器不可用 | `sender.go:4-6`；`cloud-dependencies.md:8-9,71` |
| **S2** | 中高 | Go 仅 SMTP 投递；Gmail/HubSpot OAuth、SendGrid/Mailjet API 返回 `needs_user` | ESP/OAuth 发送器不能发，只能引导到桌面表单 | `engine.go:464-470`；`cloud-dependencies.md:71,119` |
| **S3** | 中 | 配额：Python 云端事务是**团队级**；Go 是**设备级** | 两台设备各发 40，实际 80，可能烧掉冷域名 | `sendlog.py:59-128`、`sendlog.go:1-12` |
| **S4** | 中 | 无云时：Python 对**有 daily_limit** 的发送器 **fail-closed 拒绝**；Go 用本地计数**允许** | 同为“无云”场景，安全姿态不同 | `sendlog.py:75-92`；Go 本地 reserve |
| **S5** | 中 | Python 有 `threads.json`、`record_send/record_reply/extract_token`；Go 没有 | 回复路由的持久层缺失；只有 `sends.json` 行内 token | `threads.py:25-200`；Go 仅 `mailer.go:154-163` |
| **S6** | 中低 | 工具形态：Python 9 个独立工具；Go 1 个 grouped 工具 | 默认 flat 面冻结的 39 名里没有 senders；开启 feature 后模型能否看到 9 个名字，需要按 grouped/工具目录路径验证 | `senders_tool.py:266-745`；`tool.go:16,31-41` |
| **S7** | 低 | Python 云端 `credential_valid`/`from_verified`/OAuth 绑定/verified-sender 检查；Go 无 | 设置结果的“可发送性”证据更弱 | `setup.py:150-180`；`engine.go:420-470` |

## 1.6 Senders 判定

- **本地 SMTP 子集：功能/语义一致**（动作、body、轮换、日限、抑制、发送日志、幂等键）。
- **云能力：有意缺失**，`cloud-dependencies.md` 已登记；不是隐藏 bug。
- **两处真实语义分叉**：S3 团队级 vs 设备级配额；S4 无云有上限时 fail-closed vs 放行。后者是安全姿态差异，建议在文档中明确“Go 本地模式允许本地配额发送”。

---

# 二、Sentinel

## 2.1 模块映射

| Python | Go | 判定 |
|---|---|---|
| `manager.py` | `manager.go` | ✅ 投递/入站/续跑/token 索引 |
| `classifier.py` | `classifier.go` | ✅ prompt/verdict/fail-open |
| `escalation_source.py` | `escalation_source.go` | ✅ 决策引擎 |
| `listeners.py` | `listeners.go` | ◐ Telegram/Hive ✅，**Slack 入站 ⚠** |
| `notifier.py` | `notifier.go` | ✅ Telegram/Slack/Hive |
| `runtime_identity.py` | `runtime_identity.go` | ✅ `runtime_id`/kind |
| `store.py` | `store.go` | ✅ 配置/记录 |
| `token.py` | `token.go` | ✅ HMAC/ref |
| `manager._install_sentinel_log_file` | `logging.go` | ◐ 文件轮转一致，字段少 level/logger |
| `agent_loop/internals/sentinel_tool.py` | `sentinel_tool.go` | ✅ 6 actions |

## 2.2 配置

全局 `configuration.json.sentinel`：

| 键 | 默认 | 两侧 | 证据 |
|---|---|---|---|
| `classify_after_seconds` | 300 | ✅ | `store.py:35,56-60` ↔ `store.go:35-39,120-133` |
| `max_nudges_before_escalate` | 5 | ✅ | `store.py:36` ↔ `store.go:37` |
| `escalate_when_ui_attached` | false | ✅ | `store.py:37` ↔ `store.go:38` |

每 colony `colonies/<id>/notifications.json`：

| 键 | 默认 | 两侧 | 证据 |
|---|---|---|---|
| `sentinel_enabled` | **true** | ✅ | `store.py:131` ↔ `store.go:270-280` |
| `channel` | `hive` | ✅ | `store.py:41,131` ↔ `store.go:39,236` |
| `target` | `{}` | ✅ | `store.py:129` ↔ `store.go:244-255` |
| `allowlist` | `[]` | ✅ | `store.py:121-131` ↔ `store.go:206-220` |
| `thread` | `{}` | ✅ | `store.py:130` ↔ `store.go:257-260` |
| `classify_after_seconds` | nil（继承全局） | ✅ | `store.py:124-131` ↔ `store.go:222-235` |

> 注意：Python `sentinel/__init__.py` 的 docstring 仍写“Off by default”，但 `store.load_notifications_config` 实际默认 `sentinel_enabled=True`（Hive Inbox 常连）。这是上游文档漂移，不是 Go 差异。

## 2.3 决策流程（两侧同构）

```
is_queen_stream && colony_id
  → skip reasons: user_stopped / colony_suggestion / awaiting_queen
  → notifications.sentinel_enabled
  → idle_seconds >= budget（per-colony 覆盖全局）
  → 每 window 最多评估一次
  → held?（blocker/heartbeat/done 已发出）
  → park context：goal / open_tasks / last assistant / recent user / questions / errors / running workers
  → 无 goal 且无 open tasks → 不做
  → decide:
       broken park / hard_blocker       → blocker
       nudge_count >= max_nudges        → heartbeat
       turn_done && 无 open tasks && 已 nudge ≥1 → heartbeat（loop breaker）
       classifier errored               → 不动作
       needs_human                      → blocker
       done                             → done
       continue                         → progress（nudge）
  → report:
       progress 仅 Hive Inbox；远端通道在 UI 附着时抑制
       相同 (kind, summary) 连续报告折叠
       blocker/heartbeat/done 置 held，直到真实 resume
```

分类器 prompt **逐字一致**（`classifier.go:75` ↔ `classifier.py:117`），verdict 三值 `continue`/`needs_human`/`done`，失败一律 `continue` + `errored=true`。两侧都记录 `classify_decision` 结构化日志。

## 2.4 投递、入站、续跑

**出站**：两侧都写 `escalations/<id>.json`、维护 `token → (colony, escalation)` 索引、同一 session 只保留一个 open record（新报告 supersede 旧）、线程 anchor 回写 `notifications.json.thread`、Hive 渠道带 `runtime_id/kind/kind/title/correlation_token`。消息模板（done/progress/heartbeat/blocker）与 `**bold**`→`*bold*` 一致。

**入站**：
1. 从文本提取 `(ref: token)`；
2. 找不到则回退“该 channel 唯一 open escalation”或 thread anchor；
3. 校验 allowlist；
4. `StripRef`；
5. 找 live session → 注入 `is_client_input=true`；
6. resolve record、删 token 索引。

**生命周期**：manager `Start` 重建 token 索引 → 启动 drain + listeners；`Stop` 取消全部并等待；`refresh_listeners` 在配置保存后热启；`on_local_resume` 关闭同 session 的所有 open record，并尽力关闭 Hive Inbox 行。

## 2.5 Sentinel 差异

| # | 严重度 | 差异 | 影响 | 证据 |
|---|---|---|---|---|
| **N1** | **高** | **Slack Socket Mode 入站已接线（w170）**：新增 stdlib RFC 6455 `SocketDialer` 并注入 `DefaultListenerFactory`；真实 Slack 凭据未跑 | 代码链路可收发/重连；真实 Slack 仍需凭据验证 | `socket_dialer.go`、`manager.go`、`listeners_test.go` |
| **N2** | **高/中** | **冷会话回复恢复已接线（w170）**：`SessionRestorer` seam 优先于 `CreateSession`，server 适配器重建 resume 参数；未跑完整 ChatRuntime E2E | 冷会话回复可尝试恢复；失败仍保持 escalation open | `sentinel_runtime.go`、`manager.go`、`manager_test.go` |
| **N3** | 低 | 日志：Go 文件 sink 只有时间戳，缺 `[LEVEL]` 和 logger 名 | 排障信息弱于 Python | `logging.go:26-60` vs `manager.py:39-80` |
| **N4** | 低/待验 | Go manager 的 notifier 默认 env-only；靠启动 `SyncCredentialEnv` + `sentinel_setup` 的 store→env 桥补齐；Python 直接读加密 store | 正常启动/设置路径可用；建议补“仅存凭证、不设 env”的集成测试 | `notifier.go:70-110`、`sentinel_setup_tool.go:88-104`、`routes_credentials.go:154-165` |
| **N5** | 验证 | 真实 Telegram/Slack/Inbox 投递未验证；无真实多机 | `support-matrix.md:59,235` 已登记“部分” | — |
| **N6** | 低 | 小项：Go `NewEscalationRecord` 用 `time.Now` 而非注入 clock；`AppliesTo` 日志把 `SessionID` 当 stream_id；Render 持有 mutex 跨分类器调用 | 测试性/日志/锁粒度，不影响主流程 | `store.go`、`escalation_source.go` |

## 2.6 Sentinel 判定

- **配置、决策、分类器、报告种类、存储、token、runtime identity、出站投递、路由、工具**：一致。
- **Slack 入站**和**冷恢复**是两个会影响用户的实际缺口：前者让 Slack 只能单向通知，后者让重启后的回复失效。
- 其余是日志/测试性/真实环境验证差异。

---

# 三、一致性矩阵

| 维度 | Senders | Sentinel |
|---|---|---|
| 配置键/默认值 | ✅ feature flag 一致 | ✅ 全局 + per-colony 一致 |
| 特性面 | ◐ 本地子集；云/ESP/OAuth 缺失 | ◐ Telegram/Hive 双向；Slack 仅出站 |
| 主流程 | ✅ 解析→预留→投递→完成→线程 | ✅ 门禁→预算→分类→报告→投递→回复 |
| 生命周期 | ✅ 首次创建、原子写；无清理 | ✅ start/stop/refresh/token 索引；冷恢复缺 |
| 存储格式 | ◐ 本地 JSON 一致；`threads.json` 缺 | ✅ 记录/配置字段逐项一致 |
| 错误/降级 | ◐ 无云配额策略不同 | ◐ 分类器 fail-open 一致；Slack/冷恢复降级 |
| 模型/API 面 | ◐ 9 tools vs 1 grouped | ✅ 6 actions + 7 routes 一致 |
| 外部验证 | ⚠ 无真实凭据 | ⚠ 无真实投递/多机 |

---

# 四、建议

## Senders

1. **先拍板产品口径**：Go 是“本地 SMTP 发送器”还是“对齐云端发送器池”。当前代码/`cloud-dependencies.md` 已选择前者；若是前者，应在工具描述里明确“ESP/OAuth 需桌面表单/云端”，并弱化 `conversation` 的无条件承诺。
2. 若要补回复闭环：移植 `threads.json` + `record_send/extract_token/record_reply`，或明确“Go 没有入站邮件 poller”。
3. 对齐 S3/S4 的安全姿态：至少在有 `daily_limit` 且无云时，按 Python 的 fail-closed 处理，或在文档中显式声明差异。
4. 验证 feature 开启后的模型工具面：默认 flat 39 名不含 senders；确认 grouped/工具目录路径能让模型真正看到并调用这 9 个 action。

## Sentinel

1. **补 Slack SocketDialer**：复用 `browserbridge/ws` 或手写最小 WS 客户端，注入 `DefaultListenerFactory`；未补前应在 UI/文档明确“Slack 仅单向通知”。
2. **补冷恢复 seam**：把 `CreateSession` 接到 loader 的 colony/queen resume 路径（`CreateSessionParams{QueenResumeFrom}`），或明确返回可读错误并在 UI 提示“重启后请从桌面继续”。
3. 日志补齐 level/logger 名（低）。
4. 增加“仅存凭证、不设 env”的 sentinel 集成测试，确认 manager notifier 能读到加密 store。
5. 把 N1/N2 登记到 `alignment-differences.md` 或 `migration-known-gaps.md`，避免“8 包已移植”被读成“全链路可用”。

## 共同

- 给 senders/sentinel 的配置与磁盘记录各加一份机器契约（字段/默认/路径），把本轮发现的云/本地差异变成可重跑的对位测试，而不是文档叙述。

---

# 附录：架构与交互序列

## A.1 Senders 架构

### Go（本地 SMTP 版）

```
LLM
 │  senders(action, params)          ← 1 个 grouped 工具，9 个 action
 ▼
internal/senders.Engine
 ├─ Store            $HIVE_HOME/senders/senders.json      发送器池 + public_view
 ├─ CursorStore      $HIVE_HOME/senders/usage.json        轮换游标 + 当日用量
 ├─ SendLog          $HIVE_HOME/senders/sends.json        reserve/complete/幂等/历史
 ├─ SuppressionStore $HIVE_HOME/senders/suppressions.json  do-not-contact
 ├─ BodyResolver     text ↔ html / legacy html_content
 └─ Mailer           SMTP（STARTTLS 587 / implicit TLS 465）
        │
        ▼
   目标邮箱
```

### Python（云端版）

```
9 个 MCP 工具（list/send/setup/adjust/pick/campaign/suppress/list_suppressed/history）
 │
 ├─ get_registry() ──► Aden cloud /v1/senders/runtime（60s TTL）
 │                        └─ OAuth 行只带 integration_id
 ├─ providers.send_one ──► 云端连接器（Gmail/HubSpot OAuth、SendGrid/Mailjet API）
 │                        或 SMTP 邮箱
 ├─ sendlog.reserve/complete ──► 云端事务（团队级配额）；本地兜底
 ├─ rotation.pick ──► 本地 cursor + 云端 usage
 ├─ threads.record_send ──► $HIVE_HOME/senders/threads.json（Message-ID token → colony）
 └─ setup.setup_sender ──► 云端创建 + credential/verified 检查；否则 hive:// 深链
```

## A.2 Senders 交互序列

### 单封发送

```
调用方/模型
  │ send_from_sender{sender, to_email, subject, html/text, colony_id}
  ▼
Engine/tool
  │ 1. 解析 sender（id 或 name）；找不到 → error + available_senders
  │ 2. 解析 body：html/text 互推；legacy html_content
  │ 3. trackable = sender.can_receive && colony_id
  │      └─ true：mint Message-ID + conversation token
  │ 4. reserve：
  │      ├─ suppression 命中 → allowed=false, denied_because=suppressed
  │      ├─ 幂等键重复      → duplicate=true
  │      └─ 达到 daily_limit → denied_because=daily_limit
  │ 5. deliver：
  │      ├─ Go：SMTP（失败 → complete(failed) + release slot）
  │      └─ Python：云端 connector / SMTP
  │ 6. complete(send_id, status, provider_message_id, error)
  │ 7. trackable → 记录线程；返回 conversation token
  ▼
返回 {success, provider, id, remaining_today, conversation?}
```

### 批量 campaign

```
send_campaign{recipients, pool?, tags?, policy?, ...}
  → 解析 recipients
  → candidate_pool(pool, tags)                 启用且未达日限
  → for each recipient:
        reserve（抑制/重复/日限，逐个重查）
        pick sender（round_robin / weighted / least_used）
        deliver
        complete
  → 无可用发送器时停止并报告
  → 返回 {sent, failed, tracked, skipped, ...}
```

### 设置发送器

```
setup_email_sender{provider, from_email, ...}
  → normalize_provider（gmail→google；smtp.sendgrid.net→sendgrid；未知 host→smtp）
  ├─ SMTP：host + password → 保存；IMAP host 推断 → can_receive=true
  ├─ SendGrid/Mailjet：Go → needs_user + setup_url
  │                    Python → 云端创建 + credential 校验
  └─ Google/HubSpot：Go → needs_user + setup_url
                       Python → 已连接则绑定 integration_id，否则 needs_user
  → 返回 saved / needs_user(setup_url) / error
```

### 选择发送器

```
pick_sender{pool?, tags?, policy?}
  → usage_today（Python 云端；Go 本地）
  → eligible（enabled && usage < daily_limit）
  → policy：
       least_used → min usage
       weighted   → 按 weight 展开 + 持久 cursor
       round_robin→ 持久 cursor
  → 返回 sender + remaining_today（不发送）
```

## A.3 Sentinel 架构

```
                    ┌──────────────────────── AgentLoop / ChatRuntime ───────────────────────┐
                    │  queen turn 结束 → park（turn_done / ask_user / llm_error / ...）        │
                    │  ReminderHub 在 IDLE_TICK 调用 EscalationSource                          │
                    └───────────────────────────────┬─────────────────────────────────────────┘
                                                    ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ EscalationSource（sentinel/escalation_source）                                                │
│  gates：is_queen_stream + colony_id + notifications.sentinel_enabled + idle budget           │
│  ParkContext：goal / open tasks / last assistant / recent user / questions / errors / workers │
│  decide：broken/hard → blocker；nudge≥max → heartbeat；loop-breaker → heartbeat；             │
│          classifier → needs_human/done/continue(progress)                                    │
│  report：progress → 返回 Reminder（唤醒 loop）；其他 → OnEscalate(payload) 并 held            │
└───────────────────────────────┬──────────────────────────────────────────────────────────────┘
                                │ payload{escalation_id, colony, session, token, kind, question, channel, target}
                                ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ SentinelManager（sentinel/manager）                                                           │
│  enqueue（非阻塞）→ drain                                                                     │
│  1. 写 colonies/<id>/escalations/<esc>.json（open）                                           │
│  2. token → (colony, escalation) 索引                                                         │
│  3. 同 session 旧 open record → resolved_by=superseded                                        │
│  4. Notifier.Send（telegram / slack / hive）                                                  │
│  5. 首个线程 anchor → notifications.json.thread                                               │
└───────────────┬──────────────────────────────────────────────┬───────────────────────────────┘
                │                                              │
                ▼                                              ▼
        Notifier（HTTP REST）                          Inbound listeners（outbound 连接）
        ├─ Telegram sendMessage                        ├─ Telegram getUpdates 长轮询
        ├─ Slack chat.postMessage                      ├─ Slack Socket Mode（⚠ Go 无 dialer）
        └─ Hive POST /v1/inbox/messages                └─ Hive SSE /v1/inbox/runtime/stream
                                                              │
                                                              ▼
                                                       Manager.OnInbound
                                                       ├─ token → record
                                                       ├─ 唯一 open / thread 回退
                                                       ├─ allowlist
                                                       ├─ StripRef
                                                       ├─ live session → inject(client_input=true)
                                                       └─ resolve record + 删 token 索引
```

辅助组件：

```
Store（notifications.json / escalations/*.json）
TokenManager（sentinel_secret，HMAC(escalation_id) → 16 字符）
RuntimeIdentity（runtime_id / kind，Hive Inbox 路由键）
Classifier（一次性 LLM：continue / needs_human / done）
Logging（HIVE_SENTINEL_LOG → logs/sentinel.log）
```

## A.4 Sentinel 交互序列

### 逃逸（park → 通知）

```
AgentLoop park
  → ReminderHub(IDLE_TICK)
  → EscalationSource.Render
      ├─ idle < budget → 等下一 tick
      ├─ 无 goal 且无 open tasks → 不做
      ├─ classifier:
      │    continue   → progress
      │    needs_human→ blocker
      │    done       → done
      │    errored    → 不动作（不 nudge）
      └─ report:
           progress → Reminder → loop 继续
           blocker/heartbeat/done → EnqueueEscalation → held
  → Manager.drain
      → 写 record
      → 索引 token
      → Notifier.Send
      → 成功：线程 anchor 回写
      → 失败：record 仍 open，日志告警
```

### 用户回复（通道 → 唤醒 queen）

```
Telegram/Slack/Hive 入站
  → Listener 解析（sender / text / thread / chat）
  → Manager.OnInbound
      ├─ ResolveRecord：token → 唯一 open → thread anchor
      ├─ allowlist 校验
      ├─ reply = StripRef(text)
      ├─ Resume：
      │    ├─ live session → QueenNode.InjectEvent(reply, is_client_input=true)
      │    └─ cold session → Python cold restore / Go 返回不支持
      └─ 成功：resolve record + 删 token 索引
  → ChatRuntime → AgentLoop 继续（用户回复进入同一会话）
```

### 本地用户回复（dashboard / 其他通道）

```
ChatRuntime.inject_event(is_client_input=true)
  → sentinelSource.Reset()（解除 held、清 nudge 计数/去重）
  → Manager.OnLocalResume(session)
      ├─ 同 session 所有 open record → resolved_by=local
      └─ 若有 Hive 渠道 → POST /v1/inbox/resolve
```

### 生命周期

```
serve 启动
  → NewSentinelManager（Store / Sessions / Runtime / Notifier）
  → Manager.Start
      ├─ 从 open records 重建 token 索引
      ├─ 启动 drain goroutine
      └─ RefreshListeners：
           telegram token? → TelegramListener
           slack app token? → SlackSocketListener（Go 目前无 dialer）
           hive connected?  → HiveInboxListener

配置保存（sentinel_setup configure / routes）
  → PublishRefresh → Manager.RefreshListeners
      └─ 只启动尚未运行的 listener；已运行的保持

serve 停止
  → Manager.Stop
      ├─ cancel drain + listeners
      └─ 等待全部退出
```

## A.5 与 Agent 内核的交互点

| 交互点 | 方向 | 机制 |
|---|---|---|
| Park 信号 | 内核 → Sentinel | `ReminderHub` 的 `IDLE_TICK` + `Signals{awaiting_input, park_reason, idle_seconds}` |
| 任务/目标 | 内核 → Sentinel | `ParkContextProvider` 读会话 task store、goal、conversation tail、pending questions、running workers |
| 分类器模型 | 内核 → Sentinel | `LLMProvider(agentCtx)` 返回 queen 的 completer；一次性 `AComplete(JSONMode)` |
| Nudge | Sentinel → 内核 | 返回 `Reminder{body, source, meta}`；hub 注入后 loop 继续 |
| 逃逸通知 | Sentinel → 外部 | `OnEscalate` → Manager → Notifier |
| 用户回复 | 外部 → 内核 | `QueenNode.InjectEvent(reply, isClientInput=true)` |
| UI 附着判定 | 内核 → Sentinel | `LiveSession.SSEClientCount() > 0` |
| 本地回复清账 | 内核 → Sentinel | `inject_event` → `source.Reset()` + `Manager.OnLocalResume` |

## A.6 当前会断开的链路

| 链路 | 状态 |
|---|---|
| 出站 Telegram / Slack / Hive | ✅ 代码完整；真实凭据未验证 |
| 入站 Telegram | ✅ |
| 入站 Hive Inbox | ✅ |
| **入站 Slack Socket Mode** | ⚠ `DefaultListenerFactory` 传 `Dialer: nil`；生产无 `SocketDialer` 实现，listener 退避 |
| **冷会话回复 resume** | ⚠ Go `CreateSession` 返回 `ErrSentinelColdRestoreUnsupported`；重启后回复无法唤醒 |
| Senders 云端池 / ESP / OAuth | ⚠ 有意缺失；SMTP 本地路径完整 |
| Senders 回复线程 | ⚠ Go 无 `threads.json` / `record_reply` |

---

# 五、证据索引

## Senders

| 主题 | Python | Go |
|---|---|---|
| 云池/TTL | `tools/src/aden_tools/senders/registry.py:3-6,24,30,90` | 无（本地 `store.go:4,19`） |
| 云配额/发送日志 | `senders/sendlog.py:40,59-128` | `internal/senders/sendlog.go:1-12,31` |
| 无云 fail-closed | `senders/sendlog.py:75-92` | `internal/senders/sendlog.go` 本地 reserve |
| 抑制 | `senders/sendlog.py:171-191` | `internal/senders/suppress.go:6,20` |
| 轮换 | `senders/rotation.py:107-170` | `internal/senders/rotation.go:100-186` |
| 回复线程 | `senders/threads.py:25-200` | 仅 `mailer.go:154-163` + `sendlog.go` token |
| ESP/OAuth 投递 | `senders/providers.py`、`setup.py` | `engine.go:464-470`（needs_user） |
| 工具形态 | `senders_tool.py:266-745`（9 工具） | `tool.go:16,31-41`（1 grouped） |
| Feature flag | `core/framework/config.py:972-997,1366-1386` | `internal/server/config_features.go:53-76` |
| 注册 | `tools/__init__.py:163-177` | `internal/server/adapters_senders.go:26-34` |

## Sentinel

| 主题 | Python | Go |
|---|---|---|
| 配置默认 | `sentinel/store.py:35-41,93,131` | `internal/sentinel/store.go:35-39,236,270-280` |
| 决策引擎 | `sentinel/escalation_source.py:49-64,200-330` | `internal/sentinel/escalation_source.go:39-60,300-430` |
| 分类器 | `sentinel/classifier.py:32-34,117-256` | `internal/sentinel/classifier.go:24,75-291` |
| 投递 | `sentinel/manager.py:199-330` | `internal/sentinel/manager.go:400-600` |
| 入站/续跑 | `sentinel/manager.py:332-437` | `internal/sentinel/manager.go:603-733` |
| Telegram | `sentinel/listeners.py:35-86` | `internal/sentinel/listeners.go:60-215` |
| Slack | `sentinel/listeners.py:90-140` | `internal/sentinel/listeners.go:222-330`（Dialer nil） |
| Hive Inbox | `sentinel/listeners.py:145-240` | `internal/sentinel/listeners.go:436-555` |
| Notifier | `sentinel/notifier.py:23-224` | `internal/sentinel/notifier.go:26-350` |
| Token | `sentinel/token.py:35-106` | `internal/sentinel/token.go:32-194` |
| Runtime identity | `sentinel/runtime_identity.py:27-72` | `internal/sentinel/runtime_identity.go:27-136` |
| 日志 | `sentinel/manager.py:39-80` | `internal/sentinel/logging.go:26-60` |
| 路由 | `server/routes_sentinel.py:243-249`、`routes_config.py:1489-1490` | `internal/server/routes_sentinel.go:117-135`、`routes_config.go:277-278` |
| 工具 | `agent_loop/internals/sentinel_tool.py:45-90` | `internal/sentinel/sentinel_tool.go:44-58` |

---

# 六、验证边界

- 未运行真实 Telegram/Slack/邮件投递；未用真实凭据验证 `sentinel_setup` 的 store→env→listener 全链路。
- 未验证重启后 Slack/Telegram/Inbox 回复的冷恢复（当前代码明确不支持）。
- 未验证两台设备并发发送时的日限叠加（Go 本地计数）。
- 本文的“一致”指代码级行为与配置/存储形态；不等于“真实第三方投递已验证”。
