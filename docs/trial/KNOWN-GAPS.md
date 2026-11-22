# go_hive 语义一致试用版 · KNOWN-GAPS（2026-11-22）

> 版本：`hive 1.0.27-go.1`（构建批次见包内 TRIAL-MANIFEST；go1.26.11 工具链）。
> 判定口径：**语义一致 = 模型 / 客户端 SDK / 持久化数据与 resume / CLI 输出与退出码**四类观测者可见的面逐字一致或显式登记；四者都不可见的内部机制（goroutine vs asyncio、EventBus 异步派发等）属实现自由。

## 1. 本版已验证（实测证据）

| 项 | 证据 |
|---|---|
| 九道门禁 + govulncheck | wave-66 全绿：`fmt/vet/test/cross/parity/parity-targets/wiring/compat/arch` = 0；govulncheck **0 OSV**（同 scanner/DB 23→0） |
| 测试/parity | **5,722** 个测试全部带 parity 注解；refs 0 missing |
| QC | **9/9**；C12 模块清单 273 = 216 ported / 27 partial / 9 oos / 21 unported |
| 兼容契约 | compat **12/12** |
| 架构 | 82 包 / 219 边 / 8 层 / 1 info |
| 崩溃恢复 | 真实 kill -9：7/7 part 字节一致、events append-only、park 跨重启保留；cold_interrupted 生产者已对齐（上游探针 2 passed） |
| SSE | 42/42 帧一致；并发浸泡 50/50、SSE 53/53 无丢帧 |
| 工具面 | desc 47/47 逐字；schema raw 25/47、归一 33/47（差异已登记） |
| 记忆系统 | memory v2 + 1.0.15 notes 全族（推送/索引/log.jsonl/恢复）；recall 索引 15k 轮：冷建 388.5ms、warm p50 123.8ms（3s 上限内）、索引 7.05MB |
| 存储/迁移 | Python home v1/v2/v3 启动迁移 PASS；SC-06 sidecar API 双跑 0 差异；SC-04 权限已对齐（sessions 树 0600） |
| 语义评测 | LongMemEval n=60：Go 86.7% vs Py 85.0%（McNemar p=1.000）；n20+n60 并集 77 题 Go 66/77 vs Py 67/77 |
| 安装 | install.sh 六平台归档可装；Docker linux 真机 20/20 |

## 2. 外部未验证（无环境/凭据，不冒充已验证）

1. **Windows 实机**：交叉编译通过，未在 Windows 真机跑 E2E。
2. **GitHub Actions 真实执行**：workflow 与 `go-version-file` 已就绪，仓库无写权限未实跑。
3. **Sentinel 真实投递**：升级分类/记录/索引已实现并测试；Telegram/Slack/webhook 无凭据未投递。
4. **91 个第三方集成**：实现与契约在，均无真实第三方凭据，未做线上调用验证（`web_search` provider 同）。
5. **真实 MCP 服务器**：协议面双跑一致；未对接外部真实 MCP 服务验证。

## 3. 已登记的设计性差异（不修或按计划修）

- **浏览器渲染字节**：Skia vs PIL 的 JPEG 字节、字体度量标签盒、alpha、dpr 注解宽度（dpr 已修，字节级永久差异）。
- **recall 索引**：JSON 索引 vs 上游 SQLite（语义等价；性能见上）。
- **background 记忆模式**：仍同步挑选（上游异步流水线）。
- **playbook 父侧 900s 静默守护**：保护性差异。
- **EventBus 异步派发**：观测等价（SSE 42/42）。
- **版本双轨**：Go `1.0.27-go.1` / Python 参考 `1.0.15`，独立演进。
- **工具 schema 归一**：raw 25/47、归一 33/47（其余为可选字段/描述差异，逐条在 `tool-surface` 登记）。
- **`attached_credentials.json`**：Python-only（会话固定凭据），Go 无读写；不阻塞启动/迁移。
- **cold_interrupted 的 `[System]` 注记**：Go 恢复时不自动续跑回合，下一回合从用户消息开始。
- **colony sidecar 权限**：Go 原子写更严（0600 vs 0644），不放宽。

## 4. 已知功能缺口（按优先级，已在 backlog）

- `check-agent` / `resync` 凭据端点（P1，lane 修复中）。
- `attached_credentials.json` 会话固定凭据（P2）。
- `sentinel_setup` 工具、antigravity provider、GA 工具迁移（P2/P3）。
- 21 条真未移植模块（清单见 `docs/unported-triage.md`）。

## 5. 试用建议

- 核心路径（chat / colony / worker / memory notes / playbook / skill / MCP / CLI / 迁移）可放心试用；遇问题带 `~/.hive-go/logs/` 与复现步骤反馈。
- 外部集成类功能在配置真实凭据前请按「未验证」对待。
