#!/usr/bin/env python3
"""Measures the Python reference output for the R01/R02/R07 contract with the
same inputs the Go e2e uses (1280x720 viewport, one link "Docs").

Runs the real upstream tool-layer functions:
  * render_screenshot(spill=True) — the CLI screenshot return shape
  * BeelineBridge._format_ax_tree      — the AX tree text (refs/href)
  * refs.annotate_snapshot      — the [ref=eN] injection used by the MCP tool
  * classify_result             — the exit code for File not found / not started
No bridge process or Chrome needed; ports 14829/14831 stay untouched.
"""

from __future__ import annotations

import asyncio
import base64
import io
import json
import os
import sys
from pathlib import Path

# Repo root = this file's location up to the checkout root (…/go_hive/docs/fixtures/<name>).
ROOT = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(ROOT / "tools" / "src"))

SCRATCH = Path(os.environ.get("BRC_SCRATCH", "/tmp/brc-python-reference"))
OUT = Path(__file__).resolve().parent
os.environ["HIVE_STORAGE_PATH"] = str(SCRATCH / "py-storage")

from PIL import Image  # noqa: E402

from gcu.browser.bridge import BeelineBridge  # noqa: E402
from gcu.browser.tools.inspection import render_screenshot  # noqa: E402
from gcu.errors import classify_result  # noqa: E402


def viewport_png() -> str:
    img = Image.new("RGB", (1280, 720), (245, 245, 245))
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return base64.b64encode(buf.getvalue()).decode()


class FakeBridge:
    """The exact bridge-side payloads the shared e2e fake extension produces."""

    is_connected = True

    def __init__(self, png: str) -> None:
        self._png = png

    async def screenshot(self, tab_id, **kwargs):
        return {
            "ok": True,
            "tabId": tab_id,
            "url": "https://example.com/",
            "devicePixelRatio": 1,
            "cssWidth": 1280,
            "cssHeight": 720,
            "pngWidth": 1280,
            "pngHeight": 720,
            "data": self._png,
            "mimeType": "image/png",
        }


AX_NODES = [
    {"nodeId": "1", "ignored": False, "role": {"value": "RootWebArea"},
     "name": {"value": "Doc"}, "childIds": ["2", "3"], "properties": []},
    {"nodeId": "2", "ignored": False, "role": {"value": "button"},
     "name": {"value": "Go"}, "childIds": [], "properties": []},
    {"nodeId": "3", "ignored": False, "role": {"value": "link"},
     "name": {"value": "Docs"}, "childIds": [], "properties": []},
]
HREF_MAP = {"Docs": ["https://example.com/docs"]}


async def main() -> None:
    summary = {}

    # R01 — CLI screenshot (spill=True): the upstream return shape.
    shot = await render_screenshot(FakeBridge(viewport_png()), 101, spill=True)
    saved = Path(shot["saved_to"])
    with Image.open(saved) as img:
        image_info = {"format": img.format, "width": img.width, "height": img.height}
    summary["screenshot"] = {
        "keys": sorted(shot.keys()),
        "meta": {k: v for k, v in shot.items() if k not in ("saved_to", "_image")},
        "saved_to": str(saved),
        "saved_name": saved.name,
        "imageType": shot["imageType"],
        "imageWidth": shot["imageWidth"],
        "size": shot["size"],
        "_image": shot["_image"],
        "annotated": shot["annotated"],
        "decoded": image_info,
    }

    # R02 — AX tree text with the same nodes the Go e2e serves.
    tree = BeelineBridge._format_ax_tree(None, AX_NODES, 2000, "default", HREF_MAP)  # type: ignore[arg-type]
    summary["snapshot"] = {
        "tree": tree,
        "tree_chars": len(tree),
        "tool_return": {
            "ok": True, "tabId": 101, "url": "https://example.com/",
            "length": len(tree), "saved_to": "<writes browser_snapshot_<ms>_tab101.txt>",
        },
    }

    # R07 — exit-code taxonomy for the upload failures.
    summary["exit_codes"] = {
        "file_not_found": classify_result({"ok": False, "error": "File not found: /tmp/nope.txt"}),
        "not_started": classify_result({"ok": False, "error": "Browser not started"}),
    }

    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / "python-reference.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    asyncio.run(main())
