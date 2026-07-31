"""Recommendation shapes."""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class RecContext(str, Enum):
    global_feed = "global_feed"
    similar_scene = "similar_scene"
    prune_candidates = "prune_candidates"


@dataclass
class RecommendationRequest:
    context: str = ""
    recommenderId: str = ""
    config: dict = field(default_factory=dict)
    seedSceneIds: list = field(default_factory=list)
    limit: int | None = None
    offset: int = 0

    @classmethod
    def from_dict(cls, data: dict) -> "RecommendationRequest":
        return cls(
            context=data.get("context", ""),
            recommenderId=data.get("recommenderId", ""),
            config=data.get("config") or {},
            seedSceneIds=data.get("seedSceneIds") or [],
            limit=data.get("limit"),
            offset=int(data.get("offset") or 0),
        )
