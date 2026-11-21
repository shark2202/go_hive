#!/usr/bin/env python3
"""Render the vision-caption E2E screenshot fixture and its ground truth.

The caption chain is only proved by a real image whose interactive elements sit
at known viewport fractions, so the caption's coordinates can be scored. This
renders a small admin page in headless Chrome at 1000x700 and measures the blue
Submit button by colour, writing:

    <out>/screenshot.png
    <out>/screenshot-ground-truth.json

Chrome is used when present (macOS path below, or $CHROME); otherwise the same
layout is drawn with PIL, which keeps the harness runnable anywhere.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

WIDTH, HEIGHT = 1000, 700

HTML = """<!doctype html>
<html><head><meta charset="utf-8"><style>
  body { margin:0; font-family: -apple-system, Helvetica, sans-serif; background:#f7f8fa; }
  header { height:80px; background:#1f2a44; color:#fff; display:flex; align-items:center; padding:0 32px; font-size:22px; }
  .card { margin:56px auto; width:640px; background:#fff; border-radius:12px; padding:32px; box-shadow:0 2px 8px rgba(0,0,0,.12); }
  h1 { font-size:26px; margin:0 0 12px; }
  p  { color:#444; font-size:17px; line-height:1.5; }
  .row { display:flex; justify-content:flex-end; gap:16px; margin-top:36px; }
  button { font-size:18px; padding:14px 34px; border-radius:8px; border:1px solid #c9ced8; background:#fff; cursor:pointer; }
  .primary { background:#2f6bff; color:#fff; border-color:#2f6bff; }
  .search { margin:24px 0 0; display:flex; gap:12px; }
  input { flex:1; font-size:17px; padding:12px 14px; border:1px solid #c9ced8; border-radius:8px; }
</style></head><body>
  <header>Acme Admin</header>
  <div class="card">
    <h1>Create invoice</h1>
    <p>Customer: Northwind Traders &middot; Amount due: 1,280.00 USD</p>
    <div class="search">
      <input placeholder="Search customers" />
      <button id="find">Find</button>
    </div>
    <div class="row">
      <button id="cancel">Cancel</button>
      <button id="submit" class="primary">Submit</button>
    </div>
  </div>
</body></html>
"""


def chrome_path() -> str | None:
    for candidate in (
        os.environ.get("CHROME"),
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/Applications/Chromium.app/Contents/MacOS/Chromium",
        "/usr/bin/google-chrome",
        "/usr/bin/chromium",
    ):
        if candidate and os.path.exists(candidate):
            return candidate
    return None


def render_html(html_path: Path, png_path: Path) -> bool:
    chrome = chrome_path()
    if not chrome:
        return False
    cmd = [
        chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars",
        "--force-device-scale-factor=1", f"--window-size={WIDTH},{HEIGHT}",
        f"--screenshot={png_path}", html_path.as_uri(),
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, timeout=180)
    if not png_path.exists():
        print("chrome failed:", proc.returncode, proc.stderr[-800:], file=sys.stderr)
        return False
    return True


def render_pil(png_path: Path) -> None:
    from PIL import Image, ImageDraw

    img = Image.new("RGB", (WIDTH, HEIGHT), (247, 248, 250))
    draw = ImageDraw.Draw(img)
    draw.rectangle([0, 0, WIDTH, 80], fill=(31, 42, 68))
    draw.text((32, 32), "Acme Admin", fill=(255, 255, 255))
    draw.rounded_rectangle([180, 140, 820, 560], radius=12, fill=(255, 255, 255), outline=(220, 224, 232))
    draw.text((212, 172), "Create invoice", fill=(20, 20, 20))
    draw.text((212, 210), "Customer: Northwind Traders  Amount due: 1,280.00 USD", fill=(60, 60, 60))
    draw.rounded_rectangle([212, 258, 640, 300], radius=8, outline=(200, 205, 216))
    draw.text((228, 272), "Search customers", fill=(120, 124, 132))
    draw.rounded_rectangle([656, 258, 756, 300], radius=8, outline=(200, 205, 216))
    draw.text((684, 272), "Find", fill=(20, 20, 20))
    draw.rounded_rectangle([404, 460, 542, 512], radius=8, outline=(200, 205, 216))
    draw.text((440, 478), "Cancel", fill=(20, 20, 20))
    draw.rounded_rectangle([558, 460, 712, 512], radius=8, fill=(47, 107, 255))
    draw.text((600, 478), "Submit", fill=(255, 255, 255))
    img.save(png_path)


def measure_fixture(png_path: Path) -> dict:
    """Locate the blue Submit button by colour to pin ground-truth fractions."""
    from PIL import Image

    img = Image.open(png_path).convert("RGB")
    width, height = img.size
    pixels = img.load()
    xs, ys = [], []
    for y in range(height):
        for x in range(width):
            r, g, b = pixels[x, y]
            if b > 180 and b - r > 60 and b - g > 40:
                xs.append(x)
                ys.append(y)
    if not xs:
        return {}
    return {
        "submit": (round((min(xs) + max(xs)) / 2 / width, 4), round((min(ys) + max(ys)) / 2 / height, 4)),
        "submit_box": (min(xs), min(ys), max(xs), max(ys)),
        "size": (width, height),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", default="docs/e2e-assets/vision-caption/fixture")
    parser.add_argument("--force", action="store_true", help="re-render even when the PNG exists")
    args = parser.parse_args()

    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    png = out / "screenshot.png"
    truth = out / "screenshot-ground-truth.json"
    if png.exists() and not args.force:
        print(f"{png} exists; pass --force to re-render")
        return 0

    html_path = out / "screenshot.html"
    html_path.write_text(HTML, encoding="utf-8")
    renderer = "chrome"
    if not render_html(html_path, png):
        renderer = "pil"
        render_pil(png)

    ground = {"renderer": renderer, "measured": measure_fixture(png), "viewport": {"width": WIDTH, "height": HEIGHT}}
    truth.write_text(json.dumps(ground, indent=2) + "\n")
    print(json.dumps(ground, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
