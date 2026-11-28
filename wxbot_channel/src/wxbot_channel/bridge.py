"""Bridge between WeChat messages and one Hive queen session.

One Hive session is kept per WeChat conversation (private chat id, or a group
id if the SDK reports one). The mapping is persisted in the channel state
directory so a plugin restart resumes the same Hive session.
"""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Awaitable, Callable, Protocol

from . import __version__
from .protocol import (
    CODE_INVALID_PARAMS,
    CODE_METHOD_NOT_FOUND,
    RpcError,
)

MAX_IMAGES_PER_SEND = 8
MAX_IMAGE_BYTES = 4 * 1024 * 1024


class Adapter(Protocol):
    async def run(self) -> None: ...
    async def close(self) -> None: ...
    async def send_text(self, user_id: str, text: str) -> None: ...
    async def send_file(self, user_id: str, path: str) -> None: ...
    async def send_typing(self, user_id: str) -> None: ...
    async def stop_typing(self, user_id: str) -> None: ...
    async def download_image(self, image: Any) -> bytes | None: ...
    async def download_file(self, item: Any) -> bytes | None: ...


@dataclass
class BridgeConfig:
    queen_id: str
    title_prefix: str = "WeChat"
    allow_users: tuple[str, ...] = ()
    shared_session: bool = False
    max_text_chars: int = 4000
    max_images: int = MAX_IMAGES_PER_SEND
    fixed_session_id: str = ""
    base_url: str = ""
    cred_path: str = ""
    adapter_kind: str = "wechatbot-sdk"

    @classmethod
    def from_section(cls, raw: dict[str, Any], state_dir: Path) -> "BridgeConfig":
        queen = str(raw.get("queen_id") or raw.get("queen") or "").strip()
        allow = raw.get("allow_users")
        if isinstance(allow, list):
            allowed = tuple(str(item).strip() for item in allow if str(item).strip())
        elif isinstance(allow, str) and allow.strip():
            allowed = (allow.strip(),)
        else:
            allowed = ()
        max_images = raw.get("max_images", MAX_IMAGES_PER_SEND)
        try:
            max_images = max(1, min(MAX_IMAGES_PER_SEND, int(max_images)))
        except (TypeError, ValueError):
            max_images = MAX_IMAGES_PER_SEND
        try:
            max_text = max(200, int(raw.get("max_text_chars", 4000)))
        except (TypeError, ValueError):
            max_text = 4000

        cred_path = str(raw.get("cred_path") or "").strip()
        if not cred_path:
            cred_path = str(state_dir / "credentials.json")
        else:
            cred_path = os.path.expanduser(cred_path)

        return cls(
            queen_id=queen,
            title_prefix=str(raw.get("title_prefix") or "WeChat"),
            allow_users=allowed,
            shared_session=bool(raw.get("shared_session", False)),
            max_text_chars=max_text,
            max_images=max_images,
            fixed_session_id=str(raw.get("session_id") or "").strip(),
            base_url=str(raw.get("base_url") or "").strip(),
            cred_path=cred_path,
            adapter_kind=str(raw.get("adapter") or "wechatbot-sdk").strip(),
        )


class SessionStore:
    """small JSON map: external conversation key -> Hive session id."""

    def __init__(self, path: Path):
        self.path = path
        self._values: dict[str, str] = {}
        self._load()

    def _load(self) -> None:
        try:
            data = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return
        if isinstance(data, dict):
            self._values = {
                str(key): str(value)
                for key, value in data.items()
                if isinstance(key, str) and isinstance(value, str) and value
            }

    def get(self, key: str) -> str:
        return self._values.get(key, "")

    def set(self, key: str, session_id: str) -> None:
        if not session_id:
            return
        self._values[key] = session_id
        self.path.parent.mkdir(parents=True, exist_ok=True)
        temp = self.path.with_suffix(".tmp")
        temp.write_text(
            json.dumps(self._values, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        os.replace(temp, self.path)


@dataclass
class _Conversation:
    key: str
    user_id: str
    session_id: str = ""
    subscription_id: str = ""


class ChannelBridge:
    def __init__(
        self,
        transport: Any,
        *,
        adapter_factory: Callable[[BridgeConfig, Path, Callable[[Any], Awaitable[None]]], Adapter] | None = None,
        logger: logging.Logger | None = None,
    ) -> None:
        self.transport = transport
        self.adapter_factory = adapter_factory
        self.log = logger or logging.getLogger("wxbot_channel.bridge")
        self.config: BridgeConfig | None = None
        self.state_dir = Path(".")
        self.store: SessionStore | None = None
        self.adapter: Adapter | None = None
        self._adapter_task: asyncio.Task[Any] | None = None
        self._started = False
        self._stopping = False
        self._conversations: dict[str, _Conversation] = {}
        self._subscriptions: dict[str, _Conversation] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    # ── host requests ─────────────────────────────────────────────────

    async def handle(self, method: str, params: dict[str, Any]) -> Any:
        if method == "hello":
            return self._hello(params)
        if method == "start":
            await self.start()
            return {}
        if method == "stop":
            await self.stop()
            return {}
        if method == "output":
            await self.on_output(params)
            return {}
        raise RpcError(
            CODE_METHOD_NOT_FOUND,
            f"method {method!r} is not supported by wxbot-channel",
            "invalid_params",
        )

    def _hello(self, params: dict[str, Any]) -> dict[str, Any]:
        protocol = str(params.get("protocol") or "")
        if protocol and protocol != "hive-channel/1":
            raise RpcError(
                CODE_INVALID_PARAMS,
                f"unsupported protocol {protocol!r}; expected 'hive-channel/1'",
                "invalid_params",
            )
        raw_config = params.get("config")
        if not isinstance(raw_config, dict):
            raw_config = {}
        state_dir = Path(str(params.get("state_dir") or ".")).expanduser()
        state_dir.mkdir(parents=True, exist_ok=True)
        self.state_dir = state_dir
        self.config = BridgeConfig.from_section(raw_config, state_dir)
        self.store = SessionStore(state_dir / "sessions.json")
        self.log.info(
            "hello channel=%s queen=%s adapter=%s",
            params.get("name"),
            self.config.queen_id or "(missing)",
            self.config.adapter_kind,
        )
        return {
            "plugin_version": __version__,
            "capabilities": ["text", "images", "files", "typing"],
        }

    async def start(self) -> None:
        if self._started:
            return
        if self.config is None:
            raise RpcError(CODE_INVALID_PARAMS, "start before hello", "invalid_params")
        if not self.config.queen_id:
            raise RpcError(
                CODE_INVALID_PARAMS,
                "channels.wxbot.queen_id is required (the queen profile id to talk to)",
                "invalid_params",
            )
        if self.adapter_factory is None:
            self.adapter_factory = _default_adapter_factory
        self.adapter = self.adapter_factory(self.config, self.state_dir, self.on_message)
        self._started = True
        self._stopping = False
        self._adapter_task = asyncio.create_task(self._run_adapter())
        self.log.info("channel started; waiting for WeChat login/messages")

    async def _run_adapter(self) -> None:
        assert self.adapter is not None
        try:
            await self.adapter.run()
        except asyncio.CancelledError:
            raise
        except Exception:
            self.log.exception("WeChat adapter stopped with an error")

    async def stop(self) -> None:
        if self._stopping:
            return
        self._stopping = True
        self._started = False
        adapter = self.adapter
        self.adapter = None
        if adapter is not None:
            try:
                await adapter.close()
            except Exception:
                self.log.exception("WeChat adapter close failed")
        task = self._adapter_task
        self._adapter_task = None
        if task is not None and not task.done():
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)
        self.log.info("channel stopped")

    # ── WeChat inbound ────────────────────────────────────────────────

    async def on_message(self, msg: Any) -> None:
        if not self._started or self._stopping or self.adapter is None:
            return
        user_id = str(getattr(msg, "user_id", "") or "").strip()
        if not user_id:
            self.log.warning("ignoring message without user_id")
            return
        if self.config and self.config.allow_users and user_id not in self.config.allow_users:
            self.log.info("ignoring message from non-allowlisted user %s", user_id)
            return
        try:
            await self.adapter.send_typing(user_id)
        except Exception:
            self.log.debug("typing failed", exc_info=True)

        try:
            conversation = await self._ensure_conversation(user_id)
            text, images = await self._build_input(msg)
            if not text and not images:
                self.log.info("ignoring empty message from %s", user_id)
                return
            await self.transport.call(
                "send",
                {
                    "session_id": conversation.session_id,
                    "text": text,
                    "display_text": text or "[WeChat image]",
                    "images": images,
                },
                timeout=60,
            )
        except RpcError as exc:
            self.log.warning("send to Hive failed for %s: %s", user_id, exc.message)
            await self._safe_send(user_id, f"Hive unavailable: {exc.message}")
        except Exception:
            self.log.exception("failed to relay WeChat message from %s", user_id)

    async def _ensure_conversation(self, user_id: str) -> _Conversation:
        key = "__shared__" if self.config and self.config.shared_session else user_id
        existing = self._conversations.get(key)
        if existing and existing.session_id:
            return existing
        lock = self._locks.setdefault(key, asyncio.Lock())
        async with lock:
            existing = self._conversations.get(key)
            if existing and existing.session_id:
                return existing
            assert self.config is not None and self.store is not None
            stored = self.store.get(key) or self.config.fixed_session_id
            title = f"{self.config.title_prefix} · {user_id}"[:120]
            params: dict[str, Any] = {"queen_id": self.config.queen_id, "title": title}
            if stored:
                params["session_id"] = stored
            opened = await self.transport.call("open_session", params, timeout=60)
            session_id = str((opened or {}).get("session_id") or "")
            if not session_id:
                raise RpcError(CODE_INVALID_PARAMS, "host returned no session_id", "internal")
            subscribed = await self.transport.call(
                "subscribe", {"session_id": session_id}, timeout=60
            )
            subscription_id = str((subscribed or {}).get("subscription_id") or "")
            if not subscription_id:
                raise RpcError(
                    CODE_INVALID_PARAMS, "host returned no subscription_id", "internal"
                )
            conversation = _Conversation(
                key=key,
                user_id=user_id,
                session_id=session_id,
                subscription_id=subscription_id,
            )
            self._conversations[key] = conversation
            self._subscriptions[subscription_id] = conversation
            self.store.set(key, session_id)
            self.log.info("conversation %s -> session %s", user_id, session_id)
            return conversation

    async def _build_input(self, msg: Any) -> tuple[str, list[str]]:
        assert self.config is not None and self.adapter is not None
        pieces: list[str] = []
        text = str(getattr(msg, "text", "") or "").strip()
        if text:
            pieces.append(text)

        voices = getattr(msg, "voices", []) or []
        for voice in voices:
            transcript = str(getattr(voice, "text", "") or "").strip()
            if transcript:
                pieces.append(f"[voice] {transcript}")

        images: list[str] = []
        for image in (getattr(msg, "images", []) or [])[: self.config.max_images]:
            try:
                data = await self.adapter.download_image(image)
            except Exception:
                self.log.warning("image download failed", exc_info=True)
                data = None
            if not data:
                pieces.append("[WeChat image unavailable]")
                continue
            if len(data) > MAX_IMAGE_BYTES:
                pieces.append(f"[WeChat image skipped: {len(data)} bytes over 4 MiB]")
                continue
            images.append(base64.b64encode(data).decode("ascii"))

        inbox = self.state_dir / "inbox"
        for item in getattr(msg, "files", []) or []:
            name = _safe_name(str(getattr(item, "file_name", "") or "file.bin"))
            try:
                data = await self.adapter.download_file(item)
            except Exception:
                self.log.warning("file download failed", exc_info=True)
                data = None
            if not data:
                pieces.append(f"[WeChat file unavailable: {name}]")
                continue
            inbox.mkdir(parents=True, exist_ok=True)
            target = inbox / f"{int(asyncio.get_running_loop().time() * 1000)}_{name}"
            target.write_bytes(data)
            pieces.append(f"[attachment: {target}]")

        for video in getattr(msg, "videos", []) or []:
            pieces.append("[WeChat video: preview not supported by this channel]")

        quoted = getattr(msg, "quoted_message", None)
        if quoted is not None:
            quoted_text = str(getattr(quoted, "text", "") or "").strip()
            if quoted_text:
                pieces.append(f"[quoted] {quoted_text}")

        return "\n".join(pieces).strip(), images

    # ── Hive output ───────────────────────────────────────────────────

    async def on_output(self, params: dict[str, Any]) -> None:
        if self.adapter is None:
            return
        subscription_id = str(params.get("subscription_id") or "")
        conversation = self._subscriptions.get(subscription_id)
        if conversation is None:
            self.log.debug("output for unknown subscription %s", subscription_id)
            return
        kind = str(params.get("kind") or "")
        text = str(params.get("text") or "").strip()
        try:
            if kind == "idle":
                await self.adapter.stop_typing(conversation.user_id)
                return
            if kind == "files":
                for path in params.get("files") or []:
                    await self.adapter.send_file(conversation.user_id, str(path))
                return
            if kind == "question":
                text = _format_questions(params.get("questions") or [])
                if not text:
                    text = "The queen has a question; open the Hive dashboard to answer it."
            elif kind == "error":
                text = f"Hive error: {text or 'unknown error'}"
            if not text:
                return
            await self._send_chunks(conversation.user_id, text)
        except Exception:
            self.log.exception("failed to deliver Hive output to %s", conversation.user_id)

    async def _send_chunks(self, user_id: str, text: str) -> None:
        assert self.config is not None and self.adapter is not None
        limit = self.config.max_text_chars
        chunk = ""
        for line in text.splitlines(keepends=True):
            if len(chunk) + len(line) > limit and chunk:
                await self.adapter.send_text(user_id, chunk)
                chunk = ""
            while len(line) > limit:
                if chunk:
                    await self.adapter.send_text(user_id, chunk)
                    chunk = ""
                await self.adapter.send_text(user_id, line[:limit])
                line = line[limit:]
            chunk += line
        if chunk:
            await self.adapter.send_text(user_id, chunk)

    async def _safe_send(self, user_id: str, text: str) -> None:
        if self.adapter is None:
            return
        try:
            await self.adapter.send_text(user_id, text)
        except Exception:
            self.log.debug("error reply failed", exc_info=True)


def _format_questions(questions: list[Any]) -> str:
    lines = ["The queen needs your input:"]
    for index, question in enumerate(questions, 1):
        if isinstance(question, dict):
            prompt = question.get("question") or question.get("text") or question.get("prompt")
            options = question.get("options") or question.get("choices") or []
        else:
            prompt = str(question)
            options = []
        lines.append(f"{index}. {prompt}")
        if isinstance(options, list):
            for option in options:
                lines.append(f"   - {option}")
    return "\n".join(lines).strip()


def _safe_name(name: str) -> str:
    base = os.path.basename(name.replace("\\", "/")).strip() or "file.bin"
    return re.sub(r"[^A-Za-z0-9._-]", "_", base)[:120]


def _default_adapter_factory(
    config: BridgeConfig, state_dir: Path, on_message: Callable[[Any], Awaitable[None]]
) -> Adapter:
    from .wechat import SdkWeChatAdapter

    return SdkWeChatAdapter(config, state_dir, on_message)
