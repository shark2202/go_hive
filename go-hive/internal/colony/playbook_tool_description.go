package colony

// RunPlaybookDescription is the model-visible run_playbook description, byte
// exact from the Python reference (core/framework/tools/playbook_tools.py,
// frozen in go_hive/contracts/tool-definitions.json as the lifecycle layer).
//
// This is contract data, not prose: the same-name tool contract compares the
// served description byte-for-byte, so a stale edit here changes what the model
// reads and re-opens the residual. The execution model it states is real in
// this port — native-JSON and Python `.play.py` playbooks (external
// interpreter), the fast-grace window, and the background [PLAYBOOK_COMPLETE]
// notification — the previous hand-written text claimed the port still could
// not run Python scripts or notify, which stopped being true.
const RunPlaybookDescription = "Run a colony playbook — a deterministic Python script that drives a tracker table to convergence: query the undone work, dispatch workers on it, re-query, repeat. Use it whenever the goal has row shape; reach for run_worker only for one-off heterogeneous tasks. ASYNC: returns a run_id immediately and notifies you via [PLAYBOOK_COMPLETE]; the convergence loop absorbs routine worker outcomes. Re-running is resume — done rows drop out of the pending query, so a second run only does the gap.\n" +
	"\n" +
	"SUPPLY THE SCRIPT (exactly one):\n" +
	"• `playbook` — inline; saved to the colony library at playbooks/<meta-name>.play.py (returned as name+path).\n" +
	"• `playbook_name` — run a saved playbook by meta name (the normal way to resume or re-run).\n" +
	"• `playbook_path` — a script file on disk.\n" +
	"Iterate by editing the saved file (or re-sending inline with the same meta name, which overwrites) and re-running by name.\n" +
	"\n" +
	"SCRIPT SHAPE: define `meta` (name, description, optional `concurrency` = workers running at once; default 8, max 32; lanes throttle per-account within it) and `async def run(args)`. Injected in-script functions (NOT tools): converge / worker / tracker_query / tracker_count / lane / deadletter / log / phase. There is no mid-run escalation — a worker unsure about a row records that on the row (e.g. status='needs_review') for you to review after the run.\n" +
	"\n" +
	"Give every worker() a `goal` — one sentence in plain end-user language saying what that worker is doing; the UI shows it as the worker's title (it is not shown to the worker).\n" +
	"\n" +
	"THE WORK-LIST: `pending()` returns one dict per worker dispatch. For cheap-per-row work (most campaigns), each dict should be a CHUNK of 5-10 rows grouped in plain Python — every worker pays a ~2-3k-token orientation tax before its first useful action, so one-row-per-worker multiplies that tax by N. Reserve one-row dispatches for rows that are individually large jobs. Chunking and convergence compose: done rows drop out of the next round's query and stragglers re-group. Give a chunked worker a `timeout` covering the whole slice, and a task string telling it to process rows as consecutive tool calls, upserting each row right after acting on it — rate pacing happens inside the tool layer, so a worker never needs a turn per row and a partial slice re-queues cleanly.\n" +
	"\n" +
	"CONTRACT — get these right or the playbook silently dispatches 0 workers:\n" +
	"• tracker_query(sql) / tracker_count(sql) are SYNCHRONOUS — no await. tracker_query returns a LIST OF ROW DICTS; index a row's column (row['url']). A SELECT COUNT(*) is for counting — never the pending list.\n" +
	"• converge(...) and worker(...) are ASYNC: `await converge(pending=..., dispatch=lambda unit, i: worker(...))` — converge awaits the worker coroutines and owns parallelism. worker() in a bare loop without await dispatches NOTHING; `for u in units: await worker(...)` runs serially and defeats meta['concurrency']. Parallel dispatch happens ONLY through converge.\n" +
	"\n" +
	"EXAMPLE (chunked — the default for cheap-per-row work):\n" +
	"  meta = {\"name\": \"enrich\", \"description\": \"...\", \"concurrency\": 4}\n" +
	"  RECEIPT = {\"type\":\"object\",\"required\":[\"processed\"],\"properties\":{\"processed\":{\"type\":\"array\"}}}\n" +
	"  CHUNK = 8\n" +
	"  async def run(args):\n" +
	"      def pending():\n" +
	"          rows = tracker_query(\"SELECT id, url FROM leads WHERE done_at IS NULL\")\n" +
	"          return [{\"ids\": [r['id'] for r in rows[i:i+CHUNK]],\n" +
	"                   \"urls\": [r['url'] for r in rows[i:i+CHUNK]]}\n" +
	"                  for i in range(0, len(rows), CHUNK)]\n" +
	"      await converge(\n" +
	"          pending=pending,\n" +
	"          dispatch=lambda chunk, i: worker(\n" +
	"              task=f\"Enrich EACH of these, upserting its row + done_at as you go: {chunk['urls']}\",\n" +
	"              goal=f\"Researching {len(chunk['ids'])} leads' websites\",\n" +
	"              profile=\"acct-1\", skill=\"enrich\", schema=RECEIPT,\n" +
	"              timeout=120 * len(chunk['ids'])),\n" +
	"          max_rounds=3)\n" +
	"      return {\"remaining\": tracker_count(\"SELECT 1 FROM leads WHERE done_at IS NULL\")}"
