# resume-guards lane evidence (2026-11-22)

Closeout of the two parity observations the `reliability` lane recorded in
`docs/reliability-soak-recovery.md` §5:

1. **cold-resume mid-turn guard** — the Go runtime now writes
   `ParkReasonColdInterrupted` (cursor `pending_input.reason=cold_interrupted`)
   when a restart reconstructs a runtime over a mid-turn conversation, matching
   upstream §6b1½.
2. **resume snapshot / SSE first frame** — a restored park is re-announced on
   the fresh bus, so `GET /api/sessions/{id}/snapshot` and the SSE
   `session_snapshot` first frame project the wait instead of an empty state.

## Contents

- `summary.md`, `checks.tsv` — shortened real-model rerun
  (`SOAK_SESSIONS=2 SOAK_ROUNDS=2 SOAK_CONCURRENCY=2`) on the `resume-guards`
  tree: **27/27 PASS**.
- `crash-recovery/` — kill -9 mid-turn resume: `pre-kill-cursor.json` (the
  crash shape, `pending_input=null`), `post-resume-cursor.json`
  (`reason=cold_interrupted`), `post-resume-snapshot.json`
  (`awaiting_input=true, park_reason=cold_interrupted`), `metrics.tsv`.
- `park/` — ask_user park resume: `resume-cursor.json`, `resume-snapshot.json`
  (`awaiting_input=true, park_reason=ask_user, pending_questions=[…]`),
  `metrics.tsv`.
- `upstream-probe/` — the upstream probe used to pin the guard's exact write
  conditions and fields (`test_probe_cold_resume_guard.py`, run from `core/`
  with the workspace venv) and its captured output (`probe-output.txt`;
  2 passed).
