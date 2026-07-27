"""Stash access for plugins.

The standalone server kept a GraphQL client here and was handed a shared API
key. In-process the query is executed by Stash itself, so no key exists in this
process at all.
"""
from __future__ import annotations

from stash_ai.stash import graphql


class _StashAPI:
    """The subset of the old client that plugins actually used."""

    def graphql(self, query: str, variables: dict | None = None) -> dict:
        return graphql(query, variables or {})

    def fetch_tag_id(self, name: str, create_if_missing: bool = False):
        result = self.graphql(
            "query FindTags($f: String) { findTags(filter: {q: $f}) { tags { id name } } }",
            {"f": name},
        )
        for tag in (result.get("data", {}).get("findTags", {}) or {}).get("tags", []) or []:
            if tag.get("name", "").lower() == name.lower():
                return int(tag["id"])

        if not create_if_missing:
            return None

        created = self.graphql(
            "mutation TagCreate($n: String!) { tagCreate(input: {name: $n}) { id } }",
            {"n": name},
        )
        tag = (created.get("data", {}) or {}).get("tagCreate")
        return int(tag["id"]) if tag else None

    def add_tags_to_scene(self, scene_id: int, tag_ids: list) -> None:
        self.graphql(
            "mutation SceneUpdate($id: ID!, $tags: [ID!]) "
            "{ sceneUpdate(input: {id: $id, tag_ids: $tags}) { id } }",
            {"id": str(scene_id), "tags": [str(t) for t in tag_ids]},
        )


stash_api = _StashAPI()
