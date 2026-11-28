from __future__ import annotations

import contextlib
import io
import json
import tempfile
import unittest
from unittest import mock
from pathlib import Path

from wxbot_channel.__main__ import default_cred_path, main


class CliTest(unittest.TestCase):
    def test_version(self) -> None:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(main(["--version"]), 0)
        self.assertIn("wxbot-channel", out.getvalue())

    def test_login_help_lists_credential_options(self) -> None:
        out = io.StringIO()
        with self.assertRaises(SystemExit) as raised:
            with contextlib.redirect_stdout(out):
                main(["login", "--help"])
        self.assertEqual(raised.exception.code, 0)
        text = out.getvalue()
        self.assertIn("--cred-path", text)
        self.assertIn("--base-url", text)
        self.assertIn("--force", text)
        self.assertIn("--qr-output", text)

    def test_logout_is_idempotent_and_json(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "credentials.json"
            path.write_text('{"token":"secret"}\n', encoding="utf-8")
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                self.assertEqual(main(["logout", "--cred-path", str(path), "--json"]), 0)
            result = json.loads(out.getvalue())
            self.assertTrue(result["removed"])
            self.assertFalse(path.exists())

            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                self.assertEqual(main(["logout", "--cred-path", str(path), "--json"]), 0)
            self.assertFalse(json.loads(out.getvalue())["removed"])

    def test_default_cred_path_honors_env(self) -> None:
        with mock.patch.dict("os.environ", {"WXBOT_CRED_PATH": "/tmp/wxbot-creds.json"}):
            self.assertEqual(str(default_cred_path()), "/tmp/wxbot-creds.json")

    def test_login_json_does_not_print_token(self) -> None:
        class FakeBot:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

            async def login(self, force=False):
                if self.kwargs.get("on_qr_url"):
                    self.kwargs["on_qr_url"]("https://wechat.example/qr-login")
                return type(
                    "Creds",
                    (),
                    {
                        "user_id": "wxid_test",
                        "account_id": "bot_test",
                        "base_url": "https://example.test",
                        "saved_at": "2026-01-01T00:00:00+00:00",
                    },
                )()

        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "credentials.json"
            out = io.StringIO()
            with mock.patch("wechatbot.WeChatBot", FakeBot):
                with contextlib.redirect_stdout(out):
                    self.assertEqual(
                        main(["login", "--cred-path", str(path), "--json"]), 0
                    )
            result = json.loads(out.getvalue())
            self.assertEqual(result["user_id"], "wxid_test")
            self.assertNotIn("token", out.getvalue())
            image = path.parent / "login-qr.png"
            self.assertTrue(image.exists())
            self.assertEqual(image.read_bytes()[:8], b"\x89PNG\r\n\x1a\n")
            self.assertTrue((path.parent / "login-qr.html").exists())
            self.assertTrue((path.parent / "login-qr.txt").read_text().startswith("https://"))


if __name__ == "__main__":
    unittest.main()
