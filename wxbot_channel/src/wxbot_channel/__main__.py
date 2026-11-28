"""Entry point: ``python -m wxbot_channel``.

Default (no subcommand) is the Hive stdio plugin. ``login`` and ``logout`` are
standalone operator commands and use the same credential file as the channel;
they never print the token.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import sys
from pathlib import Path

from . import __version__
from .bridge import ChannelBridge
from .protocol import StdioTransport
from .qr import open_local_path, write_login_qr

DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com"


def _configure_logging(verbose: bool = False) -> None:
    logging.basicConfig(
        stream=sys.stderr,
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(asctime)s [wxbot] %(levelname)s %(message)s",
        force=True,
    )
    logging.getLogger("aiohttp").setLevel(logging.WARNING)


async def _serve(verbose: bool = False) -> None:
    _configure_logging(verbose)
    log = logging.getLogger("wxbot_channel")
    transport = StdioTransport(logger=log)
    bridge = ChannelBridge(transport, logger=log)
    try:
        await transport.serve(bridge.handle)
    finally:
        await bridge.stop()
        transport.close()


def default_cred_path() -> Path:
    configured = os.environ.get("WXBOT_CRED_PATH", "").strip()
    if configured:
        return Path(configured).expanduser()
    return Path.home() / ".wechatbot" / "credentials.json"


def _credential_summary(creds, path: Path) -> dict[str, object]:
    return {
        "cred_path": str(path),
        "user_id": getattr(creds, "user_id", ""),
        "account_id": getattr(creds, "account_id", ""),
        "base_url": getattr(creds, "base_url", ""),
        "saved_at": getattr(creds, "saved_at", None),
    }


async def _login(args: argparse.Namespace) -> int:
    _configure_logging(args.debug)
    log = logging.getLogger("wxbot_channel.login")
    from wechatbot import WeChatBot

    cred_path = Path(args.cred_path).expanduser()
    cred_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    qr_image = (
        Path(args.qr_output).expanduser()
        if args.qr_output
        else cred_path.parent / "login-qr.png"
    )
    if qr_image.suffix.lower() != ".png":
        # The QR artifact is an image; accept a stem or an old .txt-style path
        # and never hand segno an unsupported extension.
        qr_image = qr_image.with_suffix(".png")
    qr_html = qr_image.with_suffix(".html")
    qr_url_file = (
        Path(args.qr_url_output).expanduser()
        if args.qr_url_output
        else qr_image.with_suffix(".txt")
    )
    qr_written = {"image": qr_image, "html": qr_html, "url": qr_url_file}

    def on_qr(url: str) -> None:
        log.info("Scan this URL in WeChat: %s", url)
        image_path, html_path, url_path = write_login_qr(
            url,
            qr_image,
            html_path=qr_html,
            url_path=qr_url_file,
        )
        qr_written.update({"image": image_path, "html": html_path, "url": url_path})
        log.info("QR image: %s", image_path)
        if html_path is not None:
            log.info("QR page:  %s", html_path)
        if url_path is not None:
            log.info("QR URL:   %s", url_path)
        if args.open and html_path is not None:
            if not open_local_path(html_path):
                log.warning("could not open the QR page automatically")

    bot = WeChatBot(
        base_url=args.base_url or None,
        cred_path=str(cred_path),
        on_qr_url=on_qr,
        on_scanned=lambda: log.info("QR scanned; confirm in WeChat"),
        on_expired=lambda: log.warning("QR expired; requesting a new one"),
        on_error=lambda error: log.error("WeChat auth error: %s", error),
    )
    try:
        coro = bot.login(force=args.force)
        if args.timeout and args.timeout > 0:
            creds = await asyncio.wait_for(coro, timeout=args.timeout)
        else:
            creds = await coro
    except asyncio.TimeoutError:
        print(f"login timed out after {args.timeout:g}s", file=sys.stderr)
        return 1
    except Exception as exc:
        print(f"login failed: {exc}", file=sys.stderr)
        return 1

    summary = _credential_summary(creds, cred_path)
    if args.json:
        print(json.dumps({**summary, "qr_image": str(qr_written["image"])}, ensure_ascii=False))
    else:
        print(f"logged in as {summary['user_id']} ({summary['account_id']})", file=sys.stderr)
        print(f"credentials: {cred_path}", file=sys.stderr)
        print(f"QR image:    {qr_written['image']}", file=sys.stderr)
    return 0


def _logout(args: argparse.Namespace) -> int:
    _configure_logging(args.debug)
    cred_path = Path(args.cred_path).expanduser()
    existed = cred_path.exists()
    qr_files = [
        cred_path.parent / "login-qr.png",
        cred_path.parent / "login-qr.html",
        cred_path.parent / "login-qr.txt",
    ]
    removed_qr: list[str] = []
    try:
        cred_path.unlink(missing_ok=True)
        for path in qr_files:
            if path.exists():
                path.unlink()
                removed_qr.append(str(path))
    except OSError as exc:
        print(f"logout failed: {exc}", file=sys.stderr)
        return 1
    result = {"cred_path": str(cred_path), "removed": existed, "removed_qr": removed_qr}
    if args.json:
        print(json.dumps(result, ensure_ascii=False))
    else:
        print(
            f"removed credentials: {cred_path}"
            if existed
            else f"no credentials at {cred_path}",
            file=sys.stderr,
        )
    return 0


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wxbot-channel",
        description="WeChat iLink channel for the Hive stdio channel host",
    )
    parser.add_argument("--version", action="store_true", help="print version and exit")
    parser.add_argument("--debug", action="store_true", help="verbose stderr logging")
    sub = parser.add_subparsers(dest="command")

    serve = sub.add_parser("serve", help="run as a Hive stdio channel plugin (default)")
    serve.add_argument("--debug", action="store_true", help="verbose stderr logging")

    login = sub.add_parser("login", help="QR login and persist WeChat credentials")
    login.add_argument(
        "--cred-path",
        default=str(default_cred_path()),
        help="credential file (default: $WXBOT_CRED_PATH or ~/.wechatbot/credentials.json)",
    )
    login.add_argument("--base-url", default=DEFAULT_BASE_URL, help="iLink API base URL")
    login.add_argument("--force", action="store_true", help="ignore existing credentials and re-login")
    login.add_argument(
        "--qr-output",
        default="",
        help="QR PNG path (default: <cred-dir>/login-qr.png)",
    )
    login.add_argument(
        "--qr-url-output",
        default="",
        help="QR URL text path (default: <cred-dir>/login-qr.txt)",
    )
    login.add_argument("--open", action="store_true", help="open the QR HTML page after it is written")
    login.add_argument(
        "--timeout", type=float, default=0, help="overall login timeout in seconds (0 = SDK default)"
    )
    login.add_argument("--json", action="store_true", help="print metadata as JSON (never the token)")
    login.add_argument("--debug", action="store_true", help="verbose stderr logging")

    logout = sub.add_parser("logout", help="remove the persisted WeChat credentials")
    logout.add_argument(
        "--cred-path",
        default=str(default_cred_path()),
        help="credential file (default: $WXBOT_CRED_PATH or ~/.wechatbot/credentials.json)",
    )
    logout.add_argument("--json", action="store_true", help="print removal result as JSON")
    logout.add_argument("--debug", action="store_true", help="verbose stderr logging")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    if args.version:
        # Only a human standalone invocation reaches this path; the host
        # starts the plugin without arguments and stdout stays protocol-only.
        print(f"wxbot-channel {__version__}")
        return 0
    if args.command == "login":
        return asyncio.run(_login(args))
    if args.command == "logout":
        return _logout(args)
    try:
        asyncio.run(_serve(bool(getattr(args, "debug", False))))
    except KeyboardInterrupt:
        return 0
    except Exception:
        logging.getLogger("wxbot_channel").exception("fatal channel error")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
