from enum import IntEnum, StrEnum


class TaskPriority(IntEnum):
    HIGH = 0
    MEDIUM = 50
    LOW = 100


# topics on Vercel should not container underscore
class TaskQueue(StrEnum):
    HIGH_PRIORITY = "high-priority"
    MEDIUM_PRIORITY = "medium-priority"
    LOW_PRIORITY = "low-priority"
    WEBHOOKS = "webhooks"


__all__ = [
    "TaskPriority",
    "TaskQueue",
]
