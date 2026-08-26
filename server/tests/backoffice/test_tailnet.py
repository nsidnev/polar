from collections.abc import AsyncGenerator
from datetime import UTC, datetime, timedelta
from typing import Any

import httpx
import jwt
import pytest
import pytest_asyncio
from cryptography.hazmat.primitives.asymmetric import rsa
from sqlalchemy import func, select

from polar.app import create_app
from polar.backoffice import app as backoffice_app
from polar.backoffice import tailnet
from polar.config import settings
from polar.models import User
from polar.models.user_session import UserSession
from polar.postgres import AsyncSession, get_db_read_session, get_db_session
from tests.fixtures.database import SaveFixture

ISSUER = "https://oidc.vercel.com/test-team"
AUDIENCE = "https://vercel.com/test-team"
SUBJECT = "owner:test-team:project:polar:environment:production"
OPERATOR = "admin@example.com"


@pytest.fixture(scope="module")
def private_key() -> rsa.RSAPrivateKey:
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


@pytest.fixture
def make_token(private_key: rsa.RSAPrivateKey) -> Any:
    def _make(**overrides: Any) -> str:
        now = datetime.now(UTC)
        claims: dict[str, Any] = {
            "iss": ISSUER,
            "aud": AUDIENCE,
            "sub": SUBJECT,
            "iat": now,
            "exp": now + timedelta(minutes=5),
        }
        claims.update(overrides)
        return jwt.encode(claims, private_key, algorithm="RS256")

    return _make


@pytest.fixture(autouse=True)
def proxy_settings(
    monkeypatch: pytest.MonkeyPatch, private_key: rsa.RSAPrivateKey
) -> None:
    monkeypatch.setattr(settings, "BACKOFFICE_PROXY_OIDC_ISSUER", ISSUER)
    monkeypatch.setattr(settings, "BACKOFFICE_PROXY_OIDC_AUDIENCE", AUDIENCE)
    monkeypatch.setattr(settings, "BACKOFFICE_PROXY_OIDC_SUBJECT", SUBJECT)

    # Stand in for the issuer's JWKS endpoint so no network call is made.
    class StubJWKSClient:
        def get_signing_key_from_jwt(self, token: str) -> Any:
            class Key:
                key = private_key.public_key()

            return Key()

    monkeypatch.setattr(tailnet, "_jwks_client", lambda issuer: StubJWKSClient())


@pytest_asyncio.fixture
async def admin(save_fixture: SaveFixture, user: User) -> User:
    user.email = OPERATOR
    user.is_admin = True
    await save_fixture(user)
    return user


@pytest_asyncio.fixture
async def client(session: AsyncSession) -> AsyncGenerator[httpx.AsyncClient]:
    backoffice_app.dependency_overrides[get_db_session] = lambda: session
    backoffice_app.dependency_overrides[get_db_read_session] = lambda: session
    try:
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=backoffice_app),
            base_url="http://test",
        ) as client:
            yield client
    finally:
        backoffice_app.dependency_overrides.pop(get_db_session, None)
        backoffice_app.dependency_overrides.pop(get_db_read_session, None)


def _headers(token: str, operator: str = OPERATOR) -> dict[str, str]:
    return {tailnet.TOKEN_HEADER: token, tailnet.OPERATOR_HEADER: operator}


@pytest.mark.asyncio
class TestProxyAuthentication:
    async def test_no_headers_is_not_found(
        self, client: httpx.AsyncClient, admin: User
    ) -> None:
        response = await client.get("/")

        assert response.status_code == 404

    async def test_missing_operator_header_is_not_found(
        self, client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        response = await client.get("/", headers={tailnet.TOKEN_HEADER: make_token()})

        assert response.status_code == 404

    async def test_valid_token_is_authenticated(
        self, client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        response = await client.get("/", headers=_headers(make_token()))

        assert response.status_code == 200

    @pytest.mark.parametrize(
        "overrides",
        [
            pytest.param({"iss": "https://oidc.vercel.com/other-team"}, id="issuer"),
            pytest.param({"aud": "https://vercel.com/other-team"}, id="audience"),
            pytest.param(
                {"sub": "owner:test-team:project:polar:environment:preview"},
                id="subject",
            ),
        ],
    )
    async def test_mismatched_claim_is_not_found(
        self,
        client: httpx.AsyncClient,
        admin: User,
        make_token: Any,
        overrides: dict[str, Any],
    ) -> None:
        response = await client.get("/", headers=_headers(make_token(**overrides)))

        assert response.status_code == 404

    async def test_expired_token_is_not_found(
        self, client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        expired = datetime.now(UTC) - timedelta(minutes=5)
        token = make_token(iat=expired - timedelta(minutes=5), exp=expired)

        response = await client.get("/", headers=_headers(token))

        assert response.status_code == 404

    async def test_token_signed_by_another_key_is_not_found(
        self, client: httpx.AsyncClient, admin: User
    ) -> None:
        other_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        now = datetime.now(UTC)
        token = jwt.encode(
            {
                "iss": ISSUER,
                "aud": AUDIENCE,
                "sub": SUBJECT,
                "iat": now,
                "exp": now + timedelta(minutes=5),
            },
            other_key,
            algorithm="RS256",
        )

        response = await client.get("/", headers=_headers(token))

        assert response.status_code == 404

    async def test_unknown_operator_is_not_found(
        self, client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        response = await client.get(
            "/", headers=_headers(make_token(), operator="nobody@example.com")
        )

        assert response.status_code == 404

    async def test_non_admin_operator_is_not_found(
        self,
        client: httpx.AsyncClient,
        save_fixture: SaveFixture,
        admin: User,
        make_token: Any,
    ) -> None:
        admin.is_admin = False
        await save_fixture(admin)

        response = await client.get("/", headers=_headers(make_token()))

        assert response.status_code == 404

    async def test_session_is_reused_across_requests(
        self,
        client: httpx.AsyncClient,
        session: AsyncSession,
        admin: User,
        make_token: Any,
    ) -> None:
        for _ in range(2):
            response = await client.get("/", headers=_headers(make_token()))
            assert response.status_code == 200

        count = await session.scalar(
            select(func.count())
            .select_from(UserSession)
            .where(
                UserSession.user_id == admin.id,
                UserSession.user_agent == tailnet.SESSION_USER_AGENT,
            )
        )
        assert count == 1


PROXY_HOST = "polar-backoffice.example.ts.net"
DEPLOYMENT_HOST = "polar.vercel.app"


@pytest.mark.asyncio
class TestProxiedUrlGeneration:
    """The backoffice builds absolute URLs from the request.

    Behind the proxy those have to point back at the proxy, or the first link
    the operator clicks leaves the private network and hits the 404 gate.
    """

    @pytest_asyncio.fixture
    async def proxied_client(
        self, monkeypatch: pytest.MonkeyPatch, session: AsyncSession
    ) -> AsyncGenerator[httpx.AsyncClient]:
        # create_app() reads both of these, so they must be set beforehand:
        # VERCEL puts the API under /api, and the forwarded host installs the
        # middleware.
        monkeypatch.setenv("VERCEL", "1")
        monkeypatch.setattr(settings, "BACKOFFICE_PROXY_FORWARDED_HOST", PROXY_HOST)
        app = create_app()

        backoffice_app.dependency_overrides[get_db_session] = lambda: session
        backoffice_app.dependency_overrides[get_db_read_session] = lambda: session
        try:
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=app),
                base_url=f"https://{DEPLOYMENT_HOST}",
            ) as client:
                yield client
        finally:
            backoffice_app.dependency_overrides.pop(get_db_session, None)
            backoffice_app.dependency_overrides.pop(get_db_read_session, None)

    async def test_urls_point_at_the_proxy(
        self, proxied_client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        response = await proxied_client.get(
            "/api/backoffice/",
            headers={
                **_headers(make_token()),
                tailnet.FORWARDED_HOST_HEADER: PROXY_HOST,
                tailnet.FORWARDED_PROTO_HEADER: "http",
            },
        )

        assert response.status_code == 200
        # Generated absolute URLs keep the /api prefix and the proxy's origin.
        assert f"http://{PROXY_HOST}/api/backoffice/static/" in response.text
        assert DEPLOYMENT_HOST not in response.text

    async def test_urls_keep_the_deployment_host_without_the_header(
        self, proxied_client: httpx.AsyncClient, admin: User, make_token: Any
    ) -> None:
        """Counterpart to the above: the forwarded host is what rewrites them."""
        response = await proxied_client.get(
            "/api/backoffice/", headers=_headers(make_token())
        )

        assert response.status_code == 200
        assert f"https://{DEPLOYMENT_HOST}/api/backoffice/static/" in response.text
        assert PROXY_HOST not in response.text

    async def test_public_request_is_not_found(
        self, proxied_client: httpx.AsyncClient, admin: User
    ) -> None:
        response = await proxied_client.get("/api/backoffice/")

        assert response.status_code == 404


@pytest.mark.asyncio
class TestDisabled:
    async def test_falls_back_to_cookie_authentication(
        self,
        client: httpx.AsyncClient,
        monkeypatch: pytest.MonkeyPatch,
        admin: User,
        make_token: Any,
    ) -> None:
        """With no issuer configured the proxy headers carry no authority."""
        monkeypatch.setattr(settings, "BACKOFFICE_PROXY_OIDC_ISSUER", None)

        response = await client.get("/", headers=_headers(make_token()))

        assert response.status_code == 401
