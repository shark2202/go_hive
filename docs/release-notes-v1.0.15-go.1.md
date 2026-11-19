# 发布说明：hive 1.0.15-go.1

> 发布包版本：`hive 1.0.15-go.1`（Go 模块 `github.com/aden-hive/hive-go`）
> 对应上游：`aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）
> 构建日期：2026-11-19
> 构建环境：macOS 26.7.1（darwin/arm64）、Go 1.26.5、Node 24.21.0 / npm 11.19.0、`CGO_ENABLED=0`

这是 `1.0.14-go.1` 之后的行为对齐版本。发布包仍是**单二进制内嵌 dashboard**：解压即用，不需要 Node、Python 或外部静态资源目录。

## 1. 相对 1.0.14-go.1 的变化

**会话运行时**

- **SSE 事件链补全**：`ChatRuntime` 发布 `execution_started/completed/failed`、`loop_state_changed`、`node_loop_started/iteration/completed`、`client_output_delta`、`llm_turn_complete`、`tool_call_started/completed`；订阅首帧带 `session_snapshot`。此前模型已回复但 UI 永久停在 `Working…`。
- **queen 人格注入**：`serve` 从 `$HIVE_HOME/queens/<id>/profile.yaml` 读取人格并注入系统提示词（`FormatQueenIdentityPrompt`，`max_examples=0`），缺失时回退通用身份提示。
- **`ask_user` 停车**：模型调用 `ask_user` 后会话进入 `awaiting_user`，发 `client_input_requested`；用户回答后恢复同一轮。实测 `park_reason=ask_user`、`/events` 200。
- **queen 记忆**：每轮用户消息后注入 global + queen 两块 `<system-reminder>` recall，内容不变则跳过；两个 queen 互不可见；turn 成功后按触发器跑 reflection。
- **`queen_resume_from`**：按上游 `session_manager.py` 语义（`resolved_session_id = queen_resume_from or session_id`）复用旧会话 id 与目录，服务重启后旧 id 不再 404，前端重试风暴消除。
- **任务工具**：`task_create / task_update / task_list / task_get` 进入会话工具表，按会话目录隔离。

**colony 与编排**

- colony 生命周期接线：`serve` 注入 `CreateColonyFromSource`，`Host.StartOverseer` 有生产入口；colony 不存在 404、启动失败 5xx，不再返回「有 id 没 runtime」的会话。
- 会话停止时同时停止 colony 宿主（`StopColonyRuntime`），overseer 与 worker 不再泄漏到进程退出。
- judge：`agentloop.Judge` 接口、`SuccessCriteria` 触发、`ACCEPT/RETRY/ESCALATE` 与 verdict 事件（12 测试）。当前 chat 会话无成功标准，生产触发点尚未接线。

**安全**

- 附件读写 symlink 逃逸：读取先 `EvalSymlinks` 解析并校验落在 `HIVE_HOME` 内；写入以 queen 目录为锚 `os.OpenRoot` 逐段校验，拒绝时只回通用错误、不回显路径。
- `/api` 点段守卫：含 `..` 段（含 `%2e` 编码）的 API 路径在进入 `ServeMux` 前一律 400。此前 Go 的 mux 会先清洗路径并 307 跳到非 API URL，使路由自身的穿越校验失效。
- 任务请求体限流：`http.MaxBytesReader` 并读满 body，超限 413。
- 安全回归测试从 6 条扩到 9 条，3 条原先 `t.Skip` 的用例改为硬断言，并新增 1 条「会话树内 symlink 父目录仍可读」的正向用例。

**可观测与诊断**

- `GET /api/sessions/{id}/stats` 对齐上游：7 个顶层键 + `event_bus.events_by_type` 展开（递归 13 + N），colony 统计逐字段覆盖且不丢额外键。
- `/api/*` 未注册路径返回 JSON 404，不再回退 `index.html`。

**质量门禁**

- 门禁从六道扩到七道：`fmt-check`、`vet`、`test`、`cross`、`parity`、`parity-targets`、`wiring-check`。
- 新增架构分层、单文件大小、接线三类检查；`loader.go` 与 `ExecuteTurn` 以「冻结上限 + 理由」方式登记，只减不增。
- 性能基线：1000 会话建列 55.5ms、单轮 chat 49.3ms。

## 2. 质量证据

| 项 | 结果 |
| --- | --- |
| 测试声明数（parity 口径） | **4,256** |
| `check_parity.sh` | **4256/4256** 有注解，0 missing |
| `check_parity_targets.sh` | **4,489** 条引用（3,591 test + 898 prod），0 missing |
| 七道门禁 | `fmt-check` / `vet` / `test` / `cross` / `parity` / `parity-targets` / `wiring-check` 全部退出码 0 |
| 实机冒烟 | `/`、`/api/health`、静态资源、`/events`、`ask_user` 停车、resume 同 id、`/stats` 字段、附件穿越 400、9MiB 任务体 413 |

## 3. 已知差异（不隐藏）

- **token 级流式未实现**：`internal/llm` 只有整段返回，没有流式接口。Python 版 SSE 逐帧输出，本版为单帧整段。用切块伪造流式属于造假，不做。
- **fan-out worker 回报未接**：`run_worker` 与 subagent 事件订阅尚未接入编排。
- **judge 未在 chat 触发**：接口与测试齐备，但 chat 会话没有 `SuccessCriteria` 来源。
- **第三方集成未验证**：发布阶段只提供大模型凭据。81 个需凭据模块在支持矩阵中标「未验证」，26 个免凭据模块有离线验证。
- **在线漏洞库未比对**：`proxy.golang.org` 不可达，`govulncheck` 无法安装；仅做人工审查与离线构建验证。
- **行为对齐长尾**：71 条核心机制中 14 条已接、7 条部分接、50 条未接（reminders/playbook/skills/sentinel 等）。

## 4. 安装与校验

```bash
tar -xzf hive-1.0.15-go.1-darwin-arm64.tar.gz
cd hive-1.0.15-go.1-darwin-arm64
./hive --version        # hive 1.0.15-go.1
HIVE_HOME=~/.hive-go ./hive serve --host 127.0.0.1 --port 8787
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/api/health
```

`SHA256SUMS` 覆盖 6 个平台归档，校验：

```bash
shasum -a 256 -c SHA256SUMS
```

## 5. 升级说明

从 `1.0.14-go.1` 升级无需迁移：会话目录、凭据库、`configuration.json` 结构未变。旧会话在重启后可用 `queen_resume_from` 直接恢复；未带该参数的旧 URL 仍会新建会话（与上游一致）。
