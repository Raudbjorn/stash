"""What plugins have registered.

Go is authoritative for "what actions exist": it takes a snapshot of this after
every load and serves the REST API from its own copy. That is what lets the
action and recommendation endpoints keep answering while this process is
restarting.
"""
from __future__ import annotations

import threading
from typing import Any, Callable


class Registry:
    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._services: dict[str, dict] = {}
        self._actions: dict[str, dict] = {}
        self._recommenders: dict[str, dict] = {}
        self._routes: list[dict] = []
        self._handlers: dict[str, Callable] = {}
        self._owners: dict[str, str] = {}

    # -------------------------------------------------------- registration --

    def add_service(self, plugin: str, name: str, **fields: Any) -> None:
        with self._lock:
            self._services[name] = {
                "name": name,
                "plugin": plugin,
                "max_concurrency": int(fields.get("max_concurrency", 1)),
                "server_url": fields.get("server_url") or "",
            }
            self._owners[f"service:{name}"] = plugin

    def add_action(self, plugin: str, definition: dict, handler: Callable) -> None:
        action_id = definition["id"]
        with self._lock:
            definition = dict(definition)
            definition.setdefault("service", "")
            definition.setdefault("result_kind", "none")
            definition.setdefault("deduplicate_submissions", True)
            definition["plugin"] = plugin
            self._actions[action_id] = definition
            self._handlers[f"action:{action_id}"] = handler
            self._owners[f"action:{action_id}"] = plugin

    def add_route(self, plugin: str, path: str, methods: list, pattern, handler) -> None:
        with self._lock:
            self._routes.append({
                "plugin": plugin,
                "path": path,
                "methods": methods,
                "pattern": pattern,
                "handler": handler,
            })
            self._owners[f"route:{plugin}:{path}"] = plugin

    def match_route(self, plugin: str, method: str, path: str):
        """Find the handler for a request, and the parameters it captured.

        Returns (handler, params) or (None, None). A path that matches but with
        the wrong verb is reported separately so the caller can answer 405
        rather than 404 - the difference tells a plugin author whether their
        route registered at all.
        """
        matched_path = False
        with self._lock:
            routes = list(self._routes)

        for entry in routes:
            if entry["plugin"] != plugin:
                continue
            match = entry["pattern"].match(path)
            if not match:
                continue
            matched_path = True
            if method.upper() in entry["methods"]:
                return entry["handler"], match.groupdict()

        return (None, {} if matched_path else None)

    def add_recommender(self, plugin: str, definition: dict, handler: Callable) -> None:
        rec_id = definition["id"]
        with self._lock:
            definition = dict(definition)
            definition["plugin"] = plugin
            self._recommenders[rec_id] = definition
            self._handlers[f"recommender:{rec_id}"] = handler
            self._owners[f"recommender:{rec_id}"] = plugin

    # ------------------------------------------------------------- lookup ---

    def action_handler(self, action_id: str) -> Callable | None:
        with self._lock:
            return self._handlers.get(f"action:{action_id}")

    def recommender_handler(self, rec_id: str) -> Callable | None:
        with self._lock:
            return self._handlers.get(f"recommender:{rec_id}")

    def owner_of(self, kind: str, name: str) -> str:
        """Which plugin registered something.

        The dispatcher needs this at invocation time: a handler calling
        db.query() has to be attributed to its plugin, and by then the loader
        has long since finished importing it.
        """
        with self._lock:
            return self._owners.get(f"{kind}:{name}", "")

    def unregister_plugin(self, plugin: str) -> None:
        with self._lock:
            for key, owner in list(self._owners.items()):
                if owner != plugin:
                    continue
                kind, _, name = key.partition(":")
                if kind == "service":
                    self._services.pop(name, None)
                elif kind == "action":
                    self._actions.pop(name, None)
                elif kind == "recommender":
                    self._recommenders.pop(name, None)
                elif kind == "route":
                    self._routes = [r for r in self._routes if r["plugin"] != plugin]
                self._handlers.pop(key, None)
                self._owners.pop(key, None)

    def snapshot(self) -> dict:
        """Everything Go needs to serve the API without asking again."""
        with self._lock:
            return {
                "services": list(self._services.values()),
                "actions": list(self._actions.values()),
                "recommenders": list(self._recommenders.values()),
                # Patterns and handlers stay here; Go only needs to know which
                # plugins serve routes so it can decide whether to proxy.
                "routes": [
                    {"plugin": r["plugin"], "path": r["path"], "methods": r["methods"]}
                    for r in self._routes
                ],
            }
