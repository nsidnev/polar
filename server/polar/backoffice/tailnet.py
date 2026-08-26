"""Authentication for the backoffice reverse proxy.

The backoffice can be fronted by a proxy that is reachable only over a private
network. Two things travel with every proxied request:

- an OIDC token minted for the proxy's own workload identity, proving the
  request really came from the proxy and not from the public internet;
- the identity of the operator the proxy authenticated at the network layer.

Both are required. The token is what the 404 gate in `dependencies` checks, so
an unauthenticated request can't tell the backoffice apart from a path that
doesn't exist.
"""

import functools
from typing import Any

import jwt
import structlog
from fastapi import Request
from starlette.concurrency import run_in_threadpool

from polar.config import settings
from polar.logging import Logger

log: Logger = structlog.get_logger()

TOKEN_HEADER = "X-Polar-Backoffice-Token"
OPERATOR_HEADER = "X-Polar-Backoffice-User"
FORWARDED_HOST_HEADER = "X-Polar-Forwarded-Host"
FORWARDED_PROTO_HEADER = "X-Polar-Forwarded-Proto"

# The session marker lets a proxied request pick up the session an earlier one
# minted, instead of writing a new row on every page load.
SESSION_USER_AGENT = "polar-backoffice-proxy"

ALGORITHMS = ["RS256"]


def is_enabled() -> bool:
    return settings.BACKOFFICE_PROXY_OIDC_ISSUER is not None


@functools.cache
def _jwks_client(issuer: str) -> jwt.PyJWKClient:
    # Caches the JWK set, so keep one instance per issuer for the process
    # lifetime rather than re-fetching keys on every request.
    return jwt.PyJWKClient(f"{issuer}/.well-known/jwks", timeout=5)


def _decode(token: str, issuer: str) -> dict[str, Any]:
    signing_key = _jwks_client(issuer).get_signing_key_from_jwt(token)
    return jwt.decode(
        token,
        signing_key.key,
        algorithms=ALGORITHMS,
        issuer=issuer,
        audience=settings.BACKOFFICE_PROXY_OIDC_AUDIENCE,
        subject=settings.BACKOFFICE_PROXY_OIDC_SUBJECT,
        options={
            "require": ["exp", "iat", "iss", "sub"],
            "verify_aud": settings.BACKOFFICE_PROXY_OIDC_AUDIENCE is not None,
        },
    )


async def authenticate_operator(request: Request) -> str | None:
    """Return the operator email the proxy vouched for, or None.

    None means "not a valid proxied request" for every reason: the feature is
    off, a header is missing, or the token didn't verify. Callers turn that
    into a 404 without distinguishing, so probing can't map the surface.
    """
    issuer = settings.BACKOFFICE_PROXY_OIDC_ISSUER
    if issuer is None:
        return None

    token = request.headers.get(TOKEN_HEADER)
    operator = request.headers.get(OPERATOR_HEADER)
    if not token or not operator:
        return None

    try:
        # PyJWKClient fetches keys over blocking HTTP on a cache miss.
        await run_in_threadpool(_decode, token, issuer)
    except (jwt.InvalidTokenError, jwt.PyJWKClientError) as e:
        log.warning(
            "backoffice.proxy.token_rejected", error=str(e), error_type=type(e).__name__
        )
        return None

    return operator
