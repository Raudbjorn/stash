"""Declaring recommenders."""
from __future__ import annotations

from . import runtime


def recommender(*, id: str, label: str, contexts: list, description: str = "",
                config: list | None = None, **caps):
    """Register a function as a recommender."""

    def wrapper(fn):
        definition = {
            "id": id,
            "label": label,
            "description": description,
            "contexts": contexts,
            "config": config or [],
            "supports_limit_override": caps.get("supports_limit_override", True),
            "supports_pagination": caps.get("supports_pagination", True),
            "needs_seed_scenes": caps.get("needs_seed_scenes", False),
            "allows_multi_seed": caps.get("allows_multi_seed", True),
            "exposes_scores": caps.get("exposes_scores", True),
        }
        runtime.registry().add_recommender(runtime.plugin_name(), definition, fn)
        return fn

    return wrapper
