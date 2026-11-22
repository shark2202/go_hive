# Reliability: crash recovery + concurrency soak

Run: `2026-11-22T18:58:30+08:00`  
Binary: `d9ea5d5f97940c4b` built from `25968ddbe1f3fce82c7bbe4119bb969a21923b4b`  
Module: `go_hive/go-hive`  
LLM: `deepseek-flash-1017` via `openai https://token.tokenbroad.com/v1`  
Port: `8801`; disposable HIVE_HOME outside the repo (not committed)

## Verdict

**27 PASS / 0 FAIL / 0 SKIP** — reliability evidence complete.

## A. Crash recovery (kill -9 mid-turn)

- Crash session: **session_20261122_185746_63abea4e**
- Kill point: **mid-turn, in-flight tool call (hard)**
- Parts before / after restart: **6 / 6**
- Tool result before / after: **True / True**
- events.jsonl lines before / after: **25 / 27**
- Continuation reply: **CRASH_PRE_RELCRASH179534506750880**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `cr_boot1` | PASS | serve healthy on http://127.0.0.1:8801 |
| 2 | `cr_long_prompt` | PASS | delivered long terminal task prompt (token RELCRASH179534506750880) |
| 3 | `cr_mid_turn` | PASS | in-flight tool call observed: {'client_input_committed': 1, 'client_input_received': 1, 'execution_completed': 0, 'execution_failed': 0, 'execution_started': 1, 'tool_call_completed': 1, 'tool_call_started': 2} |
| 4 | `cr_kill9` | PASS | SIGKILL during the turn |
| 5 | `cr_boot2` | PASS | restarted serve healthy on http://127.0.0.1:8801 |
| 6 | `cr_cold_listing` | PASS | history lists session_20261122_185746_63abea4e cold with messages |
| 7 | `cr_resume` | PASS | queen_resume_from=session_20261122_185746_63abea4e reopened the same session id |
| 8 | `cr_parts_intact` | PASS | all 6 pre-crash parts present and byte-identical |
| 9 | `cr_tool_result` | PASS | completed echo tool result (RELCRASH179534506750880) recovered from disk |
| 10 | `cr_events_intact` | PASS | events.jsonl 25 -> 27 lines, head preserved |
| 11 | `cr_tool_result_usable` | PASS | post-restart reply used the recovered tool output: CRASH_PRE_RELCRASH179534506750880 |
| 12 | `cr_log_traces` | PASS | second boot banner + resume access in hive-server.log |

## A2. Parked ask_user survives restart

- Park session: **session_20261122_185800_30ea7620**
- Parked question: **What color should the widget be? Answer with one word.**
- Answer reply: **Blue it is. That question had nothing to do with growth, but you answered, so it's settled.  When you're ready to talk about the thing that actually matters: bring me the product, the number you're trying to move, and last month's returning users. Then we start.**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `pk_park_written` | PASS | cursor.pending_input reason=ask_user persisted |
| 2 | `pk_kill9` | PASS | SIGKILL while parked on ask_user |
| 3 | `pk_boot3` | PASS | restarted serve healthy on http://127.0.0.1:8801 |
| 4 | `pk_resume` | PASS | resumed the parked session under the same id |
| 5 | `pk_park_restored` | PASS | pending_input ask_user + question text survived resume |
| 6 | `pk_answer_continues` | PASS | answer unblocked the restored wait: Blue it is. That question had nothing to do with growth, but you answered, so it's settled.  When you're ready to talk about the thing that actually matters: bring me the product, the number you're trying to move, and last month's returning users. Then we start. |
| 7 | `pk_park_cleared` | PASS | pending ask_user cleared after the answer |

## B. Concurrency soak

- Sessions x rounds (max active): **2 x 2 (max 2)**
- Turns completed / failed / timed out: **4 / 0 / 0**
- Turn latency (min / p50 / max): **3.673s / 4.15s / 6.665s**
- RSS start / end / max: **58 MB / 64 MB / 64 MB**
- SSE boundary frames: persisted / received: **6 / 6**
- Missing execution ids: **0**
- Health polls ok / failed: **3 / 0**

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `soak_create` | PASS | 2/2 sessions live (opening turns done) |
| 2 | `soak_rounds` | PASS | 4/4 turns completed; http_bad=0 timeout=0 turn_failed=0 execution_failed=0 |
| 3 | `soak_live` | PASS | health poller 3/3 OK during soak (worst 36.3 ms) |
| 4 | `soak_rss` | PASS | RSS start 58 MB -> end 64 MB (max 64 MB, growth 6 MB) |
| 5 | `soak_no_dropped` | PASS | persisted execution_completed=6, received=6, missing=0 |
| 6 | `soak_tokens` | PASS | 4/4 round answer tokens persisted in transcripts |
| 7 | `soak_alive` | PASS | serve still alive and healthy after soak |

## Preflight

| # | Check | Result | Detail |
|---|---|---|---|
| 1 | `preflight` | PASS | binary built from 25968ddbe1f3fce82c7bbe4119bb969a21923b4b; model=deepseek-flash-1017; port=8801 |

