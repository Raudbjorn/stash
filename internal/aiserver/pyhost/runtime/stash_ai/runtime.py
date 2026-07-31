"""Per-plugin runtime state for the SDK.

While a plugin is being imported, module-level decorators need to know which
plugin they belong to. Rather than make every decorator take a plugin argument,
the loader binds the current plugin around the import and the SDK reads it here.

The invocation binding works the same way for handlers: progress() and
cancelled() need to know which invocation they are inside, and threading that
through every call would make plugin code much noisier.
"""
from __future__ import annotations

import threading
from typing import Any

_local = threading.local()

_current: dict[str, Any] = {
    "plugin": None,
    "registry": None,
    "bridge": None,
    "dispatcher": None,
    "settings": [],
}


def configure(*, plugin, registry, bridge, dispatcher, settings) -> None:
    _current.update(
        plugin=plugin,
        registry=registry,
        bridge=bridge,
        dispatcher=dispatcher,
        settings=settings,
    )


def reset() -> None:
    _current["plugin"] = None


def plugin_name() -> str:
    # A running handler wins over import state: by the time it runs the loader
    # has moved on, and several plugins may be loaded at once.
    invocation = current_invocation()
    if invocation is not None and getattr(invocation, "plugin", ""):
        return invocation.plugin

    name = _current.get("plugin")
    if not name:
        raise RuntimeError(
            "no plugin is being loaded; SDK calls must happen during import or "
            "inside a handler"
        )
    return name


def registry():
    return _current["registry"]


def bridge():
    bridge_obj = _current.get("bridge")
    if bridge_obj is None:
        raise RuntimeError("the Stash bridge is unavailable")
    return bridge_obj


def declared_settings() -> list:
    return _current.get("settings") or []


def bind_invocation(record) -> None:
    _local.invocation = record


def unbind_invocation() -> None:
    _local.invocation = None


def current_invocation():
    return getattr(_local, "invocation", None)
