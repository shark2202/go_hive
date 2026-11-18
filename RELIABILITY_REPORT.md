# R6 可靠性故障注入报告（go-hive）

- 工作树：`/Users/mac/hive/.wt/w5-rel`，分支 `w5/rel`
- 模块：`go_hive/go-hive`（Go 1.26.5，darwin/arm64）
- 运行方式：`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test -race ...`（离线、纯 Go）

## 结论

| # | 故障场景 | 结果 |
|---|----------|------|
| 1 | 损坏的 state.json / part / cursor / credentials index | **PASS**（跳过或重建；修复多处静默/覆盖缺陷，见下文） |
| 2 | LLM 超时 / 429 / 空响应 | **PASS**（有界重试、类型化错误、超时可中止；修复取消后仍睡眠） |
| 3 | 工具超时与输出上限 | **PASS**（进程组被杀、不泄漏 goroutine/池位；修复 2 处真实缺陷） |
| 4 | 并发：共享 part 目录、SSE 订阅/退订 | **PASS**（`-race` 干净；修复索引丢失与陈旧 cursor 覆盖） |
| 5 | 磁盘满 / 只读目录 | **PASS**（明确错误、无临时文件残留；ENOSPC 说明见“残余风险”） |

`make ci`：**exit 0**。全量 `go test -race ./...`：**全部通过**（见“门禁证据”）。

## 新增测试（全部带 `// Parity-exempt:` 注解）

| 包 | 测试 |
|----|------|
| `internal/atomicfs` | `TestWriteFileReadOnlyDirectoryReturnsError`、`TestWriteFileNonDirectoryParentReturnsError`、`TestReadJSONCorruptReturnsDecodeError` |
| `internal/credentials` | `TestCorruptCredentialsIndexRebuildsFromDisk`、`TestCredentialsSaveReportsIndexWriteFailure`、`TestConcurrentCredentialSavesKeepIndexEntries` |
| `internal/session` | `TestCorruptSessionStateIsSkippedByList`、`TestConversationNextSeqFollowsHighestPart`、`TestConversationCorruptCursorIsTreatedAsAbsent`、`TestConcurrentAddPartOnSharedDirectory`、`TestAddPartReadOnlyDirectoryReportsError` |
| `internal/storage` | `TestCorruptCheckpointIndexRebuilds`、`TestCorruptSessionStateSkippedAndReported`、`TestWritePartReadOnlyDirectoryReportsError`、`TestCheckpointIndexWriteFailureIsExplicit`、`TestRebuildSummarySkipsCorruptParts` |
| `internal/agentloop` | `TestRestoreConversationStaleCursorDoesNotOverwriteParts`、`TestRestoreConversationCorruptCursorRebuildsFromParts`、`TestRestoreConversationSkipsCorruptPart`、`TestConcurrentPartWritersShareOneDirectory`、`TestWritePartReadOnlyDirectoryReportsError` |
| `internal/llm` | `TestCompleteRateLimitPropagatesTypedError`、`TestCompleteCancelledContextStopsRetrying`、`TestHTTPRateLimitRetryAfterHeaderRecovers`、`TestHTTPCompletionHonoursContextDeadline`、`TestHTTPEmptyResponseRetryIsBounded`、`TestHTTPCompletionMalformedBodyPropagates` |
| `internal/tools` | `TestExecuteToolTimeoutCancelsExecutorAndFreesPool` |
| `internal/tools/builtin` | `TestTerminalExecTimeoutKillsBackgroundChildHoldingPipe`、`TestTerminalExecContextCancelKillsProcessGroup`（`!windows`） |
| `internal/eventbus` | `TestConcurrentSubscribeUnsubscribePublish`、`TestHandlerCanUnsubscribeDuringPublish` |
| `internal/server` | `TestRecordUserMessageStaleCursorDoesNotOverwrite`、`TestGlobalSSESubscriptionsReleasedUnderChurn` |

## 1. 损坏的持久化文件

注入方式：截断 JSON（`{"session_id":` 等）、删除/破坏 part、把 index 写成半截文件、在只读 cursor 上继续读。

| 检查点 | 行为 | 证据 |
|--------|------|------|
| `session.Store.List` 遇到坏 `state.json` | 跳过该会话，不返回错误 | `TestCorruptSessionStateIsSkippedByList` |
| `session.Store.ReadState` 遇到坏 `state.json` | 返回明确 decode 错误（不 panic/不静默） | 同上 |
| `storage.SessionStore.ListSessions` | 跳过坏会话；`ReadState` 报错 | `TestCorruptSessionStateSkippedAndReported` |
| part 损坏（agentloop / session / storage summary） | 跳过坏 part，其余历史可恢复 | `TestRestoreConversationSkipsCorruptPart`、`TestConversationNextSeqFollowsHighestPart`、`TestRebuildSummarySkipsCorruptParts` |
| cursor 损坏（agentloop） | 视为缺失，从 parts 重建 `next_seq` | `TestRestoreConversationCorruptCursorRebuildsFromParts` |
| cursor 损坏（session） | 视为缺失（零值），不再返回错误 | `TestConversationCorruptCursorIsTreatedAsAbsent` |
| credentials index 损坏 | 从 `*.enc` 重建；下一次保存不再清空旧条目 | `TestCorruptCredentialsIndexRebuildsFromDisk` |
| checkpoint index 损坏 | 从 checkpoint 文件重建 | `TestCorruptCheckpointIndexRebuilds` |

修复前实测失败（节选，均为修复前的真实运行输出）：

```
ListAll after corruption = [], want [alpha beta] rebuilt from .enc files
rebuilt index = <nil>, want both checkpoints
NextSeq = 4 after parts 0,1,2,5; want 6
ReadCursor on corrupt cursor returned an error instead of rebuilding
```

## 2. LLM 超时 / 429 / 空响应

| 场景 | 行为 | 证据 |
|------|------|------|
| HTTP 429 + `Retry-After` | 重试后成功；退避次数 1 | `TestHTTPRateLimitRetryAfterHeaderRecovers` |
| 429 预算耗尽 | 返回 `*ProviderError`，`StatusCode == 429`（类型不丢） | `TestCompleteRateLimitPropagatesTypedError` |
| 上游挂起 + 调用方 deadline | 约 150ms 返回，`errors.Is(err, context.DeadlineExceeded)` | `TestHTTPCompletionHonoursContextDeadline` |
| 200 空 choices | 有界重试（`EmptyResponseMaxRetries+1` 次）后返回空响应，不无限循环 | `TestHTTPEmptyResponseRetryIsBounded` |
| 200 非法 JSON | decode 错误直接上抛，不伪装成空成功 | `TestHTTPCompletionMalformedBodyPropagates` |
| 重试期间 ctx 被取消 | 立即返回 `context.Canceled`，**不再睡眠退避** | `TestCompleteCancelledContextStopsRetrying` |

## 3. 工具超时与输出上限

| 场景 | 行为 | 证据 |
|------|------|------|
| `terminal_exec` 自身超时 | 杀进程组，~300ms 返回，`timed_out: true` | 既有 `TestTerminalExecTimeoutKillsProcessGroup` |
| shell 先退出、后台子进程持有 stdout 管道 | 超时后按 **启动时的 pgid（= 子进程 pid）** 杀整组：5s 内返回且子进程 ESRCH | 新增 `TestTerminalExecTimeoutKillsBackgroundChildHoldingPipe` |
| ctx 取消（非工具超时） | 杀整组并返回 context 错误；shell pid 消失 | `TestTerminalExecContextCancelKillsProcessGroup` |
| 输出上限 | 超过上限即丢弃多余字节并标记 `output_truncated`，读取端不会 OOM | 既有 `TestTerminalExecTruncatesOutput` |
| `ExecuteTool` 超时 | 取消派生 ctx，执行器退出；单槽池可立即承接下一次调用（无池位/goroutine 泄漏） | `TestExecuteToolTimeoutCancelsExecutorAndFreesPool` |

修复前实测失败：

```
Execute: terminal_exec: command timed out after 300ms and could not be killed
（耗时 5.30s：killProcessGroup 在 shell 已退出时 Getpgid 失败，只 kill 了已死的直接子进程）
```

## 4. 并发

| 场景 | 行为 | 证据 |
|------|------|------|
| 8 个写入者共享同一 `conversations/parts`，并发读者循环读取 | 所有 part 始终是可解析 JSON，数量精确 | `TestConcurrentPartWritersShareOneDirectory`（agentloop）、`TestConcurrentAddPartOnSharedDirectory`（session） |
| 16 个并发 credentials 保存 | index 条目数精确等于写入数（读-改-写已加锁） | `TestConcurrentCredentialSavesKeepIndexEntries` |
| 12 个 goroutine 循环 Subscribe/Unsubscribe，同时 4 个 goroutine Publish | 无死锁、无 race，最终订阅数 0 | `TestConcurrentSubscribeUnsubscribePublish` |
| 处理器在 dispatch 中退订自己 | Publish 不在持锁状态执行 handler，不挂起 | `TestHandlerCanUnsubscribeDuringPublish` |
| 8 个 SSE 客户端连接/断开，同时持续 Publish | 断开后订阅全部释放（`SubscriptionCount()==0`） | `TestGlobalSSESubscriptionsReleasedUnderChurn` |
| 陈旧 cursor + record-message 路由 | 新消息落在 `max(part.seq)+1`，不覆盖旧 part | `TestRecordUserMessageStaleCursorDoesNotOverwrite` |

修复前实测失败：

```
parts = 5, want 6 (a stale cursor overwrote history)
index has 3 entries after 16 concurrent saves: [...]
next_seq = 1 after restoring 4 parts; a stale cursor regressed the sequence
```

## 5. 磁盘满 / 只读目录

| 场景 | 行为 | 证据 |
|------|------|------|
| `atomicfs.WriteFile` 写入只读目录 | 返回带路径的错误；无 `.tmp` 残留 | `TestWriteFileReadOnlyDirectoryReturnsError` |
| 父路径是普通文件（ENOTDIR） | 返回包含失败路径的错误 | `TestWriteFileNonDirectoryParentReturnsError` |
| `FileConversationStore.WritePart` 只读目录（storage / agentloop / session） | 明确错误，旧数据保持可读 | `TestWritePartReadOnlyDirectoryReportsError` ×2、`TestAddPartReadOnlyDirectoryReportsError` |
| credentials index 写入失败（metadata 只读） | `Save` 返回错误，不再“假成功” | `TestCredentialsSaveReportsIndexWriteFailure` |
| checkpoint index 写入失败（index.json 是目录） | `SaveCheckpoint` 返回错误；checkpoint 文件本身仍完整 | `TestCheckpointIndexWriteFailureIsExplicit` |

## 修复的真实缺陷（最小改动）

1. **credentials index 损坏后被当成空**：`readIndex` 把 decode 失败吞成空 envelope，`ListAll` 返回空，下一次 `Save` 只写新条目，抹掉其余索引。改为从 `.enc` 重建（`internal/credentials/storage.go:readIndexLocked`）。
2. **credentials index 无锁读-改-写**：并发 `Save` 丢条目（16 个并发只剩 3 个）。加 `indexMu` 串行化 upsert/remove/rebuild。
3. **credentials index 写失败被吞**：`Save`/`Delete` 返回 nil。现在返回带凭据 ID 的错误；构造时索引重建失败也直接报错。
4. **checkpoint index 损坏返回 nil**：下次保存会覆盖整个索引。改为从 checkpoint 文件重建，并让 `LoadIndex` 加锁（`internal/storage/checkpoint_store.go`）。
5. **`session.ConversationStore.NextSeq` 用 `len(parts)`**：压缩删除或坏 part 被跳过后会复用已存在的 seq（parts 0,1,2,5 → 返回 4）。改为扫描文件名取 `max+1`。
6. **session 派生 JSON 读取过严**：坏 `cursor.json` 让 `ReadCursor` 报错。改为派生状态解码失败按缺失处理（`readDerivedJSON`）。
7. **`RestoreConversation` 盲信 cursor**：`next_seq` 小于磁盘最大 part 时覆盖历史。改为 `max(cursor, maxPartSeq+1)`；先对未过滤的全部 part 求最大值。
8. **`handleRecordUserMessage` 同样盲信 cursor**：路由层改为始终对 part 取最大值兜底（`internal/server/routes_execution.go`）。
9. **`tools.ExecuteTool` 超时不取消执行器**：超时返回后工具仍运行并占住池位（直到其内部超时，最长 600s）。给池 ctx 加 `WithCancel`，超时/父 ctx 结束即取消；新增取消结果与池位释放断言。
10. **`terminal_exec` 进程组杀不死“先退出的 shell + 后台子进程”**：`Getpgid(pid)` 在 leader 退出后失败，退化为只 kill 已死进程；`Wait` 阻塞满 5s 宽限期后返回 “could not be killed”，后台进程泄漏。改为直接 `kill(-pid)`（`Setpgid` 保证 pgid==启动 pid，leader 退出后仍有效）。
11. **LLM 重试在 ctx 取消后仍睡眠退避**：最长 120s 的不可中断睡眠。循环入口与睡眠前检查 `ctx.Err()`，取消时返回 `context.Canceled`。

## 门禁证据

```text
$ make ci
MAKE_CI_EXIT=0
check_parity: OK — 3058 个测试全部带有 parity 注解或豁免说明。
parity refs checked: 2636  missing files: 0  missing tests: 0  shorthand/prose: 134
check_parity_targets: OK

$ go test -race -count=1 ./...
RACE_EXIT=0
FAIL_LINES=0
OK_LINES=37
```

## 残余风险 / 边界

- **ENOSPC 无法在 macOS 单测中可移植注入**：没有 `/dev/full`，挂载小容量镜像需要特权。已通过同一条 `tmp 创建 → write → fsync → rename` 路径上的 EACCES / ENOTDIR 失败验证“错误不静默、无临时文件残留”。建议发布前在 Linux CI 用 tmpfs/quota 补一条 ENOSPC 烟测。
- **共享 part 目录的“同 seq 并发写”是 last-writer-wins**：原子 rename 保证文件永不半截，但不同会话共享目录时仍可能逻辑覆盖。已消除“陈旧 cursor 主动复用 seq”的路径；若未来需要跨进程唯一序列，应引入文件锁或 O_EXCL 序列分配器。
- **summary 写入仍是 best-effort**（`storage.writeSummary` 忽略错误）：它是可重建缓存，读路径会在 stale 时 `RebuildSummary`。未改为返回错误以避免扩大 API 变更。
- **Windows 无进程组**：`terminal_exec` 在 Windows 上只杀直接进程（既有注释已声明），本次修复仅覆盖 unix。
- 只读目录测试在 root 下自动 skip（root 绕过权限位）。

## 运行记录

- 新增/相关包 `-race`：
  `go test -race ./internal/atomicfs ./internal/credentials ./internal/session ./internal/storage ./internal/agentloop ./internal/llm ./internal/tools ./internal/tools/builtin ./internal/eventbus ./internal/server` → 10 个包全部 `ok`
- 全量 `go test -race ./...` → `RACE_EXIT=0`，`FAIL` 行 0，`ok` 行 37
- `make ci`（fmt-check + vet + test + cross + parity + parity-targets）→ `MAKE_CI_EXIT=0`
