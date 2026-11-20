# Browser host（Go）：桥生命周期 + `hive-browser` CLI

> 状态：已实现并实测（2026-11-20，macOS/arm64，真实 Chrome + 真实 1.7.4 扩展）。
> 相关代码：`go_hive/go-hive/internal/cli/browser_supervisor*.go`、
> `go_hive/go-hive/internal/cli/browser_cli*.go`、`go_hive/go-hive/cmd/hive-browser/`、
> `go_hive/browserbridge/`（独立 module，主模块不 import）。
> 验证脚本：`go_hive/scripts/e2e_browser_host.sh`；
> 构建脚本：`go_hive/scripts/build_browser_host.sh`；
> 归档验收脚本：`go_hive/scripts/verify_release_archive.sh`。

## 1. 形态

```
agent / 人
   │  hive-browser <verb> --json            （CLI，进程用完即退）
   ▼
POST http://127.0.0.1:14830/command         （本地 HTTP，仅 loopback）
   │
   ▼
browserbridge  ──ws 127.0.0.1:14829/bridge──►  Chrome 扩展 1.7.4 ──chrome.debugger──► Chrome
   ▲                                            ▲
   │ ws 127.0.0.1:14831/control（client_hello + notify）  │ POST /tabs/{id}/reveal · /shutdown（侧面板）
   │ 由 `hive serve` 以子进程方式启动/守护/回收              │
hive serve（supervisor + keepalive：browser_supervisor.go / browser_control.go）
```

- 主模块**不 import** browserbridge module（后者自带 `go.mod`），两者只通过
  进程边界 + 固定端口 + `GET /health` + `ws://…:14831/control` 契约耦合。
- `hive-browser` 是纯客户端：一次调用 = 一次 `POST /command`，不自己拉桥、不常驻。
- control RPC（14831）已移植：`hive serve` 常驻一条连接（`client_hello(owner_pid)`
  + `subscribe_notifications`），桥在「零控制客户端且 desktop parent 已消失」时
  空闲 30s 自行退出（exit 0，supervisor 视为有意退出并停住，等下一次 ensure()），
  崩溃（非 0）仍按退避重启。见 §2.1 与 `browserbridge/README.md`。

## 2. `hive serve` 的桥生命周期（supervisor）

`defaultServe` 里只有 3 行接线，逻辑全在 `internal/cli/browser_supervisor.go`
（`loader.go` 受规模门禁冻结，只能 ≤1936 行）：

```go
notify := func(profile, text string, extra map[string]any) {
	manager.DeliverBrowserNotify(profile, text, extra)
}
stopBrowserBridge := startBrowserBridge(browserSupervisorConfig{Home: home, Out: r.Stdout, Notify: notify})
defer stopBrowserBridge()
```

行为：

| 环节 | 行为 |
| --- | --- |
| 开关 | `HIVE_BROWSER_BRIDGE=0/off/false` 关；`=1/on/true` 强制开；未设置时读 `<HIVE_HOME>/configuration.json` 的 `gcu_enabled`（上游 quickstart 写入 `true`），没有该键则默认**开** |
| 复用 | 启动前先探 `GET /health`：已有实例则只打印 `reusing the instance on …`，不再拉起第二个监听 |
| 二进制解析 | `HIVE_BROWSERBRIDGE_BIN` → `hive` 可执行文件同目录（发行布局）→ `$PATH`；找不到只打印一行提示，serve 照常启动 |
| 启动 | `browserbridge -http <addr> -artifacts <HIVE_HOME>/tool-artifacts`，独立进程组（`Setpgid`），`HIVE_HOME` 透传 |
| 就绪 | 轮询 `GET /health`，预算 10s；child 提前退出则立即判定失败 |
| 输出 | 就绪后打印 `Browser bridge: ready on 127.0.0.1:14830 (pid N)`；child 的 stdout/stderr 追加进 `<HIVE_HOME>/browserbridge.log` |
| 重启 | 非本进程主动停止时，按 500ms/1s/2s 退避重启，最多 3 次后放弃并打印 `giving up`；子进程 **exit 0**（空闲退出 / POST /shutdown）视为有意退出：打印 `idle — waiting for the next client` 并停住，下一次 ensure()（keepalive 重连）再拉起 |
| control 端口 | 子进程带 `-control <addr>`（默认 127.0.0.1:14831；`HIVE_BROWSERBRIDGE_CONTROL` 覆盖） |
| 视口缩放 | `gcu_viewport_scale`（0.1–1.0，默认 0.8）经 `server.GcuViewportScale` 解析，作为 `HIVE_BROWSER_VIEWPORT_SCALE` 传给子进程（显式 env 覆盖优先，超出范围夹到 0.1/1.0）；桥在启动日志与 `GET /status` 的 `viewport_scale` 里上报。上游 `get_gcu_viewport_scale` 自扩展化浏览器后没有消费点，Go 侧只读并上报，不改截图/抓取几何 |
| keepalive | 启动后 serve 常驻一条 control 连接：`client_hello(owner_pid)`（owner = `HIVE_DESKTOP_PARENT_PID`，无则 serve 自身 pid）→ `subscribe_notifications` → 持有；桥重启/空闲退出后按 1s 指数退避重连，并唤醒 supervisor 重新拉起桥 |
| notify | 侧面板 adopt/release 的推送帧写入 serve 日志 `Browser panel: notify for <profile>: <text>`，并经 `SessionManager.DeliverBrowserNotify` 注入匹配的 agent 会话（见 §2.2）；无匹配时只记一行 `notify: no live agent matched profile=…` |
| 端口被占 | child 报 `address already in use` → 打印 `leaving the existing listener …` 并停止重试（不抢别人的监听） |
| 退出 | `stopBrowserBridge()`：SIGTERM 进程组，超时 5s 后 SIGKILL；实测 serve 收到 SIGTERM 后桥子进程一并消失 |

地址覆盖：`HIVE_BROWSERBRIDGE_HTTP`（默认 `127.0.0.1:14830`）、
`HIVE_BROWSERBRIDGE_CONTROL`（默认 `127.0.0.1:14831`），CLI 与 supervisor 共用同一解析。
额外参数：`HIVE_BROWSERBRIDGE_ARGS`（空格分隔，追加给 child）。

### 2.1 control RPC（14831）与空闲退出

上游 `bridge_rpc.py` / `bridge_host.py::_idle_exit_watchdog` 的 Go 对应物：

| 环节 | 行为 |
| --- | --- |
| 服务端 | `browserbridge` 的 `control.go` 绑定 `ws://127.0.0.1:14831/control`；协议为 `{"rpc_id","method","args","params"}` → `{"rpc_id","ok","result|error"}`，方法名受 **46 条上游 `RPC_METHODS` 白名单**约束（不在表内 → `unknown_method`），每个请求一个 goroutine，慢调用不阻塞同一连接上的其它调用 |
| 身份 | `client_hello(owner_pid)`：owner = `HIVE_DESKTOP_PARENT_PID`（desktop 起 serve 时是 Electron 的 pid，否则 serve 自己，`_runtime_owner_pid` 语义）。reaper 每 10s 清理「owner 已死」与「30s 内未 identify」的僵尸连接；`active_client_count` 只算这两类之外的连接 |
| 推送 | `subscribe_notifications` 后，adopt/release（from_user）经 `Bridge.NotifyClients` 推 `{type:"notify",profile,text,tabId,action}`；serve 侧接收后打印 `Browser panel: notify for …` 并注入匹配会话（§2.2） |
| serve 侧 | `browser_control.go` 的 keepalive 常驻一条连接（hello → subscribe → 持有），桥重启/空闲退出后 1s 指数退避重连并唤醒 supervisor；serve 停止时先关这条连接再 SIGTERM 子进程 |
| 父进程监视 | `cmd/browserbridge` 的 `desktopParentWentAway`（上游 `_watch_parent` 的对应物）：`HIVE_DESKTOP_PARENT_PID` 存在时每 `-parent-check`（默认 400ms）探测，连续 2 次失败即优雅退出（exit 0），父死约 0.8s 生效（实测 783–866 ms） |
| 空闲退出 | `cmd/browserbridge` 的 `idleExitRequested`（无父进程提示时的兜底）：`-idle-check`（默认 10s）轮询，`control 客户端 == 0` 且 `HIVE_DESKTOP_PARENT_PID` 已死，持续 `-idle-grace`（默认 30s）后优雅退出（exit 0）。`-no-idle-exit` 供不持控制连接的调试/脚本使用 |
| 可观测 | `GET /status`：`listening.control_rpc`、`control_rpc_clients`、`runtime_alive`；`POST /tabs/{id}/reveal`（激活并抬升 Chrome 窗口，老扩展回退 `tab.activate` 并带 `fallback:"activate"`）、`POST /shutdown`（202 + 优雅退出） |

### 2.2 notify → agent 会话注入 + 面板 HTTP 代理

**notify 注入**（`internal/server/browser_notify.go::SessionManager.DeliverBrowserNotify`，
上游 `framework/loader/cli.py::_bridge_keepalive_loop._on_notify` 的对应物）：

| 环节 | 行为 |
| --- | --- |
| 匹配顺序 | 先按 `session.id == profile` 找 queen（经 `session.QueenExecutor.QueenNode().InjectEvent(text, true)`），再逐 session 调 `colony.InjectInput(profile, text)` 找 worker（worker id 由 colony 自己判断，无匹配返回错→继续）；顺序与上游一致 |
| 语义 | 两面都按 `is_client_input=True` 注入：文本作为 queen/worker 的下一条 user 消息进入 transcript（`chatTurnRequest{clientInput:true}` / `loop.InjectEvent(content, true, nil)`），排队后唤醒 loop，不新造事件类型。`tabId`/`action` 不写进会话（上游同：注入的 text 已是面向人的整句，这两个字段只用于日志/排障） |
| 无匹配 | 不伪造收件人：只记 `notify: no live agent matched profile=<profile> (extra=…)`，不写任何会话、不排队 |
| 注入失败 | queen 已匹配但 `InjectEvent` 报错（如 runtime 已关闭）时记 `notify: queen inject failed for session=…`，不再继续扫描 |

**面板代理**（`internal/server/routes_browser_panel.go`，上游
`framework/server/routes_workers.py` 的三条路由）：

| 路由 | 桥侧对应 | 行为 |
| --- | --- | --- |
| `GET /api/browser/contexts` | `GET /contexts` | 按上游 `handle_browser_contexts` 重塑：`{available,count,contexts:[{profile,group_id,name,tab_count,active_tab_id}]}` |
| `GET /api/browser/tabs` | `GET /tabs` + `GET /contexts` | 按上游 `handle_browser_tabs` 重塑：`{available,count,live_count,discarded_count,hive_group_count,tabs:[{id,windowId,groupId,isHiveGroup,url,title,discarded,active,audible,pinned}]}`；`isHiveGroup` 由桥 registry 的 groupId 求交 |
| `POST /api/browser/contexts/close` | `POST /contexts/close` | 请求体 `{"profile": …}` 或 `{"all": true}` 原样转发，返回 `{"closed":[…]}`；缺 profile 时透传桥的 400 `{"error":"profile (or all=true) is required"}` |
| 桥不可达 / 桥未连扩展 | — | 三条都回 **200** `{"available":false,"reason":…,<集合>: []}`，与上游「browser tools 不可用」的 200 形状一致（2026-11-21 起，此前是 503 透传） |

桥地址解析与 supervisor 共用同一约定：`HIVE_BROWSERBRIDGE_HTTP` → `HIVE_BRIDGE_PORT`+1
（默认 14829+1 = `127.0.0.1:14830`）。面板 status 探针（`GET /api/browser/status[/stream]`、
`POST /api/browser/tab/reveal`）同样先探 14830，再回落 legacy 9230。

## 3. `hive-browser` CLI

22 个上游 `browser_*` MCP 工具全部有对应动词；另外把 22 之外的 5 个桥内 L3 动词
（`click`/`type`/`type_focused`/`press`/`scroll`）也做成了子命令，因此 CLI 覆盖
桥的 **27 个 canonical command**。

| CLI | 桥命令 | 关键参数 |
| --- | --- | --- |
| `setup` | `browser_setup` | — |
| `status [--tab N]` | `browser_status` | `profile` |
| `stop` | `browser_stop` | — |
| `open <url> [--background] [--browser-profile L]` | `browser_open` | `url`,`background`,`browser_profile` |
| `navigate <url> [--wait-until X] [--timeout-ms N] [--browser-profile L] [--tab N]` | `browser_navigate` | `url`,`wait_until`,`timeout_ms` |
| `reload [--tab N]` | `browser_reload` | — |
| `interact --action A […]` | `browser_interact` | `action` + `selector`/`coordinate`/`text`/`region`/`duration`/`wait_for_selector`/… |
| `select <selector> --value V…` | `browser_select` | `selector`,`values` |
| `upload <selector> --file F… [--trigger-selector S]` | `browser_upload` | `selector`,`file_paths`,`trigger_selector` |
| `screenshot [--intent I] [--full-page] [--selector S]` | `browser_screenshot` | `intent`（可选）,`full_page`,`selector` |
| `evaluate --js SRC\|@FILE\|-` | `browser_evaluate` | `script` |
| `script --skill S --script P [--args JSON\|@file\|-] [--timeout-s F]` | `browser_script` | `skill`,`script`,`args`,`timeout_s` |
| `click <selector> [--button B] [--double]` | `browser_click` | `selector`,`button`,`double_click` |
| `type <selector> --text T` | `browser_type` | `selector`,`text`,`clear_first`,`use_insert_text` |
| `type-focused --text T` | `browser_type_focused` | `text` |
| `press <key> [--selector S] [--modifiers M]` | `browser_press` | `key`,`selector`,`modifiers` |
| `scroll [--direction D] [--amount N] [--selector S]` | `browser_scroll` | `direction`,`amount`,`selector` |
| `tab list\|close [id]\|activate <id>` | `browser_tabs` / `browser_close` / `browser_activate_tab` | `tab_id` |
| `page html [selector] [--head N]` | `browser_html` | `selector` |
| `page snapshot [--mode M] [--head N]` | `browser_snapshot` | `mode` |
| `page text <selector> [--timeout-ms N]` | `browser_get_text` | `selector`,`timeout_ms` |
| `page shadow-query <selector>` | `browser_shadow_query` | `selector` |
| `page console [--level L]` | `browser_console` | `level` |
| `page resize --width N --height N` | `browser_resize` | `width`,`height` |
| `dialog respond accept\|dismiss [--prompt-text T]` | `browser_dialog_respond` | `action`,`prompt_text` |
| `help [cmd]` | —（本地文本） | — |

公共开关（写在子命令之后，如 `hive-browser status --json`；写在前面也接受，
但前置值不生效——argparse 的叶子默认覆盖，上游 `hive-browser --json help` 同样输出文本）：
`--json`、`--profile <session>`
（默认取 `$HIVE_BROWSER_SESSION`，再退回 `default`）、`--bridge <base-url>`（默认
`$HIVE_BROWSERBRIDGE_HTTP` 或 `http://127.0.0.1:14830`；Go 自有开关，只在子命令之后）。

### 退出码（agent 的机器契约）

| 码 | 含义 | 触发示例 |
| --- | --- | --- |
| 0 | 成功 | — |
| 1 | 领域失败 | 桥返回 `tab.create returned noTabId` 之类；`interact --action screenshot` 缺 `--intent`、`interact --action zoom` 缺 `--region`（桥返回 `ok:false`，与上游同路径） |
| 2 | 用法错误 / not connected | 未知子命令/flag、缺必填参数、非法取值或类型（`--action`/`--mode`/`--wait-until`/`--scroll-direction`/`--auto-snapshot-mode`/`--coordinate`/`--region`/`--duration` 等）；桥进程没起（connection refused）或 `.connected=false` / `browser_setup` 未连扩展 |
| 3 | not started | 该 profile 还没有标签页 → `run hive-browser open <url> first` |
| 4 | not found | 元素/标签页不存在 |
| 5 | ambiguous | 选择器多匹配、多 Chrome profile 未指定 |
| 6 | bad args（Go 自己的本地校验） | 上传文件不存在、`evaluate` 缺 `--js`、`script` 缺 `--skill/--script` 或 `--args` 非 JSON、Go 独有的 `click`/`type`/`press`/`scroll` 动词 |
| 7 | pending dialog | 结果带 `pending_dialog` → `hive-browser dialog respond` |
| 8 | rate limited | 桥返回 `rate_limited`（社交平台浏览上限） |

与上游 `tools/src/gcu/errors.py` / argparse 的对照（2026-11-20 实测）：

* 用法错误与上游 argparse 一样退出 **2**（上游实测：无参数、未知子命令、未知 flag、缺必填参数、
  非法 choice、`--coordinate`/`--region` 格式错都是 2；本实现的 CLI 级校验对齐到 2，
  与 `not_connected` 共用该值——上游同样共用）。Go 自己的本地校验（上表 6）保持不变。
* `interact --action screenshot` 缺 `--intent` / `--action zoom` 缺 `--region` 不在本地预检，
  由桥返回 `ok:false` 后分类为 **1**（上游同路径：`_dispatch` → `classify_result`）。
  独立 `screenshot` 动词的 `--intent` 同样可选（2026-11-21 起）：连上桥时 capture 成功退 **0**，
  无桥 **2**、未开标签 **3**，与上游 `render_screenshot(intent=None)` 一致；桥侧
  `browser_screenshot` 也接受缺省 intent。

### 会话/标签状态

CLI 每次调用都是新进程，所以「这个 profile 的当前标签页」存在
`<HIVE_HOME>/browser_cli_state.json`（对应上游 `~/.hive/browser_contexts.json` 的客户端索引）：

```json
{ "default": { "tabId": 788245743, "browser_profile": "golden-azure-toad", "updated_at_ms": 1795170000000 } }
```

`open` / `tab activate` 写入；`tab close` / `stop` 清除；`--tab N` 始终优先。
文件写失败不影响命令结果（只是下次要显式 `--tab`）。

## 4. 构建与发布

三个二进制必须放在**同一个目录**（supervisor 按 `hive` 同目录找 `browserbridge`）：

```bash
go_hive/scripts/build_browser_host.sh              # 六个平台，等价于 `make browserbridge`
go_hive/scripts/build_browser_host.sh darwin/arm64 # 单平台
# 产物：go-hive/dist/browser-host/<os>-<arch>/{hive,hive-browser,browserbridge}[.exe]
```

单平台手工等价命令：

```bash
cd go_hive/go-hive      && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o dist/hive ./cmd/hive
cd go_hive/go-hive      && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o dist/hive-browser ./cmd/hive-browser
cd go_hive/browserbridge && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o ../go-hive/dist/browserbridge ./cmd/browserbridge
```

**发布归档已携带四个二进制（1.0.26 起）**：`scripts/release.sh` 在 `hive`/`hive-cred`
同级构建并打包 `hive-browser` 与 `browserbridge`（Windows 为 `.exe`），归档内
`SHA256SUMS` 覆盖全部四个成员；`install.sh` 把四个装进同一目录，因此解压或安装后
`hive serve` 能按「`hive` 同目录」解析到 `browserbridge` 并自动拉起（无需设置
`HIVE_BROWSERBRIDGE_BIN`）。QC 的 C4c（`scripts/qc_check.py::check_release_archives`）
断言每个归档含这四个成员、被内层 `SHA256SUMS` 覆盖，并在本机平台解包后各运行一次
（`hive --version`、`hive-cred --help`、`hive-browser help`、`browserbridge -version`）。

解包验收（本机平台，一条命令：解压 → `serve` → 桥子进程 / `/health` → `hive-browser` 退出 2 → SIGTERM 回收）：

```bash
bash go_hive/scripts/verify_release_archive.sh go_hive/go-hive/dist/release
```

手工等价（把桥端口挪开，避免与本机开发实例撞端口）：

```bash
tar -xzf go-hive/dist/release/hive-<version>-darwin-arm64.tar.gz -C /tmp/hive-rel
mkdir -p /tmp/hive-rel/home
HIVE_HOME=/tmp/hive-rel/home HIVE_BROWSERBRIDGE_HTTP=127.0.0.1:14930 \
  HIVE_BROWSERBRIDGE_ARGS="-ws 127.0.0.1:14929 -no-legacy" /tmp/hive-rel/hive serve --port 8797 &
# serve 日志出现：Browser bridge: ready on 127.0.0.1:14930 (pid N)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:14930/health    # 200
/tmp/hive-rel/hive-browser status --json                                  # 无 Chrome/扩展时 exit 2
```

## 5. 验证

### 5.1 真机联调（推荐）

```bash
bash go_hive/scripts/e2e_browser_host.sh /tmp/browser-host-evidence
```

脚本自己做完整链路：构建三个二进制 → 临时 `HIVE_HOME`（`gcu_enabled: true`）→
`hive serve` → 断言桥子进程（ppid = serve pid）与 `/health`、control RPC（14831）
与 `control_rpc_clients` → 无 Chrome 时断言 `hive-browser status --json` 退出 2 →
`chromeloader` 把仓库内 1.7.4 扩展装进真实 Chrome → CLI 驱动真实页面 →
SIGTERM serve 并断言桥子进程消失 → 裸桥（无控制客户端、无 desktop parent）
2s 空闲后自行退出（exit 0）。一次通过的真实输出（2026-11-20，Chrome 158）：

```
### step 04: start `hive serve` and assert it brings the bridge up
  ok  : serve announced the bridge: Browser bridge: ready on 127.0.0.1:14830 (pid 26966)
  ok  : bridge /health: {"connected":false,"connections":0,"ok":true,"status":"ok","uptime_ms":430}
  ok  : serve holds a control client (control_rpc_clients=1)
  ok  : control RPC is listening ({"ws":true,"status":true,"control_rpc":true,"legacy_ws":true})
  ok  : bridge sees the runtime as alive (client_hello owner pid)
  bridge child pid 26966 (parent 26960): …/browserbridge -http 127.0.0.1:14830 -control 127.0.0.1:14831 -artifacts …/tool-artifacts
  ok  : the bridge is a child of serve (ppid=26960)
### step 05: hive-browser without Chrome: explicit not-connected contract (exit 2)
  ok  : status exit code = 2 (not_connected)
  ok  : setup exit code = 2 (extension not connected)
### step 06: load the bundled extension into real Chrome
  ok  : chrome + extension ready: {"chromePid":16325,"extensionId":"jkpcegnbfimimjodblcemoheedidnppm","ok":true}
  ok  : the extension connected to the supervised bridge
### step 07: hive-browser against real Chrome
  ok  : status: {"connected":true,"profile":"default","connected_profiles":["gentle-ruby-newt"]}
  ok  : open returned tabId=2014367002 url=http://127.0.0.1:18099/index.html
  ok  : reveal raised the Chrome window: {"ok":true,"revealed":true,"prevState":"normal"}
  ok  : page text read the real DOM: {"ok":true,"selector":"#title","text":"hive browser host"}
  ok  : evaluate saw the real page title        # document.title == "Hive Browser Host E2E"
  ok  : interact --action left_click exit code = 0
  ok  : the click ran the page handler (out=clicked)
  ok  : page snapshot exit code = 0 ({"nodes":15})
  ok  : screenshot exit code = 0 ({"saved_to":null,"mimeType":"image/png"})
  ok  : tab list exit code = 0 ({"total":2})
  ok  : after tab close the CLI says 'Browser not started' (exit 3)
### step 08: stop serve and assert the bridge child is gone
  ok  : serve exited on SIGTERM
  ok  : bridge child 26966 was stopped with serve
  ok  : nothing answers http://127.0.0.1:14830/health after shutdown
### step 09: a bare bridge with no control client and no desktop parent exits on idle
  ok  : bare bridge has zero control clients before the grace
  ok  : bare bridge exited within 20s of the idle grace
  ok  : idle exit code was 0 (intentional: the supervisor will not respawn)
  ok  : idle watchdog logged its reason
PASS: browser host end-to-end
```

> harness 前置：合成输入（CDP 鼠标/键盘）只会送进**可见**的 renderer —— 最小化或
> 完全被遮挡的 Chrome 窗口会让点击/输入「返回 ok 但页面没反应」。脚本因此在驱动
> 输入前调 `POST /tabs/{id}/reveal` 激活并抬升窗口（顺便真机覆盖这条新路由）；
> 手动跑同一流程时同样先让窗口可见。

无 Chrome/无扩展时 CLI 的明确报错（同一脚本 step 05 的证据）：

```json
{ "ok": false, "connected": false,
  "error": "The Hive browser extension isn't connected. Open Chrome, enable the Hive Browser Bridge extension, and confirm it points at ws://127.0.0.1:14829/bridge …" }
```
（stderr: `hive-browser: browser bridge not reachable at http://127.0.0.1:14830 …` 时退出 2。）

### 5.2 单元测试

```bash
cd go_hive/go-hive       && go test ./internal/cli/ -run 'TestBrowser|TestResolveBrowserBridge|TestControl' -count=1
cd go_hive/go-hive       && go test ./internal/server/ -run 'TestDeliverBrowserNotify|TestBrowserPanel' -count=1
cd go_hive/browserbridge && go test -race ./...
```

control 面用假扩展 + 假控制客户端覆盖白名单/`client_hello`/notify/并发/reaper，
cmd 侧覆盖空闲退出（无客户端 + 无 desktop parent / 有 desktop parent /
有控制客户端）与 `POST /shutdown`；`internal/cli` 覆盖 keepalive（hello →
subscribe → notify → 断线重连 → 唤醒 supervisor）与「exit 0 停住、ensure() 重启」。
`internal/server` 的 `TestDeliverBrowserNotify*` 用真 `ChatRuntime`（fake LLM）
断言 notify 文本作为 user 消息落进 queen transcript、worker 命中走
`InjectInput`、无匹配不写不排队；`TestBrowserPanel*` 断言三条代理的
200/404 透传、close 请求体原样转发、以及桥不可达时的 503 `not_connected` 形状。
桥侧 `TestPanel*` 用 L2 假扩展覆盖 `GET /tabs` 与 `POST /contexts/close`。

CLI 测试用假桥（httptest）逐条断言「动词 → 桥命令 + 参数」，并断言 27 个
canonical command 全部可达；supervisor 测试用测试二进制自身充当 stub child，
覆盖启动/就绪/重启上限/端口被占/复用/退出清理。

### 5.3 面板代理 + notify 注入联调（真实 bridge + serve）

2026-11-20 在本机跑过一次隔离联调：真 `browserbridge`（17829/17830/17831）
+ 真 `hive serve`（8899，`HIVE_HOME` 为一次性 home）+ 一个 fake 扩展进程
（label `alpha`，tab 501 属 group 101；不涉及真 Chrome）。

| 步骤 | 命令 / 触发 | 观察 |
| --- | --- | --- |
| contexts | `GET :8899/api/browser/contexts` | 200，透传桥的 `{"ok":true,"contexts":[…]}` |
| tabs | `GET :8899/api/browser/tabs` | 200，透传扩展的实际 tab 行 |
| close | `POST :8899/api/browser/contexts/close {"profile":…}` / `{"all":true}` | 200，`{"closed":[{"profile":…,"ok":true,"status":"stopped",…}]}` |
| notify 命中 | 桥 `POST /command adopt_tab(from_user=true)`（profile = live session id） | serve 日志 `Browser panel: notify for session_e2e_notify_03: The user handed "E2E tab" (tab #501) over to you.`；transcript 出现该 user 消息，queen 随后跑了一轮（assistant part） |
| notify 未命中 | 同一 profile 的会话已停止后再触发 | 日志 `notify: no live agent matched profile=… (extra=map[action:handed-over tabId:501])`；transcript part 数不变（14→14） |
| 桥不可达 | kill 桥后 curl 三条路由 | 全部 **503** `{"available":false,"error":"not_connected","reason":"browser bridge not reachable at http://127.0.0.1:17830",…}` |
| 桥在、无扩展 | 只重启桥（无扩展连接）再 curl | `/tabs` 原样透传桥的 503 `no browser extension is connected …`；`/contexts` 仍 200 空表 |

> 2026-11-21 更新（R04）：上表 contexts/tabs 两行记录的是当时的透传行为。现在两条路由
> 已按上游形状 reshape（无桥/未连扩展都是 200 `available:false`），close 的 400 与
> `{"closed":[…]}` 不变。新的真桥 + fake 扩展实测见审计页 §04.2 与
> `go_hive/scripts/e2e_browser_panel.sh`（`bash go_hive/scripts/e2e_browser_panel.sh`）。

## 6. 与上游 Python 的差异（未对齐项）

| 项 | 上游 Python | 本实现 | 说明 |
| --- | --- | --- | --- |
| control RPC 14831 + `client_hello(owner_pid)` | 有；serve 持连接，桥零客户端时 30s 退出 | **已对齐** | 白名单 46 条与上游一致；空闲退出 + exit 0 停住/ensure() 重启（上游是 worker SIGTERM supervisor 的握手）；`/tabs/{id}/reveal`、`/shutdown` 同样对齐 |
| notify 落到 agent 会话 | 上游把 notify 路由到 `inject_event`（queen 按 session.id、worker 按 worker.id） | **已对齐** | `SessionManager.DeliverBrowserNotify` 按同一顺序注入；真 `ChatRuntime`（fake LLM）测试证明文本作为 user 消息落进 transcript，worker 走 `ColonyRuntime.InjectInput`，无匹配只记录 |
| 面板 3 路由 | `GET /api/browser/contexts`、`GET /api/browser/tabs`、`POST /api/browser/contexts/close`；无 bridge 时 200 + `available:false` | **已对齐**（2026-11-21） | 三条路由按上游 `handle_browser_contexts`/`handle_browser_tabs`/`handle_close_browser_context` 逐字段 reshape，桥不可达/未连扩展统一 200 `available:false`；真桥 + 假扩展实测见审计页 §04.2 与 `go_hive/scripts/e2e_browser_panel.sh` |
| CLI 自动 ensure 桥 | `errors.run` 里 `ensure_bridge_host_running()`，CLI 会拉起 `bridge_host` | **不拉** | 桥归 `hive serve` 管，避免 CLI 留下孤儿进程；桥没起时明确退出 2 并提示 `hive serve` |
| `browser_setup`/`browser_status` 的 `connected_profiles` | 来自 `_connected_profiles` | 来自 Go 桥 `connectedProfiles` | 字段一致（label/is_default/starred/version/protocol_version） |
| profile→tab 索引 | 桥侧 contexts + 客户端 `browser_contexts.json` 重水化 | 客户端 `browser_cli_state.json`（只存 tabId） | 桥侧 contexts 未持久化，CLI 用本地索引补足 tab 解析 |
| 用法错误退出码 | argparse=2 | **已对齐**（2026-11-20） | 未知子命令/flag、缺必填参数、非法 choice/类型（`--action`、`--mode`、`--wait-until`、`--scroll-direction`、`--auto-snapshot-mode`、`--coordinate`/`--region`、`--duration`、`--timeout-s`、`tab_id`）一律 2，与上游 argparse 实测一致；Go 自己的本地校验仍为 6（见 §2 表） |
| `interact screenshot/zoom` 缺 intent | 工具层 `ok:false` → 1 | **已对齐**（2026-11-20） | 本地预检已移除：桥返回 `screenshot requires intent` / `zoom requires region`，分类为 1（真 Chrome 实测） |
| 独立 `screenshot` 缺 `--intent` | `render_screenshot(intent=None)` → 连上时 0 | **已对齐**（2026-11-21） | 本地 `--intent` 预检与桥侧强制一并移除：连上 0 / 无桥 2 / 未开标签 3；`interact --action screenshot` 仍要求 intent（上一行） |
| 父进程死亡后的桥寿命 | `_watch_parent`：桌面 PID 消失即退出（5s × 2 次确认） | **已对齐（实测 ≤2s）** | 桥新增 `-parent-check`（默认 400ms，2 次确认）监视 `HIVE_DESKTOP_PARENT_PID`，父死约 0.8s 退出；原空闲看门狗（10s 轮询 + 30s grace，约 30–40s）保留为兜底；serve 侧 `ensure()`/run 循环在 owner 死后不再复活（对应上游 `_run_supervisor` 的 parent watchdog） |
| `--head` / `--no-annotate` | 上游工具侧处理 | `--head` 由 CLI 读 `saved_to` 文件取前 N 字符；`--no-annotate` 透传（Go 桥当前忽略注解开关） | 结果里仍给出 `saved_to`/`data` |
| `hive-browser script` 技能解析 | skill 名 → 目录（skills 根注入） | 桥侧 `-skills` 未由 supervisor 注入 | 指定 `HIVE_BROWSERBRIDGE_ARGS="-skills <dir>"` 可补上 |
| 发布归档成员 | hive-desktop 侧打包扩展 | 归档含 `hive`/`hive-cred`/`hive-browser`/`browserbridge`（1.0.26 起）；Chrome 扩展未随包，`chromeloader` 可从源码树侧载 | 见 §4 |

### 6.1 快照/截图形状：未对齐项（精确差异）

快照与截图是上游工具层最重的两块「形状」差异，PARITY.md 行 12（MCP 工具）、
行 133（bridge 方法）、行 175（运行特性）已记为「未移植」；本任务只做实测与列差异，
不硬改。逐项对照：

| 项 | 上游 Python | 本实现 | 影响 |
| --- | --- | --- | --- |
| 快照返回 | 桥 `snapshot` 返回 `{ok,tabId,url,tree}`；工具层把 `tree` 落盘为 `<ArtifactDir>/browser_snapshot_<ms>_tab<id>.txt` 并返回 `{ok,tabId,url,length,saved_to}` | 桥 `browser_snapshot` 直接内联 `{ok,tabId,url,tree,nodes}` | 无 `length`/`saved_to`，不落盘 → CLI `page snapshot --head N` 因没有 `saved_to` 而**拿不到内容**（对快照是 no-op） |
| 快照行格式 | `- link [ref=e1] "Home" [href="https://…"]`：交互角色（`refs.py::INTERACTIVE_ROLES`）或任何有 accessible name 的节点分配 `eN` ref；`link` 节点按名字顺序消费 `_collect_link_hrefs` 的 href 并标注 | `- link "Home"` | 无 `[ref=eN]`、无 `[href="…"]`；agent 无法按 ref 操作，链接 URL 需要额外 `evaluate` |
| 快照补充段 | 末尾追加 `(links visible in DOM but not in accessibility tree):`，把 AX 树漏掉的可见 `<a href>` 也列出来（虚拟列表/LinkedIn 场景） | 无 | 可见但被 AX 标记 ignored 的链接不可见 |
| AX 预处理/兜底 | `_clean_inline_text_boxes` 去掉冗余 InlineTextBox；>50000 节点或可见节点 <10 且 >500 时走 `_dom_snapshot`；快照前有 pending dialog 检查（返回 `pending_dialog` → 7） | 直接格式化 `Accessibility.getFullAXTree` | 大页面/对话框阻塞时上游有专门路径 |
| 截图 | 800px 宽 JPEG(q75)（`_resize_and_annotate`）+ 可选 PIL 注解（高亮框）；CLI 落盘并给 `saved_to`/`_image` | 原始 `{ok,data,mimeType,format}` base64（默认 PNG） | 无缩放/注解/落盘；大图 base64 直接进 JSON |
| 坐标动作图 | 坐标 click/hover/key 成功后追加一张注解截图（`_build_visual_response`，高亮落点） | 只回 JSON | agent 看不到落点，只能靠截图自查 |

未在本任务内实现的原因：ref/href 需要完整 ref 子系统（`refs.py` 角色集、`_collect_link_hrefs`
的 AccName 近似、按 ref 的 click/type 解析）与 DOM 兜底；注解图需要图像合成 + 字体渲染
（上游用 Pillow），在「零新依赖」约束下无法等价实现。故按任务要求只列精确差异，不硬改。

## 7. 本 lane 的真机实测（2026-11-20）

环境：macOS 26.7.1 / Apple Silicon，Google Chrome 158，bundled 1.7.4 extension。
开发者的常驻桥占用 14829–14831，因此测量用隔离端口 + 扩展的端口副本（仅改
`offscreen.js::BRIDGE_PORTS`，其余为原样扩展）。

### 7.1 父进程死亡 → 桥退出时延

| 场景 | 命令 | 实测 |
| --- | --- | --- |
| 修改前（基线） | 裸 `browserbridge`（默认空闲看门狗），`kill -9` 父 PID | **40 023 ms**（10s 轮询 + 30s grace） |
| 修改后 | 同上，`-parent-check` 默认 400ms | **783 ms** |
| 修改后（隔离父监视） | 同上 + `-no-idle-exit`（只剩父监视） | **866 ms** |
| 修改后（serve 监督） | `hive serve` 拉起桥，`kill -9` desktop parent | **815 ms**，且 6s 内无复活进程；serve 日志 `Browser bridge: runtime gone — not restarting` |

桥日志：`browserbridge: runtime pid <N> is gone — shutting down the bridge with the runtime`，
之后 `{"ok":true,"stopped":true}`，退出码 0（supervisor 视为 intentional）。

### 7.2 CLI 退出码（真实命令负例）

```
fixed:2  baseline:6  | hive-browser                      (无参数)
fixed:2  baseline:6  | hive-browser bogus                (未知子命令)
fixed:2  baseline:6  | hive-browser interact             (缺 --action)
fixed:2  baseline:6  | hive-browser interact --action nope
fixed:2  baseline:6  | hive-browser interact --action screenshot --coordinate 0.1
fixed:2  baseline:6  | hive-browser navigate             (缺 url)
fixed:2  baseline:6  | hive-browser select '#s'          (缺 --value)
fixed:2  baseline:6  | hive-browser upload '#f'          (缺 --file)
fixed:2  baseline:6  | hive-browser page resize --width 100
fixed:2  baseline:6  | hive-browser page snapshot --mode nope
fixed:2  baseline:6  | hive-browser tab close abc
fixed:2  baseline:6  | hive-browser dialog respond maybe
fixed:2  baseline:6  | hive-browser script --skill x --script y --timeout-s soon
fixed:2  baseline:6  | hive-browser status --unknown-flag
fixed:6  baseline:6  | hive-browser screenshot           (独立动词，Go 本地校验)
fixed:6  baseline:6  | hive-browser evaluate
fixed:6  baseline:6  | hive-browser type '#i'
fixed:6  baseline:6  | hive-browser help nosuchcmd
```

真 Chrome + 真扩展下的 intent 路径：

```
interact screenshot  (no --intent)  exit=1  {"ok":false,"error":"screenshot requires `intent` — …"}
interact zoom        (no region)    exit=1  {"ok":false,"error":"zoom requires region [x0, y0, x1, y1]"}
interact screenshot --intent …      exit=0
interact zoom --region …            exit=0
```

上游对照（同一批 CLI 级用例，用上游 `python -m gcu.cli` 实测）：无参数 / 未知子命令 /
缺 `--action` / 非法 choice / `--coordinate` 格式错 / 缺 positional / 缺 `--value` /
缺 `--file` / 缺 `--width` / 非法 `--mode` / `tab_id` 非整数 / 非法 dialog action 全部 2。
