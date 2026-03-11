from polar import tasks_worker as tasks  # noqa: F811
from polar.logfire import configure_logfire
from polar.logging import configure as configure_logging
from polar.posthog import configure_posthog
from polar.sentry import configure_sentry
from polar.worker import broker

configure_sentry()
configure_logfire("worker")
configure_logging(logfire=True)
configure_posthog()

__all__ = ["broker", "tasks"]
