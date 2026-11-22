# QA 复盘：界面“创建蜂群”静默失败（2026-11-23）

> 事件等级：P1（核心用户旅程不可用、无错误提示）。根因与防再发措施均已落盘。

## 1. 事件与影响

- 用户在本地 UI「创建蜂群」弹窗提交后：无成功、无报错、无新 colony。
- 实测：`POST /api/sessions {"colony_id":"<new>","queen_name":"queen_growth","initial_phase":"colony"}` → `404 {"error":"Colony not found: <new>"}`；磁盘无新目录；而所有门禁仍全绿（wave-79：CI 0、QC 9/9、parity 5,841）。

## 2. 根因（五条，均为流程问题而非单点失误）

1. **测试从实现出发，而不是从上游矩阵/用户旅程出发。**
   上游 `session_manager.py` 对 `POST /api/sessions` 定义了 4 条分支（无 colony / 已存在 colony / 缺目录+fork 源 / 缺目录 bootstrap 最小 colony），并有 docstring 逐条写明；Go 只实现了前三条，第 4 条返回 404。测试跟着 Go 的实现写，**缺失分支因此没有测试**，`// Parity:` 注解只指文件不指分支语义。
2. **门禁没有“用户旅程”层。**
   C1–C9 是结构性检查（env/config 计数、路由冻结、证据锚点），C11 是 CLI 外观，C6 的“有效缺失 0”只说明**路由存在**，不说明**语义正确**。没有任何门禁真正“点一次创建蜂群”。
3. **Fake deps 掩盖装配缺口。**
   路由测试大量使用注入的 `SessionRouteDeps` 假实现；bootstrap 行为落在 loader/装配层，假实现下永远不会暴露“未接线/未实现”。
4. **前端错误静默放大影响。**
   Create Colony 的 catch 只有 `console.error`（上游同款），用户侧零反馈，故障被发现的时间被显著拉长。
5. **汇报口径把“结构绿”误读成“产品可用”。**
   “CI 0 / QC 9/9 / parity 5,841”是**回归与结构**指标；被当成“功能完成”的证据使用，是本次复盘要纠正的口径问题。

## 3. 防再发（质量保障改造）

| 编号 | 措施 | 落点 | 何时跑 |
|---|---|---|---|
| Q1 | **API 旅程门禁**：临时 HIVE_HOME + 独立端口，真实 `hive serve`，跑 ①建 queen DM 会话 ②建新 colony（bootstrap）③列 colonies/sessions ④colony 页面路由 200 ⑤建/停 timer ⑥清理；断言 HTTP 状态+磁盘布局 | `go_hive/scripts/journey_e2e.py`（新增） | 每个 wave 发布前 |
| Q2 | **UI 浏览器 E2E**：Orca 内置浏览器真实点击「创建蜂群」→ 断言 201/侧栏出现/跳转 colony 页面/无 console error | 证据落 `docs/e2e-assets/ui-e2e/`（脚本+截图） | UI 变更或每 2 个 wave |
| Q3 | **上游矩阵测试**：对 docstring 的每个分支写 parity 测试（真实 deps，不用 fake），注解指到上游符号 | 各包 `*_test.go` | 每次 CI |
| Q4 | **报告分栏**：结构门禁（CI/QC/parity）与功能门禁（journey/UI）分开汇报，禁止合并成“全绿” | 夜间/交付报告模板 | 每次汇报 |
| Q5 | **静默失败清单**：前端 catch-only 路径登记（console.error 无 UI 反馈），作为已知 UX 差异跟踪 | `docs/alignment-differences.md` | 每轮对齐复核 |

## 4. 本次修复

- lane `colony-bootstrap`：按上游 `_ensure_minimal_colony` 逐字段实现 bootstrap 分支（metadata.json / worker.json / scaffolded 例外 / fork 与既有 colony 分支不变），补分支 parity 测试。
- lane `journey-e2e`：实现 Q1 门禁脚本，并先在当前 main 上跑出**预期失败**（证明门禁能抓到该 bug），修复合并后复跑转绿作为证据。
- 修复合并后由 Orca 浏览器执行 Q2，证据落盘。

## 5. 结论

门禁“全绿”只证明**已测的面没退化**；这次暴露的是**未测的面**。Q1/Q2 的作用是把“用户真正会点的路径”纳入每次发布前的必跑项；Q3 把上游 docstring 的每个分支变成测试义务；Q4 修正汇报口径，避免再次用结构指标代替功能证据。
