"""Service registration, forwarding to the SDK."""
from __future__ import annotations

from stash_ai.services import register_service


class _Services:
    def register(self, service=None, *, name: str = "", max_concurrent: int = 1,
                 base_url: str = "", **_kwargs) -> None:
        resolved = name or getattr(service, "name", "")
        if not resolved:
            raise ValueError("a service needs a name")
        register_service(
            resolved,
            max_concurrency=int(getattr(service, "max_concurrency", max_concurrent)),
            server_url=base_url or getattr(service, "server_url", "") or "",
        )


services = _Services()
