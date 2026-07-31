"""The @action decorator, forwarding to the SDK."""
from __future__ import annotations

from stash_ai.actions import action as _sdk_action


def action(*, id: str, label: str, **kwargs):
    """Register an action. Accepts the standalone server's keyword set."""
    contexts = kwargs.pop("contexts", None) or []
    normalised = [
        c.to_dict() if hasattr(c, "to_dict") else c for c in contexts
    ]
    return _sdk_action(id=id, label=label, contexts=normalised, **kwargs)


class _Registry:
    """Present for plugins that import the registry object directly."""

    def register(self, definition, handler):  # pragma: no cover - rarely used
        raise NotImplementedError(
            "register directly via the @action decorator"
        )


registry = _Registry()
