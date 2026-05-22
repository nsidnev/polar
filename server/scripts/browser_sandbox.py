"""Build a Vercel Sandbox snapshot for the remote browser.

Invoked by the taskipy task `browser_sandbox` from the Vercel worker
`buildCommand`. Creates a fresh snapshot via the Vercel Sandbox SDK,
then string-replaces the `__VERCEL_SANDBOX_SNAPSHOT_ID__` placeholder
in `polar/organization_review/sandbox.py` with the new id so the deploy
bundle ships with a usable id baked in.

Auth comes from the build environment's `VERCEL_OIDC_TOKEN` (automatic on
Vercel builds), or `VERCEL_TOKEN` + `VERCEL_TEAM_ID` + `VERCEL_PROJECT_ID`
for local invocation. Any failure exits non-zero, which fails the deploy.
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
    """Create a snapshot and bake its id into sandbox.py."""
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
