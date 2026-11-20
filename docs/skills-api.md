# 技能管理 HTTP API（Go 版）

上游事实源：`core/framework/server/routes_skills.py`。前端契约：`go_hive/go-hive/frontend/src/api/skills.ts`。
本表由 `internal/server/routes_skills.go` 实现，只通过下面的入口注册，不在 `app.go` 内接线：

```go
mux := http.NewServeMux()
server.RegisterSkillsRoutes(mux, server.SkillsRouteDeps{
    Home:    home,     // hivehome.Home，技能目录/HIVE_HOME 的根
    Manager: manager,  // *server.SessionManager，可空
})
```

`SkillsRouteDeps.UserHome` 只用于测试隔离：非空时覆盖 `os.UserHomeDir()` 作为 `~/.agents/skills` 的扫描根。

## 端点总表

| # | Method | Path | 成功 | 语义 |
|---|--------|------|------|------|
| 1 | GET | `/api/queen/{queen_id}/skills` | 200 | 某个 queen 的技能目录（含 framework/preset/user/queen_ui） |
| 2 | POST | `/api/queen/{queen_id}/skills` | 201 | 在该 queen 的 `skills/` 下写入技能并登记 override |
| 3 | PATCH | `/api/queen/{queen_id}/skills/{skill_name}` | 200 | 开关、参数覆盖、备注、`all_defaults_disabled` |
| 4 | GET | `/api/queen/{queen_id}/skills/{skill_name}/body` | 200 | 该 queen 视角的正文（frontmatter 已剥离） |
| 5 | PUT | `/api/queen/{queen_id}/skills/{skill_name}/body` | 200 | 用新正文重写该 queen 的可编辑副本 |
| 6 | POST | `/api/queen/{queen_id}/skills/{skill_name}/rename` | 200 | 重命名该 queen 的副本（目录 + frontmatter + override） |
| 7 | DELETE | `/api/queen/{queen_id}/skills/{skill_name}` | 200 | 删除该 queen 的副本并保留 tombstone |
| 8 | POST | `/api/queen/{queen_id}/skills/reload` | 200 | 重新加载该 queen 的技能目录并扇出到她的 live 会话/colony |
| 9 | GET | `/api/sessions/{session_id}/skill-installs` | 200 | 该会话里等待用户决定的共享库安装请求 |
| 10 | POST | `/api/sessions/{session_id}/skill-installs/{request_id}` | 200 | 批准 / 拒绝一条安装请求 |
| 11 | GET | `/api/skills` | 200 | 聚合技能库 + `visible_to` 反查 |
| 12 | GET | `/api/skills/scopes` | 200 | 选择器用的 queens / colonies 列表 |
| 13 | POST | `/api/skills/upload` | 201 | multipart 上传 `.md` 或 `.zip` 到 user/queen 作用域 |
| 14 | GET | `/api/skills/{skill_name}` | 200 | 跨作用域的技能详情（原始 SKILL.md，含 frontmatter） |
| 15 | POST | `/api/skills/reload` | 200 | Go 扩展：共享库视角的全量扇出，重载每个 live 会话/colony 的目录 |

## 响应字段

### SkillRow（端点 1、2 的行；聚合库的行是它的子集）

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | frontmatter 的 `name`，缺失时回退目录名 |
| `description` | string | frontmatter 的 `description` |
| `source_scope` | string | `framework` / `preset` / `user` / `queen_ui` / `colony_ui` / `project` |
| `provenance` | string | `framework` / `preset` / `user_dropped` / `user_ui_created` / `queen_created` / `learned_runtime` / `project_dropped` / `other` |
| `enabled` | bool | 显式 override 优先；preset 默认关；`all_defaults_disabled` 只关 framework |
| `editable` / `deletable` | bool | provenance 属于 `user_ui_created` / `queen_created` / `learned_runtime` |
| `location` | string | SKILL.md 绝对路径 |
| `base_dir` | string | 技能目录绝对路径（聚合库不下发） |
| `visibility` | string[] \| null | frontmatter `metadata.visibility` |
| `trust` | string \| null | override 的 trust 标记（聚合库不下发） |
| `created_at` | string \| null | override 创建时间，Python `isoformat()` 风格（聚合库不下发） |
| `created_by` | string \| null | 创建者（`X-User`，默认 `ui`；聚合库不下发） |
| `notes` | string \| null | override 备注（聚合库不下发） |
| `param_overrides` | object | override 参数，缺省 `{}`（聚合库不下发） |

### ScopeSkillsResponse（端点 1）

```json
{"queen_id": "ops", "all_defaults_disabled": false, "skills": [SkillRow, ...]}
```

### AggregatedSkillsResponse（端点 11）

```json
{
  "skills": [{
    "name": "hive.writing-hive-skills",
    "description": "...",
    "source_scope": "framework",
    "provenance": "framework",
    "owner": null,
    "visible_to": {"queens": ["ops"], "colonies": []},
    "enabled_by_default": true,
    "editable": false,
    "deletable": false,
    "location": "/.../SKILL.md",
    "visibility": null
  }],
  "queens": [{"id": "ops", "name": "ops"}],
  "colonies": [{"name": "research_one", "queen_id": "ops"}]
}
```

`owner` 只对 `queen_ui` / `colony_ui` 技能非空：`{"type":"queen|colony","id":...,"name":...}`。
`visible_to.queens` 按“该 queen 的 override store 是否启用”计算；colony 继承其 `metadata.json::queen_name` 的 queen 配置。

### SkillScopesResponse（端点 12）

```json
{"queens": [{"id": "ops", "name": "ops"}],
 "colonies": [{"name": "research_one", "queen_id": "ops"}]}
```

### SkillDetailResponse

端点 4（queen 作用域）返回 `body` 为 frontmatter 剥离后的 Markdown，并带 `editable`：

```json
{"name":"ops-a","description":"A skill","source_scope":"queen_ui",
 "location":"/.../SKILL.md","base_dir":"/.../ops-a","body":"## Steps\n1. Check",
 "visibility":null,"editable":true}
```

端点 14（跨作用域）返回 SKILL.md 原文（含 frontmatter），不返回 `editable`：

```json
{"name":"ops-a","description":"A skill","source_scope":"queen_ui",
 "location":"/.../SKILL.md","base_dir":"/.../ops-a",
 "body":"---\nname: ops-a\n...","visibility":null}
```

### SkillInstallRequest（端点 9）

```json
{"id":"<32hex>","name":"house-style","description":"...","body":"...",
 "files":[{"path":"scripts/check.py","content":"print('ok')\n"}],
 "requested_at":"2026-11-20T03:04:05.123456+00:00","replaces":true}
```

`replaces` 为 `<HIVE_HOME>/skills/<name>` 是否已存在。

## 请求体

### 创建（端点 2）

```json
{"name":"ops-runbook","description":"Runbook for ops","body":"## Steps\n1. Check\n",
 "files":[{"path":"scripts/check.py","content":"..."}],
 "enabled":true,"notes":null,"replace_existing":false}
```

- `name`：`^[a-z0-9.-]+$`，≤64 字符，不能以 `-` 开头/结尾或含 `--`。
- `description`：必填，单行，≤1024 字符。
- `body`：必填，去空白后不能为空。
- `files`：可选，路径必须相对技能目录，且不能是 `SKILL.md`。
- `replace_existing` 默认 `false`；同名的 queen 副本存在时返回 409。
- `X-User` 请求头写入 override 的 `created_by`，缺省 `ui`。

### PATCH（端点 3）

```json
{"enabled":false,"param_overrides":{"temperature":0.2},"notes":"hand tune",
 "all_defaults_disabled":false}
```

所有字段可选；`enabled` / `param_overrides` 类型错误返回 400。空 `{}` 会写入一条
`provenance=other` 的 override（与上游一致）。

### PUT body（端点 5）

```json
{"body":"## New body\n","description":"New desc","files":[...]}
```

`body` 必填；`description` 省略时从现有 SKILL.md 的 `description:` 行继承。
只有 provenance 属于可编辑集合的技能能写，否则 403。

### 重命名（端点 6）

```json
{"new_name":"new-name"}
```

### 安装决定（端点 10）

```json
{"decision":"approve"}   // 或 "deny"
```

## multipart 上传（端点 13）

| 字段 | 必填 | 说明 |
|------|------|------|
| `file` | 是 | `SKILL.md` 或 `.zip`（zip 内 SKILL.md 可在根或单层目录内） |
| `scope` | 是 | `user` 或 `queen` |
| `target_id` | `scope=queen` 时必填 | queen id |
| `enabled` | 否 | 缺省 `true`；大小写不敏感，仅 `"false"` 为假 |
| `replace_existing` | 否 | 仅 `"true"` 为真，缺省 `false` |
| `name` | 否 | 覆盖从 frontmatter / 文件名推断的技能名 |

成功响应：

```json
{"name":"from-upload","installed_path":"/.../skills/from-upload",
 "replaced":false,"scope":"user","target_id":null,"enabled":true}
```

`scope=user` 写入 `<HIVE_HOME>/skills` 且不写 override；`scope=queen` 写入
`<queen>/skills` 并登记 `user_ui_created` override。

## 错误码

| 状态 | 场景 | 典型 `error` |
|------|------|--------------|
| 400 | 非法技能名 | `skill_name 'X' must match [a-z0-9-] pattern` |
| 400 | 缺少描述 / 正文 | `skill_description is required` / `skill_body is required — ...` |
| 400 | PATCH 类型错误 | `'enabled' must be a bool` / `'param_overrides' must be an object` |
| 400 | PUT 空正文 | `'body' is required` |
| 400 | 非法 JSON | `invalid JSON body` |
| 400 | 安装决定非法 | `decision must be 'approve' or 'deny'` |
| 400 | 上传非 multipart | `expected multipart/form-data` |
| 400 | 上传缺少文件 / scope 非法 | `missing 'file' part` / `scope must be user|queen` |
| 400 | queen 上传缺 target | `target_id required for scope=queen` |
| 400 | 上传内容非法 | `SKILL.md must start with '---' frontmatter`、`invalid zip file`、`upload is not valid UTF-8` 等 |
| 403 | 改写 / 重命名 / 删除只读技能 | `skill 'X' is not editable (provenance=framework)` |
| 404 | queen 不存在（列表） | `queen 'X' not found` |
| 404 | queen 不存在（其余 queen 端点） | `queen not found` |
| 404 | 技能不存在 | `skill 'X' not found` |
| 404 | 重命名时没有该 queen 的副本 | `skill 'X' has no editable copy for this queen` |
| 404 | 会话 / 安装请求不存在 | `session not found` / `no skill install request X` |
| 409 | 同名技能已存在 | `skill 'X' already exists` |
| 409 | 安装请求已决定 | `skill install request X was already approved` |
| 413 | 上传超过 2 MiB | `upload exceeds 2097152 bytes` |
| 422 | 已存请求不再是合法技能 | `skill install request X is no longer a valid skill: ...` |
| 500 | 文件读写失败 | 透出底层错误 |

## 实时 reload（端点 8 / 15）

技能目录没有任何 watcher：Skills 页、queen 的 `write_skill`、共享库安装都靠一次显式
reload 让运行中的 catalog 跟上磁盘。两条 reload 端点都返回一个 `live` 块，描述真正
被重建的 runtime：

```json
{
  "ok": true, "kind": "queen", "target": "ops",
  "live": {
    "runtimes": 2, "reloaded": 2, "failed": 0, "errors": [],
    "targets": [
      {"target": "colony:research_one", "stats": {"catalog_chars": 1647, "skill_dirs": ["/.../skills"]}},
      {"target": "session:session_20261120_221843_0fdbb77c", "stats": {"catalog_chars": 28910}}
    ]
  }
}
```

- `runtimes` 是快照到的 live runtime 数；`reloaded` / `failed` 是逐 runtime 结果，
  `errors` 每条为 `"<target>: <错误>"`。单个 runtime 失败时 HTTP 仍是 200。
- `targets[].target` 形如 `session:<session_id>` / `colony:<colony_id>`；`stats` 是该
  runtime 的 `ReloadSkills` 返回值（queen 会话只有 `catalog_chars`，colony 还有
  `skill_dirs`）。
- **queen 过滤**：`POST /api/queen/{id}/skills/reload` 只重载 `Session.QueenName == id`
  的会话与其 colony；`Session.QueenName` 为空的会话（例如 `serve --colony` 预载会话，
  记录里没有 queen 名）只会被 `POST /api/skills/reload` 的全量扇出命中。
- **生命周期**：`StopSession` 先把会话标记为 closed 再拆 runtime；reload 快照到一半
  被关闭的会话不再重载（`sessionLiveSkills.Reload` / `sessionColonyReloader`
  在 closed 时直接返回）。
- **colony worker**：worker 的 prompt 通过 `DynamicSkillsCatalogProvider` 每次迭代读
  colony 的 catalog memo，reload 换掉 memo，因此已在运行的 worker 下一轮就看到新技能；
  新建 worker 仍然在 spawn 时从磁盘重新渲染。
- **queen 会话**：loader 把 `queenSkillsCatalog` 同时注入 `ChatRuntimeDeps.SkillsCatalog`
  和 `Session.LiveSkills`，reload 重建的是同一个 manager；`skills_catalog` 提醒源按
  catalog 签名变化重新播报，所以旧会话不会继续携带 stale catalog。

## 与上游的差异

1. **reload 的实时扇出**：上游 `SessionManager` 直接满足 `LiveSessionManager`；
   Go `SessionManager.ListSessions()` 返回 `[]*Session`（与接口的
   `[]LiveQueenSession` 签名冲突），因此由 `SessionManager.LiveSkills()` 返回
   `internal/server/live_skills.go` 里的适配器。语义与上游一致：queen 作用域只重载
   该 queen 的会话与 colony，不带 queen 时重载全部；单个 runtime 失败只记录并继续，
   不阻断 HTTP 响应。Go 侧额外返回 `live` 统计（见下），并跳过已关闭的会话。
2. **queen_id 路径约束**：Go 侧额外校验 queen id 不能逃出 `<HIVE_HOME>/queens`
   （`..`、绝对路径等返回 404）；上游直接拼接路径。
3. **聚合行的字段集合**：严格按上游只下发 `name/description/source_scope/provenance/
   owner/visible_to/enabled_by_default/editable/deletable/location/visibility`；
   `enabled`、`base_dir`、`trust`、`created_at`、`created_by`、`notes`、
   `param_overrides` 是 queen 作用域字段，只在端点 1/2 的行里出现。前端 `SkillRow`
   把这些字段标为可选，browse 视图不读取 `enabled`。
4. **zip 条目大小**：整个上传限 2 MiB，且单个解压条目超过 2 MiB 也会被拒绝（上游只
   限制压缩包字节数，不限制解压后大小）。
5. **非对象 JSON 请求体**：返回 400 `invalid JSON body`；上游对数组/标量会在取
   `payload.get` 时抛异常变成 500。
6. **时间戳**：技能行的 `created_at` 按 Python `datetime.isoformat()` 渲染
   （`+00:00`，微秒非零时保留 6 位）；安装请求的 `requested_at` 仍沿用
   `internal/skills/install_requests.go` 的 RFC3339Nano（`Z` 结尾）。
7. **默认 queen**：上游 v3 的 `ensure_default_queens()` 是 no-op，本实现同样要求
   queen 目录已存在（queen profile 由 queens 路由/会话创建按需物化），因此冷安装下
   `GET /api/queen/<default_id>/skills` 与上游一样先 404，直到该 queen 首次被使用。
8. **multipart 解析失败**：返回 400 `expected multipart/form-data` / `invalid
   multipart body`；上游由 aiohttp 抛 500。
9. **framework / preset 技能的 `location`**：Go 构建把这两个内置包释放到进程级临时
   目录后再扫描，因此 `location` / `base_dir` 指向该临时目录；上游指向包内源目录。
   内容与 `source_scope` 不受影响。
10. **reload 的结果字段与全量入口**：端点 8 在上游的 `ok/target/kind` 之外多一个
    `live` 块（前端 `skills.ts` 只读 `ok`，多余字段无影响）；`POST /api/skills/reload`
    是 Go 独有入口（上游只有 queen 作用域的重载），用于共享库变更后的一次全量扇出，
    响应为 `{"ok":true,"kind":"library","live":{...}}`。
