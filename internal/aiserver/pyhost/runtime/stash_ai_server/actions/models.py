"""Action context models, mirroring the shapes the frontend sends."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class ContextInput:
    page: str = ""
    entity_id: str | None = None
    is_detail_view: bool = False
    selected_ids: list | None = None
    visible_ids: list | None = None

    @classmethod
    def from_dict(cls, data: dict) -> "ContextInput":
        # Accept both the camelCase the frontend sends and the snake_case a
        # plugin might construct by hand.
        return cls(
            page=data.get("page", ""),
            entity_id=data.get("entityId", data.get("entity_id")),
            is_detail_view=bool(data.get("isDetailView", data.get("is_detail_view", False))),
            selected_ids=data.get("selectedIds", data.get("selected_ids")),
            visible_ids=data.get("visibleIds", data.get("visible_ids")),
        )


@dataclass
class ContextRule:
    pages: list = field(default_factory=list)
    selection: str = "both"

    def to_dict(self) -> dict:
        return {"pages": self.pages, "selection": self.selection}
