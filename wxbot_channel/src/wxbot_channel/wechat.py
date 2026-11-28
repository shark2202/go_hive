"""Real wechatbot-sdk adapter.

The SDK owns QR login, the iLink long-poll loop, context tokens and media
upload/download. This adapter only maps those operations to the small
interface the bridge needs, so tests can substitute a fake.
"""

from __future__ import annotations

import asyncio
import logging
from pathlib import Path
from typing import Any, Awaitable, Callable

from .bridge import BridgeConfig
from .qr import write_login_qr


class SdkWeChatAdapter:
    def __init__(
        self,
        config: BridgeConfig,
        state_dir: Path,
        on_message: Callable[[Any], Awaitable[None]],
    ) -> None:
        from wechatbot import WeChatBot

        self.config = config
        self.state_dir = state_dir
        self.on_message = on_message
        self.log = logging.getLogger("wxbot_channel.wechat")
        self._closed = False
        self._last_message: dict[str, Any] = {}
        self._send_locks: dict[str, asyncio.Lock] = {}
        Path(config.cred_path).expanduser().parent.mkdir(parents=True, exist_ok=True)
        self.bot = WeChatBot(
            base_url=config.base_url or None,
            cred_path=config.cred_path,
            on_qr_url=self._on_qr_url,
            on_scanned=lambda: self.log.info("WeChat QR scanned"),
            on_expired=lambda: self.log.warning("WeChat QR expired; restart login"),
            on_error=lambda error: self.log.error("WeChat SDK error: %s", error),
        )
        self.bot.on_message(self._handle_message)

    # ── lifecycle ─────────────────────────────────────────────────────

    async def run(self) -> None:
        delay = 1.0
        while not self._closed:
            try:
                await self.bot.login()
                self.log.info("WeChat logged in")
                await self.bot.start()
                if self._closed:
                    return
                self.log.warning("WeChat long-poll ended; retrying login")
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                if self._closed:
                    return
                self.log.error("WeChat login/start failed: %s", exc)
            await asyncio.sleep(delay)
            delay = min(delay * 2, 30.0)

    async def close(self) -> None:
        self._closed = True
        try:
            self.bot.stop()
        except Exception:
            self.log.debug("WeChat stop failed", exc_info=True)

    def _on_qr_url(self, url: str) -> None:
        # stderr is the only safe log channel; stdout is the JSON-RPC wire.
        self.log.info("Scan this URL to log in to WeChat: %s", url)
        try:
            image_path = self.state_dir / "login-qr.png"
            _image, html_path, url_path = write_login_qr(
                url,
                image_path,
                html_path=self.state_dir / "login-qr.html",
                url_path=self.state_dir / "login-qr.txt",
            )
            self.log.info("QR image written: %s", _image)
            if html_path is not None:
                self.log.info("QR page written: %s", html_path)
            if url_path is not None:
                self.log.info("QR URL written: %s", url_path)
        except Exception:
            self.log.exception("could not persist the login QR image")

    async def _handle_message(self, msg: Any) -> None:
        user_id = str(getattr(msg, "user_id", "") or "")
        if user_id:
            self._last_message[user_id] = msg
        try:
            await self.on_message(msg)
        except Exception:
            self.log.exception("message handler failed")

    # ── outgoing ─────────────────────────────────────────────────────

    def _lock(self, user_id: str) -> asyncio.Lock:
        return self._send_locks.setdefault(user_id, asyncio.Lock())

    async def send_text(self, user_id: str, text: str) -> None:
        async with self._lock(user_id):
            last = self._last_message.get(user_id)
            try:
                if last is not None:
                    await self.bot.reply(last, text)
                else:
                    await self.bot.send(user_id, text)
            except Exception as exc:
                self.log.error("WeChat text send failed for %s: %s", user_id, exc)

    async def send_file(self, user_id: str, path: str) -> None:
        try:
            data = Path(path).read_bytes()
        except OSError as exc:
            self.log.error("cannot read outgoing file %s: %s", path, exc)
            return
        ext = Path(path).suffix.lower()
        if ext in {".png", ".jpg", ".jpeg", ".gif", ".webp"}:
            content: dict[str, Any] = {"image": data}
        elif ext in {".mp4", ".mov", ".m4v", ".webm"}:
            content = {"video": data, "caption": Path(path).name}
        else:
            content = {"file": data, "file_name": Path(path).name}
        async with self._lock(user_id):
            last = self._last_message.get(user_id)
            try:
                if last is not None:
                    await self.bot.reply_media(last, content)
                else:
                    await self.bot.send_media(user_id, content)
            except Exception as exc:
                self.log.error("WeChat media send failed for %s: %s", user_id, exc)

    async def send_typing(self, user_id: str) -> None:
        try:
            await self.bot.send_typing(user_id)
        except Exception:
            self.log.debug("typing failed", exc_info=True)

    async def stop_typing(self, user_id: str) -> None:
        try:
            await self.bot.stop_typing(user_id)
        except Exception:
            self.log.debug("stop typing failed", exc_info=True)

    # ── incoming media ───────────────────────────────────────────────

    async def download_image(self, image: Any) -> bytes | None:
        media = getattr(image, "media", None)
        if media is None:
            self.log.debug("image has no CDN media reference; skipping")
            return None
        aes_key = getattr(image, "aes_key", None)
        return await self.bot.download_raw(media, aes_key)

    async def download_file(self, item: Any) -> bytes | None:
        media = getattr(item, "media", None)
        if media is None:
            return None
        return await self.bot.download_raw(media)
