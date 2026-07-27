"""Running plugin handlers, with progress and cancellation.

Handlers run on worker threads rather than the GLib main loop: a plugin doing
real work would otherwise block every other call on the connection, including
the Cancel that is meant to stop it.
"""
from __future__ import annotations

import json
import logging
import threading
import traceback
from concurrent.futures import ThreadPoolExecutor

import gi

gi.require_version("GLib", "2.0")
from gi.repository import GLib  # noqa: E402

_log = logging.getLogger("stash_ai_host.dispatch")

ERR_NOT_FOUND = "dev.stash.ai.Error.NotFound"
ERR_FAILED = "dev.stash.ai.Error.HandlerFailed"
ERR_CANCELLED = "dev.stash.ai.Error.Cancelled"


class CancelToken:
    """What a handler polls to notice it should stop."""

    def __init__(self) -> None:
        self._event = threading.Event()

    def request(self) -> None:
        self._event.set()

    def is_cancelled(self) -> bool:
        return self._event.is_set()

    def wait(self, timeout: float | None = None) -> bool:
        return self._event.wait(timeout)


class Invocation:
    """One running handler.

    Carries the OWNING PLUGIN, not the action id. Everything a handler does
    through the SDK - reading settings, querying its tables, submitting child
    tasks - is attributed to a plugin, and by the time a handler runs the loader
    has finished importing it, so the plugin cannot be read from import state.
    """

    def __init__(self, invocation_id: str, plugin: str, target: str):
        self.id = invocation_id
        self.plugin = plugin
        self.target = target
        self.cancel = CancelToken()


class Dispatcher:
    def __init__(self, registry, max_workers: int = 8):
        self.registry = registry
        self._pool = ThreadPoolExecutor(
            max_workers=max_workers, thread_name_prefix="ai-plugin"
        )
        self._lock = threading.Lock()
        self._active: dict[str, Invocation] = {}

    def invoke_action(self, conn, invocation, invocation_id, action_id, context, params):
        handler = self.registry.action_handler(action_id)
        if handler is None:
            invocation.return_dbus_error(ERR_NOT_FOUND, f"unknown action {action_id}")
            return
        plugin = self.registry.owner_of("action", action_id)
        self._run(invocation, invocation_id, plugin, action_id, handler, (context, params))

    def invoke_recommender(self, conn, invocation, invocation_id, rec_id, request):
        handler = self.registry.recommender_handler(rec_id)
        if handler is None:
            invocation.return_dbus_error(ERR_NOT_FOUND, f"unknown recommender {rec_id}")
            return
        plugin = self.registry.owner_of("recommender", rec_id)
        self._run(invocation, invocation_id, plugin, rec_id, handler, (request,))

    def handle_http(self, conn, invocation, request_id, plugin, method, path, query,
                    headers, body):
        """Serve a request a plugin registered a route for.

        Runs on a worker thread like any other handler: a plugin route that
        does real work must not block the connection the rest of the host
        shares.
        """
        from stash_ai.http import Request, Response

        handler, params = self.registry.match_route(plugin, method, path)
        if handler is None:
            status = 405 if params is not None else 404
            reason = "method not allowed" if status == 405 else "no such route"
            self._reply_http(invocation, status, {}, json.dumps({"detail": reason}).encode())
            return

        request = Request(method, path, query, headers, body)
        request.params = params

        record = Invocation(request_id, plugin, f"http {method} {path}")

        def work() -> None:
            from stash_ai import runtime as sdk_runtime

            try:
                sdk_runtime.bind_invocation(record)
                result = handler(request)

                if isinstance(result, Response):
                    response = result
                elif isinstance(result, tuple) and len(result) == 2:
                    response = Response(status=result[0], body=result[1])
                else:
                    response = Response(body=result)

                self._reply_http(invocation, response.status, response.headers, response.body)

            except Exception as exc:  # noqa: BLE001
                _log.exception("plugin route %s %s failed", method, path)
                # A plugin's exception becomes a 500 with its text, not a
                # transport error: the caller is an HTTP client and should get
                # an HTTP answer.
                payload = json.dumps({"detail": repr(exc)}).encode()
                self._reply_http(invocation, 500, {}, payload)
            finally:
                sdk_runtime.unbind_invocation()

        self._pool.submit(work)

    def _reply_http(self, invocation, status: int, headers: dict, body: bytes) -> None:
        import base64

        encoded = base64.b64encode(body or b"").decode("ascii")

        def emit() -> bool:
            invocation.return_value(
                GLib.Variant("(qss)", (int(status), json.dumps(headers or {}), encoded))
            )
            return False

        GLib.idle_add(emit)

    def _run(self, invocation, invocation_id, plugin, target, handler, args) -> None:
        record = Invocation(invocation_id, plugin, target)
        with self._lock:
            self._active[invocation_id] = record

        def work() -> None:
            from stash_ai import runtime as sdk_runtime

            try:
                sdk_runtime.bind_invocation(record)
                result = handler(*args)
                payload = json.dumps(result if result is not None else None)
                self._reply(invocation, payload)

            except Exception as exc:  # noqa: BLE001
                _log.exception("handler for %s failed", target)
                if record.cancel.is_cancelled():
                    self._reply_error(invocation, ERR_CANCELLED, "cancelled")
                else:
                    self._reply_error(
                        invocation,
                        ERR_FAILED,
                        f"{exc!r}\n{traceback.format_exc()}",
                    )
            finally:
                sdk_runtime.unbind_invocation()
                with self._lock:
                    self._active.pop(invocation_id, None)

        self._pool.submit(work)

    # Replies are marshalled back onto the main loop: GDBus is not safe to call
    # concurrently from worker threads.
    def _reply(self, invocation, payload: str) -> None:
        def emit() -> bool:
            invocation.return_value(GLib.Variant("(s)", (payload,)))
            return False

        GLib.idle_add(emit)

    def _reply_error(self, invocation, code: str, message: str) -> None:
        def emit() -> bool:
            invocation.return_dbus_error(code, message)
            return False

        GLib.idle_add(emit)

    def cancel(self, invocation_id: str) -> None:
        with self._lock:
            record = self._active.get(invocation_id)
        if record is not None:
            record.cancel.request()
            _log.info("cancellation requested for %s", invocation_id)

    def cancel_all(self) -> None:
        with self._lock:
            records = list(self._active.values())
        for record in records:
            record.cancel.request()
