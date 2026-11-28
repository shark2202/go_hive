# server/streamtoken_refresh.py 归属决策（云端 streamToken 轮换）

> 对象：`core/framework/server/streamtoken_refresh.py`（419 行）——运行时自轮换 `hive` streamToken 的后台任务。
> 问题：Go 端口是否要实现它（实现），还是登记为范围外（out_of_scope）？
> 结论：**ported**（w168 车道重开并实现）——调度/过期/重试引擎在 `internal/streamtoken`，serve 装配在 `internal/server/streamtoken.go`，LLM 调用侧旁路在 `internal/llm/streamtoken.go`；原 `out_of_scope(cloud)` 裁定保留为决策历史（§1-§3、§4）。
> 基线：main `52672d1c`（audit-docs 车道，2026-11-22）裁定 out_of_scope；w168 车道（2026-11-25）重开并移植，数字均来自 §附录 实测命令。

## 1. 现状（Python 侧）

模块自述（`streamtoken_refresh.py:1-50`）：桌面客户端原本独占 streamToken 刷新（`hive-desktop/src/main/cloud.ts::refreshStreamToken`，经 `POST /user/refresh-stream-token` + `configureRemoteLlm` 推入 VM）；一旦桌面离线，VM 内 `hive` token 24h 内过期，所有 LLM 调用收到 `hive_stream_token_invalid`。本模块让运行时在 VM 内自刷新，走云后端 `POST /user/refresh-stream-token-from-stream`（streamToken 签名本身即身份，无需用户 JWT；过期后拒绝刷新，撤销仍靠 sid 黑名单/master-secret 轮换）。

关键常量与行为：

| 项 | 值 | 出处 |
| --- | --- | --- |
| 凭据 | `CREDENTIAL_ID = "hive"`，`KEY_NAME = "api_key"` | `streamtoken_refresh.py:69-70` |
| 刷新阈值 | `REFRESH_BEFORE_SECONDS = 7200`（2h，对齐桌面 `STREAM_REFRESH_THRESHOLD_MS`） | `:75` |
| 睡眠边界 | `MIN_SLEEP_SECONDS = 60` / `MAX_SLEEP_SECONDS = 3600` | `:82-83` |
| 云后端端点 | `POST {HIVE_CLOUD_BASE}/user/refresh-stream-token-from-stream` | `:153-183`（URL 组装 `:166`） |
| 运行门控 | 未设置 `HIVE_CLOUD_BASE` → 直接 return（"runtime not in cloud mode"） | `:243-246` |
| 写回 | `save_credential` 覆盖同一 `hive` 凭据（缓存失效内建） | `:211-223`、`:291` |
| 强制刷新 hook | `app["streamtoken_force_refresh"]`（幂等唤醒循环） | `:407-413` |

## 2. 调用链（Python，复核用的 file:line）

1. **启动注册**：`core/framework/server/app.py:1069-1072` —— `app.on_startup.append(start_streamtoken_refresh)`；同处注释写明「No-ops when HIVE_CLOUD_BASE isn't set」。
2. **后台循环**：`start_streamtoken_refresh:401` → `_refresh_loop:226`：
   - 读 `hive` 凭据（`:261`，缺失则短睡等待桌面首次推送）；
   - 解 JWT `exp`（`decode_jwt_exp:96`）→ 算下次睡眠（`compute_sleep_seconds:121`）；
   - 到点 `post_refresh`（`:153`）→ 成功写回凭据库（`:291`）；失败仅 warn，下一 tick 重试（`:305-307`）。
3. **失败旁路（LLM 调用侧）**：`core/framework/llm/litellm.py:2001-2021` —— 收到 `hive_stream_token_invalid` 时 `try_refresh_now()`（`:2008`，定义在 `streamtoken_refresh.py:351`），成功则带新 token 重试同一调用一次（`:2020-2021`），失败上抛原错误。
4. **测试面**：`core/tests/test_streamtoken_refresh.py`（331 行）——sleep 边界、端点 URL、循环、force hook 的假时钟/假端点用例。

## 3. Go 侧等价检查

- `grep -rn 'streamtoken\|streamToken\|refresh-stream-token' go_hive/go-hive --include='*.go' | wc -l` → **0**（含测试文件）。
- Go 的云端面只到「env 消费」一层：

| 面 | Go 证据（file:line） | 说明 |
| --- | --- | --- |
| cloud provider 显隐 | `internal/llm/catalog.go:525-529` | `managedCloudProviders{"hive"}`；`hideManagedProviders()` 只看 `HIVE_CLOUD_BASE` 是否为空——目录门控，无 token 轮换 |
| `hive` provider 凭据 | `internal/server/llm_config.go:38/65` | `{"hive", "HIVE_API_KEY"}`；key 来自 env/凭据库 |
| `hive` key 检查 | `internal/llm/keycheck.go:519/564` | 用 hive 端点做 keycheck |
| sentinel `hive` 渠道 | `internal/sentinel/notifier.go:125-133` | `HiveBackendBase()`/`HiveJWT()` 直接读 env；未登录即未连接 |
| global DB | `internal/globaldb/client.go:45` | 无 cloud session 时 `NotSignedInError` |

即：Go 把云凭据当**外部输入**（env 或 `POST /api/credentials` 推送），没有云登录/续期生命周期，也没有 refresh endpoint 客户端——正是 Python 模块诞生前的那套模型（模块头 `:7-16` 所述 desktop-owned 模型）。

## 4. 结论：ported（w168 车道重开并实现）

原裁定为 `out_of_scope(cloud)`（§1-§3 保留为决策历史）。w168 车道重开并移植，重开理由：

1. 上节重开条件之一的宿主问题已不成立——Go serve 进程现在**自己持有**轮换挂靠点（启动装配 + LLM 调用侧 seam），不再需要桌面端推 token 才能续期；
2. C12 要求「未移植模块必须有结论」，而 token 轮换的**调度/过期/重试语义**（阈值、睡眠边界、失败不覆盖凭据、强制刷新）是可测试的纯逻辑，与「云登录态是否由 Go 拥有」解耦，按 unported 排期不合理；
3. 实现不引入云登录生命周期：仍只消费 `hive` 凭据库里的 streamToken，仅在设置 `HIVE_CLOUD_BASE` 时启动（未设置直接 no-op，与 Python 的 early return 一致）。

### 4.1 实现映射（Python -> Go）

| Python 面 | Go 面 | 说明 |
| --- | --- | --- |
| `decode_jwt_exp` (:96) | `internal/streamtoken/token.go` `DecodeJWTExp` | 手动 base64url + `json.Number`，不验签 |
| `compute_sleep_seconds` (:121) | `internal/streamtoken/token.go` `Policy.SleepSeconds` | 阈值 7200s，睡眠 `[60, 3600]`，无 exp 睡 min |
| `post_refresh` (:153) | `internal/streamtoken/client.go` `Client.Refresh` | `POST {base}/user/refresh-stream-token-from-stream`，body `{"streamToken": ...}`，200 + 非空 `streamToken` 才算成功，15s 超时 |
| `_refresh_loop` (:226) | `internal/streamtoken/refresher.go` `Refresher.Run` / `tick` | 读凭据 → 算睡眠 → 醒后复读（别人已刷新则跳过）→ 强制标记 → 刷新 → 写回；失败只记日志，下一 tick 重试；`MIN_SLEEP` 兜底防忙等 |
| `_sleep_with_force` (:310) | `Refresher.sleepWithForce` | 先快照 wake channel 再查强制标记，避免 set/clear 竞态 |
| `try_refresh_now` (:351) | `Refresher.RefreshNow` | 串行锁 + 新鲜度复检：并发调用只产生一次 POST |
| `_force_refresh` (:407) | `Refresher.ForceRefresh` | 幂等唤醒（多次调用折叠为一次强制 tick） |
| `start_streamtoken_refresh` (:401) | `internal/server/streamtoken.go` `StartStreamTokenRefresh` | `HIVE_CLOUD_BASE` 门控、凭据库装配、llm seam 注册、stop 取消并等待 |
| 启动注册 `app.py:1072` | `internal/cli/loader_background_services.go:36` | 与 turn-context/bridge/soul 同批启动 |
| 调用侧旁路 `litellm.py:2008` | `internal/llm/streamtoken.go` + `stream_openai.go` / `stream_anthropic.go` / `complete.go` | 401 且 body 含 `hive_stream_token_invalid` 时刷新一次并用新 token 重放同一调用；其它 401/403 保持终态 |

### 4.2 有意保留的 Go 差异（可靠性）

| 差异 | 原因 |
| --- | --- |
| 循环用 `context.Context` 取消，而非 `asyncio.CancelledError` | Go 生命周期惯例；stop 函数取消并等待 goroutine 退出，测试断言无泄漏 |
| 凭据库以 `WithCacheTTL(0)` 构建（每次 tick 直读加密文件） | Go 的 credential 路由按请求各建一个 store 实例，桌面经 `POST /api/credentials` 推送的 token 不会失效本循环的缓存；关缓存后下一 tick 即可见（Python 共享单 store，`save_credential` 自带失效）。每分钟一次文件读可接受 |
| 强制刷新用 mutex + `force` 标记代替 `asyncio.Event` | 语义等价（幂等 set / 醒后 clear），且能在任意 goroutine 调用 |

### 4.3 验收对照

Python 测试面（`core/tests/test_streamtoken_refresh.py`）逐条对应到 `internal/streamtoken/*_test.go` 与 `internal/server`、`internal/llm` 的 seam 测试；额外补了参考模块缺少的循环级用例（过期 token 仍刷新、瞬时失败下一 tick 重试、永久失败不覆盖凭据、取消、强制唤醒、负向回归：200 缺 token 不写库 / 新鲜 token 不 POST / 非 streamToken 401 不触发刷新）。

## 5. registry 登记同步

`contracts/module-inventory.json` 对应条目已更新为 `ported`（2026-11-25，w168 车道）：

```json
{
  "python_path": "core/framework/server/streamtoken_refresh.py",
  "domain": "server",
  "status": "ported",
  "kind": "parity",
  "go_counterpart": "go_hive/go-hive/internal/streamtoken/refresher.go",
  "go_counterparts": ["go_hive/go-hive/internal/streamtoken/refresher.go", "go_hive/go-hive/internal/streamtoken/token.go", "go_hive/go-hive/internal/streamtoken/client.go", "go_hive/go-hive/internal/server/streamtoken.go", "go_hive/go-hive/internal/llm/streamtoken.go", "go_hive/go-hive/internal/cli/loader_background_services.go"],
  "basis": "python call chain: core/framework/server/app.py:1072 -> core/framework/server/streamtoken_refresh.py:226; try_refresh_now:351 <- core/framework/llm/litellm.py:2008; go: go_hive/go-hive/internal/streamtoken/refresher.go:111 / :218, serve 装配 go_hive/go-hive/internal/server/streamtoken.go:52, seam go_hive/go-hive/internal/llm/streamtoken.go:33",
  "evidence": "go_hive/go-hive/internal/streamtoken/refresher_test.go（含 server/llm seam 测试）",
  "note": "Go 可靠性差异见 §4.2"
}
```

脚本 `CURATED` 同步：`scripts/gen_module_inventory.py` 的该条目已改为 `ported`，并让重建时保留 `go_counterparts`（否则 registry 会丢多文件清单）。

## 附录：实测命令与输出


命令均在仓库根执行（`cd <repo root>`；本工作树为 `.wt/audit-docs`）。

```text
# B1 调用点（Python；去测试与注释后）
$ grep -rn 'start_streamtoken_refresh\|try_refresh_now' core/framework --include='*.py' | grep -v '/tests/' | grep -v '#'
core/framework/llm/litellm.py:2005:                                try_refresh_now,
core/framework/llm/litellm.py:2008:                            new_token = await try_refresh_now()
core/framework/llm/litellm.py:2011:                                "[hive-auth] try_refresh_now import/call failed",
core/framework/server/streamtoken_refresh.py:351:async def try_refresh_now() -> str | None:
core/framework/server/streamtoken_refresh.py:401:async def start_streamtoken_refresh(app: Any) -> None:
core/framework/server/app.py:1070:    from framework.server.streamtoken_refresh import start_streamtoken_refresh
core/framework/server/app.py:1072:    app.on_startup.append(start_streamtoken_refresh)

# B2 Go 侧 streamToken 实现（含测试）
$ grep -rn 'streamtoken\|streamToken\|refresh-stream-token' go_hive/go-hive --include='*.go' | wc -l
0

# B3 Go 的云端面（env 消费点）
$ grep -rn 'HIVE_CLOUD_BASE' go_hive/go-hive --include='*.go' | grep -v _test
go_hive/go-hive/internal/llm/catalog.go:528:	return strings.TrimSpace(os.Getenv("HIVE_CLOUD_BASE")) == ""
go_hive/go-hive/internal/sentinel/notifier.go:31:	// cloud JWT (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE).
go_hive/go-hive/internal/sentinel/notifier.go:125:	return strings.TrimSpace(n.getenv("HIVE_CLOUD_BASE"))
go_hive/go-hive/internal/globaldb/client.go:19:	EnvBase = "HIVE_CLOUD_BASE"
go_hive/go-hive/internal/globaldb/client.go:45:		return nil, &NotSignedInError{Message: "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."}

# B4 门禁（登记后）
$ uv run --no-project python scripts/check_module_inventory.py --check
module-inventory: total=271 registered=271 unregistered=0 ported=186 partial=10 out_of_scope=7 unported=68
verdict=PASS
```

核对日期：2026-11-22（CST）；基线 main `52672d1c`。

## 附录 B：w168 移植后实测（2026-11-25，工作树 `.wt/w168-streamtoken`）

```text
# W1 Go 实现面（非测试）
$ grep -rln 'streamtoken\|streamToken' go_hive/go-hive --include='*.go' | grep -v _test
go_hive/go-hive/internal/streamtoken/token.go
go_hive/go-hive/internal/streamtoken/client.go
go_hive/go-hive/internal/streamtoken/refresher.go
go_hive/go-hive/internal/server/streamtoken.go
go_hive/go-hive/internal/llm/streamtoken.go
go_hive/go-hive/internal/cli/loader_background_services.go
（另：internal/llm/stream_openai.go、stream_anthropic.go、complete.go 各加一处 401 旁路）

# W2 包测试（CGO_ENABLED=0）
$ CGO_ENABLED=0 go test ./internal/streamtoken ./internal/server ./internal/llm -count=1
ok  github.com/shark2202/go_hive/go-hive/internal/streamtoken
ok  github.com/shark2202/go_hive/go-hive/internal/server
ok  github.com/shark2202/go_hive/go-hive/internal/llm

# W3 门禁
$ make parity                     # 全部 TestXxx 带 // Parity: 或 // Parity-exempt:
$ gofmt -l .                      # 无输出
$ go vet ./internal/streamtoken ./internal/server ./internal/llm
$ uv run --no-project python scripts/check_module_inventory.py --check
module-inventory: total=273 registered=273 unregistered=0 ported=228 partial=27 out_of_scope=8 unported=10
verdict=PASS
```

核对日期：2026-11-25（CST）；基线 `4b90c9e08`。
