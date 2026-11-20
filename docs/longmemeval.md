# LongMemEval 端到端 harness（Go 移植）

> 上游事实源：`core/tests/e2e/longmemeval.py`（789 行）、`core/tests/e2e/test_longmemeval.py`、`core/tests/e2e/conftest.py`（`run_queen` 驱动）、`docs/memory-benchmark.md`。
> Go 实现：`internal/longmemeval/`（数据集、选题、seed、judge、结果、汇总）、`internal/cli/longmemeval.go`（一题一次真实 queen 会话）、`cmd/hive-longmemeval/`（命令）、`internal/hiveclock/`（时钟冻结 seam）。
>
> 口径：本页所有「实测」表示在本仓库工作树里真跑过并留了输出；「代码路径」表示由实现直接确认、但没有单独实跑。**本移植尚未产生任何 LongMemEval 分数**——见第 7 节。

## 1. 命令面

```bash
# 跑分（S/M，默认每题一个新隔离 HIVE_HOME）
go build ./cmd/hive-longmemeval && ./hive-longmemeval run \
  --data <raw 文件或 split 目录>            # 缺省 $HIVE_LME_DATA 或 ./.cache/longmemeval
  --dataset s|m                            # 缺省 $HIVE_LME_DATASET 或 s
  --questions all|12|id1,id2               # 缺省 $HIVE_LME_QUESTIONS 或 12（按题型分层抽样）
  --resume                                 # 跳过 results.jsonl 里已有题号（$HIVE_LME_RESUME=1）
  --run-dir <dir>                          # 缺省 $HIVE_LME_RUN_DIR 或 <data>/runs/<时间戳>
  --queen queen_technology                  # 缺省 $HIVE_LME_QUEEN
  --model <queen 模型>                      # 缺省 $HIVE_LME_MODEL / $HIVE_LIVE_MODEL
  --api-base <queen 端点>                   # 缺省 $HIVE_LME_API_BASE / $HIVE_LIVE_API_BASE
  --api-key <queen key>                    # 缺省 $HIVE_LME_API_KEY / $HIVE_LIVE_API_KEY（推荐走环境变量）
  --judge-model gpt-4o-2024-08-06          # 缺省 $HIVE_LME_JUDGE_MODEL，官方 judge 自动 10-token cap
  --judge-api-base https://api.openai.com/v1
  --judge-api-key <key>                    # 缺省 $HIVE_LME_JUDGE_API_KEY / $OPENAI_API_KEY
  --timeout 420                            # 每题 queen 轮次上限秒数（$HIVE_LIVE_TIMEOUT）
  --keep-homes                             # 保留每题的隔离 HIVE_HOME（默认跑完删除）
  --download                               # 缺 raw 文件时从 HuggingFace 拉取（默认 true）
  --verbose

# 用另一个 judge 重判已保存的答案
./hive-longmemeval rejudge <run-dir> [--concurrency 8] [judge/data 同 run]
# 官方 reader 参照点（只给答案会话 = oracle，或给全量历史）
./hive-longmemeval reference oracle|full-history --out <dir> --questions all [--reader-model ...]
# 重新打印 / 落盘汇总
./hive-longmemeval summary <run-dir>
```

`hive-longmemeval` 不内嵌任何数据：数据集始终从 `--data` / `$HIVE_LME_DATA` 读，缺文件且 `--download` 时才走网络。

## 2. 数据格式

官方 2025 cleaned 版（`longmemeval_s_cleaned.json` ≈ 277 MB，`longmemeval_m_cleaned.json` ≈ 2.7 GB）是一个 JSON 数组，元素字段与上游一致：

```json
{
  "question_id": "e47becba",            // 或以 _abs 结尾的弃权题
  "question_type": "single-session-user|single-session-assistant|multi-session|temporal-reasoning|knowledge-update|single-session-preference",
  "question": "...", "answer": "...",
  "question_date": "2023/05/30 (Tue) 23:40",
  "haystack_session_ids": ["..."], "haystack_dates": ["..."],
  "haystack_sessions": [[{"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]],
  "answer_session_ids": ["..."]
}
```

- 下载 URL：`https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_<s|m>_cleaned.json`（与上游同源）。
- 首次使用把数组**流式**拆成 `$CACHE/<variant>_cleaned/index.json` + 每题一个 `<question_id>.json`（S 277 MB / M 2.7 GB 不会整文件进内存）；`index.json` 存在就直接复用。
- `question_id` 会被校验为安全文件名（拒绝路径分隔符），`haystack_*` 三个数组长度必须一致，日期必须是 `YYYY/MM/DD (周几) HH:MM`，否则整份数据报错而不是静默丢题。
- 仓库内的脱敏 fixture：`internal/longmemeval/testdata/fixture_cleaned.json`（3 题：single-session-user / knowledge-update / 一道 `_abs` 弃权题），字段与 cleaned 版同构，可直接 `--data internal/longmemeval/testdata/fixture_cleaned.json` 使用（测试里如此跑）。

## 3. 运行目录与产物

```
<run-dir>/
  results.jsonl            # 每题一行（见下），中途被杀只截断最后一行，读时自动修复
  transcripts/<id>.json    # {"result": 行, "turns": 会话原文 parts}
  summary.txt              # 与上游同格式的文字报告
  summary.json             # 机器可读汇总（overall/CI/题型/弃权/错误/中位耗时）
  homes/<id>/              # 该题的隔离 HIVE_HOME（默认跑完删除；--keep-homes 保留）
  rejudged-<judge 名>/     # rejudge 输出（results.jsonl + summary.*）
```

`results.jsonl` 行（与上游同名字段）：`question_id / question_type / abstention / question / answer / hypothesis / correct / judge / judge_model / reader_model / searched / search_calls / evidence_retrieved / tools / seconds`，失败题附 `error`，judge 失败附 `judge_error`（保留答案、按错计、可 rejudge）。

汇总口径同样照抄上游：`overall`（全题正确率 + 95% Wilson 区间）、`task-averaged`（六类均值）、按题型表、abstention 行、超时/错误行、未评分行、每题耗时中位数；`searched` = 调用过 `search_messages`/`search_timeline`，`evidence` = 答案会话出现在搜索结果文本里（自动注入的 recall 不算）。

## 4. 每题是怎么跑的

1. **隔离 home**：为每题新建 `<run-dir>/homes/<id>`，把该题历史按 `haystack_dates` 写成**真实 queen 会话**（`queens/<queen>/sessions/session_YYYYMMDD_HHMMSS_xxxxxxxx/{events.jsonl,meta.json}`，事件形状与运行时写入的完全一致，`search_messages` 直接可搜；seed 测试用真实 `memory.SearchMessages` 验证）。
2. **冻结时钟**：题目日期写入 `HIVE_FAKE_NOW`（RFC3339），`internal/hiveclock` 只在三处读取——消息 `[YYYY-MM-DD HH:MM TZ]` 戳（`chat_runtime.go`）、`get_current_time` 工具（`local_time_csv.go`）、循环内部时间戳（`agentloop`）——即上游 `freeze_clock` 补丁的那几处；变量不设置时 `Now()` 就是 `time.Now()`。
3. **真实 queen 会话**：走与 `POST /api/sessions` 相同的装配路径——`chatProviderFactory` 解析 provider、`newSessionToolCatalogue` 安装完整工具目录（含 `search_messages`/`search_timeline`）、`enableQueenSessionTools`、`hiveserver.NewChatRuntime`（内存 recall、会话事件日志、反思调度都在）。题目以真实用户消息投递，等 `execution_completed` / `execution_failed` 后从会话 transcript 取最终回答、工具调用与搜索结果（溢出结果按 `Full result at:` 指针读全文）。
4. **官方 judge**：per-type prompt 逐字移植（含 temporal off-by-one 段、knowledge-update 段、preference rubric 段、`_abs` 弃权段），`temperature=0`；`gpt-4o-2024-08-06` 自动加官方 10-token cap，其他模型不加。429/500/502/503/504 指数退避重试 8 次；端点报 temperature 不支持时去掉该字段重试一次（与上游 `_chat` 相同）。
5. **顺序执行**：单进程逐题（上游是 pytest + xdist 多 worker）。断点续跑用 `--resume`。

`reference` 子命令复刻官方 reader prompt（`json` history 格式 + CoT），`oracle` 只给 `answer_session_ids` 的会话、`full-history` 给全部，历史按日期字符串排序、turns 用 Python `json.dumps` 兼容的转义（含非 ASCII 的 `\uXXXX`/代理对），便于与上游参照点对齐。

## 5. 与上游的结构差异

| 项 | 上游 Python | Go 移植 | 影响 |
| --- | --- | --- | --- |
| 驱动 | pytest 每题一个 test，`-n` 并行，xdist 共享 run dir | `hive-longmemeval run` 单进程顺序 | 只影响速度；结果文件格式相同 |
| 选题随机源 | `random.Random(0)`（CPython） | `math/rand` seed 0 | 同为确定性，但抽到的子集与 Python 不同；跨实现对比请直接传 `--questions id1,id2` |
| seed 会话 id | uuid4 后缀 | `sha256(LongMemEval session id)[:8]` | 会话 id 不参与评分；换来 fixture 可字节复现 |
| 冻结时钟 | monkeypatch `datetime.now` 的模块列表 | `HIVE_FAKE_NOW` + `internal/hiveclock` 三个读取点 | 环境变量是进程级；顺序跑无影响 |
| 会话 id 时间戳 | 真实 now | 真实 now（问题日期只用于日期面） | 一致 |
| 时间线抽取 | `build_timelines`（可选，走模型） | 未移植（`search_timeline` 仍可用，timeline 特性默认关闭） | 与上游默认（timeline off）一致 |
| 索引预热 | `sync_scope` 预热记忆索引 | 无索引（Go 直接读 events.jsonl） | 无需预热 |
| transcript | 每题 turns（text/tools/seconds） | 原始会话 parts 数组 | 调试信息更细，评分口径相同 |
| skills 目录 | queen prompt 读 home 内技能 | 同左；`~/.agents/skills` 的全局技能也会进 prompt | 真实跑分时 prompt 随本机技能集变化 |
| 结果行 reader_model | 上游写答题模型 | 同左（`--model`） | — |

## 6. 测试与门禁

- `internal/longmemeval`：数据拆分/截断拒绝、id 校验、选题分层与确定性、judge prompt 逐段断言 + 官方请求设置 + 重试/温度回退、结果行与尾部修复、断点续跑、汇总口径（含 Wilson 区间数值）、rejudge、fixture 端到端（脚本化 asker + `httptest` fake judge）、seed 后真实 `search_messages` 可搜到证据会话。
- `internal/cli`：真实 `ChatRuntime` + 完整工具目录 + 冻结时钟 + seed 历史的会话级测试（脚本化 provider 触发 `search_messages` 并断言搜索命中）。
- 七门禁：`make ci`（fmt-check / vet / test / cross / parity / parity-targets / wiring-check）全过；所有新测试带 `// Parity:` 或 `// Parity-exempt:` 注解。`make cross` 的 `go build ./...` 覆盖新命令。

```bash
# fixture 确定性测试（无网络、无凭据）
CGO_ENABLED=0 go test ./internal/longmemeval/ ./internal/cli/
```

## 7. 已验证 / 未验证

**已验证（本工作树实跑）**

- fixture 全链路命令级 smoke（`hive-longmemeval run/rejudge/summary --resume`，queen 与 judge 都指向本地脚本化 OpenAI-compatible 端点）：3 题全部产出结果行、transcript、`summary.txt/json`；结果行含 `searched=true`、`search_calls`、`evidence_retrieved=true`；`rejudge` 生成 `rejudged-fake-judge/`；`--resume` 不追加行。**这是管线验证，不是分数**（模型与 judge 都是脚本化的）。
- 数据层：fixture 拆分/重载、截断数组报错、`--data` 指 raw 文件或 split 目录两种形态、缺文件且 `--download=false` 时明确报错。
- 门禁：见第 6 节。

**未验证（环境阻塞，故本移植不声称任何 LongMemEval 分数）**

- **真实数据集下载**：本机无外网出口——`curl -sS -m 20 https://huggingface.co/` 与 `https://api.openai.com/v1/models` 都在 20s 超时（`curl: (28) Connection timed out`）。因此 S/M cleaned 文件与官方 judge 端点均未触达；下载与真实数据拆分只有代码路径与 fixture 实证。
- **真实 judge / 真实 queen 模型**：同上无凭据、无出口。本机 `127.0.0.1:8790` 返回的是 Hive 面板 HTML（`/v1/models` 也是 HTML），不是 OpenAI-compatible 模型端点；未发现其它本地网关。发布分所需的 `gpt-4o-2024-08-06` judge + 10-token cap 已按官方设置实现，但从未对真实题目调用过。
- 因此：`docs/memory-benchmark.md` 上游的 88.4% / 82.0% 等数字**未被复现、也未被引用为本移植结果**；在网络与凭据就绪后按第 1 节命令即可复跑，届时 `summary.json` 里才会有真实分数。

## 8. 复现上游发布分的条件（留给有出口/凭据的环境）

```bash
export HIVE_LME_DATA=/path/with/277MB-or-2.7GB.json
export HIVE_LME_MODEL=<与上游同源的答题模型>        # 上游用 gpt-6.1-sol
export HIVE_LME_API_BASE=<OpenAI 兼容端点> HIVE_LME_API_KEY=<key>
export HIVE_LME_JUDGE_MODEL=gpt-4o-2024-08-06
export HIVE_LME_JUDGE_API_BASE=https://api.openai.com/v1
export HIVE_LME_JUDGE_API_KEY=$OPENAI_API_KEY
./hive-longmemeval run --dataset s --questions all --timeout 420 --verbose
```

只有答题模型、judge、数据版本都与上游一致时，分数才可与发布数字比较；否则请把 `summary.json` 当作自建基线。
