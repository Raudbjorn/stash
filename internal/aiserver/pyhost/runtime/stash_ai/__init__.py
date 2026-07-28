"""The plugin-facing SDK.

Everything a plugin needs is re-exported here, so `from stash_ai import action,
progress, db` is enough for the common cases.
"""
from __future__ import annotations

from .actions import action
from .db import batch, execute, query
from .http import Request, Response, route
from .recommenders import recommender
from .services import register_service
from .settings import get_setting, get_settings, set_setting
from .stash import graphql
from .tasks import cancelled, mark_controller, progress, submit_child_tasks

__all__ = [
    "action",
    "recommender",
    "register_service",
    "query",
    "execute",
    "batch",
    "get_setting",
    "get_settings",
    "set_setting",
    "graphql",
    "progress",
    "cancelled",
    "mark_controller",
    "submit_child_tasks",
    "route",
    "Request",
    "Response",
]
