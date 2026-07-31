"""The task manager surface plugins referenced directly."""
from __future__ import annotations

from ._errors_proxy import unsupported


class _Manager:
    def mark_controller(self, _task=None) -> None:
        """No-op: Stash releases a coordinator's slot on fan-out itself."""

    def submit(self, *_args, **_kwargs):
        raise unsupported(
            "submitting directly to the task manager",
            "stash_ai.tasks.submit_child_tasks",
        )


manager = _Manager()
