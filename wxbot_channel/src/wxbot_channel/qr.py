"""QR login artifacts: a scannable PNG plus a tiny HTML viewer."""

from __future__ import annotations

import html
import os
from pathlib import Path


def write_login_qr(
    url: str,
    image_path: Path,
    *,
    html_path: Path | None = None,
    url_path: Path | None = None,
) -> tuple[Path, Path | None, Path | None]:
    """Write the QR URL as a PNG, optionally an HTML page and text URL file.

    The QR image is the primary artifact. The HTML page embeds it so a desktop
    can open one file and show a scannable code without a QR library in the
    browser. Every artifact is written 0600 because the URL is a short-lived
    login capability.
    """
    try:
        import segno
    except ImportError as exc:  # pragma: no cover - pyproject installs it
        raise RuntimeError(
            "QR image generation requires segno; run `uv sync` in wxbot_channel"
        ) from exc

    image_path = image_path.expanduser()
    image_path.parent.mkdir(parents=True, exist_ok=True)
    qr = segno.make(url, error="m")
    qr.save(str(image_path), scale=8, border=4)
    try:
        image_path.chmod(0o600)
    except OSError:
        pass

    written_url: Path | None = None
    if url_path is not None:
        url_path = url_path.expanduser()
        url_path.parent.mkdir(parents=True, exist_ok=True)
        url_path.write_text(url + "\n", encoding="utf-8")
        try:
            url_path.chmod(0o600)
        except OSError:
            pass
        written_url = url_path

    written_html: Path | None = None
    if html_path is not None:
        html_path = html_path.expanduser()
        html_path.parent.mkdir(parents=True, exist_ok=True)
        # Use a sibling filename so the page works when opened from disk.
        src = html.escape(image_path.name, quote=True)
        page = f"""<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<title>WeChat login</title>
<style>
  body {{ font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 2rem;
         background: #111; color: #eee; }}
  .card {{ max-width: 560px; margin: auto; background: #1c1c1c; padding: 24px;
           border-radius: 16px; border: 1px solid #333; }}
  img {{ width: 100%; max-width: 420px; height: auto; display: block; margin: 16px auto;
         background: white; border-radius: 8px; }}
  code {{ word-break: break-all; color: #9cdcfe; }}
</style>
<div class="card">
  <h1>Scan to log in to WeChat</h1>
  <p>Open WeChat on your phone and scan this QR code.</p>
  <img src="{src}" alt="WeChat login QR">
  <p>If the image cannot be scanned, open this URL on the phone:</p>
  <p><code>{html.escape(url)}</code></p>
</div>
</html>
"""
        html_path.write_text(page, encoding="utf-8")
        try:
            html_path.chmod(0o600)
        except OSError:
            pass
        written_html = html_path

    return image_path, written_html, written_url


def open_local_path(path: Path) -> bool:
    """Best-effort OS open; returns False instead of making login fail."""
    try:
        if os.name == "nt":
            os.startfile(path)  # type: ignore[attr-defined]
        else:
            import webbrowser

            return webbrowser.open(path.resolve().as_uri())
    except Exception:
        return False
    return True
