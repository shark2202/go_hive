#!/usr/bin/env python3
"""Two-sided dpr probe — analysis half.

For every dpr directory the Go probe wrote (manifest.json + raw.png + go.jpg):

* feed the SAME raw capture through the upstream implementation
  (tools/src/gcu/browser/tools/inspection.py::_resize_and_annotate) with the
  same highlight rect, producing the reference annotated JPEG;
* measure both annotated JPEGs at the geometry/region level (encoding bytes
  are allowed to differ):
    - image size;
    - the annotation rectangle's four edges via scanlines through the
      rectangle's middle (the label band sits above the rectangle, so the
      scans stay clean);
    - the mean RGB of an inside-rect patch and of an outside patch;
    - the label band bounding box above the rectangle.

Prints a table and writes report.json next to the evidence.

Usage (needs the tools project env for PIL + gcu):
  uv run --offline --project tools python browser_dpr_probe_analyze.py <evidence-dir>
"""

from __future__ import annotations

import base64
import json
import sys
from pathlib import Path

from PIL import Image

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "tools" / "src"))

from gcu.browser.tools.inspection import _resize_and_annotate  # noqa: E402


def annotation_blue(rgb) -> bool:
    """Overlay blue: the translucent fill, the outline and the label background.

    Thresholds catch the 0.27-alpha fill over both the white page and the dark
    target, but not the un-annotated dark target (#334155: b-r=34, b=85 < 100).
    A tighter outline-only threshold fails on the Go side at dpr 1-3: the DOM
    border is 2 CSS px, i.e. ~1.25 image px at an 800px output, and JPEG chroma
    subsampling dilutes a 1px blue line below it (measured).
    """
    r, g, b = rgb[0], rgb[1], rgb[2]
    return b >= 100 and (b - r) >= 30 and (b - g) >= 8


def patch_mean(px, x0: int, y0: int, w: int, h: int):
    n = 0
    acc = [0, 0, 0]
    for y in range(y0, y0 + h):
        for x in range(x0, x0 + w):
            r, g, b = px[x, y][:3]
            acc[0] += r
            acc[1] += g
            acc[2] += b
            n += 1
    return tuple(round(v / max(1, n), 1) for v in acc)


def measure(path: Path, highlight: dict, css_w: int, css_h: int) -> dict:
    img = Image.open(path).convert("RGB")
    w, h = img.size
    px = img.load()

    sx, sy = w / css_w, h / css_h
    exp_left = highlight["x"] * sx
    exp_top = highlight["y"] * sy
    exp_right = (highlight["x"] + highlight["w"]) * sx
    exp_bottom = (highlight["y"] + highlight["h"]) * sy

    # Row scan through the rect's vertical middle: only the left/right borders
    # are strong blue there (the label band is above the rect).
    y_mid = int(round((exp_top + exp_bottom) / 2))
    xs = [x for x in range(w) if annotation_blue(px[x, y_mid])]
    # Column scan at 3/4 across the rect (right of where the label band ends):
    # only the top/bottom borders are strong blue there.
    x_mid = int(round(exp_left + 0.75 * (exp_right - exp_left)))
    ys = [y for y in range(h) if annotation_blue(px[x_mid, y])]

    left = min(xs) if xs else None
    right = max(xs) if xs else None
    top = min(ys) if ys else None
    bottom = max(ys) if ys else None
    if None in (left, right, top, bottom):
        raise SystemExit(f"{path}: annotation rectangle not found (scanlines empty)")

    inside = patch_mean(px, int((exp_left + exp_right) / 2) - 20, y_mid - 10, 40, 20)
    out_y = min(h - 21, int(exp_bottom) + 40)
    outside = patch_mean(px, int((exp_left + exp_right) / 2) - 20, out_y, 40, 20)

    label_pts = [(x, y) for y in range(0, max(0, (top or 0) - 2)) for x in range(w)
                 if annotation_blue(px[x, y])]
    if label_pts:
        lxs = [p[0] for p in label_pts]
        lys = [p[1] for p in label_pts]
        label_box = [min(lxs), min(lys), max(lxs), max(lys)]
    else:
        label_box = None

    def fmt(v):
        return None if v is None else round(v, 2)

    return {
        "path": str(path),
        "image": [w, h],
        "scale": [round(sx, 4), round(sy, 4)],
        "expected": [fmt(exp_left), fmt(exp_top), fmt(exp_right), fmt(exp_bottom)],
        "rect_edges": {"left": left, "top": top, "right": right, "bottom": bottom},
        "rect_px": None if None in (left, top, right, bottom) else [right - left, bottom - top],
        "rect_fraction": None if left is None else {
            "x0": round(left / w, 4), "y0": round(top / h, 4),
            "w": round((right - left) / w, 4), "h": round((bottom - top) / h, 4),
        },
        "inside_mean": inside,
        "outside_mean": outside,
        "label_box": label_box,
        "label_gap_above_rect": None if (label_box is None or top is None) else top - label_box[3],
    }


def main() -> int:
    ev = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    report = []
    print(f"evidence: {ev}")
    print()
    header = ("dpr side image       rect_edges(l,t,r,b)        rect_px     insideRGB          "
              "outsideRGB         label_box(l,t,r,b)  gap")
    print(header)
    print("-" * len(header))

    for dpr_dir in sorted(
        (p for p in ev.rglob("dpr*") if (p / "manifest.json").exists()),
        key=lambda p: int(p.name[3:]),
    ):
        manifest = json.loads((dpr_dir / "manifest.json").read_text())
        dpr = manifest["dpr"]
        css_w, css_h = manifest["cssWidth"], manifest["cssHeight"]
        highlight = manifest["highlight"]

        raw = Image.open(dpr_dir / "raw.png")
        expected_raw = (round(css_w * dpr), round(css_h * dpr))

        # Upstream reference: same raw PNG, same highlight, same css width/dpr.
        py_b64, py_scale = _resize_and_annotate(
            base64.b64encode((dpr_dir / "raw.png").read_bytes()).decode(),
            css_w,
            dpr,
            [highlight],
        )
        py_path = dpr_dir / "py.jpg"
        py_path.write_bytes(base64.b64decode(py_b64))

        go = measure(dpr_dir / "go.jpg", highlight, css_w, css_h)
        py = measure(py_path, highlight, css_w, css_h)

        entry = {
            "dpr": dpr,
            "raw_png": {"size": list(raw.size), "expected": list(expected_raw),
                        "match": list(raw.size) == list(expected_raw)},
            "highlight": highlight,
            "go_meta": manifest["goMeta"],
            "raw_meta": manifest["rawMeta"],
            "python_physical_scale": py_scale,
            "python": py,
            "go": go,
        }
        report.append(entry)

        for side, m in (("py", py), ("go", go)):
            e = m["rect_edges"]
            print(f"{dpr:<3} {side:<4} {m['image'][0]}x{m['image'][1]:<7} "
                  f"({e['left']},{e['top']},{e['right']},{e['bottom']})        "
                  f"{str(m['rect_px']):<11} {str(m['inside_mean']):<18} {str(m['outside_mean']):<18} "
                  f"{str(m['label_box']):<18} {m['label_gap_above_rect']}")
        dl = None if (go["rect_edges"]["left"] is None or py["rect_edges"]["left"] is None) \
            else go["rect_edges"]["left"] - py["rect_edges"]["left"]
        entry["go_minus_py_edges"] = {
            k: (None if go["rect_edges"][k] is None or py["rect_edges"][k] is None
                else go["rect_edges"][k] - py["rect_edges"][k])
            for k in ("left", "top", "right", "bottom")
        }
        print(f"    raw {raw.size} (expected {expected_raw})  "
              f"go-image {go['image']}  py-image {py['image']}  "
              f"go-py left edge delta: {dl}")

    (ev / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print()
    print(f"report: {ev / 'report.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
