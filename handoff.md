# Handoff — 2026-11-19 10:10（重启前存档）

> 目的：机器卡顿需重启。本文件记录当前可验证状态、未完成工作、恢复步骤与已知缺陷，重启后照此续跑。
> 主仓库 `main` HEAD：`26a9909b`（merge w13/chatevents）。工作树除台账外干净。

## 1. 一句话现状

Go 版 hive 已能**用真模型跑通对话**（UI 与 API 都验证过），工具/凭据/事件链已接通；但 `docs/key_concepts` 里「自我改进」类概念（reflexion、playbook、reminder hub、goal judge、learned skills）**仍未接进运行时**；与 Python 版的逐端点 A/B 对比尚未完成。

## 2. 已验证可用（有实测证据）

| 能力 | 证据 |
| --- | --- |
| 启动 + 单二进制 UI | `HIVE_HOME=~/.hive-go ./dist/hive-embed serve`，`/`、`/api/health` 200 |
| LLM 配置生效 | `GET /api/config/llm` → `model=deepseek-flash-1017`、`api_base=https://token.tokenbroad.com/v1`、`has_api_key=true` |
| 真实对话 | 会话 `parts/0000000001.json` 落盘模型回复；UI 显示用户消息 + queen + 回复，`Working…` 消失 |
| 事件链 | `/events/history` 返回 10 条：`loop_state_changed → node_loop_started → node_loop_iteration → client_output_delta → llm_turn_complete → node_loop_completed → loop_state_changed → execution_completed`；`events.jsonl` 已生成 |
| 凭据目录 | `/api/credentials/specs` 123 条（118 上游 + 4 模型 + 1 Aden），113 条带 `tool_modules`；UI 显示 `0 active · 314 available` |
| 凭据保存即生效 | 保存后 `env sync injected/refreshed`，无需重启 |
| 门禁 | `fmt-check/vet/test/cross/parity` 全 0；测试 **4,183**；parity refs **4,396**，0 missing |
| 发布包 | `dist/release/` 7 个文件（6 归档 + SHA256SUMS），tag `v1.0.25-go.1`（当前）；`v1.0.15-go.1` 为上一快照 |

## 3. 本次会话修复的缺陷（已合并）

1. **config 路由零值 Home** → `/api/config/llm` 返回空、UI 报 `No AI model yet`。`NewApp` 已回退到 app home（`app.go`）。
2. **凭据库不可用** → `HIVE_CREDENTIAL_KEY` 未设时保存返回 503，且每次构造 store 随机生成新 key。`start.sh` 现在生成并持久化到 `~/.hive-go/secrets/credential_key`（600）。
3. **chat 不发事件** → UI 永远 `Working…`。`w13/chatevents` 已合并：`LoopObserver` + `ChatRuntime` 发射器 + `events.jsonl`。
4. **start.sh 漏 `export HIVE_HOME`** → 配置不生效。已修。
5. **`.gitignore` 的 `data/` 误伤 `frontend/src/data`** → 已锚定为 `/data/`。

## 4. 未完成 / 已知缺陷

### P0
- **旧 session URL 重试风暴**：服务重启后访问 `?session=<旧 id>`，`/events` 404 → 前端反复 `POST /api/sessions`（一秒上百次），堆出上百个内存空会话，`GET /api/sessions` 涨到 13–14 秒。需要 Python 版对照定性（上游 `resolve_session` 同样 404，差异可能在前端重试策略）。**临时止血：把标签页导航到 `about:blank` 并重启服务清空内存会话。**
- **`/api/*` 未注册路径落入 SPA fallback**，返回 200 + index.html，会把缺路由误判为成功。

### P1（概念未接线，见 `docs/key_concepts`）
- reflexion：`internal/memory/reflection.go` 的 `ReflectionLoop`/`RunShortReflection`/`RunLongReflection` 零调用
- playbook：`internal/playbook` 零调用
- reminder hub：`internal/tasks/reminders.go` 零调用
- goal judge：`SuccessCriteria` 只解析进 agent_spec/worker_definition，`llmjudge` 只在 cli debugger 用
- learned skills：会话工具表里没有 skill 工具
- queen memory：`internal/memory/QueenMemoryDir` 只有端点用到，chat runtime 未注入（**w15 已提交首版 `cf531ef9`，待合并与验证**）

### P2
- 真实第三方 API 一次未调用（863 action 只有假上游）；R10 按用户决定本阶段不验证，以支持矩阵声明代替
- Linux/Windows 只交叉编译未执行；无签名/公证；release workflow 未真跑
- `attach_file` 多模态只到工具结果层

## 5. 进行中的 lane（分支已保存，未合并）

| 分支 | 提交 | 内容 | 下一步 |
| --- | --- | --- | --- |
| `w14/api-parity` | `b7404b97` | `go_hive/scripts/api_parity.py` + `API_PARITY_REPORT.md` 草稿 | 跑完对比、出差异结论（含风暴场景） |
| `w15/queen-memory` | `cf531ef9` | 每轮注入 queen 作用域 recall（global + queen 两块、变化才注入、turn 后触发 reflection）；worktree 内门禁已绿：fmt/vet/test/cross/parity 全 0，parity refs 4,383 / 0 missing | **只需合并**，再验证两个 queen 互相不可见 |
| `w16/concept-audit` | 无提交 | 概念接线审计 | 重新派，产出 `CONCEPT_WIRING_AUDIT.md` |

> 合并前先数提交：`git log --oneline main..<branch> | wc -l`，为 0 就是没提交（已踩两次）。

## 6. 重启后恢复步骤

```bash
# 1) 门禁（拆开跑，整跑 parity-targets 会超时）
cd /Users/mac/hive/go_hive/go-hive
for t in fmt-check vet test cross parity; do CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod make $t; echo "$t=$?"; done
bash ../scripts/check_parity_targets.sh . ../../core

# 2) 起服务（可选：不重建 UI 也能跑，dist/hive-embed 已存在）
/Users/mac/.hive-go/start.sh          # 前台；或放到后台终端
# 浏览器：http://127.0.0.1:8788

# 3) 治理/看门狗（重启后需重新拉起；report 带异常告警 + macOS 通知）
cd /Users/mac/hive
nohup bash go_hive/scripts/watchdog_loop.sh 240 >/dev/null 2>&1 &
nohup bash go_hive/scripts/watchdog_report.sh 300 >/dev/null 2>&1 &
nohup bash go_hive/scripts/timegov_record.sh 300 >/dev/null 2>&1 &
```

## 7. 配置与凭据（不含密钥）

- 运行 home：`~/.hive-go`
- 配置：`~/.hive-go/configuration.json`
  ```json
  {"llm":{"provider":"openai","model":"deepseek-flash-1017",
          "api_base":"https://token.tokenbroad.com/v1","api_key_env_var":"BROADTOKEN_API_KEY"}}
  ```
- 密钥文件：`~/.hive-go/env`（`export BROADTOKEN_API_KEY=...`，600）
- 凭据库密钥：`~/.hive-go/secrets/credential_key`（600，自动生成）
- 启动脚本：`~/.hive-go/start.sh`

## 8. 独立仓库拆分（进行中）

- 暂存位置：`/Users/mac/hive/.standalone/go_hive`，**281 个提交**，用 `git subtree split --prefix=go_hive` 提取历史
- 已在该仓库验证：`make ci` 退出码 0；`check_parity_targets.sh` 支持 `HIVE_UPSTREAM_ROOT`
- 未做的**原子切换**（等所有 lane 收口）：父仓库 `git rm -r --cached go_hive` + `.gitignore` 加 `go_hive/`，把独立仓库落到 `/Users/mac/hive/go_hive`
- 无远端可推（离线）

## 9. 重启前提醒

- 本机所有后台进程会终止：serve（pid 95575）、watchdog（45538）、timegov（45898）、w14 自己的 58079 服务（45513）
- 内存里的上百个空会话会随重启消失，`/api/sessions` 会恢复毫秒级
- 工作树 `.wt/*` 大量历史 worktree 仍在；如需清理：`git worktree prune`（先确认没有 lane 在用）
- 未提交内容已全部落库：`w14` → `b7404b97`，`w15` → `cf531ef9`，主树仅台账 `TIME_EVENTS.csv`
