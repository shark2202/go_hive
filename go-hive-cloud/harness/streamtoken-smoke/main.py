#!/usr/bin/env python3
"""M3 L2 smoke: streamToken rotation with the REAL Python runtime client.

The rotation requests are made by ``framework.server.streamtoken_refresh``
itself (``post_refresh`` and ``_refresh_loop`` / ``try_refresh_now``), not by a
reimplementation. The LLM side is validated by the real go_hive keycheck in the
sibling Go harness; this script also pins the raw 401 bodies so the two layers
cannot drift.

No external cloud is contacted: both base URLs are local servers started by
scripts/m3-smoke.sh. Tokens are fingerprint-free in the evidence (no secrets).
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import hmac
import json
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
from pydantic import SecretStr

from framework.credentials.models import CredentialKey, CredentialObject
from framework.server import streamtoken_refresh as refresh

STREAM_SECRET = os.environ["M3_STREAM_SECRET"].encode()
BASE = os.environ["HIVE_CLOUD_BASE"].rstrip("/")
BASE_NO_PREVIOUS = os.environ["M3_BASE_NO_PREVIOUS"].rstrip("/")
OLD_TOKEN = os.environ["M3_OLD_TOKEN"]
NEAR_EXPIRY_TOKEN = os.environ["M3_NEAR_EXPIRY_TOKEN"]
EXPIRED_TOKEN = os.environ["M3_EXPIRED_TOKEN"]
WRONG_TOKEN = os.environ["M3_WRONG_TOKEN"]
USER_JWT = os.environ["M3_USER_JWT"]
EVIDENCE_OUT = os.environ.get("EVIDENCE_OUT", "")

CHECKS: list[dict[str, Any]] = []


def add(name: str, ok: bool, detail: dict[str, Any] | None = None, error: str = "") -> None:
    CHECKS.append({"name": name, "ok": bool(ok), "detail": detail or {}, "error": error})


def decode_claims(token: str) -> dict[str, Any]:
    parts = token.split(".")
    if len(parts) != 3:
        raise ValueError("not a compact JWS")
    payload = parts[1] + "=" * (-len(parts[1]) % 4)
    return json.loads(base64.urlsafe_b64decode(payload.encode()).decode())


def verify_signature(token: str) -> bool:
    parts = token.split(".")
    if len(parts) != 3:
        return False
    body = f"{parts[0]}.{parts[1]}".encode()
    expected = hmac.new(STREAM_SECRET, body, hashlib.sha256).digest()
    got = base64.urlsafe_b64decode(parts[2] + "=" * (-len(parts[2]) % 4))
    return hmac.compare_digest(expected, got)


def is_hive_stream_token(token: str) -> bool:
    claims = decode_claims(token)
    return claims.get("typ") == "stream" and bool(claims.get("user_id")) and bool(claims.get("team_id"))


def ttl_seconds(token: str) -> int:
    return int(decode_claims(token)["exp"] - time.time())


def fingerprint(claims: dict[str, Any]) -> dict[str, Any]:
    return {
        "typ": claims.get("typ"),
        "user_id": claims.get("user_id"),
        "team_id": claims.get("team_id"),
        "exp": claims.get("exp"),
    }


class FakeCredentialStore:
    """The duck-typed surface streamtoken_refresh uses: get/save_credential."""

    def __init__(self, token: str):
        self.credential = CredentialObject(
            id="hive",
            keys={"api_key": CredentialKey(name="api_key", value=SecretStr(token))},
        )
        self.saved: str | None = None

    def get_credential(self, credential_id: str):
        return self.credential if credential_id == "hive" else None

    def save_credential(self, credential) -> None:
        self.credential = credential
        self.saved = credential.get_key("api_key")


async def rotation_checks() -> str:
    started = datetime.now(timezone.utc).isoformat()
    rotated = await refresh.post_refresh(BASE, OLD_TOKEN)
    old_claims = decode_claims(OLD_TOKEN)
    if rotated:
        new_claims = decode_claims(rotated)
        add(
            "rotation_refresh_rotates",
            rotated != OLD_TOKEN and verify_signature(rotated) and is_hive_stream_token(rotated),
            {"new": fingerprint(new_claims), "signed_by_current_secret": verify_signature(rotated)},
        )
        add(
            "rotation_ttl_24h",
            23 * 3600 < ttl_seconds(rotated) <= 25 * 3600,
            {"ttl_seconds": ttl_seconds(rotated)},
        )
        add(
            "rotation_preserves_identity",
            new_claims.get("user_id") == old_claims.get("user_id")
            and new_claims.get("team_id") == old_claims.get("team_id")
            and new_claims.get("typ") == "stream",
            {"old": fingerprint(old_claims), "new": fingerprint(new_claims)},
        )
    else:
        add("rotation_refresh_rotates", False, error="post_refresh returned None")
        add("rotation_ttl_24h", False, error="no rotated token")
        add("rotation_preserves_identity", False, error="no rotated token")

    again = await refresh.post_refresh(BASE, OLD_TOKEN)
    add(
        "rotation_old_token_still_valid",
        again is not None and again != OLD_TOKEN,
        {"rotated_without_revoking_old": again is not None},
    )

    denied = await refresh.post_refresh(BASE_NO_PREVIOUS, OLD_TOKEN)
    add(
        "old_token_rejected_without_previous_secret",
        denied is None,
        {"server": "current-secret-only"},
    )

    for name, token in (
        ("refresh_expired_401", EXPIRED_TOKEN),
        ("refresh_wrong_signature_401", WRONG_TOKEN),
        ("refresh_user_jwt_401", USER_JWT),
        ("refresh_garbage_401", "garbage"),
    ):
        r = httpx.post(f"{BASE}/user/refresh-stream-token-from-stream", json={"streamToken": token}, timeout=5.0)
        try:
            body = r.json()
        except ValueError:
            body = {}
        add(
            name,
            r.status_code == 401 and body == {"error": "hive_stream_token_invalid"},
            {"status": r.status_code, "body": body},
        )

    r = httpx.post(f"{BASE}/user/refresh-stream-token-from-stream", content=b"{not json", timeout=5.0)
    add(
        "refresh_invalid_json_400",
        r.status_code == 400 and r.json() == {"error": "invalid JSON body"},
        {"status": r.status_code, "body": r.json()},
    )

    # Real runtime loop: reads the `hive` credential, sees <=2h left, POSTs the
    # refresh endpoint, and writes the new token back under id `hive`/`api_key`.
    os.environ["HIVE_CLOUD_BASE"] = BASE
    store = FakeCredentialStore(NEAR_EXPIRY_TOKEN)
    force = asyncio.Event()
    app = {"credential_store": store, "streamtoken_force_refresh_event": force}
    task = asyncio.create_task(refresh._refresh_loop(app))
    # _sleep_with_force waits on the force event rather than the injected sleep
    # function (streamtoken_refresh.py:_sleep_with_force). Waking it is exactly
    # the LLM-call-site path that demands an immediate refresh.
    force.set()
    try:
        for _ in range(200):
            if store.saved:
                break
            await asyncio.sleep(0.01)
    finally:
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task
    saved = store.saved or ""
    add(
        "rotation_refresh_loop_saves_hive_credential",
        bool(saved) and saved != NEAR_EXPIRY_TOKEN and verify_signature(saved) and is_hive_stream_token(saved),
        {
            "credential_id": "hive",
            "key": "api_key",
            "saved": bool(saved),
            "current_claims": fingerprint(decode_claims(NEAR_EXPIRY_TOKEN)),
            "saved_claims": fingerprint(decode_claims(saved)) if saved else {},
        },
    )

    # Forced-refresh path used by the LLM call site after a 401.
    store2 = FakeCredentialStore(NEAR_EXPIRY_TOKEN)
    refresh._credential_store_ref = store2
    refresh._aden_base_url_ref = BASE
    refresh._refresh_now_lock = asyncio.Lock()
    forced = await refresh.try_refresh_now()
    refresh._credential_store_ref = None
    refresh._aden_base_url_ref = None
    refresh._refresh_now_lock = None
    add(
        "rotation_try_refresh_now",
        bool(forced) and forced != NEAR_EXPIRY_TOKEN and verify_signature(forced) and is_hive_stream_token(forced),
        {"forced": bool(forced), "claims": fingerprint(decode_claims(forced)) if forced else {}},
    )

    # LLM credential boundary: raw wire bodies. The real go_hive keycheck runs
    # in harness/llm-credential-smoke and must interpret the same server. A
    # valid credential must still pass the M3 boundary. Since M5 the
    # unconfigured face answers 400 llm_upstream_not_configured instead of the
    # retired M3 placeholder; the point of this check is "not 401".
    for name, token in (("valid", OLD_TOKEN), ("valid_operator_jwt", USER_JWT)):
        r = httpx.post(
            f"{BASE}/v1/messages",
            json={"model": "queen", "max_tokens": 1, "messages": []},
            headers={"Authorization": f"Bearer {token}"},
            timeout=5.0,
        )
        body = r.json() if r.headers.get("content-type", "").startswith("application/json") else {}
        add(
            f"llm_{name}_credential_accepted",
            r.status_code == 400 and body.get("error", {}).get("type") == "llm_upstream_not_configured",
            {"status": r.status_code, "body": body},
        )

    for name, token in (("expired", EXPIRED_TOKEN), ("garbage", "garbage"), ("missing", "")):
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        r = httpx.post(f"{BASE}/v1/messages", json={"model": "queen"}, headers=headers, timeout=5.0)
        body = r.json() if r.headers.get("content-type", "").startswith("application/json") else {}
        add(
            f"llm_{name}_401",
            r.status_code == 401 and body == {"error": {"type": "hive_stream_token_invalid"}},
            {"status": r.status_code, "body": body},
        )

    # Legacy desktop refresh stays unknown: no guessed request/response.
    r = httpx.post(f"{BASE}/user/refresh-stream-token", json={}, timeout=5.0)
    add("legacy_refresh_unknown_404", r.status_code == 404, {"status": r.status_code})

    return started


def main() -> int:
    started_at = asyncio.run(rotation_checks())
    finished_at = datetime.now(timezone.utc).isoformat()
    ok = all(check["ok"] for check in CHECKS)
    evidence = {
        "harness": "streamtoken-smoke (real Python runtime refresh client)",
        "base_url": BASE,
        "base_url_without_previous_secret": BASE_NO_PREVIOUS,
        "started_at": started_at,
        "finished_at": finished_at,
        "ok": ok,
        "checks": CHECKS,
    }
    if EVIDENCE_OUT:
        Path(EVIDENCE_OUT).write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"ok": ok, "checks": len(CHECKS), "failed": [c["name"] for c in CHECKS if not c["ok"]]}))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
