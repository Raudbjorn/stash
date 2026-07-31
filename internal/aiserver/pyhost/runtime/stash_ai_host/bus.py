"""Peer-to-peer D-Bus server and method dispatch.

The host is the LISTENER and Stash is the connecting peer. That direction is not
arbitrary: godbus ships only the client half of the SASL handshake, while GLib
ships both, so the listener has to be the side with the batteries.

No session bus is involved. Gio.DBusServer on a private unix socket gives a
bus-less, bidirectional connection, which was verified before any of this was
written.
"""
from __future__ import annotations

import hmac
import json
import logging
import os
import time
import uuid

import gi

gi.require_version("Gio", "2.0")
gi.require_version("GLib", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

_log = logging.getLogger("stash_ai_host.bus")

OBJECT_PATH = "/dev/stash/ai/Host"
IFACE_HOST = "dev.stash.ai.Host1"
IFACE_PLUGINS = "dev.stash.ai.Plugins1"
IFACE_DISPATCH = "dev.stash.ai.Dispatch1"

BRIDGE_PATH = "/dev/stash/ai/Bridge"
IFACE_BRIDGE = "dev.stash.ai.Bridge1"

# Errors are namespaced so the Go side can distinguish them from transport
# failures.
ERR_UNAUTHORIZED = "dev.stash.ai.Error.Unauthorized"
ERR_BAD_PROTOCOL = "dev.stash.ai.Error.BadProtocol"
ERR_UNKNOWN_METHOD = "dev.stash.ai.Error.UnknownMethod"
ERR_INTERNAL = "dev.stash.ai.Error.Internal"


def _load_interfaces() -> Gio.DBusNodeInfo:
    """Parse the IDL that ships beside this module."""
    path = os.path.join(os.path.dirname(__file__), "interfaces.xml")
    with open(path, "r", encoding="utf-8") as handle:
        return Gio.DBusNodeInfo.new_for_xml(handle.read())


class HostBus:
    """Serves the host interfaces over a private socket."""

    def __init__(self, *, address: str, token: str, protocol: int, loader, loop):
        self.address = address
        self._token = token
        self.protocol = protocol
        self.loader = loader
        self.loop = loop

        self._node = _load_interfaces()
        self._connections: list[Gio.DBusConnection] = []
        self._authenticated: set[int] = set()
        self._draining = False

        guid = uuid.uuid4().hex
        # An empty observer means EXTERNAL authentication only: the kernel
        # vouches for the peer's uid. ANONYMOUS is never offered.
        self._server = Gio.DBusServer.new_sync(
            address, Gio.DBusServerFlags.NONE, guid, None, None
        )
        self._server.connect("new-connection", self._on_new_connection)

        # Given to the dispatcher so plugins can call back into Stash.
        self.bridge = BridgeClient(self)

    @property
    def client_address(self) -> str:
        return self._server.get_client_address()

    def start(self) -> None:
        self._server.start()
        _log.info("host listening on %s", self.client_address)

    # ------------------------------------------------------------- wiring ---

    def _on_new_connection(self, _server, conn: Gio.DBusConnection) -> bool:
        conn.set_exit_on_close(False)
        self._connections.append(conn)
        conn.connect("closed", self._on_connection_closed)

        for iface in (IFACE_HOST, IFACE_PLUGINS, IFACE_DISPATCH):
            info = self._node.lookup_interface(iface)
            if info is None:
                _log.error("interface %s missing from the IDL", iface)
                continue
            # register_object is deprecated in PyGObject 3.56; the closures
            # variant is the supported path.
            conn.register_object_with_closures2(
                OBJECT_PATH, info, self._on_method_call, None, None
            )

        _log.debug("peer connected")
        return True

    def _on_connection_closed(self, conn, _remote_peer_vanished, _error) -> None:
        """Forget a peer that has gone.

        Without this a stale connection stays in the list and bridge calls are
        routed to a socket nobody is listening on.
        """
        self._authenticated.discard(id(conn))
        try:
            self._connections.remove(conn)
        except ValueError:
            pass
        _log.debug("peer disconnected")

    def _on_method_call(
        self, conn, _sender, _path, interface, method, params, invocation
    ) -> None:
        try:
            # Everything except the handshake requires authentication first.
            if not (interface == IFACE_HOST and method == "Hello"):
                if id(conn) not in self._authenticated:
                    invocation.return_dbus_error(
                        ERR_UNAUTHORIZED, "handshake required"
                    )
                    return

            handler = self._resolve(interface, method)
            if handler is None:
                invocation.return_dbus_error(
                    ERR_UNKNOWN_METHOD, f"{interface}.{method}"
                )
                return

            handler(conn, params, invocation)

        except Exception as exc:  # noqa: BLE001 - never let a plugin kill the loop
            _log.exception("error handling %s.%s", interface, method)
            invocation.return_dbus_error(ERR_INTERNAL, repr(exc))

    def _resolve(self, interface: str, method: str):
        table = {
            (IFACE_HOST, "Hello"): self._hello,
            (IFACE_HOST, "Ping"): self._ping,
            (IFACE_HOST, "GetInfo"): self._get_info,
            (IFACE_HOST, "SetLogLevel"): self._set_log_level,
            (IFACE_HOST, "Shutdown"): self._shutdown,
            (IFACE_PLUGINS, "Load"): self._load,
            (IFACE_PLUGINS, "Unload"): self._unload,
            (IFACE_PLUGINS, "List"): self._list,
            (IFACE_DISPATCH, "InvokeAction"): self._invoke_action,
            (IFACE_DISPATCH, "InvokeRecommender"): self._invoke_recommender,
            (IFACE_DISPATCH, "Cancel"): self._cancel,
            (IFACE_DISPATCH, "HandleHTTP"): self._handle_http,
        }
        return table.get((interface, method))

    # ------------------------------------------------------------- Host1 ----

    def _hello(self, conn, params, invocation) -> None:
        token, protocol = params.unpack()

        # Constant-time comparison keeps the local capability check honest.
        if not hmac.compare_digest(token, self._token):
            _log.warning("rejected a peer presenting a bad token")
            invocation.return_dbus_error(ERR_UNAUTHORIZED, "token mismatch")
            return
        if protocol != self.protocol:
            invocation.return_dbus_error(
                ERR_BAD_PROTOCOL, f"host speaks protocol {self.protocol}"
            )
            return

        self._authenticated.add(id(conn))
        invocation.return_value(GLib.Variant("(su)", ("1.0", self.protocol)))

    def _ping(self, _conn, _params, invocation) -> None:
        invocation.return_value(GLib.Variant("(t)", (time.monotonic_ns(),)))

    def _get_info(self, _conn, _params, invocation) -> None:
        import sys

        info = {
            "python": ".".join(str(p) for p in sys.version_info[:3]),
            "executable": sys.executable,
            "pid": os.getpid(),
            "gi": gi.__version__,
            "plugins_loaded": self.loader.loaded_names(),
            "draining": self._draining,
        }
        invocation.return_value(GLib.Variant("(s)", (json.dumps(info),)))

    def _set_log_level(self, _conn, params, invocation) -> None:
        (level,) = params.unpack()
        logging.getLogger("stash_ai_host").setLevel(level.upper())
        invocation.return_value(None)

    def _shutdown(self, _conn, params, invocation) -> None:
        (grace_ms,) = params.unpack()
        self._draining = True
        invocation.return_value(None)

        # Answer first, then wind down: the caller needs the reply before the
        # socket closes.
        GLib.timeout_add(max(1, int(grace_ms)), self._quit)

    def _quit(self) -> bool:
        _log.info("shutting down")
        self.loop.quit()
        return False

    # ----------------------------------------------------------- Plugins1 ---

    def _load(self, _conn, params, invocation) -> None:
        name, directory, manifest_json = params.unpack()
        manifest = json.loads(manifest_json) if manifest_json else {}

        status, error, registry = self.loader.load(name, directory, manifest, self.bridge)
        invocation.return_value(
            GLib.Variant("(sss)", (status, error, json.dumps(registry)))
        )

    def _unload(self, _conn, params, invocation) -> None:
        (name,) = params.unpack()
        self.loader.unload(name)
        invocation.return_value(None)

    def _list(self, _conn, _params, invocation) -> None:
        invocation.return_value(
            GLib.Variant("(s)", (json.dumps(self.loader.registry_snapshot()),))
        )

    # ---------------------------------------------------------- Dispatch1 ---

    def _invoke_action(self, conn, params, invocation) -> None:
        invocation_id, action_id, context_json, params_json = params.unpack()
        self.loader.dispatcher.invoke_action(
            conn,
            invocation,
            invocation_id,
            action_id,
            json.loads(context_json or "{}"),
            json.loads(params_json or "{}"),
        )

    def _invoke_recommender(self, conn, params, invocation) -> None:
        invocation_id, recommender_id, request_json = params.unpack()
        self.loader.dispatcher.invoke_recommender(
            conn,
            invocation,
            invocation_id,
            recommender_id,
            json.loads(request_json or "{}"),
        )

    def _handle_http(self, conn, params, invocation) -> None:
        import base64

        request_id, method, path, query, headers_json, body_b64 = params.unpack()

        # The plugin owning the route is the first path segment: routes are
        # mounted under /api/v1/plugins/<plugin>/, so a plugin cannot register
        # outside its own namespace.
        trimmed = path.strip("/")
        plugin, _, rest = trimmed.partition("/")

        self.loader.dispatcher.handle_http(
            conn,
            invocation,
            request_id,
            plugin,
            method,
            "/" + rest,
            query,
            json.loads(headers_json or "{}"),
            base64.b64decode(body_b64) if body_b64 else b"",
        )

    def _cancel(self, _conn, params, invocation) -> None:
        (invocation_id,) = params.unpack()
        self.loader.dispatcher.cancel(invocation_id)
        invocation.return_value(None)

    # ------------------------------------------------------------ signals ---

    def emit_progress(
        self, invocation_id: str, fraction: float, message: str, detail: dict | None
    ) -> None:
        payload = GLib.Variant(
            "(sdss)", (invocation_id, fraction, message, json.dumps(detail or {}))
        )
        for conn in list(self._connections):
            try:
                conn.emit_signal(None, OBJECT_PATH, IFACE_DISPATCH, "Progress", payload)
            except Exception:
                # A dead peer is the supervisor's problem, not ours.
                pass

    def call_bridge(self, method: str, signature: str, args: tuple, reply: str):
        """Call an object Stash exported on the same connection.

        Peer-to-peer means there is no destination name; the connection itself
        identifies the peer.
        """
        # Only an authenticated peer exports a Bridge; an unauthenticated one
        # has no object at that path and the call would fail confusingly.
        candidates = [c for c in self._connections if id(c) in self._authenticated]
        if not candidates:
            raise RuntimeError("no authenticated connection to Stash")
        conn = candidates[-1]
        result = conn.call_sync(
            None,
            BRIDGE_PATH,
            IFACE_BRIDGE,
            method,
            GLib.Variant(signature, args),
            GLib.VariantType(reply),
            Gio.DBusCallFlags.NONE,
            30000,
            None,
        )
        return result.unpack()


class BridgeClient:
    """Plugin-facing view of the services Stash exposes.

    Everything a plugin needs from Stash goes through here, which is what keeps
    credentials out of this process entirely.
    """

    def __init__(self, bus: HostBus):
        self._bus = bus

    def query(self, plugin: str, sql: str, params: list | None = None) -> list[dict]:
        (rows_json,) = self._bus.call_bridge(
            "Query", "(sss)", (plugin, sql, json.dumps(params or [])), "(s)"
        )
        return json.loads(rows_json)

    def execute(self, plugin: str, sql: str, params: list | None = None) -> int:
        (affected,) = self._bus.call_bridge(
            "Execute", "(sss)", (plugin, sql, json.dumps(params or [])), "(t)"
        )
        return int(affected)

    def execute_batch(self, plugin: str, statements: list) -> int:
        """Run several statements in one transaction and one round trip."""
        payload = [
            {"sql": sql, "params": params or []} for sql, params in statements
        ]
        (affected,) = self._bus.call_bridge(
            "ExecuteBatch", "(ss)", (plugin, json.dumps(payload)), "(t)"
        )
        return int(affected)

    def mark_controller(self, plugin: str, invocation_id: str) -> None:
        """Declare the running task a coordinator, releasing its slot."""
        self._bus.call_bridge(
            "MarkController", "(ss)", (plugin, invocation_id), "()"
        )

    def get_settings(self, plugin: str) -> dict:
        (settings_json,) = self._bus.call_bridge(
            "GetSettings", "(s)", (plugin,), "(s)"
        )
        return json.loads(settings_json)

    def set_setting(self, plugin: str, key: str, value) -> None:
        self._bus.call_bridge(
            "SetSetting", "(sss)", (plugin, key, json.dumps(value)), "()"
        )

    def graphql(self, plugin: str, query: str, variables: dict | None = None) -> dict:
        (result_json,) = self._bus.call_bridge(
            "StashGraphQL", "(sss)", (plugin, query, json.dumps(variables or {})), "(s)"
        )
        return json.loads(result_json)

    def submit_task(
        self,
        plugin: str,
        action_id: str,
        context: dict,
        params: dict,
        parent_invocation_id: str = "",
        priority: str = "normal",
    ) -> str:
        (invocation_id,) = self._bus.call_bridge(
            "SubmitTask",
            "(ssssss)",
            (
                plugin,
                action_id,
                json.dumps(context),
                json.dumps(params),
                parent_invocation_id,
                priority,
            ),
            "(s)",
        )
        return invocation_id

    def emit_progress(
        self, invocation_id: str, fraction: float, message: str = "", detail=None
    ) -> None:
        self._bus.emit_progress(invocation_id, fraction, message, detail)
