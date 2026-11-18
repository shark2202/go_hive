# R5 安全审查报告（w5/sec）

- 审查对象：`go_hive/go-hive`（Go 纯离线运行时）
- 分支：`w5/sec`
- 环境：macOS，无外网；只读代码 + 本地探针/单元测试，未发起真实网络请求
- 门禁：`cd go_hive/go-hive && make ci` → **0**（fmt-check / vet / test / cross / parity / parity-targets 全过）
- 测试总数：3045（新增安全回归用例 21 个），parity 注解 3045/3045

## 结论摘要

本轮确认并修复 **14 类可复现缺陷**，其中 4 类高严重度：调用方自定义 `session_id` 的路径穿越、report-bundle 的归档/临时文件越权、skills 安装的路径穿越与 git 参数注入、history 删除/打开接口的校验绕过。另有 8 项残余风险已记录（主要为本地攻击面、tar 解压体量、DNS rebinding TOCTOU），未在本轮扩大改动。

威胁模型说明：桌面版默认只监听 `127.0.0.1`，未设置 `HIVE_DESKTOP_TOKEN` 时 API 无鉴权。因此“路径/注入”漏洞在本机浏览器、被 prompt 注入的 agent、同机其他进程仍有实际影响；本轮同时增加了“非 loopback 监听必须配置 token”的 fail-closed 逻辑。

---

## 一、已修复缺陷

### W5-SEC-01（High）自定义 `session_id` 路径穿越

- 位置：`internal/server/routes_session_create.go:99`、`internal/cli/loader.go:431`、`internal/session/session.go:120`
- 问题：`POST /api/sessions` 允许 body 传 `session_id`；CLI 的 `createChatSession` 直接
  `filepath.Join(home.QueensDir(), "default", "sessions", sessionID)`，并把该目录作为
  `ChatRuntime` 的 `Root`（会话记录、工具产物都写在这里）。传 `../../..`、`a/b`、NUL
  即可把运行时根目录挪到会话树之外，形成任意目录写入/读取。
- 原有防护：只有 mux 对 URL path 的清理；body 值完全未校验。
- 修复：
  - `session.ValidSegment` 统一限制“单路径段”：非空、≤255、非 `.`/`..`、不含 `/`、`\`、NUL；
    `Store.Dir` 复用该规则。
  - `handleCreateSession` 在调用 hook 前同时校验 `session_id` 与 `queen_resume_from`，非法返回 400。
  - `createChatSession` 二次校验（防御其他调用方绕过 HTTP 层）。
- 回归：`TestValidSegmentAllowsCustomIDsButRejectsTraversal`、
  `TestCreateSessionRejectsTraversalSessionID`（6 种载荷均 400 且不触发 hook，安全自定义 ID 仍 201）。
- 残余：自定义 ID 仍允许任意安全字符（保持既有 “my-custom-session” 兼容），只限制路径语义。

### W5-SEC-02（High）report-bundle 归档前缀、临时目录与权限

- 位置：`internal/server/routes_session_report.go:17`、`internal/sessionreport/session_report.go:146`、`:268`
- 问题：
  1. `sessionID` 直接拼进 tar 成员前缀 `session_<id>/...` 和下载文件名；含 `../` 时归档条目可越出解压目录（tar-slip）。
  2. bundle 固定写到 `/tmp/hive-session-reports/`（0755），文件 0644、名字可预测；同机其他用户可读会话转录。
  3. `collectSessionFiles` 用 `os.Stat`（跟随符号链接）判断普通文件，session 目录内的符号链接会把外部文件打进 bundle。
- 原有防护：仅 `data/reports` 目录排除；无 ID/符号链接校验。
- 修复：
  - `validArchiveSessionID` 校验归档前缀；非法 ID 拒绝生成。
  - 每次生成使用 `os.MkdirTemp(..., "hive-session-reports-")`（0700），bundle 文件 0600。
  - `collectSessionFiles` 改用 `DirEntry.Info()`（lstat 语义），只收普通文件，跳过符号链接。
- 回归：`TestBuildSessionReportBundleRejectsUnsafeSessionID`、
  `TestBuildSessionReportBundleSkipsSymlinkedFiles`；
  `TestReportBundleRouteWritesMarkerAndScrubbedArchive` 新增 0600/0700 权限断言。
- 残余：文本类文件仍用 `os.ReadFile` 全量读取，超大文本可造成内存压力（见 R-02）。

### W5-SEC-03（High）skills 安装/删除/分叉的路径穿越与 git 参数注入

- 位置：`internal/skills/installer.go:96`、`:125`、`:217`、`:245`、`:315`
- 问题：
  1. `InstallFromGit` / `RemoveInstalledSkill` / `ForkSkill` 用调用方提供的 skill name 直接
     `filepath.Join(skillsDir, name)`；`--name ../../victim` 可写/删 skills 根之外。
  2. `--subdir` 直接 `filepath.Join(tmpDir, subdirectory)`；`../../etc` 可把任意目录当技能源拷贝进 skills。
  3. `git clone ... <gitURL> <target>` 没有 `--` 分隔；URL/分支以 `-` 开头时可注入
     `--upload-pack=<cmd>` 这类 git 选项，导致命令执行。
- 原有防护：无。
- 修复：
  - `ValidSkillName`：单段、非 `.`/`..`、不含分隔符/NUL；三处入口统一校验。
  - `resolveInstallSource`：拒绝绝对路径、`..` 越界，并对已存在的 subdir 做 `EvalSymlinks` 后再确认在 clone 根内。
  - `gitCloneArgv`：拒绝以 `-` 开头的 URL/ref，参数改为 `git clone --depth=1 [--branch V] -- <url> <dst>`。
- 回归：`TestSkillInstallPathsCannotEscape`（先放哨兵文件，确认删除失败且文件仍在）、
  `TestSkillInstallSubdirectoryStaysInsideClone`（`../outside`、嵌套 `..`、绝对路径、符号链接）、
  `TestGitCloneArgvSeparatesOptions`（`--upload-pack=...` 被拒，正常参数含 `--`）。

### W5-SEC-04（Medium）Sentinel `colony_id` / escalation 路径穿越

- 位置：`internal/server/routes_sentinel.go:193`、`:211`、`:300`；
  `internal/sentinel/store.go:183`、`:200`、`:466`
- 问题：`/api/colony/{colony_id}/notifications` 的 `colony_id` 直接进入
  `filepath.Join(ColoniesDir, colonyID, "notifications.json")`；store 的
  `NotificationsConfigPath` / `EscalationsDir` 同样未校验。Mux 会清理点段，但 Windows
  反斜杠、以及内部调用方（配置/升级记录）仍可越界。
- 原有防护：仅路由层无校验；`LoadNotificationsConfig` 对坏文件降级，但不阻止路径拼接。
- 修复：路由层 `validateColonyName`（`[a-z0-9_]+`）先拒绝；store 层
  `validColonyStoreID` 把非法 ID 导向 `colonies/__invalid__/...`，保证路径不越出 colonies 根。
- 回归：`TestSentinelRejectsInvalidColonyID`（大写、`-`、`.` 均 400）、
  `TestStoreRejectsTraversalColonyID`（路径 `Rel` 检查 + 合法 `c1` 仍正常）。

### W5-SEC-05（Medium）Queen 档案/头像路径穿越

- 位置：`internal/queen/profiles.go:169`、`:308`；`internal/server/routes_queens_avatar.go:34`、`:115`、`:143`
- 问题：`LoadQueenProfile` / `QueenHasAvatar` / 头像上传、读取、删除都用
  `filepath.Join(queensDir, queenID)`；`queen_id` 可来自 URL 或 body（`queen_resume_from`），
  未校验时可读写 queens 根之外。
- 修复：新增 `queen.ValidQueenID`，`LoadQueenProfile` 与 `QueenHasAvatar` 入口拒绝；
  三个头像 handler 先校验再拼路径。
- 回归：`TestLoadQueenProfileRejectsTraversalID`（确认外部 `profile.yaml` 不会被读取）。

### W5-SEC-06（Medium）qmail 信件 ID / Claude queen 目录穿越

- 位置：`internal/qmail/store.go:42`、`:54`、`:69`；
  `internal/qmail/conversations.go:63`、`:74`、`:281`
- 问题：`Store.Load/Save` 用 `id + ".json"` 直接拼路径；`queue.jsonl` / `inbox.jsonl`
  被本地篡改后可读写 letters 目录之外。`ClaudeStore.Home` 直接拼配置里的 queen id。
- 修复：`validLetterID`（单段、≤128、非 `.`/`..`、不含分隔符/NUL）用于 Save/Load 与
  inbox 行读取；`validClaudeQueenID` 让非法 queen 落到 `claude_code/__invalid__`。
- 回归：`TestStoreRejectsTraversalLetterID`、`TestStoreRoundTrips...` 增加信件文件 0600 断言。

### W5-SEC-07（Medium）会话目录解析缺少统一入口校验

- 位置：`internal/server/session_dirs.go:14`、`:41`、`:57`
- 问题：`FindQueenSessionDir` / `IterQueenSessionDirs` 本身不校验 `sessionID`，只依赖调用方。
  多个路由（附件、报告、历史删除等）把 URL 值传入；一旦调用方漏检（或 mux 规则变化），
  就会在 `.../sessions/<id>` 上越界。附件路由原先还会在 `Find...` 失败后把空字符串当目录，
  导致相对当前工作目录的 `data/attachments`。
- 修复：两个入口统一 `validSessionDirID` 并返回错误；附件/报告 handler 不再忽略错误，
  非法 ID 返回 400/404，空路径不再参与拼接。
- 回归：既有 `internal/storage/path_traversal_test.go` + 新增 server 侧用例；
  `QueenDMSessionDir` 原有限制保留。

### W5-SEC-08（Medium）密钥/凭据落盘权限

- 位置：`internal/credentials/storage.go:108`、`:272`；`internal/senders/store.go:56`；
  `internal/hivehome/home.go:68`；`internal/sentinel/token.go:88`、`:123`；
  `internal/qmail/store.go:197`
- 问题与修复：
  - `EncryptedFileStorage.ensureDirs` 原为 0755；`index.json` 原为 0644。改为 0700（并 chmod 已存在目录）、
    index 0600；`.enc` 原本就是 0600。
  - senders 目录原仅“新建时 0700”，已存在 0755 不收紧；persist 现在显式 chmod 0700。
  - `hivehome.EnsureDirs` 原把所有目录建成 0755（含 `secrets/`、`credentials/`）；现在这两个目录 0700 并对旧目录 chmod。
  - `sentinel_secret` 文件原本 0600；新增 Lstat 拒绝符号链接，避免 `O_TRUNC` 写穿到任意目标。
  - qmail 信件/队列文件原 0644（正文含私信）；改为 0600。审计日志仍 0644（只含元数据）。
- 回归：`TestEncryptedStorageUsesPrivateModes`、`TestStoreCreatesAndReloadsSenders`（目录 0700）、
  `TestResolveEnvOverride`（secrets/credentials 0700）、
  `TestSecretFileIsPrivateAndRefusesSymlink`、qmail 0600 断言。

### W5-SEC-09（Medium）日志与错误中的密钥泄漏

- 位置：`internal/server/app.go:381`、`:395`；
  `internal/sentinel/notifier.go:207`、`:214`、`:251`、`:288`
- 问题：
  1. access log 记录完整 `RequestURI`，OAuth 回调 `?code=...`、webhook `?token=...` 会原样落日志。
  2. Sentinel 发送失败时把 `err.Error()` 直接写日志/返回；`net/http` 的 `*url.Error` 会包含
     `https://api.telegram.org/bot<TOKEN>/sendMessage`，即 bot token 泄漏。
- 修复：
  - access log 对 `token/access_token/refresh_token/code/key/api_key/secret/password/signature/...`
    及 `*_token`/`*_secret`/`*_key`、`*-token` 等查询参数值替换为 `REDACTED`，保留其余 query 便于排障。
  - `redactSecret` 在 Telegram/Slack/Hive 发送失败的错误与日志中把 token/JWT 替换为 `[REDACTED]`。
- 回归：`TestAccessLogRedactsSensitiveQueryValues`、`TestSensitiveQueryKeyMatchesSuffixes`、
  `TestNotifierRedactsTokenFromTransportErrors`。
- 残余：path 段内的秘密（如 webhook 自定义路径里的 token）仍会进 access log（R-06）。

### W5-SEC-10（Medium）HTTP 输入长度、超时与非 loopback 鉴权

- 位置：`internal/server/http_limits.go:12`、`:22`；`internal/server/routes_session_history.go:24`；
  `internal/server/routes_messages_extra.go:74`；`internal/server/webhook_server.go:68`、`:130`；
  `internal/server/routes_session_attachment.go:172`；`internal/cli/loader.go:246`、`:259`
- 问题：
  - 通用 JSON 解码 `io.ReadAll(r.Body)` 无上限；webhook 也无上限。
  - 附件上传 multipart 无体量上限，失败还留下半写文件。
  - `http.Server` 未设 `ReadHeaderTimeout` / `IdleTimeout` / `MaxHeaderBytes`（Slowloris/慢速头）。
  - `--host 0.0.0.0` + 未设 `HIVE_DESKTOP_TOKEN` 时 API 完全无鉴权。
- 修复：
  - JSON API 统一 8 MiB cap（`readLimitedBody`），超限 `errBodyTooLarge`；webhook 10 MiB（413）；
    附件上传 100 MiB（`http.MaxBytesReader`），超限删半成品并返回 413。
  - 主 server 设 `ReadHeaderTimeout=10s`、`IdleTimeout=120s`、`MaxHeaderBytes=1 MiB`；
    webhook server 另加 `ReadTimeout=30s`。
  - 非 loopback 监听且未配置 `HIVE_DESKTOP_TOKEN` 时拒绝启动（fail-closed）。
- 回归：`TestReadLimitedBodyRejectsOversizedJSON`、`TestLoopbackListenHost`；
  既有 server 测试确认正常请求不受影响。
- 残余：multipart 的每个 part 仍可很多（总字节受限）。

### W5-SEC-11（Medium）附件读取/写入与报告打包的符号链接逃逸

- 位置：`internal/server/routes_session_attachment.go:277`、`:210`；
  `internal/sessionreport/session_report.go:268`、`internal/attachments/attachments.go:154`
- 问题：
  - `regularFile` 原用 `os.Stat` 跟随符号链接，attachments 目录里放一个指向 `/etc/...` 的链接
    即可通过 `/attachment/<name>` 读取任意同用户可读文件。报告打包同理（已并入 SEC-02）。
  - 上传路径用 `os.Create`，若攻击者预先放了指向不存在目标的符号链接，`os.Create` 会跟随它创建外部文件。
- 修复：
  - 读取改用 `os.Lstat`，只接受普通文件；报告侧 `DirEntry.Info()`。
  - 命名冲突判断 `pathExists` 改用 `Lstat`（把符号链接视为已存在），写入改为
    `O_CREATE|O_EXCL`；冲突时重新消歧命名，绝不跟随链接。
- 回归：`TestSessionAttachmentDoesNotFollowSymlink`、`TestUploadAttachmentDoesNotFollowSymlink`。

### W5-SEC-12（Medium）PDF URL SSRF：DNS 解析与重定向

- 位置：`internal/tools/integrations/batch3_pdf.go:443`；
  `internal/tools/integrations/batch3_scrape.go:302`；`internal/tools/integrations/integration.go:139`
- 问题：`pdf_read` 仅做字符串 deny-list（`127.`、`10.`、`192.168.` 等），
  不解析 DNS，也不检查重定向；`https://internal.corp/doc.pdf` 或公网 URL 302 到
  `169.254.169.254` 可绕过。`web_scrape` 已有 DNS+逐跳重定向检查，PDF 没有。
- 原有防护：字面量私网拒绝（保留为快速失败）。
- 修复：`Runtime.ResolveHost` 注入（测试可 stub，默认系统 DNS），PDF 下载前调用
  `batch3CheckURLTarget`；自定义 `CheckRedirect` 对每跳重新检查，最多 5 跳。
- 回归：`TestBatch3PDFReadURLRejectsDNSPrivateAndRedirectTargets`（DNS→10.0.0.7、302→169.254.169.254 均拦截）；
  既有 `TestBatch3PDFReadURLRejectsPrivateHosts` / URL 下载测试保持通过。
- 残余：DNS rebinding TOCTOU（解析与拨号两次解析）在 `web_scrape` 与 PDF 都存在（R-03）。

### W5-SEC-13（High）history 删除/打开接口的校验绕过

- 位置：`internal/server/routes_session_history.go:116`（delete）、`:180`（reveal）
- 问题：两个 handler 在 `FindQueenSessionDir` 返回错误时都有一个 fallback：
  `filepath.Join(QueensDir(), "default", "sessions", sessionID)`，且 fallback 不再校验 `sessionID`。
  `FindQueenSessionDir` 现已拒绝非法段并返回错误，这个 fallback 反而把拒绝变成了原始拼接：
  - `DELETE /api/sessions/history/<traversal>` 可 `os.RemoveAll` 会话树之外的目录；
  - `POST /api/sessions/<traversal>/reveal` 可 `os.MkdirAll` 并在文件管理器打开任意目录。
- 修复：两个 handler 入口先 `validSessionDirID(sessionID)`；`FindQueenSessionDir` 报错直接 400；
  空结果时只在“已校验”的 ID 上回退默认目录；reveal 的 `MkdirAll` 从 0777 降为 0755。
- 回归：`TestDeleteHistorySessionRejectsTraversalID`（哨兵文件仍存在）、
  `TestRevealSessionRejectsTraversalID`（返回 400、未创建/打开越界目录）。

### W5-SEC-14（Medium）session create 的 colony/source ID 与 colony tools 路径

- 位置：`internal/server/routes_session_create.go:126`、`:254`；
  `internal/server/routes_colony_tools.go:60`
- 问题：
  - `colony_id`、`source_session_id` 从 body 直接转发给 `CreateSession` / `CreateColonyFromSource`；
    这些值会进入 `Session.ColonyID`，被 worker 路径、colony 元数据路径使用。恶意值可让路径检查
    指向 `colonies/..` 之外的、恰好存在 `metadata.json` 的目录。
  - `ColonyToolsDeps.colonyExists` 只用 `os.Stat(filepath.Join(colonies, colonyID, "metadata.json"))`，
    未校验 ID；`loadColonyToolsConfig` / `updateColonyToolsConfig` 紧随其后拼接同名路径，形成读写面。
- 修复：`handleCreateSession` 对 `source_session_id` 使用 `session.ValidSegment`，对 `colony_id`
  使用 `validateColonyName`；`colonyExists` 入口先做同一校验。
- 回归：`TestCreateSessionRejectsTraversalColonyAndSourceIDs`、`TestColonyToolsRejectsTraversalColonyID`。

---

## 二、已有防护（复核通过，未改动）

### 1. colony import tar（路径穿越）

- 位置：`internal/server/routes_colonies_import.go:992`（`secureTarget`）、`:1039`（`safeExtractArchive`）
- 结论：已拒绝 `..` 成员、绝对路径、符号链接/硬链接、设备/FIFO；`secureTarget` 会解析最深存在祖先的
  symlink 并确认仍在目标根内；多根导入的 colony/queen/session 名称走 `validateColonyName` /
  `validateColonySessionSegment`。`internal/server/routes_colonies_import_test.go` 覆盖。
- 残余：解压总字节无上限（R-01）、校验与创建之间的本地 TOCTOU（R-05）。

### 2. 附件下载文件名

- 位置：`internal/server/routes_session_attachment.go:82`
- 结论：`filename` 拒绝 `/`、`\`、`..`，再只在候选目录内拼接；路径段由 mux 保证单段。已足够；
  新增的会话 ID 校验与 symlink 防护补上了其余入口。

### 3. run/summary 存储 key

- 位置：`internal/storage/concurrent.go`（`ValidateKey`），测试 `internal/storage/path_traversal_test.go`
- 结论：拒绝绝对路径、分隔符、`.` 开头、`..`、NUL 与 shell 元字符；`SessionStore.GetSessionPath` 同规则。
  本轮未发现绕过。

### 4. webhook 签名

- 位置：`internal/server/webhook_server.go:130`、`:196`
- 结论：secret 非空时校验 `X-Hub-Signature-256`，HMAC-SHA256 + `hmac.Equal`（常量时间）；
  路由表精确匹配 path，方法白名单。无命令执行面。仅补了 body 上限与 server 超时。

### 5. qmail 鉴权与内容

- 位置：`internal/qmail/mail.go:110`（`Authorizes`）、`internal/qmail/ref.go`
- 结论：conversation ref 的 token 是 HMAC-SHA256 + `hmac.Equal`；信件文本进入模型上下文前用
  `Said()` 打断 `]:`，降低“冒名”注入。未发现 shell/路径注入（新增 ID 校验后）。

### 6. terminal 工具

- 位置：`internal/tools/builtin/exec.go:19`、`internal/tools/builtin/terminal_jobs.go:544`
- 结论：`terminal_exec` 按设计用 `/bin/bash -lc` 执行任意命令，是 agent 的“手”，不是注入缺陷；
  `terminal_job_start(shell=false)` 用 `strings.Fields` 直接 exec，不经过 shell；PTY 只允许 bash。
  真正应做的是工具分级/审批与提示词注入防护，而不是对命令做黑名单（R-09）。

### 7. OpenPath

- 位置：`internal/platform/open.go:8`
- 结论：`exec.Command("open"|"explorer"|"xdg-open", path)` 单参数、无 shell；调用方传入的是服务端解析后的
  会话/殖民地路径，不是原始请求字符串。低风险（R-10）。

---

## 三、残余风险与建议（本轮未修）

| ID | 严重度 | 位置 | 说明 | 建议 |
| --- | --- | --- | --- | --- |
| R-01 | Medium | `routes_colonies_import.go:1039` | tar 解压无总字节/单文件上限；2 GiB 压缩包可解压出巨量数据（压缩炸弹） | 增加 `maxTotalExtractedBytes`（如 8 GiB）与单文件上限，`io.Copy` 用 `LimitReader` 并超限中止清理 |
| R-02 | Medium | `sessionreport/session_report.go:172` | 文本文件 `os.ReadFile` 全量读入；超大日志可打爆内存 | 按 `info.Size()` 限制（如 64 MiB/文件），超出记入 `omitted_paths` |
| R-03 | Medium | `batch3_scrape.go:302`、`batch3_pdf.go:443` | DNS 解析与 HTTP 拨号是两次解析，存在 rebinding TOCTOU | 自定义 `DialContext` 固定已校验 IP，或在 transport 层做 IP 校验 |
| R-04 | Low | `routes_colonies_import.go:1000` | `secureTarget` 校验后到 `os.Create/Rename` 之间存在本地 symlink 替换窗口 | 解压到独立临时目录后整体 rename；或使用 `openat`/`O_NOFOLLOW` |
| R-05 | Low | `server/app.go:395` | 只脱敏 query，path 段中的秘密仍会进日志（如 webhook 自定义路径） | 对已知 token 形状（`/bot<digits>:...`）或配置的 webhook secret 做 path 脱敏 |
| R-06 | Low | `server/app.go:378` | `HIVE_DESKTOP_TOKEN` 为空时本地 API 无鉴权 | 保持默认 loopback；已加非 loopback fail-closed；如需远程强烈建议配 token |
| R-07 | Info | `internal/skills/_default_skills/image-generation/SKILL.md` | Go 运行时未实现 `image_generate`/`reference_images`，因此没有对应 SSRF 面 | 实现时复用 `batch3CheckURLTarget` + 重定向逐跳检查，并限制本地路径在会话根内 |
| R-08 | Low | `tools/builtin/exec.go:19` | 终端工具按设计可执行任意命令；被 prompt 注入的模型可直接获得 shell | 依赖工具 tier/审批策略；不要把不可信网页内容直接喂给拥有 terminal 的会话 |
| R-09 | Low | `platform/open.go:8` | `open`/`xdg-open` 可能把以 `-` 开头的 path 当选项；当前路径均为服务端生成绝对路径 | 可加 `--`（macOS `open -- path`、`xdg-open -- path`）作为纵深防御 |
| R-10 | Low | `credentials/storage.go:117` (`safeCredentialID`) | 仅替换 `/`、`\`、`..`，仍允许换行/控制字符进入文件名（不会穿越，但文件名可怪异） | 用 `[A-Za-z0-9._-]` 白名单重写，非法字符统一 `_` |

---

## 四、验证与证据

### 门禁

```text
cd go_hive/go-hive && make ci
→ fmt-check: OK
→ go vet ./...: OK
→ go test ./...: 3045 passed（含新增安全用例）
→ cross: darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64 全部编译通过
→ parity: 3045/3045 带注解
→ parity-targets: OK
exit 0
```

### 关键回归用例（均通过）

- 路径穿越：`TestCreateSessionRejectsTraversalSessionID`、
  `TestValidSegmentAllowsCustomIDsButRejectsTraversal`、
  `TestBuildSessionReportBundleRejectsUnsafeSessionID`、
  `TestSkillInstallPathsCannotEscape`、`TestSkillInstallSubdirectoryStaysInsideClone`、
  `TestStoreRejectsTraversalLetterID`、`TestStoreRejectsTraversalColonyID`、
  `TestLoadQueenProfileRejectsTraversalID`
- 符号链接：`TestBuildSessionReportBundleSkipsSymlinkedFiles`、
  `TestSessionAttachmentDoesNotFollowSymlink`、`TestUploadAttachmentDoesNotFollowSymlink`、
  `TestSecretFileIsPrivateAndRefusesSymlink`
- 权限：`TestEncryptedStorageUsesPrivateModes`、`TestResolveEnvOverride`（0700）、
  `TestStoreCreatesAndReloadsSenders`（0700）、报告 bundle 0600/0700
- 注入/SSRF：`TestGitCloneArgvSeparatesOptions`、
  `TestBatch3PDFReadURLRejectsDNSPrivateAndRedirectTargets`
- 日志：`TestAccessLogRedactsSensitiveQueryValues`、
  `TestNotifierRedactsTokenFromTransportErrors`
- HTTP：`TestReadLimitedBodyRejectsOversizedJSON`、`TestLoopbackListenHost`

### 未做的验证

- 未对真实公网域名做 DNS/HTTP 请求（环境无外网）；SSRF 用例使用注入 resolver 与 httptest。
- 未做动态 fuzzing（tar/HTML/PDF parser）；本轮以代码审计 + 定向单元测试为主。

---

## 五、改动文件

- 新增：`go_hive/go-hive/internal/server/http_limits.go`、`internal/server/http_limits_test.go`、
  `internal/server/access_log_test.go`、`internal/cli/loader_host_test.go`
- 修改：`session`、`server`（会话/附件/报告/colony import/sentinel/webhook）、`sessionreport`、
  `skills`、`queen`、`qmail`、`sentinel`、`credentials`、`senders`、`hivehome`、
  `tools/integrations`（PDF SSRF）、`cli/loader.go`
- 全部改动仅限 `go_hive/go-hive` 内，未触碰其他 lane 的 Python/前端文件。
