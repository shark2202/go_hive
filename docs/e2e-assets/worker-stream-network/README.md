# Worker/Overseer stream-stall — real network E2E evidence

Real loopback HTTP/SSE evidence that the colony worker and overseer production
loops detect a stalled model stream, inject the `STREAM_STALLED`
continue-nudge, and re-stream the **same inner turn** to a successful
completion. No external model or network is contacted.

## What is exercised

`TestWorkerOverseerStreamStallRealNetwork` in
`go_hive/go-hive/internal/colony/stream_network_e2e_test.go`:

- builds the real production loops (`newWorkerAgentLoop` for `worker:w1`,
  `startOverseerReminders` for the persistent overseer);
- installs the real `llm.Provider` OpenAI-compatible SSE transport
  (`stream_openai.go`) pointed at an `httptest.NewServer` on a loopback TCP
  socket — **not** a fake streaming completer;
- the server serves `/v1/chat/completions` with `stream: true`:

| case | first request | retried request |
| --- | --- | --- |
| `ttft` | writes no status line, header or body byte at all | complete SSE stream |
| `inactive` | one SSE delta, flushed, then silence | complete SSE stream |
| `healthy` | complete SSE stream (negative control) | n/a — no retry |

A worker's successful stream ends in a `report_to_parent` tool call (how a
worker turn legitimately terminates); the overseer's ends in text.

## Passed assertions (both roles)

- the watchdog installed on the loop's LLM seam is
  `*agentloop.StreamingCompleter` and every request carried `stream: true`;
- `ttft`: the stalled socket received **0 body bytes** and was cancelled after
  ≈ the 0.5 s TTFT budget;
- `inactive`: the stalled socket delivered 171 bytes (one event) and was then
  cancelled after ≈ the 0.5 s inter-event budget;
- exactly one nudge was produced with the matching reason, `limit_seconds=0.5`
  and elapsed ≥ 0.4 s; `loop.Counter("stream_stall_nudged") == 1`;
- the retried request body carried `<system-reminder>The previous stream
  stalled (...)`, its stream delivered bytes, completed, and returned the
  recovered answer;
- exactly 2 model calls and `InnerTurns == 1`, so the retry did not consume a
  new turn; the turn result is `success`;
- `healthy` control: 1 request, no nudge, counter 0.

## Files

- `results.json` — machine-readable case/request/nudge/turn results produced by
  the test (`HIVE_WORKER_STREAM_E2E_OUT`).
- `test.log.txt` — verbose run of the E2E.
- `stream-stall-packages.log.txt` — `go test ./internal/colony
  ./internal/agentloop -run 'Stream|Stall' -count=1`.
- `commands.txt` — exact commands, workdir and exit codes.
- `SHA256SUMS` — checksums of the above.

## Reproduce

```sh
go_hive/scripts/worker_stream_network_e2e.sh
```

## Parity

The watchdog verdicts and continue-nudge re-stream are upstream parity
(`core/framework/agent_loop/agent_loop.py::_run_turn_loop`). The *network
harness* is `Parity-exempt`: the Python reference has no socket-level stall
test; the Go transport (`llm.Provider` SSE) is what is being verified here.
