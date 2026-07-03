#!/usr/bin/env python3
"""
Smoke test for Puls.

Spins up the stack with docker compose (local mode), or runs against an
already-deployed instance (remote mode).

Usage (from project root):

  Local (default — requires a .env file with PULS_JWT_SECRET + PULS_ADMIN_SECRET):
    python smoke_test/smoke.py

  Remote / Render:
    BASE_URL=https://puls.onrender.com \\
    ADMIN_SECRET=<your-admin-secret> \\
    SKIP_COMPOSE=1 \\
    python smoke_test/smoke.py

Env vars:
    BASE_URL       API base URL            (default: http://localhost:8080)
    ADMIN_SECRET   PULS_ADMIN_SECRET value (required; reads .env as fallback in local mode)
    SKIP_COMPOSE   set to 1 to skip docker compose up/down
    KEEP_RUNNING   set to 1 to leave the local stack running after the test

Dependencies:
    websockets     pip install websockets
"""

import asyncio
import json
import os
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = SCRIPT_DIR.parent

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080").rstrip("/")
_is_remote = "localhost" not in BASE_URL and "127.0.0.1" not in BASE_URL
SKIP_COMPOSE = os.environ.get("SKIP_COMPOSE", "1" if _is_remote else "0") == "1"
KEEP_RUNNING = os.environ.get("KEEP_RUNNING", "0") == "1"

# ADMIN_SECRET: explicit env var, or fall back to PULS_ADMIN_SECRET in project .env
def _resolve_admin_secret() -> str:
    if v := os.environ.get("ADMIN_SECRET"):
        return v
    if v := os.environ.get("PULS_ADMIN_SECRET"):
        return v
    env_file = PROJECT_ROOT / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            if line.startswith("PULS_ADMIN_SECRET="):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    return ""

ADMIN_SECRET = _resolve_admin_secret()

COMPOSE_CMD = [
    "docker", "compose",
    "-f", str(PROJECT_ROOT / "docker-compose.yml"),
    "-p", "puls-smoke",
]

# Derive WebSocket URL from BASE_URL
WS_BASE = BASE_URL.replace("https://", "wss://").replace("http://", "ws://")

# ---------------------------------------------------------------------------
# HTTP helpers (stdlib only — no requests)
# ---------------------------------------------------------------------------

def _headers(token: str | None = None, extra: dict | None = None) -> dict:
    h = dict(extra or {})
    if token:
        h["Authorization"] = f"Bearer {token}"
    return h


def _do(method: str, url: str, *, body=None, token: str | None = None,
        extra_headers: dict | None = None, timeout: int = 30):
    req = urllib.request.Request(url, method=method,
                                  headers=_headers(token, extra_headers))
    if body is not None:
        encoded = json.dumps(body).encode()
        req.add_header("Content-Type", "application/json")
        req.data = encoded
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, json.loads(resp.read())


def get(path: str, token: str | None = None, **kw):
    _, data = _do("GET", f"{BASE_URL}{path}", token=token, **kw)
    return data


def post(path: str, body: dict, token: str | None = None, **kw):
    _, data = _do("POST", f"{BASE_URL}{path}", body=body, token=token, **kw)
    return data


def wait_ready(retries: int = 60, delay: float = 2.0) -> None:
    for _ in range(retries):
        try:
            get("/readyz")
            return
        except Exception:
            time.sleep(delay)
    _die("server did not become ready in time")


# ---------------------------------------------------------------------------
# WebSocket device session (runs in a background thread)
# ---------------------------------------------------------------------------

class DeviceSession(threading.Thread):
    """
    Simulates a Puls device: connects via WebSocket, sends continuous heartbeats,
    and responds to any diag_request message it receives.
    """

    def __init__(self, token: str):
        super().__init__(daemon=True)
        self.token = token
        self.ready = threading.Event()      # set after first heartbeat lands
        self.diag_done = threading.Event()  # set after diag_response is sent
        self.diag_request_id: str | None = None
        self.error: str | None = None
        self._loop: asyncio.AbstractEventLoop | None = None
        self._stop_event: asyncio.Event | None = None

    def stop(self) -> None:
        if self._loop and self._stop_event:
            self._loop.call_soon_threadsafe(self._stop_event.set)

    def run(self) -> None:
        asyncio.run(self._session())

    async def _session(self) -> None:
        try:
            import websockets
        except ImportError:
            _die("websockets is not installed — run: pip install websockets")

        self._loop = asyncio.get_running_loop()
        self._stop_event = asyncio.Event()
        uri = f"{WS_BASE}/api/v1/ws"
        headers = [("Authorization", f"Bearer {self.token}")]

        try:
            async with websockets.connect(uri, additional_headers=headers) as ws:
                hb_task = asyncio.create_task(self._heartbeat_loop(ws))
                try:
                    async for raw in ws:
                        if self._stop_event.is_set():
                            break
                        msg = json.loads(raw)
                        if msg.get("type") == "diag_request":
                            self.diag_request_id = msg.get("requestId")
                            await ws.send(json.dumps({
                                "type": "diag_response",
                                "requestId": self.diag_request_id,
                                "data": {
                                    "hostname": "smoke-device",
                                    "cpuCores": 4,
                                    "memoryGB": 8,
                                    "platform": "smoke-linux-1.0",
                                },
                            }))
                            self.diag_done.set()
                finally:
                    hb_task.cancel()
        except Exception as exc:
            self.error = str(exc)
            self.ready.set()    # unblock main thread so it can fail fast
            self.diag_done.set()

    async def _heartbeat_loop(self, ws) -> None:
        count = 0
        while not self._stop_event.is_set():
            await ws.send(json.dumps({
                "type": "heartbeat",
                "data": {
                    "cpuPercent": 5.2,
                    "memoryPercent": 43.1,
                    "diskPercent": 28.7,
                    "uptimeSeconds": 3600 + count * 2,
                    "osVersion": "smoke-linux-1.0",
                },
            }))
            if count == 0:
                self.ready.set()
            count += 1
            try:
                await asyncio.wait_for(self._stop_event.wait(), timeout=2.0)
            except asyncio.TimeoutError:
                pass


# ---------------------------------------------------------------------------
# SSE check
# ---------------------------------------------------------------------------

def check_sse(admin_token: str, timeout: int = 12) -> int:
    """Open the event stream and return the number of data lines seen."""
    url = f"{BASE_URL}/api/v1/events"
    req = urllib.request.Request(
        url,
        headers=_headers(admin_token, {"Accept": "text/event-stream"}),
    )
    events_seen = 0
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            if resp.headers.get("Content-Type", "").split(";")[0].strip() != "text/event-stream":
                _die(f"SSE Content-Type wrong: {resp.headers.get('Content-Type')}")
            for raw in resp:
                line = raw.decode().rstrip("\r\n")
                if line.startswith("data:"):
                    events_seen += 1
                    if events_seen >= 2:
                        return events_seen
    except (TimeoutError, OSError):
        pass  # timeout is normal for a long-lived SSE stream
    return events_seen


# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------

def _die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def _assert_eq(label: str, got, want) -> None:
    if got != want:
        _die(f"{label}: expected {want!r}, got {got!r}")


def _assert_in(label: str, got, choices) -> None:
    if got not in choices:
        _die(f"{label}: expected one of {choices!r}, got {got!r}")


# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

def preflight() -> None:
    if not ADMIN_SECRET:
        _die(
            "ADMIN_SECRET is not set.\n"
            "  Local: add PULS_ADMIN_SECRET=... to .env in the project root.\n"
            "  Remote: export ADMIN_SECRET=<your-admin-secret>"
        )

    if not SKIP_COMPOSE:
        if not (PROJECT_ROOT / "docker-compose.yml").exists():
            _die("docker-compose.yml not found in project root")
        result = subprocess.run(["docker", "--version"], capture_output=True)
        if result.returncode != 0:
            _die("docker not found")

        env_file = PROJECT_ROOT / ".env"
        if not env_file.exists():
            _die(
                ".env not found in project root.\n"
                "  Create it with PULS_JWT_SECRET and PULS_ADMIN_SECRET:\n"
                "    echo \"PULS_JWT_SECRET=$(openssl rand -base64 32)\" > .env\n"
                "    echo \"PULS_ADMIN_SECRET=$(openssl rand -base64 24)\" >> .env"
            )


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

TOTAL = 11


def main() -> None:
    preflight()
    step = 1

    # ------------------------------------------------------------------
    # 1. Start stack
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] starting stack")
    step += 1
    if not SKIP_COMPOSE:
        subprocess.run(COMPOSE_CMD + ["up", "-d", "--build"], check=True,
                       cwd=PROJECT_ROOT)
    else:
        print(f"  skipped ({'remote' if _is_remote else 'SKIP_COMPOSE=1'}): {BASE_URL}")

    import atexit
    def teardown():
        if not KEEP_RUNNING and not SKIP_COMPOSE:
            print("\n[teardown] docker compose down -v")
            subprocess.run(COMPOSE_CMD + ["down", "-v"], cwd=PROJECT_ROOT)
    atexit.register(teardown)

    # ------------------------------------------------------------------
    # 2. Health / readiness
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] waiting for /readyz ...")
    step += 1
    wait_ready()
    liveness = get("/healthz")
    _assert_eq("/healthz status", liveness.get("status"), "ok")
    readiness = get("/readyz")
    _assert_eq("/readyz status", readiness.get("status"), "ready")
    print("  ok")

    # ------------------------------------------------------------------
    # 3. Admin token
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] minting admin token")
    step += 1
    resp = post("/api/v1/auth/admin-token", {"secret": ADMIN_SECRET})
    admin_token = resp.get("token")
    if not admin_token:
        _die(f"admin-token response missing token: {resp}")
    print(f"  token: {admin_token[:20]}…")

    # Verify wrong secret is rejected
    try:
        post("/api/v1/auth/admin-token", {"secret": "wrong-secret-value"})
        _die("expected 401 for wrong admin secret, got 200")
    except urllib.error.HTTPError as e:
        _assert_eq("wrong-secret status", e.code, 401)
    print("  wrong secret -> 401  ok")

    # ------------------------------------------------------------------
    # 4. Register device
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] registering device")
    step += 1
    reg = post("/api/v1/devices/register", {
        "name": "smoke-device",
        "os": "linux",
        "arch": "amd64",
        "secret": "smoke-device-secret-1",
    })
    device_id = reg.get("deviceId")
    device_token = reg.get("token")
    if not device_id or not device_token:
        _die(f"register response incomplete: {reg}")
    print(f"  device: {device_id}")

    # ------------------------------------------------------------------
    # 5. Connect device WebSocket + send heartbeats
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] connecting device WebSocket + sending heartbeats")
    step += 1
    session = DeviceSession(device_token)
    session.start()
    if not session.ready.wait(timeout=15):
        _die("WebSocket session did not become ready within 15s")
    if session.error:
        _die(f"WebSocket session error: {session.error}")
    print("  connected + first heartbeat sent")

    # ------------------------------------------------------------------
    # 6. Verify device is online
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] verifying device is online")
    step += 1
    # Poll briefly — status update is async
    for _ in range(10):
        detail = get(f"/api/v1/devices/{device_id}", token=admin_token)
        if detail.get("status") == "online":
            break
        time.sleep(0.5)
    _assert_eq("device status", detail.get("status"), "online")
    heartbeats = detail.get("recentHeartbeats", [])
    if not heartbeats:
        _die("recentHeartbeats is empty — no heartbeat stored")
    hb = heartbeats[0]
    _assert_eq("heartbeat osVersion", hb.get("osVersion"), "smoke-linux-1.0")
    print(f"  status=online  heartbeats={len(heartbeats)}  cpu={hb.get('cpuPercent')}%")

    # ------------------------------------------------------------------
    # 7. List devices
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] listing devices")
    step += 1
    devices = get("/api/v1/devices", token=admin_token)
    ids = [d["id"] for d in devices]
    if device_id not in ids:
        _die(f"registered device {device_id} missing from list")
    print(f"  {len(devices)} device(s) listed  ok")

    # ------------------------------------------------------------------
    # 8. SSE — verify event stream delivers heartbeat events
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] SSE — streaming events (up to 12s)")
    step += 1
    n = check_sse(admin_token, timeout=12)
    if n == 0:
        _die("SSE stream opened but delivered no events within 12s")
    print(f"  received {n} event(s)  ok")

    # ------------------------------------------------------------------
    # 9. Request diagnostics
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] requesting diagnostics")
    step += 1
    diag = post(f"/api/v1/devices/{device_id}/diagnose",
                {"scope": "full"}, token=admin_token)
    request_id = diag.get("requestId")
    if not request_id:
        _die(f"diagnose response missing requestId: {diag}")
    print(f"  requestId: {request_id}")

    # Wait for device to respond (WebSocket thread handles it)
    if not session.diag_done.wait(timeout=15):
        _die("device did not send diag_response within 15s")
    if session.error:
        _die(f"WebSocket error during diagnostics: {session.error}")
    print("  device sent diag_response")

    # Poll until result has a payload
    result = None
    for _ in range(20):
        results = get(f"/api/v1/devices/{device_id}/diagnostics", token=admin_token)
        result = next((r for r in results if r["requestId"] == request_id), None)
        if result and result.get("payload") is not None:
            break
        time.sleep(0.5)
    if not result:
        _die(f"diagnostic result for requestId {request_id} not found")
    if result.get("payload") is None:
        _die("diagnostic result has no payload — device response was not stored")
    payload = result["payload"]
    _assert_eq("diag payload hostname", payload.get("hostname"), "smoke-device")
    print(f"  payload: hostname={payload.get('hostname')}  cpuCores={payload.get('cpuCores')}")

    # ------------------------------------------------------------------
    # 10. Metrics endpoint
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] metrics endpoint")
    step += 1
    req = urllib.request.Request(f"{BASE_URL}/metrics")
    with urllib.request.urlopen(req, timeout=10) as resp:
        body = resp.read().decode()
    if resp.status != 200:
        _die(f"/metrics returned {resp.status}")
    for metric in ("puls_http_requests_total", "puls_heartbeats_total", "puls_devices_connected"):
        if metric not in body:
            _die(f"/metrics missing expected metric: {metric}")
    hb_line = next((l for l in body.splitlines() if l.startswith("puls_heartbeats_total")), None)
    if hb_line:
        count_str = hb_line.split()[-1]
        print(f"  puls_heartbeats_total={count_str}  ok")

    # ------------------------------------------------------------------
    # 11. Teardown WebSocket session
    # ------------------------------------------------------------------
    print(f"[{step}/{TOTAL}] closing device WebSocket")
    step += 1
    session.stop()
    session.join(timeout=5)
    print("  closed")

    print("\nDone.")


if __name__ == "__main__":
    main()
