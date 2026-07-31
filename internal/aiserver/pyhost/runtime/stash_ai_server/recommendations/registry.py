"""The @recommender decorator, forwarding to the SDK."""
from __future__ import annotations

from stash_ai.recommenders import recommender  # noqa: F401


class _RecommenderRegistry:
    def register(self, definition, handler):  # pragma: no cover
        raise NotImplementedError("register via the @recommender decorator")


recommender_registry = _RecommenderRegistry()
