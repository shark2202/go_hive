# Reliability: crash recovery + concurrency soak

Run: `2026-11-22T18:20:05+08:00`  
Binary: `a93af17d91c676e0` built from `cb05c2ecab66e9efb1b3f82e381d76464656f347`  
Module: `go_hive/go-hive`  
LLM: `deepseek-flash-1017` via `openai https://token.tokenbroad.com/v1`  
Port: `8803`; disposable HIVE_HOME outside the repo (not committed)

## Verdict

**27 PASS / 0 FAIL / 0 SKIP** — reliability evidence complete.

## A. Crash recovery (kill -9 mid-turn)

- Crash session: **session_20261122_181835_4de3fe1e**
- Kill point: **mid-turn, in-flight tool call (hard)**
- Parts before / after restart: **7 / 7**
- Tool result before / after: **True / True**
- events.jsonl lines before / after: **29 / 29**
- Continuation reply: **CRASH_PRE_RELCRASH179534271532429**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `cr_boot1` | PASS | serve healthy on http://127.0.0.1:8803 |
| 2 | `cr_long_prompt` | PASS | delivered long terminal task prompt (token RELCRASH179534271532429) |
| 3 | `cr_mid_turn` | PASS | in-flight tool call observed: {'client_input_committed': 1, 'client_input_received': 1, 'execution_completed': 0, 'execution_failed': 0, 'execution_started': 1, 'tool_call_completed': 2, 'tool_call_started': 3} |
| 4 | `cr_kill9` | PASS | SIGKILL during the turn |
| 5 | `cr_boot2` | PASS | restarted serve healthy on http://127.0.0.1:8803 |
| 6 | `cr_cold_listing` | PASS | history lists session_20261122_181835_4de3fe1e cold with messages |
| 7 | `cr_resume` | PASS | queen_resume_from=session_20261122_181835_4de3fe1e reopened the same session id |
| 8 | `cr_parts_intact` | PASS | all 7 pre-crash parts present and byte-identical |
| 9 | `cr_tool_result` | PASS | completed echo tool result (RELCRASH179534271532429) recovered from disk |
| 10 | `cr_events_intact` | PASS | events.jsonl 29 -> 29 lines, head preserved |
| 11 | `cr_tool_result_usable` | PASS | post-restart reply used the recovered tool output: CRASH_PRE_RELCRASH179534271532429 |
| 12 | `cr_log_traces` | PASS | second boot banner + resume access in hive-server.log |

## A2. Parked ask_user survives restart

- Park session: **session_20261122_181856_1e1eaa7b**
- Parked question: **What color should the widget be? Answer with one word.**
- Answer reply: **Blue it is. That's the whole job you gave me, so there's nothing else running behind it.  When you want to put that widget somewhere real, tell me what it does and who it's for, and I'll start with whether the people who touch it come back a second time.**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `pk_park_written` | PASS | cursor.pending_input reason=ask_user persisted |
| 2 | `pk_kill9` | PASS | SIGKILL while parked on ask_user |
| 3 | `pk_boot3` | PASS | restarted serve healthy on http://127.0.0.1:8803 |
| 4 | `pk_resume` | PASS | resumed the parked session under the same id |
| 5 | `pk_park_restored` | PASS | pending_input ask_user + question text survived resume |
| 6 | `pk_answer_continues` | PASS | answer unblocked the restored wait: Blue it is. That's the whole job you gave me, so there's nothing else running behind it.  When you want to put that widget somewhere real, tell me what it does and who it's for, and I'll start with whether the people who touch it come back a second time. |
| 7 | `pk_park_cleared` | PASS | pending ask_user cleared after the answer |

## B. Concurrency soak

- Sessions x rounds (max active): **10 x 5 (max 3)**
- Turns completed / failed / timed out: **50 / 0 / 0**
- Turn latency (min / p50 / max): **1.108s / 2.137s / 4.666s**
- RSS start / end / max: **102 MB / 115 MB / 115 MB**
- SSE boundary frames: persisted / received: **53 / 53**
- Missing execution ids: **0**
- Health polls ok / failed: **9 / 0**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `soak_create` | PASS | 10/10 sessions live (opening turns done) |
| 2 | `soak_rounds` | PASS | 50/50 turns completed; http_bad=0 timeout=0 turn_failed=0 execution_failed=0 |
| 3 | `soak_live` | PASS | health poller 9/9 OK during soak (worst 34.0 ms) |
| 4 | `soak_rss` | PASS | RSS start 102 MB -> end 115 MB (max 115 MB, growth 12 MB) |
| 5 | `soak_no_dropped` | PASS | persisted execution_completed=53, received=53, missing=0 |
| 6 | `soak_tokens` | PASS | 50/50 round answer tokens persisted in transcripts |
| 7 | `soak_alive` | PASS | serve still alive and healthy after soak |

## Preflight

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `preflight` | PASS | binary built from cb05c2ecab66e9efb1b3f82e381d76464656f347; model=deepseek-flash-1017; port=8803 |

