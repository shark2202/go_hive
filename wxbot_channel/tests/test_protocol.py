from __future__ import annotations

import asyncio
import io
import json
import unittest

from wxbot_channel.protocol import StdioTransport


class TransportTest(unittest.IsolatedAsyncioTestCase):
    async def test_request_response_and_notification(self) -> None:
        inbound = io.BytesIO(
            (
                json.dumps(
                    {"jsonrpc": "2.0", "id": "1", "method": "hello", "params": {}}
                )
                + "\n"
                + json.dumps(
                    {
                        "jsonrpc": "2.0",
                        "method": "output",
                        "params": {"kind": "message", "text": "x"},
                    }
                )
                + "\n"
            ).encode()
        )
        outbound = io.BytesIO()
        transport = StdioTransport(reader=inbound, writer=outbound)
        seen: list[str] = []

        async def handler(method, params):
            seen.append(method)
            if method == "hello":
                return {"plugin_version": "0.1.0"}
            return {}

        await asyncio.wait_for(transport.serve(handler), timeout=2)
        frames = [
            json.loads(line)
            for line in outbound.getvalue().decode().splitlines()
            if line
        ]
        self.assertEqual(seen, ["hello", "output"])
        self.assertEqual(frames[0]["id"], "1")
        self.assertEqual(frames[0]["result"]["plugin_version"], "0.1.0")

    def test_frame_rejects_plain_text(self) -> None:
        inbound = io.BytesIO(b"hello, this is not json\n")
        outbound = io.BytesIO()
        transport = StdioTransport(reader=inbound, writer=outbound)

        async def handler(method, params):
            return {}

        asyncio.run(asyncio.wait_for(transport.serve(handler), timeout=2))
        self.assertEqual(outbound.getvalue(), b"")


if __name__ == "__main__":
    unittest.main()
