#!/usr/bin/env python3
"""记录一次全量 CI 结果到 docs/reports/ci-latest.json 并提交（供汇报引用）。

用法: record_ci.py <wave> <ci_rc> <qc_rc> <publish_rc> [note]
"""
from __future__ import annotations

import json
import subprocess
import sys
from datetime import datetime
from pathlib import Path

REPO = Path("/Users/mac/hive")
OUT = REPO / "go_hive" / "docs" / "reports" / "ci-latest.json"


def main(argv):
    if len(argv) < 5:
        print(__doc__)
        return 2
    wave, ci, qc, pub = argv[1], argv[2], argv[3], argv[4]
    note = " ".join(argv[5:]) if len(argv) > 5 else ""
    head = subprocess.run(
        ["git", "-C", str(REPO), "rev-parse", "--short", "HEAD"],
        capture_output=True, text=True, check=False,
    ).stdout.strip()
    data = {
        "wave": int(wave),
        "at": datetime.now().strftime("%Y-%m-%d %H:%M"),
        "head": head,
        "ci": int(ci),
        "qc": int(qc),
        "publish": int(pub),
        "note": note,
    }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(data, ensure_ascii=False, indent=1) + "\n")
    rel = str(OUT.relative_to(REPO))
    subprocess.run(["git", "-C", str(REPO), "add", rel], check=False)
    subprocess.run(
        ["git", "-C", str(REPO), "-c", "user.name=hive-governor", "-c", "user.email=hive-governor@local",
         "commit", "-m", f"reports: CI wave-{wave} 结果记录（ci={ci} qc={qc} publish={pub}）", "--", rel],
        check=False,
    )
    print(f"recorded wave-{wave}: ci={ci} qc={qc} publish={pub} head={head}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
