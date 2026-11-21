# Handoff — 2026-11-21 10:05（磁盘空间不足暂停）

> 上一版 handoff（2026-11-20 22:50，wave5 后）已被本版取代。
> 暂停原因：宿主 Data 卷 98% 占用（460Gi 用 414Gi，可用约 11Gi）。
> 恢复条件：先清磁盘（§5），再按 §4 操作。

**已暂停**：看门狗 `watchdog_report.sh`、时间台账 `timegov_record.sh` 已杀；3 条在途 lane 已停（env/config 由我主动停，storage 已自然完成）。
**仍运行**：`~/.hive-go/start.sh` → `dist/hive-embed serve` on `127.0.0.1:8788`（health 200）；bridge 端口 WS 14829 / HTTP 14830 / control 14831。

## 1. 一句话现状

wave10（browser R15 / colony doom-loop / 分页+alias / SSE 时机+judge）与 `tool-library-lazy-surface`（首轮 tools[] 24）已全部合并；main 全量 CI **5,197 测试 / 5,539 refs / 0 missing**，QC **8/8 PASS**，镜像已发布。字段级逐字一致的三条 lane：storage 已完成待合并（5 提交、干净），env 完成 13/20（45 文件未提交），config 修复 parity 引用后门禁重跑被中断（20 文件未提交）。

## 2. 主线状态（可直接继续）

| 项 | 值 |
| --- | --- |
| 主仓库 | `/Users/mac/hive`，分支 `main`，HEAD `78649329` |
| 主模块 | `/Users/mac/hive/go_hive/go-hive`（module `github.com/aden-hive/hive-go`） |
| 全量 CI | 5,197 测试；5,539 refs；missing 0 |
| QC | 8/8 PASS（提交 `78649329`） |
| 发布镜像 | `github.com/shark2202/go_hive` 远端 `main = 4f03815 = 78649329` |
| serve | `127.0.0.1:8788` health 200 |
| bridge | 14829 WS / 14830 HTTP / 14831 control |
| 版本 | `hive 1.0.26-go.1`（tag `v1.0.26-go.1`） |
| 构建环境 | `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` |

### 本轮已合并（主线）

| 主题 | 提交 | 证据要点 |
| --- | --- | --- |
| 首轮 tools[] = 24 | `62c9be02` | 与 Python 两次独立抓包逐字一致；search_tools 延迟层 51；colony 恢复 39；grouped=150 |
| Browser R15 注解图 | `2d528cd4` | 坐标 click/hover/key 返回 `data`+`mimeType`；CLI `saved_to`+`_image`；真 Chrome 19 步 PASS |
| colony doom-loop | `6f870c24` | `node_tool_doom_loop` 真实 E2E seq 26；payload 全字段；事件/升级各 1 次 |
| 分页 + alias | `f0ce08b7` | GitHub Link 头 3 个、Stripe last_item 16 个；OAuth 账户 alias 索引 + 本地回退 |
| SSE 时机 + judge | `385bc4d9` | ask_user 帧序、judge_verdict 3/3、message_count 1/3/5/8 |
| QC / 发布 | `3f8d57a0`、`78649329` | 8/8 PASS；镜像同步 |

## 3. 在途 lane（worktree 全部保留）

### 3.1 `storage-verbatim`（已完成，干净，待合并）**优先合并**

- worktree `.wt/storage-verbatim`，分支 `storage-verbatim`
- 提交链（5 个，干净）：`77a2d333`（tracker DDL/meta 逐字）→ `5b7cd83c`（旧 home 双跑夹具）→ `63164a18`（字段级契约+报告+HTML）→ `cedf5b04`（脚本可执行）；另含 1 个早前提交
- 七门禁 0（parity 5,170）；双跑结果：v1 20/20、v2 59/59、colony-fork 32/32 文件；JSON 字段差异 0、SQLite schema/行差异 0；写入探针 14/14 文件、10 项值/排版级差异
- **凭据结论：格式兼容，双向互读实测通过**——两侧同为标准 Fernet + `credentials/credentials/<id>.enc` + `index.json`(2.0)；Go 读 Python 写的、Python 读 Go 写的，键值/`expires_at`/metadata/`_alias`/`_identity_email` 全保留（`docs/storage-compat/credentials-crossread.json`）
- 已修字段级差异 3 处：`parts.is_skill_content` 往返丢失、tracker bootstrap DDL 注释、`_tracker_meta.updated_at` `Z`→`+00:00`；修后字段级差异 0
- **SC-01 已修（原 P0 条目移除）**：`.wt/migrate-on-serve`（派生自 main `85595e30`）把 v2/v3/colony_sessions + tracker ensure 接到 `hive serve` 启动链（`internal/cli/loader.go` → `internal/server/storage_startup.go`，失败非致命）；真实 serve 前后 diff、二次启动幂等与 v1/v2 夹具断言见 `docs/storage-compat/serve-startup-go.json` + `internal/server/storage_startup_test.go`
- 产物：`go_hive/scripts/storage_compat_run.sh`（6 件 harness）、`go_hive/docs/storage-contract-diff.md`、`go_hive/contracts/storage-fields.json`（10 类 159 字段全 both）、`go_hive/docs/storage-compat/*.json`、`go_hive/docs/storage-compat.html`（SC-01…08）
- 恢复：`cd /Users/mac/hive && git merge --no-ff storage-verbatim` → 全量 CI → QC

### 3.2 `config-verbatim`（被中断，20 文件未提交）

- worktree `.wt/config-verbatim`，最后提交 = 基线 `93193450`，改动全在工作区
- 已修：`internal/server/channels_config_test.go:87` 的 parity 引用改为上游真实名 `core/tests/test_routes_config_features.py::test_put_features_preserves_other_config_keys`（原缺 `core/` 前缀且文件名断言过严）
- 中断点：改完正在重跑门禁（此前仅 `parity-targets` 因该缺失引用 FAIL）
- 涉及文件：`contracts/config-keys.json`、`config-support-ledger.json`、`env-vars.json`、`internal/llm/{provider,request_shape,stream_openai,transport}.go`、`internal/cli/loader.go`、`internal/server/chat_memory_wiring.go`、`docs/{configuration.md,env-config-contract-diff.md,support-matrix.md}` 等
- 恢复：`cd .wt/config-verbatim/go_hive/go-hive && make test && make parity-targets && make ci` → 全 0 → commit → 合并

### 3.3 `env-verbatim-port`（迭代耗尽，45 文件未提交）

- worktree `.wt/env-verbatim-port`，最后提交 = 基线 `93193450`
- 进度：20 个 Python 独有 env 中 **13 个已实现**（build + 测试绿）：`HIVE_TRACKER_WORKER_TOOL_CALLS`（新增 `internal/pyenv` 统一 Python `int()` 语义）、`HIVE_RESOURCE_{INTERVAL_S,WARN_AVAIL_FRAC,CRIT_AVAIL_FRAC,WARN_RENDERERS,CRIT_RENDERERS,HISTORY}`、`HIVE_NO_FILE_LOG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_COMPACTION_DEBUG`、`HIVE_SENTINEL_LOG`、`HIVE_AGENT_NAME`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_PLAYBOOK_FAST_GRACE`
- 契约已更新并通过 `--strict`：Python 89 / Go 118 / 共有 83 / Python 独有 **6**（仅 GCU 类）/ Go 独有 35；13 个已实现键状态为 `aligned`（`HIVE_AGENT_NAME` 为 `literal_only`）；3 个既有 config anchor 漂移已修
- 未完成（lane 明确列出）：
  1. `internal/envcontract/support_ledger_test.go` 的 `ledgerStatuses` 加 `"platform_diff"` 并要求 reason+anchors，否则 `TestSupportLedgerCoversEveryGap` 会失败
  2. 写 `go_hive/docs/env-verbatim-port.md` 20 行事实表（py 读取点/默认/解析 → Go）
  3. 更新 `go_hive/docs/environment-variables.html` 与 platform-difference 段
  4. 跑七门禁（新测试已带 `// Parity-exempt:`，门禁尚未跑）
  5. 提交
- 备注：早期多包并行跑出现过一次 `internal/server` 瞬时失败，三连跑（含 `-count=1` 全量）未复现，gate 时留意
- 恢复：`cd .wt/env-verbatim-port/go_hive/go-hive && make ci` → 修门禁 → commit → 合并
- lane 总结日志：`/Users/mac/.hermes/profiles/omh-tui/cache/delegation/live/deleg_2ae092f6/task-0.log`（config=task-1、storage=task-2）

## 4. 恢复操作手册

```bash
# 0) 先清磁盘，确保 ≥20Gi 可用
cd /Users/mac/hive

# 1) 合并 storage lane（已完成）
git merge --no-ff storage-verbatim -m "merge storage-verbatim: 旧 home 双跑与字段级契约"

# 2) 继续 config lane
cd .wt/config-verbatim/go_hive/go-hive && make ci

# 3) 继续 env lane
cd .wt/env-verbatim-port/go_hive/go-hive && make ci

# 4) 主线全量验证
cd /Users/mac/hive/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod make ci
uv run --no-project python ../scripts/qc_check.py

# 5) 重建 serve + 发布
cd /Users/mac/hive/go_hive && SKIP_FRONTEND_BUILD=1 bash scripts/embed_ui.sh
# 重启 ~/.hive-go/start.sh
bash /Users/mac/hive/go_hive/scripts/publish_go_hive.sh
cd /Users/mac/.publish-go-hive/repo && git ls-remote origin main   # 校验远端 ref

# 6) 看门狗 / 时间台账 / 晨报（由 launchd 托管，重启后自动恢复）
#    LaunchAgent：~/Library/LaunchAgents/com.hive.{serve-keepalive,release-cadence,governance-recheck,watchdog,timegov}.plist
launchctl list | grep com.hive                      # 五个守护应均为运行态
launchctl kickstart -k gui/$(id -u)/com.hive.watchdog          # 需要时单独重启
launchctl kickstart -k gui/$(id -u)/com.hive.release-cadence
launchctl kickstart -k gui/$(id -u)/com.hive.governance-recheck
launchctl kickstart -k gui/$(id -u)/com.hive.timegov
launchctl kickstart -k gui/$(id -u)/com.hive.serve-keepalive   # 8788 health 连续失败自动拉起 serve
# 注意：launchd 的 PATH 必须含 ~/.vfox/sdks/golang/bin（go）与 ~/.local/bin（uv），
# 已在 plist 的 EnvironmentVariables 里固定；否则 slot 构建 rc=127。
bash go_hive/scripts/morning_report.sh
```

## 5. 磁盘占用与清理候选

- `.wt` 约 **24G**（60+ worktree）；`go_hive/go-hive/dist` 513M；`~/.hive-go` 2.7M
- 可清（可再生；清前 `git worktree list` 确认分支已合并）：
  - 已合并 lane 的 worktree 整体：`browser-annotated-actions`、`colony-doomloop-event`、`integration-pagination-alias`、`sse-timing-judge`、`tool-library-lazy-surface`，以及更早 wave 的已合并 worktree
  - `.wt/*/go_hive/go-hive/dist/*.tar.gz`、`*.exe`（跨平台产物）
  - `go clean -cache`（Go 构建缓存，最大单项；清后需重编译）
  - `/Users/mac/.publish-go-hive/repo` 旧对象（谨慎）
- **不要删**：`.wt/storage-verbatim`、`.wt/config-verbatim`、`.wt/env-verbatim-port`（含未提交工作）；`~/.hive-go`（配置/凭据/会话）

## 6. 字段级逐字一致：剩余待办

1. env：剩余 GCU 类键实现或平台差异登记；每键默认值/类型/解析对照测试
2. config：134 键逐键消费证据；`channels.*`（python_only）与 `mcp.*`（go_only）定性；unread 1 键
3. 存储/G2：**已完成**——SC-01 已接线（serve 启动执行 v2 → v3 → colony_sessions → tracker ensure，失败非致命；真实 serve 实测见 `docs/storage-compat/serve-startup-go.json`）；非空旧 home 双跑、凭据 Fernet 双向互读、回滚与升级演练均实测通过，结论已写入 `go_hive/docs/support-matrix.md` §1.1 与 `go_hive/docs/g2-lossless-replacement.html`；剩余 P0-G2-1（exports 基准）与 F4/F6 待决策，详见 §10
4. LLM 侧：工具输出/失败文案 golden；lifecycle 层 5、`session_cwd` 4、`exa_search` 1 的 schema 残差；skill catalog 渲染文本双跑；MCP 同注册表双跑（命名空间/transport/reload）
5. API/WS：值级 canonical golden（`system_prefix_sha`、`max_iterations` 999999 vs 50、普通回合 execution 信封、context_usage 粒度）
6. 集成：91 个 B 类仅假服务；OAuth 7 模块、分页 2 模块；senders ESP 不真实投递；LongMemEval 零分不得引用
7. 平台：Windows 未实机、GitHub Actions release 未执行、Sentinel 真实投递未验证、desktop/migration 未审计

## 7. 配置 / 凭据（不含密钥值）

- `HIVE_HOME=~/.hive-go`；LLM：deepseek-flash-1017 @ `https://token.tokenbroad.com/v1`（key 变量 `BROADTOKEN_API_KEY`）
- 凭据库 key：`~/.hive-go/secrets/credential_key`（600）
- **judge（本轮新增）**：`~/.hive-go/configuration.json` → `judge.protocol=typesafe`、`api_base=http://127.0.0.1:18099/v1`、`model=0.8B-Instant-MLX-4bit`、`threshold=0.5`；`stall_check=true`
  - 本地服务 = `startlux-decide`（`/Users/mac/hive/startlux-decision`，PID 以 `lsof -iTCP:18099` 为准）
  - 真实 E2E：女王只说不做的一轮 judge 返回 `p=0.68 ≥ 0.50` → 注入 `<system-reminder>` → 第二次 execution 补做
  - 备份：`~/.hive-go/configuration.json.bak-20261121-095807`
- Chrome 扩展 1.7.4；Claude Code CLI `/Users/mac/.npm-global/bin/claude` 2.1.329

## 8. 关键文档 / 脚本

- 治理台账：`go_hive/GOVERNANCE_LEDGER.csv`、`go_hive/docs/delivery-targets.json`（截止时间只提前不延后，`scripts/governance_recheck.py`）
- 2 小时 slot 版排期：`go_hive/docs/release-cadence.html`（`scripts/gen_release_cadence.py --check`）；调度 `scripts/release_cadence_loop.sh`
- 交付治理板：`go_hive/docs/delivery-governance.html`（G1/G2/G3、WIP、门禁、度量、风险）
- 配置参考（HTML，自动生成）：`go_hive/docs/configuration.html`（`scripts/gen_configuration_html.py --check`）

- 契约：`go_hive/contracts/{env-vars,config-keys,tool-definitions,builtin-skills,api-routes}.json`；reference=`python@1b9c458a`
- 审计/矩阵：`go_hive/docs/{appearance-master-matrix.html,appearance-scope-registry.json,support-matrix.md,env-config-contract-diff.md,tool-flat-surface.md,sse-payload-parity.md,storage-compat/}`
- 脚本：`go_hive/scripts/{qc_check.py,publish_go_hive.sh,retry_publish.sh,embed_ui.sh,watchdog_report.sh,timegov_record.sh,storage_compat_run.sh,freeze_env_config_contract.py,check_env_config_contract.py,gen_appearance_master_matrix.py}`
- 本轮 wave 证据：`go_hive/docs/e2e-assets/`（browser-return-contract、worker-escalation、sse-payload-parity 等）

## 9. 不要做的事

- 不要删除任何 dirty / unmerged worktree（`.wt/storage-verbatim`、`.wt/config-verbatim`、`.wt/env-verbatim-port` 必留）
- 不要把 SIGTERM(143) 当 CI 通过
- 不要用 fake/stub 冒充真实端到端；LongMemEval 无真实数据/judge 时不得输出分数
- 不要提交密钥、`~/.hive-go/env`、凭据值
- 不要为对齐实现 `csv_sql`/`excel_sql` 而破坏纯 Go / 无 cgo 约束

## 10. G2 状态更新（2026-11-21 14:15）

G2「老用户无损替换」证据齐备（基线 main `059f4afe`；取证 lane `.wt/g2-home-e2e`、`.wt/g2-rollback-install` 均已合并）。结论：**数据级无损、凭据互读、可回滚、升级实测通过**；数据级无损 ≠ 逐字节一致。

| 验收项 | 结论 | 证据 |
| --- | --- | --- |
| 数据级无损（非空旧 home 双跑） | v1-rich 50/50、v2-rich 99/99 文件；逐字节 47/95；JSON 字段 / SQLite schema+行 / 读探针 / API 探针差异均 0 | `docs/g2-lossless-migration.md`、`docs/e2e-assets/g2-home-e2e/summary.json` |
| 凭据互读 | 两侧标准 Fernet，双向互读值/`expires_at`/metadata 一致（假值；真实第三方 token 未测） | `docs/storage-compat/credentials-crossread.json` |
| 回滚可行 | Python CLI/API/`serve` 读回 Go 迁移后的 home；0 删除 / 0 覆盖；仅 `summary.json` 缓存与共享日志追加；Go 二次启动幂等 | `go_hive/docs/upgrade-and-rollback.md` §3、`go_hive/docs/e2e-assets/g2-rollback/` |
| 升级实测 | v1.0.25 v2 home → 1.0.26 启动即迁移；health / `/` 200；install.sh 四二进制；旧二进制回读 0 写入；归档自验 13/13 | `go_hive/docs/upgrade-and-rollback.md` §4、`go_hive/docs/e2e-assets/g2-rollback/summary.json` |

例外：**P0-G2-1（v1 `exports/` 基准：Python=CWD vs Go=`$HIVE_HOME`）待用户决策，本次未改 exports 行为**；F4（`.migrations` 被当作 colony）待两侧联合决策；F5（迁移前旧二进制看不到 v2 会话）已文档化；F6（CLI 数字序列化）归 value-parity lane。落盘：`go_hive/docs/support-matrix.md` §1.1、`go_hive/docs/g2-lossless-replacement.html`；门禁复跑 gofmt/vet 目标包/parity 5306/5306 全绿（未跑全量 CI）。
