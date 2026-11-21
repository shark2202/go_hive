"""Upstream-format fixture: meta + lane + converge + retry/backoff + deadletter.

Byte-compatible with the scripts the Python Hive runner accepts (it is a
`playbooks/<name>.play.py` file verbatim); the Go port must run it unchanged and
leave the file untouched.
"""

meta = {
    "name": "upstream-lanes",
    "description": "upstream fixture: lanes + converge + deadletter",
    "concurrency": 2,
}

async def run(args):
    lane("acct-1", concurrency=1)
    await converge(
        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),
        dispatch=lambda row, i: worker(
            task=f"Process job {row['name']} (id {row['id']})",
            data={"id": row["id"], "name": row["name"]},
            goal=f"Processing {row['name']}",
            lane="acct-1",
            retries=1,
            backoff="linear",
            timeout=60,
        ),
        max_rounds=2,
    )
    log("converge finished")
    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0"), "deadletter": deadletter.size}
