"""Plugin API helpers.

Routing lives in Go's chi mux now, so a plugin cannot mount FastAPI routes here.
"""
from __future__ import annotations

from stash_ai_server._errors import unsupported


def _require_plugin_active(*_args, **_kwargs) -> bool:
    """Always true: Stash only dispatches to loaded plugins."""
    return True


def register_plugin_router(*_args, **_kwargs):
    raise unsupported(
        "mounting a FastAPI router",
        "the routes declared in your plugin's manifest",
    )
