# recall 性能实测：15,000 轮存储上的冷建 / warm 查询 / 搜索 / 冷启动 / 内存

> lane `perf-recall`，2026-11-22。基线 `main = f0a1899e`，本工作树基于 `25968ddb`
> （`perf-recall` 分支，`.wt/perf-recall`）。
> 脚本：`go_hive/scripts/perf_recall_15k.sh`（可重跑，任一步失败即非零退出；自带
> `tee` 全量转录到证据目录）。
> 证据：`go_hive/docs/e2e-assets/perf-recall-2026-11-22/`（本文所有数字与该目录的
> `bench.json` / `serve-*.json` / `gen.json` 一一对应，`run.txt` 是完整原始输出）。
> 本 lane 未改产品代码：仅新增 ① 构建忽略的性能 harness（`go-hive/e2e/perfrecall15k/main.go`，
> `//go:build ignore`，不进 `go list ./...`/vet/arch 图）、② 脚本、③ 证据与本文档；
> `bash go_hive/scripts/merge_postcheck.sh` 四关（gofmt / wiring-check / C12 / arch）全过。
> 环境：Apple M5、16 GB、macOS 26.7.1（Darwin 25.6.0 arm64）、Go 1.26.5；
> 全程离线（`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod`），无真实 LLM、无网络。
> 本轮实测宿主为共享工作机：本目录（第 6 次运行）记录 `vm.loadavg` 5.9→5.6（中度负载），
> 运行间波动与负载敏感性见 §8。

## 0. 结论

| 轴 | 上游 v1.0.15 声称 | Go 实测（15,000 轮） | 判定 |
|---|---|---|---|
| recall 索引冷建 | ~19 s / 15,000 turns（升级后首条消息后台建） | **direct 全量重建 388.5 ms**（3 个 queen scope 之和；6 次运行 355–389 ms，仅宿主满编译时 851 ms）；真实首查路径：索引 **156 ms 可用**，该次查询 292 ms 返回 | 优于声称（约 49×）；未发现异常 |
| warm 查询延迟 | ~30 ms | **p50 123.8 ms / p95 171.0 ms / p99 220.9 ms**（120 次，alpha 6,000 轮 scope；轻载运行 p50 117–119 ms，见 §8） | 未达声称（约 4.1×），但距 3 s 上限约 24×；瓶颈已定位（见 §4） |
| recall 3 s 上限 | 上限 3 s | 首查全程 **292 ms**（含后台建索引等待；最慢一次运行 606 ms），远低于上限 | PASS |
| `search_messages` | 无 1.0.15 数字（本轮补测） | 三个 scope 共 72 次：**p50 162.6 ms / p95 231.7 ms / p99 269.7 ms**，max 286.8 ms | 无对照；亚秒级 |
| `search_timeline` | 无 1.0.15 数字（本轮补测） | 72 次：**p50 2.8 ms / p95 5.5 ms / p99 15.1 ms**，max 18.3 ms | 无对照 |
| serve 冷启动（到 `/api/health` 200） | 无声称 | 空 home：**median 48.8 ms**（首次全新 boot ~1 s，一次性初始化）；15k home：**median 68.6 ms** | 两者同量级；boot 不扫会话 |
| 内存（RSS） | 无声称 | bench 进程峰值 **67.6 MiB**；`hive serve` 驻留 **34–37 MB**（15k home 就绪时 36.0 MB） | 无异常 |
| 索引体积 | “小索引” | 15,000 轮共 **7.05 MB**（3 文件；alpha 2.95 MB/6,000 轮 ≈ 492 B/轮） | 符合“小索引” |

数据规模：**15,000 轮 / 300 会话 / 3 个 queen scope / 220,946 条事件 / 50.0 MB events.jsonl**（每次运行字节级一致）。

## 1. 方法与口径

生成（`perfrecall15k gen`，确定性 seed=20261122，可 `--turns/--seed` 重跑）：

- 3 个 queen scope：`queen_alpha` 6,000 轮 / 120 会话、`queen_beta` 5,000 / 100、`queen_gamma` 4,000 / 80，
  共 15,000 轮、300 个 `session_*` 目录（每会话 40–60 轮，接近长会话用户的分布）。
- 每轮写入真实 `events.jsonl` 形态：`client_input_received`（用户消息）、`reasoning_delta`、
  5–10 条**累计快照** `client_output_delta`（真实流式的最后写赢语义）、`tool_call_started/completed`
  （工具结果 JSON；约 5% 为 4–11 KB 大结果），约 30% 轮次有第二次工具调用与后续回复。
- 另写 `timeline.jsonl`（每会话 2–4 条 event/fact/plan，共 891 条）供 `search_timeline`。
- 约 15% 轮次为中文，其余英文；回复 400–700 字符、用户消息 60–300 字符。
- 记录口径：轮数=用户消息数（tools/memory 语义），事件行 220,946，存储 50 MB——与“15,000 轮的真实重度使用”
  同量级；本轮为合成语料，绝对毫秒数会随语料长度浮动（见 §8）。

测量（`perfrecall15k bench` / `servebench`，均为进程内直调 `internal/memory`，不经模型）：

| 指标 | 定义 | 次数 |
|---|---|---|
| 冷建（direct） | `memory.BuildRecallIndex` 按 scope 逐一直调（升级全量重建口径） | 3 scope 各 1 次 |
| 冷建（真实首查） | 删除 `.message_index` 后触发一次 `RecallPastConversations`；同时轮询索引文件出现时刻 | 1 次/run |
| warm 查询 | 索引在位后完整 `RecallPastConversations`（含每次重读/解析索引 JSON + refresh + BM25 打分） | 120 次（20 个查询循环）+ 每 scope 20 次 |
| 索引读取成本 | 与运行时同口径（`os.Open`+`json.Decoder`）解析索引文件 | 20 次 |
| `search_messages` / `search_timeline` | 三个 scope × 3 个模式（2 个有命中 + 1 个零命中）× 8 次 | 各 72 次 |
| serve 冷启动 | `hive serve -p <port>` 从 spawn 到 `/api/health` 返回 200 | 每 home 3 次（另有 1 次预热不计） |
| RSS | bench：`getrusage` 峰值；serve：`ps -o rss=`（就绪时 / 2 s 后） | — |
| 瓶颈归因 | 追加 20 次 warm 查询的 CPU profile（`go tool pprof`） | 1 份 |

## 2. 数据与体积（`sizes.txt` / `gen.json`）

| scope | 轮数 | 会话 | 事件行 | events.jsonl |
|---|---|---|---|---|
| queen_alpha | 6,000 | 120 | 88,069 | 20.9 MB |
| queen_beta | 5,000 | 100 | 73,566 | 17.5 MB |
| queen_gamma | 4,000 | 80 | 59,311 | 14.0 MB |
| **合计** | **15,000** | **300** | **220,946** | **50.0 MB** |

索引文件（bench 结束态，`index-files.txt`）：`queen_alpha.json` 2,954,681 B / 6,000 轮、
`queen_beta.json` 2,462,918 B / 5,000 轮、`queen_gamma.json` 1,974,615 B / 4,000 轮，
合计 **7,392,214 B（7.05 MB）**——即 15,000 轮的全量 recall 索引只占源存储的约 14%。

## 3. recall 索引冷建

direct（逐 scope `BuildRecallIndex`，等价升级后的全量重建）：

| scope | 时间 | 折算 |
|---|---|---|
| queen_alpha（6,000 轮） | 155.3 ms | 25.9 µs/轮 |
| queen_beta（5,000 轮） | 128.0 ms | 25.6 µs/轮 |
| queen_gamma（4,000 轮） | 105.2 ms | 26.3 µs/轮 |
| **合计** | **388.5 ms** | ≈25.9 µs/轮 |

真实首查（升级路径，alpha scope）：索引在 **156.4 ms** 可用；该次查询自身 **292.4 ms** 返回
（=等待索引出现 + 读取 11.2 ms + 6,000 轮打分约 125 ms），结果非空（10 条）。后续查询 132.1 ms。
全程远低于 3 s 等待上限；不存在“首查被上限截断、结果为空”的情形。

上游声称 ~19 s：本实现（Go、JSON 索引）约 **49×** 快。差异属实现语言/存储引擎量级差（上游为
Python + SQLite 逐轮写入），不是口径差异——两边都是“重建 15,000 轮的派生索引”。

## 4. warm 查询延迟与瓶颈定位

120 次完整 warm 查询（alpha 6,000 轮，`bench.json:warm_queries`）：

| p50 | p95 | p99 | min | max | mean | 非空结果 |
|---|---|---|---|---|---|---|
| 123.8 ms | 171.0 ms | 220.9 ms | 112.3 ms | 275.0 ms | 129.3 ms | 114/120 |

按 scope 的 20 次对照组（`warm_by_scope`）显示成本与 scope 内被索引轮数线性相关
（轻载运行时为 118.9 / 99.0 / 80.6 ms，比例一致）：

| scope | 索引轮数 | p50 |
|---|---|---|
| queen_alpha | 6,000 | 146.2 ms |
| queen_beta | 5,000 | 111.9 ms |
| queen_gamma | 4,000 | 89.2 ms |

≈ **19–26 µs/轮/次查询**，即每次 recall 查询都会把该 scope 的全部轮次重新分词/计频一次。

分阶段计时（同口径子集，`index_load_parse_replica` = 运行时同样的 `json.Decoder` 读取）：

| 阶段 | 耗时 | 占 warm p50 |
|---|---|---|
| 索引读取+解析（~2.95 MB JSON） | 11.2 ms | ≈9.0% |
| 会话 stat/refresh（300 目录量级） | ~1–2 ms（含在总时长） | ≈1% |
| **逐轮重新分词+BM25 打分（6,000 轮）** | ≈110 ms | **≈90%** |

CPU profile 归因（20 次 warm 查询，`pprof-cum.txt`）：`pastRecallTurnCounts` 36.7%、
`pastRecallTermCounts` 31.5%（其中 `regexp.FindAllString` 24.1%）、GC/madvise ≈34%
（逐轮 map 分配造成的回收压力）、`atomicfs.ReadJSON` ≈5%。
结论：**瓶颈是“查询侧无缓存的逐轮计频”**——索引只存原始文本（user/reply），每次查询对全部轮次
重建 term counts；读取索引本身只占 ~9%。

对 3 s 上限的解读：实现中 3 s（`HIVE_PAST_RECALL_TIMEOUT`，默认 3）只约束“等索引可用”的路径
（`recallIndexForQuery` 的等待循环），**不约束打分步骤本身**。当前 124 ms 距上限有 24× 余量；
但打分成本 O(scope 轮数) 线性增长——按 ~20 µs/轮外推，单 scope 到约 15 万轮时才会触及 3 s
（外推值，未实测；按本轮存储即 10 个 scope 的规模）。不构成当前回归，仅作为后续扩容时的
性能预算输入。

## 5. search_messages / search_timeline（15k 轮）

`search_messages`（正则跨会话扫描 + turn 窗口富化，有命中模式会命中数千条）：

| 组 | p50 | p95 | p99 | max |
|---|---|---|---|---|
| 全部 72 次（3 scope × 3 模式） | 162.6 ms | 231.7 ms | 269.7 ms | 286.8 ms |
| alpha（6,000 轮）`(?i)postgres\|kubernetes` | 231.7 ms | 278.4 ms | 285.1 ms | 286.8 ms |
| alpha `(?i)refund policy` | 182.4 ms | 197.1 ms | 199.3 ms | 199.8 ms |
| alpha 零命中 | 169.2 ms | 203.5 ms | 208.5 ms | 209.7 ms |

轻载运行时同一组分别为 136.7 / 197.5 / 164.6 / 138.2 ms（§8）。即纯扫描地板（alpha）≈138–169 ms，
命中后富化叠加；随 scope 事件量（88k 行 / 21 MB）线性。
`search_timeline`（读 timeline.jsonl，891 条）：全部 72 次 **p50 2.8 / p95 5.5 / p99 15.1 ms**，max 18.3 ms
（p99 由个别模式的单次 GC/调度停顿抬高；其余组 p50 1.9–5.1 ms）。

## 6. serve 冷启动与内存

| 场景 | median ready | 各次 | 预热（首次全新 boot） | RSS 就绪 / 2 s |
|---|---|---|---|---|
| 空 home | 48.8 ms | 25.4 / 48.8 / 68.1 ms | 1034.7 ms（一次性 home 初始化 + 冷页缓存） | 34.2 MB / 34.5 MB |
| 15k home（300 会话，无派生索引） | 68.6 ms | 68.6 / 73.7 / 46.9 ms | 44.6 ms | 36.0 MB / 36.4 MB |

15k 存储对 boot 没有额外开销（boot 不扫描会话/不建索引）；RSS 差异 ≈2 MB。bench 进程自身
峰值 RSS **70,877,184 B（67.6 MiB）**，其中 warm 后 heap_alloc ≈36.9 MB。

## 7. 对比表（Go 实测 vs 上游声称 vs 备注）

| 项 | 上游 v1.0.15 声称 | Go 实测（本树 25968ddb） | 备注 |
|---|---|---|---|
| 冷建 | ~19 s / 15,000 turns | 全量 direct **388.5 ms**（6 次运行 355–389；宿主满编译时 851）；首查路径索引 **156 ms 可用** | Go/JSON vs Python/SQLite 的实现差；同口径（重建派生索引） |
| warm 查询 | ~30 ms | **p50 123.8 ms**（p95 171.0 / p99 220.9；轻载 p50 117–119、p95 121–124） | 未达声称，超 4.1×；原因=每次查询逐轮重算 term counts（§4）；距 3 s 上限 24× |
| recall 等待上限 | 3 s | 首查 292 ms（最差 606 ms，PASS）；**上限只覆盖等索引，不覆盖打分** | 观察项：打分本身无 deadline，线性增长 |
| 旧扫描路径 ~1.2 s（升级前） | 上游旧实现 | 本移植无旧扫描路径可测（只走索引） | 不构成对照 |
| 索引文件 | “小索引” | 7.05 MB / 15,000 轮（492 B/轮） | 与源存储 50 MB 之比 ≈14% |
| `search_messages` | 未声称 | p50 162.6 ms / p95 231.7 ms（轻载 136.7 / 197.4） | 补测；无上游数字 |
| `search_timeline` | 未声称 | p50 2.8 ms / p95 5.5 ms（轻载 2.4 / 3.2） | 补测；无上游数字 |
| serve 冷启动 | 未声称 | 空 48.8 ms / 15k 68.6 ms（到 health 200） | 首次全新 boot ~0.3–1.2 s 为一次性初始化 |
| 内存 | 未声称 | serve 34–37 MB；bench 峰值 67.6 MiB | 无异常 |

## 8. 运行间波动（共享工作机）

同一脚本完整跑过 6 次（数据生成字节级一致：52,470,968 B / 220,946 行）。`vm.loadavg` 从第 6 次
开始记录；前 5 次未采样，备注来自当次运行期间观察到的宿主进程：

| # | 宿主负载 | 冷建 total | warm p50 | warm p95 | 备注 |
|---|---|---|---|---|---|
| 1 | 未采样（轻） | 356.1 ms | 117.3 ms | 121.3 ms | 轻载基准 |
| 2 | 未采样（轻） | 354.9 ms | 118.3 ms | 121.6 ms | 轻载基准 |
| 3 | 未采样（轻） | 378.1 ms | 119.0 ms | 124.2 ms | 轻载基准 |
| 4 | 未采样（中途升高） | 355.4 ms | 137.7 ms | 343.0 ms | 尾部延迟被外部负载抬高 |
| 5 | 未采样（宿主全量 Go 编译） | 850.7 ms | 122.4 ms | 234.8 ms | 冷建被 CPU 争抢拉长 2.4× |
| 6（本目录证据） | `{5.92 8.78 6.37}`→`{5.56 8.56 6.33}` | 388.5 ms | 123.8 ms | 171.0 ms | 中度负载 |

读数：冷建在持续 CPU 争抢下可到 ~0.85 s（仍远低于声称的 19 s）；warm **p50 稳定在 117–138 ms**，
**p95 随宿主负载在 121–343 ms 之间**；所有运行全部低于 3 s 上限。结论对负载不敏感，
但引用具体毫秒时应连同宿主负载一起读（`bench.json.host_load_before/after_warm` 已入证据）。

数据与结果限制：① 合成语料（50 MB/220,946 事件行）与真实重度 15k 轮存储同量级，但真实 transcript
的工具结果与流式碎片分布更重时，绝对耗时按事件量线性上浮（冷建 ≈25.9 µs/轮、`search_messages`
≈1.5 µs/事件行）；② 每 scope 独立索引，“15,000 轮”由 6k/5k/4k 三个 scope 组成，warm 数字以最大
scope（6,000 轮）为准，整体全量场景应读 §4 的线性折算；③ 宿主为共享工作机（见上表）。

## 9. 复现

```bash
cd /Users/mac/hive/.wt/perf-recall
bash go_hive/scripts/perf_recall_15k.sh                 # 默认 15,000 轮、seed=20261122、serve 3 次
bash go_hive/scripts/perf_recall_15k.sh --turns 2000    # 缩小规模快跑
bash go_hive/scripts/perf_recall_15k.sh --reuse-home    # 复用已生成 home 重测
```

- 数据 home / 二进制在 `$TMPDIR/perf-recall-15k/`（仓库外，不提交）；原始输出覆盖写入
  `go_hive/docs/e2e-assets/perf-recall-2026-11-22/`；脚本自带 `tee`，全量转录落在 `run.txt`。
- 证据文件（全部入库；仓库 `.gitignore` 忽略 `*.log`，故转录与 serve 日志均为 `.txt`）：
  `env.txt`（环境/commit/loadavg）、`gen.json|txt`、`sizes.txt`、`bench.json|txt`、
  `index-files.txt`、`cpu.pprof` + `pprof-top.txt` + `pprof-cum.txt`、`serve-empty.json|txt`
  + `serve-empty-run*.txt`、`serve-15k.json|txt` + `serve-15k-run*.txt`、`summary.txt`、`run.txt`。
- harness 子命令：`gen` / `bench` / `servebench` / `report`（`report` 从 JSON 生成 `summary.txt`，
  本文数字可直接从这些 JSON 复算）。

## 10. 优化后对比：term counts 缓存（lane `perf-opt`，2026-11-22）

> lane `perf-opt`（分支 `perf-opt`，工作树 `.wt/perf-opt`，基线 `d9aab9cf`）。§4 定位的瓶颈
> ——「每次查询对全部轮次重算 term counts（~90%）」——已消除；本节数字全部来自本 lane 复测，
> 证据目录 `go_hive/docs/e2e-assets/perf-opt-2026-11-22/`。索引文件格式/版本、recall 语义、
> 匹配与打分结果、`HIVE_PAST_RECALL_TIMEOUT` 均未改动（逐条等价证明见 §10.3）。
> 本 lane 的产品代码改动：新增 `go-hive/internal/memory/recall_counts.go`（进程内逐轮计数
> 缓存），`past_conversations.go` 打分改读该缓存形式，`recall_index.go` 在 refresh/rebuild
> 落地后 prune 过时会话；另新增等价性测试、`e2e/paritydump` 与 `scripts/perf_recall_parity.sh`
> （均为证据工具，不进产品构建）。

**实现一句话**：按 `(home, scope, owner, session, ordinal)` 缓存每轮 term counts；失效信号是
该会话 `events.jsonl` 的大小——与索引 refresh 跳过会话的条件同源（append-only：大小未变=内容
未变），大小一变即丢弃该会话并重算；refresh/rebuild 写盘后 prune 已消失会话。计数以「排序术语
blob + offsets + counts」的指针无关形式保存，前缀频次=二分查找+少量命中键，取代逐键扫 map。
索引文件仍是原格式（所以本节的旧/新二进制可以共用同一份索引）。

### 10.1 warm 查询（同负载 A/B，主结果）

同一 home、同一脚本、同负载（loadavg ~3.8–4.3）交替跑 d9aab9cf 二进制与优化后二进制
（`ab/bench-{base,new}-same-load.json|txt`）：

| 指标 | 基线 d9aab9cf | 优化后 | 变化 |
|---|---|---|---|
| warm 查询 p50 | 120.7 ms | **12.8 ms** | 9.4× |
| warm 查询 p95 | 128.5 ms | **13.8 ms** | 9.3× |
| warm 查询 p99 | 133.9 ms | **14.5 ms** | 9.2× |
| 建索引后首个查询（含一次性缓存填充） | 131.8 ms | **15.2 ms** | 8.7× |
| 升级首查（含等索引；两版同量级） | 295.8 ms | 302.5 ms | 持平 |
| 索引读取+解析（未改动路径） | 10.2 ms | 10.1 ms | 0% |

同脚本 canonical 复跑（`bench.json`，load 4.49→4.90，与 §0 原记录的 5.92→5.56 同量级）：
**p50 12.7 / p95 13.5 / p99 14.1 ms**（基线原记录 123.8 / 171.0 / 220.9，约 **9.7×** 快），
低于本 lane 的 <50 ms 目标、也低于上游声称的 ~30 ms；120 次查询非空结果 114 次且与基线逐条
一致（§10.3）。升级首查 296.5 ms（索引 151.8 ms 可用），仍远低于 3 s 上限。canonical 共复跑
4 次（load 4.5–6.9），warm p50 12.7–20.5 ms（p95 13.5–29.0 ms）——跑间差异随宿主负载，与 §8
记录的基线波动同性质；上表同负载 A/B 是受控对比。

### 10.2 冷建 / 搜索 / 内存 / serve 不回退

| 轴 | 基线 | 优化后 | 判定（±10%） |
|---|---|---|---|
| 冷建 direct 合计 | 388.5 ms（§3，load 5.9）；同负载 A/B 490.0 ms | 393.0 ms（canonical，load 4.5）；同负载 A/B **373.3 ms** | 通过（canonical +1.2%，A/B 更快；4 次复跑 393–602 ms 随瞬时争抢，冷建路径未改） |
| `search_messages` alpha 三模式 p50 | 231.7 / 182.4 / 169.2 ms（§5）；A/B 196.5 / 159.0 / 134.3 | 199.3 / 170.7 / 124.4（canonical）；A/B 189.9 / 161.8 / 127.2 | 通过（持平～更快；搜索路径未改） |
| `search_timeline` alpha p50 | 2.8 ms | 2.4–3.0 ms | 通过 |
| bench 进程峰值 RSS | 70.9 MB（§6）；A/B 71.3 MB | 79.9 MB（canonical）；A/B 80.0 MB | +12–13%（缓存常驻 ~17 MB @15k 轮；见 §10.4 上限） |
| `hive serve` 冷启动 median（空/15k） | 48.8 / 68.6 ms | 47.8 / 44.6 ms | 通过；serve RSS 33–36 MB 不变 |
| 索引文件 | 7.05 MB / 15,000 轮 | 7.05 MB（格式/版本未改） | 不变 |

### 10.3 等价性证据（旧实现逐条相等）

1. **端到端 dump 对比**（`scripts/perf_recall_parity.sh`）：同一 dump 工具（`e2e/paritydump`）
   分别链接 `d9aab9cf` 导出的基线与工作树，在 15k home 的字节级同源副本上跑同一查询集
   （`parity/queries.txt`，120 条语料派生查询）× 3 个 scope × {默认, 排除首会话} =
   **720 条记录、7,194 个返回 turn**；两份 JSONL **逐字节相同**
   （sha256 `8ba735ba17622096c1dc0431c3e07d673be8a2eb6b2a328a1de3d86ccf58ff9f`，
   `parity/parity-report.txt` 明文 `PARITY: PASS`；含每条 turn 的分数）。
2. **包内测试**（`internal/memory/recall_counts_test.go`，均带 `// Parity:`）：排序频次与旧
   map 频次在普通/前缀/中文/数字/撇号术语上逐一相等；缓存路径与未缓存 reference（原
   `pastRecallTurnCounts` + `pastRecallChoose` 路径）在 cold/warm/ExcludeSession/会话增长/
   会话删除/多 home 隔离下逐条 `DeepEqual`（含 Score）；miss/hit 计数断言证明命中路径被覆盖。
3. 索引格式未变：旧、新二进制共用同一索引文件，上述对比即在同一索引上完成。

### 10.4 瓶颈迁移（pprof，`pprof-cum.txt`）

优化后 20 次 warm 查询的 profile：`loadRecallIndex`（索引文件读取+JSON 解析）**61.5%**、
`madvise` 26.9%；逐轮计频（`pastRecallTurnCounts`/`pastRecallTermCounts`/regexp）与打分不再
出现在 top（cum <10 ms/20 次）。剩余成本 ≈ 每查询重读解析 2.95 MB 索引 JSON（9.9 ms p50
replica）——后续如要继续提速，下一个目标是索引读取/增量格式，不在本 lane 范围。

缓存边界：进程内、按 owner 上限 `50000` 轮（超出整体丢弃、按需重填）；进程重启或首次触达某
scope 的首个查询付一次性重建（6,000 轮 ≈ 90 ms，见 §10.1 升级首查），此后约 13 ms/查询。

### 10.5 复现

```bash
cd /Users/mac/.wt/perf-opt
bash go_hive/scripts/perf_recall_15k.sh --reuse-home --out go_hive/docs/e2e-assets/perf-opt-2026-11-22
bash go_hive/scripts/perf_recall_parity.sh --home "$TMPDIR/perf-recall-15k/home-15000" \
     --out go_hive/docs/e2e-assets/perf-opt-2026-11-22/parity
bash go_hive/scripts/perf_recall_parity.sh --base-ref d9aab9cf --help   # 详见脚本头注释
```

- 本 lane 证据目录：`go_hive/docs/e2e-assets/perf-opt-2026-11-22/`（canonical 全脚本输出的
  `env/bench/summary/pprof/serve-*`；`ab/` 为同负载 A/B 对测；`parity/` 为等价性对比与
  压缩后的两份 JSONL dump）。
- 等价性脚本可独立复跑：它用 `git archive` 导出 `--base-ref`（默认 `d9aab9cf`）、复制当前的
  dump 源码到导出树、分别构建并在 home 的两个同源副本上 dump、逐字节比较。
