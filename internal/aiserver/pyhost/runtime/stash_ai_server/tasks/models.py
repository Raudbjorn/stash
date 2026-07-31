"""Task shapes exposed to plugins."""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class TaskPriority(str, Enum):
    high = "high"
    normal = "normal"
    low = "low"


class TaskStatus(str, Enum):
    queued = "queued"
    running = "running"
    completed = "completed"
    failed = "failed"
    cancelled = "cancelled"
    streaming = "streaming"


@dataclass
class TaskRecord:
    """A running task, as a handler sees it.

    Backed by the SDK rather than by local state: Stash owns the scheduler now.
    """

    id: str = ""
    action_id: str = ""
    service: str = ""
    status: str = "running"
    params: dict = field(default_factory=dict)

    def is_cancelled(self) -> bool:
        from stash_ai.tasks import cancelled

        return cancelled()

    def emit_progress(self, fraction: float = 0.0, message: str = "", **detail) -> None:
        from stash_ai.tasks import progress

        progress(fraction, message, **detail)
