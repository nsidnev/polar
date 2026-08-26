from fastapi import Depends, Request
from fastapi.exceptions import HTTPException

from polar.auth.scope import Scope
from polar.auth.service import auth as auth_service
from polar.config import settings
from polar.models.user_session import UserSession
from polar.postgres import AsyncSession, get_db_session
from polar.user.repository import UserRepository

from . import tailnet


async def get_admin(
    request: Request,
    session: AsyncSession = Depends(get_db_session),
) -> UserSession:
    if tailnet.is_enabled():
        return await _get_proxied_admin(request, session)

    user_session = await auth_service.authenticate(session, request)
    orig_user_session = await auth_service.authenticate(
        session, request, cookie=settings.IMPERSONATION_COOKIE_KEY
    )
    # Original session (admin-user) takes precedence
    user_session = orig_user_session or user_session

    if user_session is None:
        raise HTTPException(status_code=401, detail="Unauthorized")

    user = user_session.user

    if not user.is_admin:
        raise HTTPException(status_code=403, detail="Forbidden")

    return user_session


async def _get_proxied_admin(request: Request, session: AsyncSession) -> UserSession:
    """Authenticate an operator vouched for by the backoffice proxy.

    Every failure is a 404, so the backoffice is indistinguishable from a path
    that doesn't exist unless the request came through the proxy. Cookies are
    deliberately not consulted: falling back to them would serve the backoffice
    on the public origin to anyone still holding a session.
    """
    email = await tailnet.authenticate_operator(request)
    if email is None:
        raise HTTPException(status_code=404, detail="Not Found")

    user_repository = UserRepository.from_session(session)
    user = await user_repository.get_by_email(email)
    if user is None or not user.is_admin:
        raise HTTPException(status_code=404, detail="Not Found")

    return await auth_service.get_or_create_user_session(
        session,
        user,
        user_agent=tailnet.SESSION_USER_AGENT,
        scopes=list(Scope),
        expire_in=settings.BACKOFFICE_PROXY_SESSION_TTL,
    )
