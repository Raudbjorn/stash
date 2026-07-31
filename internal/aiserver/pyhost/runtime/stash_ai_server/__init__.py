"""Compatibility shim for plugins written against the standalone server.

Existing catalog plugins import from `stash_ai_server.*` - the package name of
the Python server this replaces. Rather than require every plugin to be rewritten
on day one, those import paths still resolve, but the implementations now
marshal to Go over the Bridge instead of doing the work locally.

What genuinely cannot be preserved fails with a specific, actionable message
rather than an import traceback:

  * Direct SQLAlchemy sessions. Go owns the database file and a second engine
    writing it concurrently is not safe. Use stash_ai.db instead.
  * FastAPI routers. Routing lives in Go's chi mux now.

An audit of the official catalog found this affects exactly one real plugin file
(personalized_tfidf), so the shim covers the ecosystem with one contained patch
rather than a rewrite.
"""

__all__ = ["__version__"]

__version__ = "0.9.3"
