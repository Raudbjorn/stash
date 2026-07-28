"""Database access for plugins.

Plugins no longer open the database themselves. Go owns the file, and letting a
second engine write it concurrently is not safe, so queries are proxied.

The surface is deliberately narrow: one statement per call, parameters always
bound rather than interpolated, and a row cap - a plugin doing SELECT * on a
large table would otherwise try to marshal the whole thing across the wire.
"""
from __future__ import annotations

from contextlib import contextmanager

from . import runtime


def query(sql: str, params: list | None = None) -> list[dict]:
    """Run a read query and return rows as dictionaries."""
    return runtime.bridge().query(runtime.plugin_name(), sql, params or [])


def execute(sql: str, params: list | None = None) -> int:
    """Run a write statement and return the number of rows affected."""
    return runtime.bridge().execute(runtime.plugin_name(), sql, params or [])


@contextmanager
def batch():
    """Group writes into one transaction and one round trip.

    Per-row round trips are the obvious performance trap with a proxied
    database, so plugin authors are given the alternative rather than left to
    discover the problem. The whole batch commits or none of it does.
    """
    statements: list[tuple[str, list]] = []

    class Batch:
        def execute(self, sql: str, params: list | None = None) -> None:
            statements.append((sql, params or []))

    collector = Batch()
    # No try/finally: an exception inside the block must abandon the batch
    # rather than commit a half-built one.
    yield collector

    if statements:
        runtime.bridge().execute_batch(runtime.plugin_name(), statements)
