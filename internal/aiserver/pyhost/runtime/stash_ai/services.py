"""Declaring services."""
from __future__ import annotations

from . import runtime


def register_service(name: str, *, max_concurrency: int = 1, server_url: str = "") -> None:
    """Declare a service, which groups actions and bounds their concurrency."""
    runtime.registry().add_service(
        runtime.plugin_name(),
        name,
        max_concurrency=max_concurrency,
        server_url=server_url,
    )
