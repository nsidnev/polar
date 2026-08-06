"""APScheduler entrypoint for Vercel deployments.

Declared as a queue subscriber in pyproject.toml: the vercel-apscheduler
integration drives this scheduler through delayed queue messages instead
of a resident process. Jobs enqueue their actor onto its dramatiq queue,
mirroring how polar.worker.scheduler dispatches cron triggers.
"""

from apscheduler.schedulers.blocking import BlockingScheduler

from polar.worker.run import broker

scheduler = BlockingScheduler(timezone="UTC")


def enqueue_actor(actor_name: str) -> None:
    broker.get_actor(actor_name).send()


for _actor_name in sorted(broker.get_declared_actors()):
    _cron_trigger = broker.get_actor(_actor_name).options.get("cron_trigger")
    if _cron_trigger is not None:
        scheduler.add_job(
            enqueue_actor,
            _cron_trigger,
            args=(_actor_name,),
            id=_actor_name,
            replace_existing=True,
        )

__all__ = ["scheduler"]
