"""Path rewriting, now a no-op.

The standalone server rewrote Stash's stored file paths because it ran in a
different container with different mounts. In-process the paths are already
correct, so this passes through unchanged.
"""
from __future__ import annotations


def mutate_path_for_plugin(path: str, *_args, **_kwargs) -> str:
    return path
