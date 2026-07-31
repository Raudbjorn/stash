"""Logging for the host.

Everything goes to stderr, which Stash pumps into its own log and keeps a tail
of for crash reports. Nothing is written to stdout: that channel carries the
readiness handshake and must stay clean.
"""
from __future__ import annotations

import logging
import sys

_LEVELS = {
    "trace": logging.DEBUG,
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warn": logging.WARNING,
    "warning": logging.WARNING,
    "error": logging.ERROR,
}


def configure_logging(level: str = "info") -> None:
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(logging.Formatter("%(levelname)s %(name)s: %(message)s"))

    root = logging.getLogger("stash_ai_host")
    root.handlers.clear()
    root.addHandler(handler)
    root.setLevel(_LEVELS.get(level.lower(), logging.INFO))
    root.propagate = False

    # Plugins log through their own namespace; route it the same way.
    plugin_root = logging.getLogger("stash_ai")
    plugin_root.handlers.clear()
    plugin_root.addHandler(handler)
    plugin_root.setLevel(root.level)
    plugin_root.propagate = False
