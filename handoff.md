# Handoff — 2026-11-20 22:50（wave5 合并后）

> 用户已恢复网络并继续推进。本文件记录可验证状态、未完成工作、恢复步骤与已知缺口。
> 主仓库 `main` HEAD：`33cbcc91`；本版 handoff 提交紧随其后。
> **网络提醒**：本地 UI/REST/bridge 可用；聊天/Claude Code/LongMemEval 需要外网或本地模型网关；本机到 HuggingFace/OpenAI 当前超时。

## 1. 一句话现状

Wave 3A、Wave 3B、Wave 4 与 wave5（冷会话事件 / notify 注入 + 面板路由 / 技能 live reload / 附件多模态 / soul reflect）均已合并；main 全量 CI **4,937 测试 / 5,209 refs / 0 missing**，QC **8/8**（surface audit Python 缺失 0）。LongMemEval 真实数据集/judge、Windows 实机、81 个凭据集成未验证，因此**仍不能宣布全部对齐/可发布完成**。

## 2. 当前实测状态

| 项 | 值 |
| --- | --- |
| main HEAD | `33cbcc91` |
| 今日提交 | 137 |
| 工作树 | 20 个 |
| 默认会话工具 | 137 |
| 全量 CI | 4,937 测试；5,209 refs；missing 0 |
| QC | 8/8 PASS（4,937 时点；C4c 四二进制） |
| serve | `127.0.0.1:8788`，health 200 |
| browser bridge | `127.0.0.1:14830` health 200；14829 WS、14831 control RPC；`control_rpc_clients=1` |
| 当前进程 | 由 `/Users/mac/.hive-go/start.sh` 启动（pid 随重启变化） |
| 看门狗 | `watchdog_report.sh 300`（2026-11-21 00:0x 重启；PID 以 `pgrep -fl watchdog_report.sh` 为准） |
| 时间台账 | `timegov_record.sh` pid 94972 |

## 3. 本轮已合并并验证

| 能力 | 合并提交 | 证据 |
| --- | --- | --- |
| chart_render（ECharts/Mermaid → PNG） | `9a753c77` | 真实 Chrome PNG：`go_hive/docs/e2e-assets/chart-render-{echarts-bar-line,mermaid-flowchart,dark-theme}.png`；我视觉复核通过 |
| file_ops 残差（hashline / stale guard / write_safe_root） | `7c8d0aff` | 23 个新测试；目标包测试通过 |
| harness 五组 + csv/excel 同名工具 + browser_setup | `690c8735` | 组目录/别名/开关测试；chart 冲突已手工解析 |
| hive-browser CLI + serve 自动拉起 bridge | `07691cd0` | 我独立重跑 `e2e_browser_host.sh`：PASS；serve 退出时 bridge 子进程被回收 |
| 真实 `/api/claude-code-queens` + transcript | `3962d587` | 空配置 200 `{"queens":[]}`；lane 用临时配置验证非空列表/transcript/404 |
| Soul 每日读聊天反思 | `c5858389` | state/cursor/证据/max_edits/review-auto/重试；soul/server/cli/quality 测试通过 |
| memory 双跑（timeline/past recall/recall） | `d592c154` | 与 Python 双跑 PASS；stop flush、past recall twice 已修；报告 `docs/memory-e2e.md` |
| LongMemEval harness | `50d54519` | fixture + 脚本化管线 PASS；真实数据集/judge 未验证，无分数 |
| 发布归档四二进制 | `7cb9c40e` | 我独立重建六平台归档并跑 `verify_release_archive.sh`：13/13 PASS；QC C4c 四二进制 |
| Browser control RPC 14831 | `5734c503` | 46 方法白名单 + client_hello/notify + 30s 空闲退出；真机 e2e PASS；live `control_rpc_clients=1` |
| 冷会话 5 条开幕事件 | `6e1bac36` | 实测新建会话 events.jsonl 5 行（identity/reminder/node_loop_started/iteration/context_usage）；首轮不重复 |
| 技能 live reload 扇出 | `f105d7e3` | 实测新增 SKILL.md 后 reload：catalog 28894→29084 chars；停止会话后不再命中；`POST /api/skills/reload` 返回 live 报告 |
| 附件多模态（图片/PDF 内容块） | `4937606c` | fake/真实 HTTP 请求体断言 OpenAI `image_url`/`file`、Anthropic `image`/`document`；真实视觉模型未验证 |
| Browser notify 注入 + 3 条面板代理 | `c1048b79` | 真桥+真 serve 联调；contexts/tabs 200、无扩展 503、close 透传；豁免删除，surface Python 缺失 0 |
| Soul `/soul/reflect` 真实接线 | `59769152` | 202/409/review/auto；真实二进制 E2E（脚本化模型）PASS；真实模型未跑 |
| overseer flake 等待放宽 3s→10s | `80b695ac` | 全量 CI 满负载下复现的既有 flake；隔离 `-count=5`、随后全量 CI 均 PASS |
| loader 生命周期整合 | `c5858389` | `loader.go` 1935 行（上限 1936） |

服务当前二进制是 memory/longmemeval 合并后重建的；`go_hive/go-hive/dist/browserbridge` 是**手工构建的未跟踪产物**，serve 靠它才能自动拉起桥。

## 4. 进行中 / 未合并

当前**无运行中的 lane**；wave5 的 5 个 lane（`cold-events`、`browser-notify`、`skill-live-reload`、`attach-multimodal`、`soul-reflect`）全部已合并并清理；此前本轮另 10 个 lane 也已合并清理。

其余旧 worktree（`parity`、`queen-lifecycle`、`storage`、`tasks`、`triggers`、`w6-*` 等）中有 dirty/unmerged，**不要批量删除**；清理前逐个看 `git status`。

## 5. 已知缺口 / 恢复后的优先级

### P0 — 发布包（已闭环，附已知差异）

- `release.sh`/`install.sh`/QC C4c 已改为四二进制归档；`verify_release_archive.sh` 独立复验 13/13 PASS；六平台 SHA256SUMS 覆盖四项。
- 已对齐：`.github/workflows/release.yml` 改为现行 `release.sh` 契约（`VERSION/DIST/RELEASE_DIR/FRONTEND_DIST`、仓库根调用 `go_hive/scripts/release.sh`、`install.sh` 取 `go_hive/install.sh`），本机逐步仿真通过；R9 仍只能标「workflow 已对齐，Actions 未跑」——未打 tag、Actions 未跑、制品未上传。
- Windows 二进制只做交叉编译 + PE 格式校验，未在 Windows 上运行。
- `RELEASE_CHECKLIST.md` §2 已于 2026-11-20 更新为本机实测大小/SHA（哈希随构建变化，重跑命令见该节）。

### P1 — LongMemEval 未真实跑分（已合并 harness）

- harness/fixture/脚本化管线已合并（`50d54519`）；本机到 HuggingFace/OpenAI 超时，真实 cleaned 数据集与官方 judge 未跑。
- **不得编造 88.4%/82.0% 或任何分数**；只写“fixture 可测，真实跑分未验证”。

### P1 — memory 双跑（已完成）

- 双方 timeline/past recall/recall 均 PASS；stop flush 与 past recall twice 已修。
- 未验证：真实 LLM 抽取质量、UI/dashboard 路径、cooldown 到期分支、worker/colony 与多 queen 作用域；报告 `docs/memory-e2e.md`。

### P1 — Browser 控制面残差

- control RPC 14831 / `client_hello(owner_pid)` / 30s 空闲退出 / `reveal` / `shutdown`
  已对齐并实测（`control_rpc_clients=1`；证据见 `docs/browser-host.md`、`browserbridge/PARITY.md` §11）。
- notify 已注入 agent 会话：`SessionManager.DeliverBrowserNotify` 按 queen（session.id）/ worker（colony worker id）顺序走 `InjectEvent`，真桥 + 真 serve + 真网关联调（2026-11-20，端口 17829/17830/17831 + 8899）观察：adopt(from_user) → serve 日志 `Browser panel: notify for session_e2e_notify_03: …` → transcript 出现该 user 消息且 queen 实际起了一轮；无匹配 profile 只记 `notify: no live agent matched …`，transcript part 数不变。见 `docs/browser-host.md` §2.2/§5.3。
- 3 条前端 browser panel 路由已实现为桥代理（`internal/server/routes_browser_panel.go`），surface_audit 豁免已删除；contexts/tabs 透传 200，close 支持 `{profile}`/`{all:true}`，桥不可达统一 503 `not_connected`（有/无扩展两种 503 形状均实测）。
- CLI 错误码存在差异：用法错误 2→6；`interact screenshot/zoom` 缺 `--intent` 本地判 6（上游 1）。

### P2 — 其他

- Soul：路由与真实二进制 E2E（脚本化模型）已通过；**真实模型的一天调度 E2E 未跑**，见 `docs/soul-daily-reflection.md`。
- 附件多模态：真实视觉模型读图未验证（本机无本地网关、远程网关无可解析明文凭据）；5MB 以上图片未缩放；`/chat` 直传图片不在范围。
- live skill reload 只验证单 queen；colony 多 queen 并发 reload 未压测。
- chart_render：每次独立起 Chrome、无 CDN 回退、整视口截图、无字体等待；见 `docs/chart-render.md`。
- file_ops：读路径仍严格限 root；上游系统 deny list 未逐条复刻；`write_safe_root` 仅放宽写。
- `csv_sql` / `excel_sql` 因 DuckDB/cgo 有意排除；不要为“全绿”做假 stub。
- 81 个需第三方凭据的集成仍是“代码路径/测试”，真实 API 未验证。
- Claude Code queen：真实模型回合中主动调用 MCP `message_queen` 未联调；dashboard 列表按 id 排序（上游按配置顺序）。
- `hive-manual-zh.html` / `surface-tracking-matrix.html` 已同步到本轮状态；发布前仍需重新生成架构契约/矩阵快照。

## 6. 恢复步骤

### 启动服务

```bash
cd /Users/mac/hive/go_hive/go-hive

# bridge 二进制若不在 dist/ 旁边，先构建（零新依赖）
(cd ../browserbridge && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go build -o ../go-hive/dist/browserbridge ./cmd/browserbridge)

/Users/mac/.hive-go/start.sh     # 前台；或后台运行
```

验证：

```bash
curl -s http://127.0.0.1:8788/api/health
curl -s http://127.0.0.1:14830/health
curl -s http://127.0.0.1:8788/api/claude-code-queens
```

### 看门狗 / 时间台账（重启后需重新拉起）

```bash
cd /Users/mac/hive
nohup bash go_hive/scripts/watchdog_loop.sh 240 >/dev/null 2>&1 &
nohup bash go_hive/scripts/watchdog_report.sh 300 >/dev/null 2>&1 &
nohup bash go_hive/scripts/timegov_record.sh 300 >/dev/null 2>&1 &
```

### 09:00 晨报（夜间推进后）

```bash
# 手动生成一次（脚本会先刷新 surface 快照，然后写报告并发 macOS 通知）
bash go_hive/scripts/morning_report.sh

# 定时到次日 09:00 的调度（本机进程即可；不依赖 gateway）
# 产物：go_hive/docs/morning-report-<date>.md（软链副本 morning-report-latest.md）
```

晨报内容：HEAD/提交数、serve/bridge 健康、最近 CI/QC 结论、TIME_EVENTS、ALERTS、契约快照、未对齐项与下一步。未验证项不写成通过。

### 门禁（lanes 少时一次跑）

```bash
cd /Users/mac/hive/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod make ci
uv run --no-project python ../scripts/qc_check.py
```

**并发 lane 多时不要在每条 lane 内跑全量 `make ci`**：会在 parity-targets 被 SIGTERM(143)。lane 只跑 gofmt/vet/目标包测试，合并后由 integrator 统一跑全量。

### 恢复 lane

```bash
cd /Users/mac/hive/.wt/longmemeval && git log --oneline -3 && git status --short
cd /Users/mac/hive/.wt/memory-e2e   && git log --oneline -3 && git status --short
```

不要清理带 dirty 的 memory-e2e；LongMemEval 干净但未合并，先复核再并。

## 7. 配置 / 凭据（不含密钥）

- `HIVE_HOME=~/.hive-go`
- 配置：`~/.hive-go/configuration.json`（LLM deepseek-flash-1017，网关 `https://token.tokenbroad.com/v1`）
- 密钥：`~/.hive-go/env`（`BROADTOKEN_API_KEY`，600）
- 凭据库 key：`~/.hive-go/secrets/credential_key`（600）
- Claude Code CLI：`/Users/mac/.npm-global/bin/claude`（2.1.329，已验证 `-p`/`--resume`；非 Anthropic 模型会打 `unrecognized_model` 警告但成功）
- Chrome：`/Applications/Google Chrome.app`；扩展 1.7.4 在 `go_hive/browserbridge/extension/`
- 断网时：本地 UI/API/bridge 可用；聊天、Claude、LongMemEval 数据/judge 不可用。

## 8. 关键文档

- `go_hive/docs/chart-render.md`
- `go_hive/docs/fileops-parity.md`
- `go_hive/docs/harness-parity.md`
- `go_hive/docs/browser-host.md`
- `go_hive/docs/claude-code-dashboard.md`
- `go_hive/docs/soul-daily-reflection.md`
- `go_hive/docs/tool-parity-matrix.html`
- `go_hive/QC_REPORT.md`

## 9. 建议恢复顺序

1. 检查 `git status` / `git log` / service+bridge health
2. 复核并合并 `longmemeval`（只并已自测分支）
3. 完成 `memory-e2e` 双跑并合并报告
4. 做 release 打包（browserbridge + hive-browser），跑 QC C4/C4c
5. 补 browser control RPC 14831 / 空闲退出 / 3 条面板路由
6. 单次全量 CI + QC + 重建 UI + 重启 8788 + 刷新矩阵/架构快照
7. 只清理已确认合并且 clean 的 worktree；dirty/unmerged 保留

## 10. 不要做的事

- 不要删除 `.wt/memory-e2e`（未提交）或任何 dirty/unmerged worktree
- 不要把 SIGTERM(143) 当 CI 通过
- 不要用 fake CLI/假扩展/假 PNG 冒充真实联调
- 不要在 LongMemEval 没有真实数据/judge 时输出分数
- 不要为了“全部对齐”实现 `csv_sql`/`excel_sql` 而破坏纯 Go/无 cgo 约束
- 不要提交任何密钥、`~/.hive-go/env` 或凭据值
