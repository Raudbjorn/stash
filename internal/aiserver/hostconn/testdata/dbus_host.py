#!/usr/bin/env python3
"""Phase 0.2 spike: the Python half of the plugin host transport.

Stands up a peer-to-peer GDBus server on a unix socket with NO session bus,
exports Host1/Dispatch1, calls back into a Go-exported Bridge1 on the same
connection, and emits a signal. Prints AIHOST-READY on stdout when listening.
"""
from __future__ import annotations

import json
import sys
import uuid

import gi

gi.require_version("Gio", "2.0")
gi.require_version("GLib", "2.0")
from gi.repository import Gio, GLib  # noqa: E402

INTERFACES = """
<node>
  <interface name='dev.stash.ai.Host1'>
    <method name='Hello'>
      <arg type='s' name='token' direction='in'/>
      <arg type='u' name='protocol' direction='in'/>
      <arg type='s' name='host_version' direction='out'/>
      <arg type='u' name='protocol' direction='out'/>
    </method>
    <method name='Ping'>
      <arg type='t' name='monotonic_ns' direction='out'/>
    </method>
    <method name='CallBridge'>
      <arg type='s' name='sql' direction='in'/>
      <arg type='s' name='rows_json' direction='out'/>
    </method>
    <method name='EmitProgress'>
      <arg type='s' name='invocation_id' direction='in'/>
      <arg type='d' name='fraction' direction='in'/>
    </method>
    <signal name='Progress'>
      <arg type='s' name='invocation_id'/>
      <arg type='d' name='fraction'/>
    </signal>
  </interface>
</node>
"""

PROTOCOL = 1
OBJECT_PATH = "/dev/stash/ai/Host"
IFACE = "dev.stash.ai.Host1"


class Host:
    def __init__(self, address: str, token: str) -> None:
        self.address = address
        self.token = token
        self.authed = False
        self.loop = GLib.MainLoop()
        self.node_info = Gio.DBusNodeInfo.new_for_xml(INTERFACES)
        self.connections: list[Gio.DBusConnection] = []

        # Empty observer => EXTERNAL auth only (no ANONYMOUS).
        self.server = Gio.DBusServer.new_sync(
            address,
            Gio.DBusServerFlags.NONE,
            str(uuid.uuid4()).replace("-", ""),
            None,
            None,
        )
        self.server.connect("new-connection", self._on_new_connection)

    def _on_new_connection(self, _server, conn: Gio.DBusConnection) -> bool:
        conn.set_exit_on_close(False)
        self.connections.append(conn)
        # register_object is deprecated in PyGObject 3.56; the closures2 variant
        # is the supported path and is what the real host must use.
        conn.register_object_with_closures2(
            OBJECT_PATH,
            self.node_info.lookup_interface(IFACE),
            self._on_method_call,
            None,
            None,
        )
        return True  # keep the connection

    def _on_method_call(
        self, conn, _sender, _path, _iface, method, params, invocation
    ) -> None:
        try:
            if method == "Hello":
                token, protocol = params.unpack()
                if token != self.token:
                    invocation.return_dbus_error(
                        "dev.stash.ai.Error.BadToken", "token mismatch"
                    )
                    return
                if protocol != PROTOCOL:
                    invocation.return_dbus_error(
                        "dev.stash.ai.Error.BadProtocol", f"want {PROTOCOL}"
                    )
                    return
                self.authed = True
                invocation.return_value(GLib.Variant("(su)", ("spike-1.0", PROTOCOL)))

            elif method == "Ping":
                invocation.return_value(
                    GLib.Variant("(t)", (GLib.get_monotonic_time() * 1000,))
                )

            elif method == "CallBridge":
                # Reverse direction: call an object Go exported on THIS
                # connection, with no destination (peer-to-peer).
                (sql,) = params.unpack()
                result = conn.call_sync(
                    None,  # no destination
                    "/dev/stash/ai/Bridge",
                    "dev.stash.ai.Bridge1",
                    "Query",
                    GLib.Variant("(ss)", (sql, "[]")),
                    GLib.VariantType("(s)"),
                    Gio.DBusCallFlags.NONE,
                    5000,
                    None,
                )
                invocation.return_value(GLib.Variant("(s)", (result.unpack()[0],)))

            elif method == "EmitProgress":
                inv_id, fraction = params.unpack()
                conn.emit_signal(
                    None,  # broadcast to the peer
                    OBJECT_PATH,
                    IFACE,
                    "Progress",
                    GLib.Variant("(sd)", (inv_id, fraction)),
                )
                invocation.return_value(None)

            else:
                invocation.return_dbus_error(
                    "dev.stash.ai.Error.UnknownMethod", method
                )
        except Exception as exc:  # noqa: BLE001 - spike: report everything
            invocation.return_dbus_error("dev.stash.ai.Error.Internal", repr(exc))

    def run(self) -> None:
        self.server.start()
        print(
            "AIHOST-READY "
            + json.dumps(
                {
                    "address": self.server.get_client_address(),
                    "protocol": PROTOCOL,
                    "python": sys.version.split()[0],
                    "gi": gi.__version__,
                }
            ),
            flush=True,
        )
        self.loop.run()


def main() -> int:
    address = sys.argv[1]
    token = sys.argv[2]
    Host(address, token).run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
