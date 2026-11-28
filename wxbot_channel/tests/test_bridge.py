from __future__ import annotations

import asyncio
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from wxbot_channel.bridge import BridgeConfig, ChannelBridge, SessionStore


class FakeTransport:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict]] = []
        self._session = 0

    async def call(self, method: str, params: dict | None = None, timeout: float = 60) -> dict:
        params = params or {}
        self.calls.append((method, params))
        if method == "open_session":
            self._session += 1
            return {"session_id": params.get("session_id") or f"session-{self._session}"}
        if method == "subscribe":
            return {"subscription_id": f"sub-{params['session_id']}"}
        if method == "send":
            return {"session_id": params["session_id"]}
        return {}


class FakeAdapter:
    def __init__(self) -> None:
        self.ready = asyncio.Event()
        self.closed = False
        self.sent_text: list[tuple[str, str]] = []
        self.sent_files: list[tuple[str, str]] = []
        self.typing: list[str] = []
        self.stopped_typing: list[str] = []
        self.images = [b"image-bytes"]

    async def run(self) -> None:
        await self.ready.wait()

    async def close(self) -> None:
        self.closed = True
        self.ready.set()

    async def send_text(self, user_id: str, text: str) -> None:
        self.sent_text.append((user_id, text))

    async def send_file(self, user_id: str, path: str) -> None:
        self.sent_files.append((user_id, path))

    async def send_typing(self, user_id: str) -> None:
        self.typing.append(user_id)

    async def stop_typing(self, user_id: str) -> None:
        self.stopped_typing.append(user_id)

    async def download_image(self, image) -> bytes | None:
        return self.images.pop(0) if self.images else None

    async def download_file(self, item) -> bytes | None:
        return b"file-bytes"


def message(**overrides):
    values = dict(
        user_id="wxid_alice",
        text="hello",
        type="text",
        images=[],
        voices=[],
        files=[],
        videos=[],
        quoted_message=None,
    )
    values.update(overrides)
    return SimpleNamespace(**values)


class BridgeTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.state_dir = Path(self.temp.name)
        self.transport = FakeTransport()
        self.adapter = FakeAdapter()
        self.factory_calls = []

        def factory(config, state_dir, on_message):
            self.factory_calls.append((config, state_dir, on_message))
            return self.adapter

        self.bridge = ChannelBridge(self.transport, adapter_factory=factory)
        welcome = await self.bridge.handle(
            "hello",
            {
                "protocol": "hive-channel/1",
                "name": "wxbot",
                "state_dir": str(self.state_dir),
                "config": {"queen_id": "queen_growth", "title_prefix": "WeChat"},
            },
        )
        self.welcome = welcome

    async def asyncTearDown(self) -> None:
        await self.bridge.stop()
        self.temp.cleanup()

    async def test_hello_reports_protocol_and_config(self) -> None:
        self.assertEqual(self.welcome["plugin_version"], "0.1.0")
        self.assertIn("text", self.welcome["capabilities"])
        self.assertEqual(self.bridge.config.queen_id, "queen_growth")
        self.assertEqual(self.factory_calls, [])

    async def test_start_requires_queen(self) -> None:
        bad = ChannelBridge(self.transport)
        await bad.handle(
            "hello",
            {"state_dir": str(self.state_dir), "config": {}},
        )
        with self.assertRaises(Exception):
            await bad.start()

    async def test_message_opens_subscribes_and_sends(self) -> None:
        await self.bridge.handle("start", {})
        await self.bridge.on_message(message())
        methods = [method for method, _ in self.transport.calls]
        self.assertEqual(methods, ["open_session", "subscribe", "send"])
        open_params = self.transport.calls[0][1]
        self.assertEqual(open_params["queen_id"], "queen_growth")
        self.assertEqual(self.adapter.typing, ["wxid_alice"])
        send_params = self.transport.calls[2][1]
        self.assertEqual(send_params["text"], "hello")

    async def test_second_message_reuses_session(self) -> None:
        await self.bridge.handle("start", {})
        await self.bridge.on_message(message())
        await self.bridge.on_message(message(text="again"))
        methods = [method for method, _ in self.transport.calls]
        self.assertEqual(methods.count("open_session"), 1)
        self.assertEqual(methods.count("subscribe"), 1)
        self.assertEqual(methods.count("send"), 2)

    async def test_output_message_and_question_are_sent_back(self) -> None:
        await self.bridge.handle("start", {})
        await self.bridge.on_message(message())
        await self.bridge.handle(
            "output",
            {
                "subscription_id": "sub-session-1",
                "session_id": "session-1",
                "kind": "message",
                "text": "hi from queen",
            },
        )
        await self.bridge.handle(
            "output",
            {
                "subscription_id": "sub-session-1",
                "session_id": "session-1",
                "kind": "question",
                "questions": [{"question": "Which color?", "options": ["red", "blue"]}],
            },
        )
        self.assertEqual(self.adapter.sent_text[0], ("wxid_alice", "hi from queen"))
        self.assertIn("Which color?", self.adapter.sent_text[1][1])
        self.assertIn("blue", self.adapter.sent_text[1][1])

    async def test_files_are_sent_as_media(self) -> None:
        await self.bridge.handle("start", {})
        await self.bridge.on_message(message())
        await self.bridge.handle(
            "output",
            {
                "subscription_id": "sub-session-1",
                "kind": "files",
                "files": ["/tmp/report.pdf"],
            },
        )
        self.assertEqual(self.adapter.sent_files, [("wxid_alice", "/tmp/report.pdf")])

    async def test_allowlist_ignores_unknown_user(self) -> None:
        await self.bridge.stop()
        bridge = ChannelBridge(
            self.transport,
            adapter_factory=lambda config, state_dir, on_message: self.adapter,
        )
        await bridge.handle(
            "hello",
            {
                "state_dir": str(self.state_dir),
                "config": {"queen_id": "queen_growth", "allow_users": ["wxid_bob"]},
            },
        )
        await bridge.start()
        await bridge.on_message(message(user_id="wxid_alice"))
        self.assertEqual(self.transport.calls, [])
        await bridge.stop()

    async def test_session_store_persists(self) -> None:
        store = SessionStore(self.state_dir / "sessions.json")
        store.set("wxid_alice", "session-9")
        again = SessionStore(self.state_dir / "sessions.json")
        self.assertEqual(again.get("wxid_alice"), "session-9")


class ConfigTest(unittest.TestCase):
    def test_defaults_and_aliases(self) -> None:
        cfg = BridgeConfig.from_section(
            {"queen": "queen_x", "allow_users": "wxid_a", "max_images": "2"},
            Path("/tmp/state"),
        )
        self.assertEqual(cfg.queen_id, "queen_x")
        self.assertEqual(cfg.allow_users, ("wxid_a",))
        self.assertEqual(cfg.max_images, 2)
        self.assertTrue(cfg.cred_path.endswith("credentials.json"))


if __name__ == "__main__":
    unittest.main()
