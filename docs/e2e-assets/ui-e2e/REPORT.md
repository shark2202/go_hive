# UI E2E：界面创建蜂群（Orca 浏览器实测）

- 时间：2026-11-23 01:18 CST
- 环境：本地 serve `127.0.0.1:8788`（新建 hive-embed，含 colony-bootstrap 修复 `9ffd2c70`）
- 工具：Orca 内置浏览器（tab `9690cbc3-5819-43c2-ba10-38f96c06a11f`）

## 步骤与结果（逐字取实测）

| # | 动作 | 结果 |
|---|---|---|
| 0 | 打开 `http://127.0.0.1:8788` | 侧栏显示「蜂群 3」 |
| 1 | 点击侧栏「创建蜂群」[e8] | 弹窗打开：蜂后列表 6 位、名称输入（placeholder「例如 research_team」）、目标输入、`创建`（初始 disabled） |
| 2 | 选择蜂后 `Steve Jobs` [e84] | 选中 |
| 3 | 填写名称 `ui-e2e-011834`（页面 sanitize 为 `ui_e2e_011834`）+ 目标文本 | 输入生效 |
| 4 | 点击 `创建` [e93] | **成功**：侧栏计数变「蜂群 4」，URL 变为 `http://127.0.0.1:8788/#/colony/ui-e2e-011834` |
| 5 | 控制台检查 | 仅 `[SSE] opening/connected /Users/mac/.hive-go/colonies/ui_e2e_011834 session_20261123_011841_07a35d76`；**无 error** |
| 6 | 磁盘核对 | `~/.hive-go/colonies/ui_e2e_011834/` 存在：`metadata.json`、`worker.json`、`tracker/`、`triggers.json`、`skills/`、`conversations/`、`runs/` |

截图：`colony-created.png`（本目录，创建后落地的 colony 页面）。

## 结论

界面创建蜂群端到端通过（修复前为静默 404）。该步骤已纳入发布前必跑项（复盘 Q2）；自动化门禁（Q1 API 旅程）由 lane `journey-e2e` 落盘。

## 附：清理

- 测试创建的 `ui_e2e_011834` 保留供查看（可在 UI 删除）。
- API 级测试 `qc-verify-011811` 已清理（注：bootstrap 路径接受了含 `-` 的名称，但 `DELETE /api/colonies` 要求 `[a-z0-9_]+`——前端已 sanitize，属边缘口径差异，已记入复盘补充）。
