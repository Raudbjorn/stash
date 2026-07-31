"""Plugin HTTP routes.

A plugin can serve its own endpoints under /api/v1/plugins/<prefix>/. Stash
owns the socket and the routing table; this only decides what a matched request
returns, which is the same split the rest of the SDK follows.

Handlers are ordinary functions taking a request and returning a response. There
is no framework here on purpose: the Python server used FastAPI routers, but
importing FastAPI into the host just to describe three endpoints would drag in a
web stack the host has no other use for.
"""
from __future__ import annotations

import json
import re
from typing import Any, Callable

from . import runtime


class Request:
    """One HTTP request routed to a plugin."""

    def __init__(self, method: str, path: str, query: str, headers: dict, body: bytes):
        self.method = method
        self.path = path
        self.query = query
        self.headers = headers
        self.body = body
        # Filled in by the router when the pattern has named segments.
        self.params: dict[str, str] = {}

    def json(self) -> Any:
        """Decode the body as JSON, or None when there is none."""
        if not self.body:
            return None
        return json.loads(self.body.decode("utf-8"))

    def query_params(self) -> dict[str, list[str]]:
        """Parse the query string into a mapping of name to values."""
        from urllib.parse import parse_qs

        return parse_qs(self.query)


class Response:
    """What a plugin route returns."""

    def __init__(self, status: int = 200, body: Any = None, headers: dict | None = None,
                 content_type: str = "application/json"):
        self.status = status
        self.headers = dict(headers or {})

        if isinstance(body, (bytes, bytearray)):
            self.body = bytes(body)
        elif body is None:
            self.body = b""
        elif isinstance(body, str):
            self.body = body.encode("utf-8")
        else:
            self.body = json.dumps(body).encode("utf-8")
            content_type = "application/json"

        self.headers.setdefault("Content-Type", content_type)


# Path patterns use {name} for a segment, which is what plugin authors already
# write in FastAPI decorators.
_PARAM = re.compile(r"\{([A-Za-z_][A-Za-z0-9_]*)\}")


def _compile(pattern: str) -> re.Pattern:
    escaped = re.escape(pattern)
    # re.escape leaves braces alone in modern Python, but the pattern text has
    # been escaped, so the parameter syntax is matched in its escaped form too.
    escaped = escaped.replace(r"\{", "{").replace(r"\}", "}")
    regex = _PARAM.sub(lambda m: f"(?P<{m.group(1)}>[^/]+)", escaped)
    return re.compile("^" + regex + "$")


def route(path: str, methods: list[str] | None = None):
    """Register a handler for an HTTP path within the plugin's own prefix.

    The path is relative to /api/v1/plugins/<plugin>/, so a plugin cannot
    register a route outside its own namespace - unlike the FastAPI routers this
    replaces, where a plugin chose its own mount point and could shadow the
    server's endpoints.
    """
    normalised = "/" + path.strip("/")
    verbs = [m.upper() for m in (methods or ["GET"])]

    def wrapper(fn: Callable[[Request], Any]):
        runtime.registry().add_route(
            runtime.plugin_name(), normalised, verbs, _compile(normalised), fn
        )
        return fn

    return wrapper
