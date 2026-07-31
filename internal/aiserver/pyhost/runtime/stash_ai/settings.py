"""Plugin settings, stored by Stash."""
from __future__ import annotations

from . import runtime


def get_settings() -> dict:
    """Return every setting for this plugin, as key to effective value."""
    return runtime.bridge().get_settings(runtime.plugin_name())


def get_setting(key: str, default=None):
    """Return one setting's effective value."""
    return get_settings().get(key, default)


def set_setting(key: str, value) -> None:
    """Store a setting value."""
    runtime.bridge().set_setting(runtime.plugin_name(), key, value)
