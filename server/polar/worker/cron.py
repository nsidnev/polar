# transform APScheduler cron jobs into Vercel crons format

from collections.abc import Callable, Sequence
from typing import Any

from apscheduler.triggers.cron import CronTrigger
from apscheduler.triggers.cron.fields import BaseField

from polar import tasks  # noqa: F401 - ensure all actors are registered

from ._broker import scheduler_middleware

_CRONTAB_FIELDS = ("minute", "hour", "day", "month", "day_of_week")


def _expr_to_str(expr: Any, remap: Callable[[int], int] | None = None) -> str:
    if not hasattr(expr, "first"):
        s = "*"
    else:
        first = remap(expr.first) if remap else expr.first
        last = remap(expr.last) if remap else expr.last
        if last != first:
            s = f"{first}-{last}"
        else:
            s = str(first)
    if expr.step:
        s += f"/{expr.step}"
    return s


def _field_to_str(field: BaseField, remap: Callable[[int], int] | None = None) -> str:
    return ",".join(_expr_to_str(e, remap) for e in field.expressions)


def _apscheduler_dow_to_posix(n: int) -> int:
    """APScheduler: mon=0..sun=6 -> POSIX: sun=0..sat=6"""
    return (n + 1) % 7


def trigger_to_crontab(trigger: CronTrigger) -> str:
    fields = {f.name: f for f in trigger.fields}
    parts = []
    for name in _CRONTAB_FIELDS:
        remap = _apscheduler_dow_to_posix if name == "day_of_week" else None
        parts.append(_field_to_str(fields[name], remap))
    return " ".join(parts)


def _triggers_to_crontab(
    cron_triggers: Sequence[tuple[Callable[..., Any], CronTrigger]],
) -> list[tuple[str, str]]:
    result = []
    for send_fn, trigger in cron_triggers:
        actor = send_fn.__self__  # type: ignore[attr-defined]
        module = actor.fn.__module__
        name = actor.fn.__name__
        schedule = trigger_to_crontab(trigger)
        result.append((f"{module}:{name}", schedule))
    return result


class CronTab:
    def get_crons(self) -> list[tuple[str, str]]:
        return _triggers_to_crontab(scheduler_middleware.cron_triggers)


crontab = CronTab()


if __name__ == "__main__":
    for name, sched in crontab.get_crons():
        print(f"{name:60s} {sched}")  # noqa: T201
