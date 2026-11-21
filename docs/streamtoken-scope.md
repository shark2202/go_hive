# server/streamtoken_refresh.py 归属决策（云端 streamToken 轮换）

> 对象：`core/framework/server/streamtoken_refresh.py`（419 行）——运行时自轮换 `hive` streamToken 的后台任务。
> 问题：Go 端口是否要实现它（实现），还是登记为范围外（out_of_scope）？
> 结论：**out_of_scope(cloud)** —— 不移植；该模块属于云端登录态/凭据生命周期宿主，Go 端口只消费外部提供的云凭据。
> 基线：main `52672d1c`（audit-docs 车道，2026-11-22）；数字均来自 §附录 实测命令。

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

## 4. 结论：out_of_scope(cloud)

对 C12 的 out_of_scope 口径（Python 调用链锚点 + Go 锚点证明端口停在该宿主之前）逐条成立：

1. 该任务只在**云模式**存在（`HIVE_CLOUD_BASE` 门控，未设置直接 return）；
2. 它操作的是**云签发**的 `hive` streamToken（24h TTL、`/user/refresh-stream-token-from-stream` 端点、sid 黑名单/master-secret 撤销都是云端契约）；
3. Go 端口没有云登录/会话生命周期可挂靠（§3 表：只有 env 门控与 JWT 消费）；桌面宿主部署时 token 仍由外部推入（`POST /api/credentials`）；
4. 所以它不是「排期缺口」（`unported`），而是**宿主边界**——与 graph 执行引擎同类，登记为 `out_of_scope`。

**何时重开**（避免决策僵化；满足任一条件应把它提回 backlog 的实现车道）：

- Go 需要在**无桌面**环境长期（>24h）以 `HIVE_CLOUD_BASE` 云模式无人值守运行；或
- Go 开始自己拥有云登录/JWT 生命周期（届时才有挂靠点）。

重开后的验收（对齐 Python 行为）：同阈值 `exp-7200s`、同睡眠边界 `[60, 3600]`、`hive`/`api_key` 写回、`hive_stream_token_invalid` 强制刷新并重试一次、失败不崩循环；测试用假端点 + 假时钟（用例面参考 `core/tests/test_streamtoken_refresh.py`）。

## 5. registry 登记同步

`contracts/module-inventory.json` 对应条目已同步（2026-11-22）：

```json
{
  "python_path": "core/framework/server/streamtoken_refresh.py",
  "domain": "server",
  "status": "out_of_scope",
  "kind": "call_chain",
  "go_counterpart": null,
  "go_counterparts": [],
  "basis": "python call chain: core/framework/server/app.py:1072 (...) -> core/framework/llm/litellm.py:2001 (...); go_hive/go-hive/internal/llm/catalog.go:525 // ... ; go_hive/go-hive/internal/sentinel/notifier.go:129 (...)",
  "evidence": "n/a (cloud host not ported; 归属决策与调用链见 go_hive/docs/streamtoken-scope.md)",
  "note": "Go 无 streamToken/refresh-stream-token 实现（grep 0 命中，含测试）；云端凭据由 env/凭据库外部提供，轮换属云登录态宿主"
}
```

（上为节选；完整 `basis` 以 registry 为准。）

**维护注意**：`contracts/module-inventory.json` 由 `scripts/gen_module_inventory.py` 重建，人工边界写在脚本的 `CURATED` 表。本次按 lane 的允许修改清单只更新了 JSON 条目；若下次重建 registry，需把同一条目补进 `CURATED`，否则会回退为 scan 的 `unported`。

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
