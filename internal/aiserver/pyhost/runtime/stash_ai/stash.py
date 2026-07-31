"""Access to Stash's own API.

The query is executed by Stash in-process, so no API key exists in this process
to be leaked or stolen - a strict improvement on the standalone server, which
was handed a shared key.
"""
from __future__ import annotations

from . import runtime


def graphql(query_text: str, variables: dict | None = None) -> dict:
    """Run a GraphQL query against Stash and return the decoded result."""
    return runtime.bridge().graphql(runtime.plugin_name(), query_text, variables or {})
