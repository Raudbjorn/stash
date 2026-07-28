"""RemoteServiceBase: an HTTP client for a plugin's inference server.

This is one of the few pieces that stays pure Python. It talks to a remote
server the plugin owns, which has nothing to do with Stash, so proxying it
through Go would add a hop for no benefit.
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request

# Two hours: a single request may be an entire video analysis.
DEFAULT_TIMEOUT = 7200
CONNECT_TIMEOUT = 10
USER_AGENT = "stash-ai-server-plugin/1.0"


class ServiceBase:
    name: str = ""
    max_concurrency: int = 1


class RemoteServiceBase(ServiceBase):
    """A service backed by an HTTP inference server."""

    server_url: str | None = None
    ready_endpoint: str = "/ready"
    request_timeout: int = DEFAULT_TIMEOUT

    def _url(self, path: str) -> str:
        if path.startswith("http://") or path.startswith("https://"):
            return path
        base = (self.server_url or "").rstrip("/")
        if not base:
            raise RuntimeError(f"service {self.name!r} has no server_url")
        return base + (path if path.startswith("/") else "/" + path)

    def request(self, method: str, path: str = "", *, body=None, headers=None,
                timeout: int | None = None):
        payload = None
        request_headers = {"User-Agent": USER_AGENT}
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        request_headers.update(headers or {})

        request = urllib.request.Request(
            self._url(path), data=payload, headers=request_headers, method=method
        )
        with urllib.request.urlopen(request, timeout=timeout or self.request_timeout) as resp:
            raw = resp.read()
        if not raw:
            return None
        try:
            return json.loads(raw)
        except ValueError:
            return raw

    def get(self, path: str = "", **kwargs):
        return self.request("GET", path, **kwargs)

    def post(self, path: str = "", **kwargs):
        return self.request("POST", path, **kwargs)

    async def ensure_remote_ready(self, *, force: bool = False) -> bool:
        """Probe the remote server.

        Stash gates the whole service's queue on readiness as well, with its own
        caching and backoff; this exists for plugins that check explicitly.
        """
        if not self.server_url:
            return True
        try:
            self.request("GET", self.ready_endpoint, timeout=CONNECT_TIMEOUT)
            return True
        except (urllib.error.URLError, OSError, RuntimeError):
            return False
