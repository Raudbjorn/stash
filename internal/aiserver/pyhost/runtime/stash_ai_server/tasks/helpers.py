"""Task helpers, forwarding to the SDK."""
from __future__ import annotations

from stash_ai.tasks import submit_child_tasks as _submit


def task_handler(fn=None, **_kwargs):
    """Compatibility no-op.

    The standalone server used this to record a handler's arity so it could be
    called with the right number of arguments. Go has one signature, so nothing
    needs recording.
    """
    if fn is None:
        return lambda f: f
    return fn


async def spawn_chunked_tasks(*, parent_task=None, handler=None, items, chunk_size=1,
                              params=None, context_factory=None, priority="high",
                              hold_children=True, **_kwargs):
    """Fan work out into child tasks.

    Stash releases the parent's concurrency slot automatically, so waiting on
    children cannot deadlock the parent's own service.
    """
    action_id = getattr(parent_task, "action_id", "") or ""
    ids = _submit(action_id, list(items), chunk_size=chunk_size,
                  params=params or {}, priority=priority)
    return {"children": ids, "count": len(ids)}
