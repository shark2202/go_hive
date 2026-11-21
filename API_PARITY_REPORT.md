# Go hive vs Python hive — API 行为对比报告

- 生成时间：2026-11-21T16:03:09+08:00
- 仓库：`/Users/mac/hive/.wt/appearance-api-strict`
- 分支 / 提交：`appearance-api-strict` / `f8aa5fe9`
- Python hive：`hive 1.0.14`，端口 `56916`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-89180/python-home`
- Go hive：`hive 1.0.26-go.1`，端口 `56917`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-89180/go-home`
- LLM：provider=`openai` model=`deepseek-flash-1017` api_base=`https://token.tokenbroad.com/v1` key_env=`BROADTOKEN_API_KEY`
- 启动耗时：Python 5.59s / Go 0.26s
- 阶段耗时：静态端点 0.36s → 创建会话 1.65s → 会话端点 0.14s
- 进程存活：Python 正常 / Go 正常

**端点对比：18/18 PASS，0 FAIL。**

> 对比规则：状态码必须相同；JSON 键集合与类型（递归，数组取前 3 个元素）必须相同；
> 动态值（id、时间戳、端口等）不参与比较。`额外字段` 记入差异但仍判 FAIL，
> 因为本报告服务于后续修复，需要看到完整缺口。

## 结论

- 端点契约 18/18 全部通过：状态码相同，JSON 键集合与类型一致，端点级语义检查全部成立。

## 汇总

| # | 端点 | 方法 | 状态码 (Py/Go) | 结果 | 差异数 |
|---|------|------|----------------|------|--------|
| 1 | `/api/health` | GET | 200/200 | **PASS** | 0 |
| 2 | `/api/config/llm` | GET | 200/200 | **PASS** | 0 |
| 3 | `/api/credentials` | GET | 200/200 | **PASS** | 0 |
| 4 | `/api/credentials/specs` | GET | 200/200 | **PASS** | 0 |
| 5 | `/api/queen/profiles` | GET | 200/200 | **PASS** | 0 |
| 6 | `/api/models` | GET | 404/404 | **PASS** | 0 |
| 7 | `/api/models/overview` | GET | 200/200 | **PASS** | 0 |
| 8 | `/api/org-chart` | GET | 404/404 | **PASS** | 0 |
| 9 | `/api/skills` | GET | 200/200 | **PASS** | 0 |
| 10 | `/api/mcp/servers` | GET | 200/200 | **PASS** | 0 |
| 11 | `/api/sessions` | POST | 201/201 | **PASS** | 0 |
| 12 | `/api/sessions` | GET | 200/200 | **PASS** | 0 |
| 13 | `/api/sessions/{id}` | GET | 200/200 | **PASS** | 0 |
| 14 | `/api/sessions/{id}/events` | GET | 200/200 | **PASS** | 0 |
| 15 | `/api/sessions/{id}/events/history` | GET | 200/200 | **PASS** | 0 |
| 16 | `/api/sessions/{id}/snapshot` | GET | 200/200 | **PASS** | 0 |
| 17 | `/api/sessions/{id}/stats` | GET | 200/200 | **PASS** | 0 |
| 18 | `/api/sessions/{id}/tasks` | GET | 404/404 | **PASS** | 0 |

## 端点明细

### 1. GET `/api/health` — PASS

- 状态码：Python `200` / Go `200`
- ✓ status == 'ok' — python='ok' go='ok'

### 2. GET `/api/config/llm` — PASS

- 状态码：Python `200` / Go `200`
- ✓ provider 相同 — python='openai' go='openai'
- ✓ model 相同 — python='deepseek-flash-1017' go='deepseek-flash-1017'
- ✓ has_api_key 两边都为 true — python=True go=True

### 3. GET `/api/credentials` — PASS

- 状态码：Python `200` / Go `200`
- ✓ credentials 是数组
- ✓ credential 数量相同 — python=0 go=0

### 4. GET `/api/credentials/specs` — PASS

- 状态码：Python `200` / Go `200`
- ✓ specs 数量相同 — python=115 go=115
- ✓ credential_id 集合相同 — python 独有 0 个

### 5. GET `/api/queen/profiles` — PASS

- 状态码：Python `200` / Go `200`
- ✓ queen 数量相同 — python=13 go=13
- ✓ queen id 集合相同

### 6. GET `/api/models` — PASS

- 状态码：Python `404` / Go `404`
- 备注：两边都没有 /api/models，真实端点名为 /api/models/overview
- ✓ 两边都未实现 /api/models — python=404 go=404

### 7. GET `/api/models/overview` — PASS

- 状态码：Python `200` / Go `200`
- ✓ llm 槽位 provider/model 相同 — python={'source': 'catalog', 'provider': 'openai', 'model': 'deepseek-flash-1017', 'endpoint': None, 'subscription': None, 'api_base': 'https://token.tokenbroad.com/v1', 'max_context_tokens': None, 'has_extra_body': False} go={'api_base': 'https://token.tokenbroad.com/v1', 'endpoint': None, 'has_extra_body': False, 'max_context_tokens': None, 'model': 'deepseek-flash-1017', 'provider': 'openai', 'source': 'catalog', 'subscription': None}

### 8. GET `/api/org-chart` — PASS

- 状态码：Python `404` / Go `404`
- ✓ 两边都未实现 /api/org-chart — python=404 go=404

### 9. GET `/api/skills` — PASS

- 状态码：Python `200` / Go `200`
- ✓ skill 数量相同 — python=195 go=195
- ✓ skill 名称集合相同 — python 独有 ｜go 独有 

### 10. GET `/api/mcp/servers` — PASS

- 状态码：Python `200` / Go `200`
- ✓ server 名称集合相同 — python=5 go=5

### 11. POST `/api/sessions` — PASS

- 状态码：Python `201` / Go `201`
- ✓ 两边都创建成功 (201) — python=201 go=201
- ✓ 响应都带 session_id — python='session_20261121_160307_6c1d6321' go='session_20261121_160309_fa1efcaa'
- ✓ queen 身份已绑定 (queen_id 非空) — python='queen_growth' go='queen_growth'
- ✓ queen_phase 相同 — python='independent' go='independent'

### 12. GET `/api/sessions` — PASS

- 状态码：Python `200` / Go `200`
- ✓ 两边列表都包含刚建的会话 — python=1 go=1

### 13. GET `/api/sessions/{id}` — PASS

- 状态码：Python `200` / Go `200`
- ✓ session_id 回显正确 — python='session_20261121_160307_6c1d6321' go='session_20261121_160309_fa1efcaa'
- ✓ queen_phase 相同 — python='independent' go='independent'
- ✓ queen_id 相同 — python='queen_growth' go='queen_growth'

### 14. GET `/api/sessions/{id}/events` — PASS

- 状态码：Python `200` / Go `200`
- ✓ 都返回 200 — python=200 go=200
- ✓ Content-Type 都声明 text/event-stream — python='text/event-stream' go='text/event-stream'

### 15. GET `/api/sessions/{id}/events/history` — PASS

- 状态码：Python `200` / Go `200`
- ✓ 历史事件非空（turn 之后） — python total=5 go total=5
- ✓ events 都是数组

### 16. GET `/api/sessions/{id}/snapshot` — PASS

- 状态码：Python `200` / Go `200`
- ✓ activity 相同 — python=None go=None
- ✓ awaiting_input 相同 — python=False go=False

### 17. GET `/api/sessions/{id}/stats` — PASS

- 状态码：Python `200` / Go `200`
- ✓ stats 都非空 — python keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers'] go keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers']

### 18. GET `/api/sessions/{id}/tasks` — PASS

- 状态码：Python `404` / Go `404`
- 备注：状态码 Python 404 / Go 404（该会话尚无任务列表，两边同为 404，属预期一致）
- ✓ tasks 都是数组

## 流程对比（创建会话 → /chat → 落盘 → SSE）

本次运行使用 `--skip-flow`，未执行对话流程。

## 磁盘证据

| 证据 | Python | Go |
|------|--------|----|
| `events.jsonl` 文件数 | 1 | 1 |
| `events.jsonl` 总行数（前 50 个文件） | 5 | 5 |
| `queens/` 下的身份目录 | `['queen_growth']` | `['queen_growth']` |
| 会话目录数 | 1 | 1 |

两侧的 `events.jsonl` 与会话树都随会话创建落盘；上表用于核对同一组文件的规模。

## 最重要的 5 个差异

无：所有探针完全一致。

## 复现

```bash
uv run python go_hive/scripts/api_parity.py
```

脚本每次自行挑选空闲端口、创建临时 HIVE_HOME、拉起两侧 serve，结束后清理（`--keep` 可保留）。
