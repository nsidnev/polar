from enum import IntEnum, StrEnum


class TaskPriority(IntEnum):
    HIGH = 0
    MEDIUM = 50
    LOW = 100


# topics on Vercel must not contain underscores
class TaskQueue(StrEnum):
    HIGH_PRIORITY = "high-priority"
    MEDIUM_PRIORITY = "medium-priority"
    LOW_PRIORITY = "low-priority"
    WEBHOOKS = "webhooks"
    TINYBIRD = "tinybird"
    INVOICES_AND_RECEIPTS = "invoices-and-receipts"


__all__ = [
    "TaskPriority",
    "TaskQueue",
]
