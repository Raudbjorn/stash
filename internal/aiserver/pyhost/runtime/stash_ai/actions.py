"""Declaring actions."""
from __future__ import annotations

from . import runtime


def action(*, id: str, label: str, service: str = "", description: str = "",
           contexts: list | None = None, result_kind: str = "none",
           dialog_type: str | None = None, input_schema: dict | None = None,
           deduplicate_submissions: bool = True):
    """Register a function as an action offered in the Stash UI."""

    def wrapper(fn):
        definition = {
            "id": id,
            "label": label,
            "description": description,
            "service": service,
            "result_kind": result_kind,
            "dialog_type": dialog_type,
            "contexts": contexts or [],
            "input_schema": input_schema,
            "deduplicate_submissions": deduplicate_submissions,
        }
        runtime.registry().add_action(runtime.plugin_name(), definition, fn)
        return fn

    return wrapper
