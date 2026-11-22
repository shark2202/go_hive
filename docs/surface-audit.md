# API / 仓库表面审计

- 生成时间：2026-11-22T08:02:00+08:00
- 根目录：`/Users/mac/hive`
- 豁免清单：`/Users/mac/hive/go_hive/scripts/surface_audit_allowlist.json`
- 重新生成：`uv run python go_hive/scripts/surface_audit.py --write-report`

本报告只回答一个问题：**文档/前端/上游声明的入口，在 Go 运行时里是否真的可达。**
空目录、lane 命名的落地文件、以及指向幻影文件的规模豁免同样计入。
归档是否携带 `hive`/`hive-cred`/`hive-browser`/`browserbridge` 由 `qc_check.py` 的 C4c 守护，本审计不重复。

## 摘要

| 项 | 数量 |
| --- | ---: |
| 前端路由（去重） | 160 |
| Go 已注册路由 | 220 |
| Python 上游路由 | 207 |
| 前端 → Go 缺失 | 0（待修 0） |
| Python → Go 缺失 | 0（待修 0） |
| Go 多余（仅提示） | 11 |
| 空目录 | 0 |
| batch/lane 命名文件（未豁免） | 0 |
| 悬空 size waiver | 0 |

**结论：通过**

## 前端 → Go 缺失

无。

## Python → Go 缺失（按前缀分组）

无。

## Go 多余（仅提示，不阻断）

| 方法 | 路径 | 来源 |
| --- | --- | --- |
| GET | `/api/browser/status` | routes_app_extra.go |
| GET | `/api/browser/status/stream` | routes_app_extra.go |
| POST | `/api/browser/tab/reveal` | routes_app_extra.go |
| GET | `/api/channels` | routes_credentials_channels.go |
| GET | `/api/health` | app.go |
| GET | `/api/health/resources` | routes_app_extra.go |
| POST | `/api/mcp/reload` | routes_mcp.go |
| POST | `/api/queen-mail/letters` | routes_queens_mail.go |
| GET | `/api/queen-mail/queens` | routes_queens_mail.go |
| GET | `/api/sessions/{param}/reminders` | routes_reminders.go |
| POST | `/api/skills/reload` | routes_skills.go |

## 仓库卫生

### 空目录

无。

### batch/lane 命名文件

无待修 / 无豁免项。

### 悬空 size waiver

无。

## 已知待修与豁免的口径

- **待修**：缺失属实，`--strict` 会阻断；目前 skills 路由整组归 `skills-api` lane。
- **已豁免**：确认不属于 Go 主服务表面（独立进程/上游 Python 专有），理由写入
  `go_hive/scripts/surface_audit_allowlist.json`，并在上表逐条可见。
- 新增豁免必须带 `reason`；空理由视为静默豁免，评审应拒绝。

