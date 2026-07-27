"""AI result storage, proxied to Stash.

The schema is unchanged, so recommenders reading these results keep working
whether the detections came from a remote inference server or a native pipeline.
"""
from __future__ import annotations

import json

from stash_ai.db import query as _query
from stash_ai import runtime as _runtime


def get_scene_timespans(*, service: str, scene_id: int):
    """Return stored timespans bucketed by category then tag id."""
    rows = _query(
        """SELECT t.category, t.value_id, t.start_s, t.end_s, t.value_json
           FROM ai_result_timespans t
           JOIN ai_model_runs r ON r.id = t.run_id
           WHERE r.service = ? AND r.entity_type = 'scene' AND r.entity_id = ?
             AND t.payload_type = 'tag'
           ORDER BY t.category, t.value_id, t.start_s""",
        [service, int(scene_id)],
    )
    if not rows:
        return None

    buckets: dict = {}
    for row in rows:
        tag_id = row.get("value_id")
        if tag_id is None:
            continue
        payload = row.get("value_json")
        if isinstance(payload, str) and payload:
            try:
                payload = json.loads(payload)
            except ValueError:
                payload = {}
        payload = payload or {}

        entry = {
            "start": float(row.get("start_s") or 0.0),
            "end": float(row.get("end_s") if row.get("end_s") is not None else row.get("start_s") or 0.0),
            "confidence": payload.get("confidence"),
        }
        buckets.setdefault(row.get("category"), {}).setdefault(str(tag_id), []).append(entry)
    return buckets


async def get_scene_timespans_async(*, service: str, scene_id: int):
    return get_scene_timespans(service=service, scene_id=scene_id)


def get_scene_tag_totals(*, service: str, scene_id: int) -> dict:
    """Return total detected seconds per tag id."""
    rows = _query(
        """SELECT a.value_id AS tag_id, SUM(a.value_float) AS total
           FROM ai_result_aggregates a
           JOIN ai_model_runs r ON r.id = a.run_id
           WHERE r.service = ? AND r.entity_type = 'scene' AND r.entity_id = ?
             AND a.payload_type = 'tag' AND a.metric = 'duration_s'
             AND a.value_id IS NOT NULL
           GROUP BY a.value_id""",
        [service, int(scene_id)],
    )
    return {int(r["tag_id"]): float(r["total"] or 0.0) for r in rows}


def get_image_tag_ids(*, service: str, image_id: int) -> list:
    rows = _query(
        """SELECT DISTINCT a.value_id AS tag_id
           FROM ai_result_aggregates a
           JOIN ai_model_runs r ON r.id = a.run_id
           WHERE r.service = ? AND r.entity_type = 'image' AND r.entity_id = ?
             AND a.payload_type = 'tag' AND a.value_id IS NOT NULL
           ORDER BY a.value_id""",
        [service, int(image_id)],
    )
    return [int(r["tag_id"]) for r in rows]
