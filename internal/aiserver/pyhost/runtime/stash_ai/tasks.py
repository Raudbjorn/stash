"""Progress, cancellation and child tasks."""
from __future__ import annotations

from . import runtime


def progress(fraction: float, message: str = "", **detail) -> None:
    """Report progress for the running task.

    A no-op outside a handler, so calling it from module scope or a helper that
    is sometimes used outside a task does not raise.
    """
    record = runtime.current_invocation()
    if record is None:
        return
    runtime.bridge().emit_progress(record.id, float(fraction), message, detail or None)


def cancelled() -> bool:
    """Report whether cancellation has been requested.

    Long-running handlers should poll this; a handler that never does can only
    be stopped by recycling the host.
    """
    record = runtime.current_invocation()
    return bool(record and record.cancel.is_cancelled())


def mark_controller() -> None:
    """Declare this task a coordinator rather than a worker.

    Releases its concurrency slot back to its service, so waiting on children
    cannot deadlock against itself - a service with concurrency 1 would
    otherwise have its only slot held by the parent that is waiting for it.

    A no-op outside a handler.
    """
    record = runtime.current_invocation()
    if record is None:
        return
    runtime.bridge().mark_controller(runtime.plugin_name(), record.id)


def submit_child_tasks(action_id: str, items: list, *, chunk_size: int = 1,
                       context: dict | None = None, params: dict | None = None,
                       priority: str = "high") -> list[str]:
    """Fan work out into child tasks and return their invocation ids.

    The parent stays alive to coordinate; Stash releases its concurrency slot so
    waiting on children cannot deadlock its own service.
    """
    record = runtime.current_invocation()
    parent_id = record.id if record else ""
    bridge = runtime.bridge()
    plugin = runtime.plugin_name()

    # The parent coordinates rather than works from here on, so its slot goes
    # back before it starts waiting on what it is about to submit.
    if parent_id:
        mark_controller()

    base_context = dict(context or {})
    base_params = dict(params or {})

    ids: list[str] = []
    for start in range(0, len(items), max(1, chunk_size)):
        chunk = items[start:start + max(1, chunk_size)]

        child_context = dict(base_context)
        # One item is a detail-shaped context; several is a bulk selection.
        if len(chunk) == 1:
            child_context["entityId"] = str(chunk[0])
            child_context["selectedIds"] = []
        else:
            child_context["selectedIds"] = [str(item) for item in chunk]

        child_params = dict(base_params)
        child_params["items"] = chunk

        ids.append(
            bridge.submit_task(
                plugin, action_id, child_context, child_params, parent_id, priority
            )
        )
    return ids
