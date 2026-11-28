# 触发器 cron/webhook 差异收口报告

- 日期：2026-11-25
- 代码基线：`d341640b3`（merge w137/w138）
- 状态：**full CI 已验证**；本报告的文档差异在 gate 之后落盘，属于 docs-only delta
- 范围：croniter 扩展语法、webhook 响应字节、payload 数字类型、已知 wire 残差

## 一、目标

把 2026-11-25 审计中的触发器差异按“一致优先”收口：

1. Go 的 cron 解析器接受 croniter 6.0.0 对 trigger 可接受的完整语法，而不是只接受数字 5/6 字段。
2. 消除 webhook 可修复的 wire 差异：202/400/401/404 响应字节、JSON 整数/浮点类型。
3. 不把 croniter 自身拒绝的 Quartz 语法（`W`、`L-n` 等）伪装成已支持。
4. 用真实 croniter 生成对拍 fixture，禁止手写期望值。

## 二、进度与提交

| lane | 内容 | 提交 / 合并 |
|---|---|---|
| w137 | 全量 croniter 语法、fixture、Go/Python 对拍、契约页 | `9a9efbe8f`、`51dd27322`、`8e91d8b55`；合并 `340e73a81`、`d341640b3` |
| w138 | webhook 精确响应字节、JSON 数字类型 | `4978b19c1`；合并 `7656b4ad5` |

### cron

fixture：`go_hive/go-hive/internal/triggers/testdata/cron_seconds_parity.json`

- 由真实 `croniter 6.0.0` 生成
- **126 个表达式 / 4,911 个匹配步**
- 11 个语法族：numeric、macro、names、dom-last、dow-last、nth-weekday、question-mark、wrap-range、year-field、field-alias、combined
- 82 条拒绝语料、4 条无匹配语料
- 生成命令：`uv run python core/tests/test_cron_seconds_parity.py --write-fixture`

已支持：月份/星期名称、croniter 宏、`L` / `L<dow>`、`#n`、`?`、跨夜区间、7 字段年份（1970–2099）。

### webhook

- 202 / 400 / 401 / 404 响应体改为 aiohttp 精确字节：22 / 40 / 30 / 22 bytes，无尾换行，`application/json; charset=utf-8`
- payload 整数保留整数类型（超出 int64 用 `big.Int`），小数/指数为 `float64`
- 共享 `writeJSON` 未改，只改 webhook 本地响应路径

## 三、质量与实测

### full CI

命令：`cd go_hive/go-hive && make ci`

结果：**RC=0**

- tests：**6,468 / 6,468**
- parity refs：**7,310 checked, 0 missing**
- compat：**13 / 13 PASS**
- env：**131**（Go 125 / go_only 41）
- C12：273 registered / 221 ported / 27 partial / 9 out_of_scope / 16 unported
- 全量日志：`/tmp/hive-ci-cron-webhook-2.log`

### 专项

- `go test ./internal/triggers ./internal/server -count=1`：PASS
- `uv run pytest core/tests/test_cron_seconds_parity.py -q`：**10 passed**
- `gofmt -l`：clean
- `go.mod` / `go.sum`：未改
- 触发器契约页 1440/390：页面级 `scrollWidth == innerWidth`；390 下 8 个表格均在 `.tablewrap` 内，内部可横向滚动；修复前 390 曾出现 `scrollWidth=860` 的裸表格溢出，已在本轮修掉

## 四、残差（未闭环，按事实登记）

1. `W` / `LW`、`L-3`、裸 DOW `L`、`5L`、数字区间 + `#n`：**croniter 6.0.0 本身拒绝**，两侧均拒绝；不能把它们算作 Go 缺口。
2. 首位置秒（`second_at_beginning=True`）：仍不支持；仓库内无调用方构造该布局。
3. webhook 头名 wire 拼写：Go `net/http` canonical 化，aiohttp 保留原始拼写；值语义已对齐，拼写层无法在 handler 上修复。
4. 超限后 drain：aiohttp lingering-close vs Go `net/http` ≤256 KiB；状态码/响应体不受影响。
5. `NaN` / `Infinity` / `-Infinity`：Python `json.loads` 接受，Go 走 `{"raw_body": ...}`；极端 body 才触发。
6. croniter 自身三个运行时缺陷未镜像：短月 DOM 搜索越界、DOM 与 `#n` 不可能交集、50 年搜索上限；Go 保持日历语义和 400 年周期。

## 五、结论

- cron 扩展语法差异已从“Go 主动拒绝”收口为“与 croniter 6.0.0 接受面一致”。
- webhook 可修复的响应字节与数字类型差异已关闭。
- 仍不能宣称“触发器 100% 无差异”：头名拼写、drain、NaN/Infinity 是已登记的 wire 级残差；`W` 等是 croniter 本身不支持的语法。
- 本报告的数字来自实际命令输出；文档 delta 不冒充 full CI 覆盖。
