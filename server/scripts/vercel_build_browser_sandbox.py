"""Build a Vercel Sandbox snapshot for the remote browser.

Creates a fresh snapshot and replaces `__VERCEL_SANDBOX_SNAPSHOT_ID__` placeholder
in `polar/organization_review/sandbox.py` with the new ID so the deploy
bundle ships with an ID baked in.

Is used by `buildCommand` in `vercel.json` for `worker` service.
"""

import asyncio
from pathlib import Path

import typer

from polar.organization_review.sandbox import build_snapshot

_ADAPTER_PATH = (
    Path(__file__).resolve().parent.parent
    / "polar"
    / "organization_review"
    / "sandbox.py"
)
_PLACEHOLDER = "__VERCEL_SANDBOX_SNAPSHOT_ID__"


def main() -> None:
    snapshot_id = asyncio.run(build_snapshot())
    typer.echo(f"built snapshot: {snapshot_id}")

    content = _ADAPTER_PATH.read_text()
    if _PLACEHOLDER not in content:
        typer.echo(
            f"placeholder {_PLACEHOLDER!r} not found in {_ADAPTER_PATH} — "
            "deploy refuses to ship without a baked-in snapshot id",
            err=True,
        )
        raise typer.Exit(code=1)
    _ADAPTER_PATH.write_text(content.replace(_PLACEHOLDER, snapshot_id))
    typer.echo(f"wrote snapshot id to {_ADAPTER_PATH}")


if __name__ == "__main__":
    typer.run(main)
