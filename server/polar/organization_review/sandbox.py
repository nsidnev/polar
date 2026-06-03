# On Vercel the runtime lacks support for some of the dependencies Chromium requires
# so instead of running playwright directly, on Vercel a sandbox is used with
# connection to the service through websockets

from __future__ import annotations

import asyncio
import os
import secrets
from importlib.metadata import version as pkg_version
from urllib.parse import urlsplit, urlunsplit

import httpx
import structlog
from vercel.sandbox import (
    AsyncSandbox,
    NetworkPolicyCustom,
    NetworkPolicySubnets,
    SnapshotSource,
)

log = structlog.get_logger(__name__)


# Replaced at deploy time by `scripts/vercel_build_browser_sandbox.py`
SNAPSHOT_ID = "__VERCEL_SANDBOX_SNAPSHOT_ID__"


BROWSER_SERVER_PORT = 9222
SANDBOX_TIMEOUT_MS = 120_000
BUILD_SANDBOX_TIMEOUT_MS = 15 * 60_000
READY_POLL_TIMEOUT_S = 30.0
READY_POLL_INTERVAL_S = 0.5

PLAYWRIGHT_VERSION = pkg_version("playwright")

# AL2023 packages Chromium needs at runtime. List borrowed from the
# vercel-sandbox + agent-browser.
CHROMIUM_DNF_DEPS = [
    "nss",
    "nspr",
    "libxkbcommon",
    "atk",
    "at-spi2-atk",
    "at-spi2-core",
    "libXcomposite",
    "libXdamage",
    "libXrandr",
    "libXfixes",
    "libXcursor",
    "libXi",
    "libXtst",
    "libXScrnSaver",
    "libXext",
    "mesa-libgbm",
    "libdrm",
    "mesa-libGL",
    "mesa-libEGL",
    "cups-libs",
    "alsa-lib",
    "pango",
    "cairo",
    "gtk3",
    "dbus-libs",
    "fontconfig",
    "liberation-fonts",
]

PRIVATE_CIDRS = [
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "127.0.0.0/8",
    "169.254.0.0/16",
]

_START_SCRIPT_PATH = "/vercel/sandbox/start.sh"
_SECRET_PATH = "/vercel/sandbox/secret"

_START_SCRIPT = (
    f"""#!/bin/bash
set -euo pipefail
SECRET=$(cat "{_SECRET_PATH}")
exec python3 -m playwright run-server \\
    --port {BROWSER_SERVER_PORT} \\
    --host 0.0.0.0 \\
    --path "/$SECRET"
"""
).encode()


def is_vercel_runtime() -> bool:
    return (
        os.environ.get("VERCEL") is not None
        and os.environ.get("VERCEL_ENV") != "development"
    )


async def provision_browser_sandbox(allowed_domain: str) -> tuple[AsyncSandbox, str]:
    if not SNAPSHOT_ID.startswith("snap_"):
        raise RuntimeError(
            "Vercel sandbox snapshot id is missing — "
            "`uv run task vercel_browser_sandbox` must run during deploy."
        )

    secret = secrets.token_urlsafe(32)

    sandbox = await AsyncSandbox.create(
        source=SnapshotSource(snapshot_id=SNAPSHOT_ID),
        ports=[BROWSER_SERVER_PORT],
        timeout=SANDBOX_TIMEOUT_MS,
        network_policy=NetworkPolicyCustom(
            allow=[allowed_domain, f"*.{allowed_domain}"],
            subnets=NetworkPolicySubnets(deny=PRIVATE_CIDRS),
        ),
    )

    try:
        # write per-instance secret with 0600 perms so other tenants
        # can't read it via a sibling process.
        await sandbox.write_files(
            [
                {
                    "path": _SECRET_PATH,
                    "content": secret.encode(),
                    "mode": 0o600,
                },
            ]
        )

        await sandbox.run_command_detached("bash", [_START_SCRIPT_PATH])

        public_url = sandbox.domain(BROWSER_SERVER_PORT)
        url_parts = urlsplit(public_url)
        chromium_url = urlunsplit(
            (url_parts.scheme, url_parts.netloc, f"/{secret}", "", "")
        )
        await _wait_for_server_ready(chromium_url)

        ws_url = _to_wss(chromium_url)
        log.info(
            "website_collector.sandbox_ready",
            sandbox_id=sandbox.sandbox_id,
            host=urlsplit(public_url).netloc,
        )
        return sandbox, ws_url
    except Exception:
        try:
            await sandbox.stop()
        except Exception:
            log.warning(
                "website_collector.sandbox_stop_after_provision_failure",
                exc_info=True,
            )
        raise


async def build_snapshot() -> str:
    sandbox = await AsyncSandbox.create(
        runtime="python3.13",
        timeout=BUILD_SANDBOX_TIMEOUT_MS,
    )
    try:
        log.info("website_collector.snapshot_build.dnf_install")
        await sandbox.run_command(
            "sh",
            [
                "-c",
                "dnf clean all 2>&1 && "
                "dnf install -y --skip-broken "
                + " ".join(CHROMIUM_DNF_DEPS)
                + " 2>&1 && "
                "ldconfig 2>&1",
            ],
            sudo=True,
        )

        log.info(
            "website_collector.snapshot_build.pip_install",
            playwright_version=PLAYWRIGHT_VERSION,
        )
        await sandbox.run_command(
            "pip", ["install", f"playwright=={PLAYWRIGHT_VERSION}"]
        )

        log.info("website_collector.snapshot_build.playwright_install")
        await sandbox.run_command(
            "python3", ["-m", "playwright", "install", "chromium"]
        )

        log.info("website_collector.snapshot_build.write_start_script")
        await sandbox.write_files(
            [
                {
                    "path": _START_SCRIPT_PATH,
                    "content": _START_SCRIPT,
                    "mode": 0o755,
                },
            ]
        )

        log.info("website_collector.snapshot_build.creating_snapshot")
        snap = await sandbox.snapshot(expiration=0)
        return snap.snapshot_id
    finally:
        # `sandbox.snapshot()` auto-stops the sandbox, but call stop() just
        # in case something has failed before creation finished.
        try:
            await sandbox.stop()
        except Exception:
            pass


async def _wait_for_server_ready(url: str) -> None:
    deadline = asyncio.get_event_loop().time() + READY_POLL_TIMEOUT_S
    last_exc: Exception | None = None
    async with httpx.AsyncClient(timeout=2.0, follow_redirects=False) as client:
        while asyncio.get_event_loop().time() < deadline:
            try:
                resp = await client.get(url)
            except httpx.HTTPError as exc:
                last_exc = exc
            else:
                if resp.status_code < 500:
                    return
                last_exc = RuntimeError(f"unexpected status {resp.status_code}")
            await asyncio.sleep(READY_POLL_INTERVAL_S)
    raise TimeoutError(
        f"playwright run-server did not become ready within {READY_POLL_TIMEOUT_S}s "
        f"(last error: {last_exc!r})"
    )


def _to_wss(https_url: str) -> str:
    parts = urlsplit(https_url)
    if parts.scheme == "https":
        return urlunsplit(("wss", parts.netloc, parts.path, parts.query, ""))
    if parts.scheme == "http":
        return urlunsplit(("ws", parts.netloc, parts.path, parts.query, ""))
    return https_url
