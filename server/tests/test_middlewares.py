import asyncio
import json
from typing import Any, cast

from starlette.datastructures import Headers, MutableHeaders
from starlette.types import Message, Receive, Scope, Send

from polar.middlewares import ForwardedHostMiddleware, MaxBodySizeMiddleware


def _http_scope(headers: list[tuple[bytes, bytes]], method: str = "POST") -> Scope:
    return cast(
        Scope,
        {
            "type": "http",
            "method": method,
            "path": "/v1/files/",
            "headers": headers,
        },
    )


def _run(
    limit: int,
    headers: list[tuple[bytes, bytes]],
    body_chunks: list[bytes],
    method: str = "POST",
) -> tuple[list[Message], bool]:
    app_called = False

    async def reading_app(scope: Scope, receive: Receive, send: Send) -> None:
        nonlocal app_called
        app_called = True
        more_body = True
        while more_body:
            message = await receive()
            more_body = message.get("more_body", False)
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b"ok"})

    middleware = MaxBodySizeMiddleware(reading_app, limit=limit)

    messages = [
        {
            "type": "http.request",
            "body": chunk,
            "more_body": i < len(body_chunks) - 1,
        }
        for i, chunk in enumerate(body_chunks)
    ]
    messages_iter = iter(messages)

    async def receive() -> Message:
        return cast(Message, next(messages_iter))

    sent: list[Message] = []

    async def send(message: Message) -> None:
        sent.append(message)

    asyncio.run(middleware(_http_scope(headers, method), receive, cast(Send, send)))
    return sent, app_called


def _response_status(sent: list[Message]) -> int:
    return next(m["status"] for m in sent if m["type"] == "http.response.start")


def _response_json(sent: list[Message]) -> dict[str, Any]:
    body = b"".join(
        m.get("body", b"") for m in sent if m["type"] == "http.response.body"
    )
    return json.loads(body)


class TestMaxBodySizeMiddleware:
    def test_under_limit(self) -> None:
        sent, app_called = _run(
            limit=10,
            headers=[(b"content-length", b"4")],
            body_chunks=[b"1234"],
        )
        assert app_called is True
        assert _response_status(sent) == 200

    def test_content_length_over_limit(self) -> None:
        sent, app_called = _run(
            limit=10,
            headers=[(b"content-length", b"11")],
            body_chunks=[b"12345678901"],
        )
        assert app_called is False
        assert _response_status(sent) == 413
        assert _response_json(sent)["error"] == "RequestBodyTooLarge"

    def test_missing_content_length_post(self) -> None:
        sent, app_called = _run(
            limit=10,
            headers=[],
            body_chunks=[b"1234"],
        )
        assert app_called is False
        assert _response_status(sent) == 411
        assert _response_json(sent)["error"] == "LengthRequired"

    def test_missing_content_length_get(self) -> None:
        sent, app_called = _run(
            limit=10,
            headers=[],
            body_chunks=[b""],
            method="GET",
        )
        assert app_called is True
        assert _response_status(sent) == 200


PROXY_HOST = "backoffice.example.ts.net"
DEPLOYMENT_HOST = "polar.vercel.app"


def _forward(
    path: str,
    headers: list[tuple[bytes, bytes]],
    host: str = PROXY_HOST,
    path_prefix: str = "/api/backoffice",
) -> Scope:
    """Run ForwardedHostMiddleware and return the scope the app observed."""
    seen: Scope = cast(Scope, {})

    async def app(scope: Scope, receive: Receive, send: Send) -> None:
        nonlocal seen
        seen = scope

    outer = cast(
        Scope,
        {
            "type": "http",
            "method": "GET",
            "path": path,
            "scheme": "https",
            "headers": [(b"host", DEPLOYMENT_HOST.encode()), *headers],
        },
    )
    asyncio.run(
        ForwardedHostMiddleware(app, host=host, path_prefix=path_prefix)(
            outer, cast(Receive, None), cast(Send, None)
        )
    )
    return seen


def _host(scope: Scope) -> str:
    return Headers(scope=scope)["host"]


class TestForwardedHostMiddleware:
    def test_forwarded_host_is_applied(self) -> None:
        scope = _forward(
            "/api/backoffice/customers",
            [(b"x-polar-forwarded-host", PROXY_HOST.encode())],
        )
        assert _host(scope) == PROXY_HOST

    def test_forwarded_proto_is_applied(self) -> None:
        scope = _forward(
            "/api/backoffice/",
            [
                (b"x-polar-forwarded-host", PROXY_HOST.encode()),
                (b"x-polar-forwarded-proto", b"http"),
            ],
        )
        assert scope["scheme"] == "http"

    def test_unknown_proto_is_ignored(self) -> None:
        scope = _forward(
            "/api/backoffice/",
            [
                (b"x-polar-forwarded-host", PROXY_HOST.encode()),
                (b"x-polar-forwarded-proto", b"gopher"),
            ],
        )
        assert scope["scheme"] == "https"

    def test_other_host_is_ignored(self) -> None:
        """A request can't relabel itself as arriving somewhere else."""
        scope = _forward(
            "/api/backoffice/",
            [(b"x-polar-forwarded-host", b"attacker.example.com")],
        )
        assert _host(scope) == DEPLOYMENT_HOST

    def test_path_outside_prefix_is_ignored(self) -> None:
        scope = _forward(
            "/api/v1/orders",
            [(b"x-polar-forwarded-host", PROXY_HOST.encode())],
        )
        assert _host(scope) == DEPLOYMENT_HOST

    def test_prefix_must_match_a_path_segment(self) -> None:
        scope = _forward(
            "/api/backoffice-public/leak",
            [(b"x-polar-forwarded-host", PROXY_HOST.encode())],
        )
        assert _host(scope) == DEPLOYMENT_HOST

    def test_bare_prefix_is_included(self) -> None:
        scope = _forward(
            "/api/backoffice",
            [(b"x-polar-forwarded-host", PROXY_HOST.encode())],
        )
        assert _host(scope) == PROXY_HOST

    def test_no_forwarded_header_is_untouched(self) -> None:
        scope = _forward("/api/backoffice/", [])
        assert _host(scope) == DEPLOYMENT_HOST

    def test_outer_scope_is_not_mutated(self) -> None:
        """Vercel's own routing must keep seeing the deployment host."""
        outer_headers = [(b"host", DEPLOYMENT_HOST.encode())]

        async def app(scope: Scope, receive: Receive, send: Send) -> None:
            MutableHeaders(scope=scope)["host"] = "mutated.example.com"

        outer = cast(
            Scope,
            {
                "type": "http",
                "method": "GET",
                "path": "/api/backoffice/",
                "scheme": "https",
                "headers": [
                    *outer_headers,
                    (b"x-polar-forwarded-host", PROXY_HOST.encode()),
                ],
            },
        )
        asyncio.run(
            ForwardedHostMiddleware(
                app, host=PROXY_HOST, path_prefix="/api/backoffice"
            )(outer, cast(Receive, None), cast(Send, None))
        )

        assert Headers(scope=outer)["host"] == DEPLOYMENT_HOST

    def test_non_http_scope_passes_through(self) -> None:
        seen: Scope = cast(Scope, {})

        async def app(scope: Scope, receive: Receive, send: Send) -> None:
            nonlocal seen
            seen = scope

        outer = cast(Scope, {"type": "lifespan"})
        asyncio.run(
            ForwardedHostMiddleware(
                app, host=PROXY_HOST, path_prefix="/api/backoffice"
            )(outer, cast(Receive, None), cast(Send, None))
        )
        assert seen["type"] == "lifespan"
