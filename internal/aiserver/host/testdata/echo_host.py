#!/usr/bin/env python3
"""A minimal stand-in for the plugin host, used to exercise supervision.

It performs the real startup contract - read the token from the passed file
descriptor, bind nothing, print the AIHOST-READY line - and then behaves however
the test asks, so crash handling, backoff and restart caps can be tested without
the D-Bus transport or any plugin code.

Behaviours are selected with --behave:
  serve        stay alive until signalled (the normal case)
  crash-now    exit non-zero immediately, before reporting ready
  crash-after  report ready, then exit non-zero shortly after
  no-ready     stay alive but never report ready (readiness timeout)
  bad-ready    print a malformed readiness line
  no-token     exit if the token was not delivered on the expected fd
  noisy        report ready, write to stderr, then exit non-zero
"""
from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import time

READY_PREFIX = "AIHOST-READY "
TOKEN_FD = 3


def read_token(fd: int) -> str | None:
    try:
        with os.fdopen(fd, "r") as handle:
            return handle.readline().strip()
    except Exception:
        return None


def report_ready(address: str) -> None:
    print(
        READY_PREFIX
        + json.dumps(
            {
                "address": address,
                "pid": os.getpid(),
                "protocol": 1,
                "python": ".".join(str(p) for p in sys.version_info[:3]),
                "gi": "test-echo",
            }
        ),
        flush=True,
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--address", required=True)
    parser.add_argument("--token-fd", type=int, default=TOKEN_FD)
    parser.add_argument("--plugins-dir", default="")
    parser.add_argument("--log-level", default="info")
    parser.add_argument("--behave", default=os.environ.get("ECHO_BEHAVE", "serve"))
    args = parser.parse_args()

    behave = args.behave

    if behave == "crash-now":
        print("exiting before reporting ready", file=sys.stderr, flush=True)
        return 17

    token = read_token(args.token_fd)

    if behave == "no-token":
        if not token:
            print("no token delivered", file=sys.stderr, flush=True)
            return 78
        # Echo it so the test can assert it arrived off-argv.
        print(f"token-received:{token}", file=sys.stderr, flush=True)

    if behave == "no-ready":
        # Stay alive without ever reporting; the supervisor should time out.
        while True:
            time.sleep(0.5)

    if behave == "bad-ready":
        print(READY_PREFIX + "{not json", flush=True)
        while True:
            time.sleep(0.5)

    if behave == "noisy":
        report_ready(args.address)
        for i in range(10):
            print(f"noisy line {i}", file=sys.stderr, flush=True)
        print("Traceback (most recent call last):", file=sys.stderr, flush=True)
        print("RuntimeError: simulated plugin failure", file=sys.stderr, flush=True)
        return 9

    report_ready(args.address)

    if behave == "crash-after":
        time.sleep(0.2)
        print("exiting after reporting ready", file=sys.stderr, flush=True)
        return 23

    # serve: run until signalled.
    running = True

    def stop(_signum, _frame):
        nonlocal running
        running = False

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    while running:
        time.sleep(0.05)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
