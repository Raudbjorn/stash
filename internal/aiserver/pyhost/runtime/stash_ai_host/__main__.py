#!/usr/bin/env python3
"""Entry point for the Stash AI plugin host.

Stash spawns this, hands it a shared token on a file descriptor, and connects
over a private peer-to-peer D-Bus socket. Everything that needs to execute
plugin code happens here; everything else - manifests, the catalog, planning,
settings storage - stays in Go, which is why most of the plugin API keeps
working even when this process is down.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import threading

import gi

gi.require_version("Gio", "2.0")
gi.require_version("GLib", "2.0")
from gi.repository import GLib  # noqa: E402

from .bus import HostBus  # noqa: E402
from .loader import PluginLoader  # noqa: E402
from .logging_setup import configure_logging  # noqa: E402

PROTOCOL = 1
READY_PREFIX = "AIHOST-READY "


def read_token(fd: int) -> str:
    """Read the shared secret from the descriptor Stash passed it on.

    Deliberately not argv or the environment: both are readable by other local
    processes, and this token gates access to the Bridge.
    """
    with os.fdopen(fd, "r", closefd=True) as handle:
        return handle.readline().strip()


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="stash_ai_host")
    parser.add_argument("--address", required=True, help="D-Bus address to listen on")
    parser.add_argument("--token-fd", type=int, required=True)
    parser.add_argument("--plugins-dir", default="")
    parser.add_argument("--log-level", default="info")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv if argv is not None else sys.argv[1:])
    configure_logging(args.log_level)

    try:
        token = read_token(args.token_fd)
    except Exception as exc:
        print(f"could not read host token: {exc!r}", file=sys.stderr, flush=True)
        return 78

    if not token:
        print("empty host token", file=sys.stderr, flush=True)
        return 78

    loop = GLib.MainLoop()
    loader = PluginLoader(args.plugins_dir)

    try:
        bus = HostBus(
            address=args.address,
            token=token,
            protocol=PROTOCOL,
            loader=loader,
            loop=loop,
        )
        bus.start()
    except Exception as exc:
        print(f"could not start host bus: {exc!r}", file=sys.stderr, flush=True)
        return 70

    # Only announce readiness once the socket is actually accepting, so Stash
    # never races the bind.
    print(
        READY_PREFIX
        + json.dumps(
            {
                "address": bus.client_address,
                "pid": os.getpid(),
                "protocol": PROTOCOL,
                "python": ".".join(str(p) for p in sys.version_info[:3]),
                "gi": gi.__version__,
            }
        ),
        flush=True,
    )

    # A dying parent must not leave this process orphaned holding the socket.
    threading.Thread(target=_exit_when_stdin_closes, args=(loop,), daemon=True).start()

    try:
        loop.run()
    except KeyboardInterrupt:
        pass
    return 0


def _exit_when_stdin_closes(loop: GLib.MainLoop) -> None:
    """Quit when Stash goes away.

    Stash holds the write end of our stdin; if it exits without stopping us
    cleanly, the read returns EOF and we shut down rather than lingering.
    """
    try:
        sys.stdin.read()
    except Exception:
        pass
    loop.quit()


if __name__ == "__main__":
    raise SystemExit(main())
