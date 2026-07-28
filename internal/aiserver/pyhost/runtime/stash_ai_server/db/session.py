"""The database session surface plugins used to import.

Go owns the database file now. A second engine writing it concurrently is not
safe, so a real SQLAlchemy session cannot be handed out - this fails with an
actionable message rather than an import error, which is the difference between
a plugin author knowing what to change and guessing.

An audit of the official catalog found exactly one real plugin file reaching for
this (personalized_tfidf); everything else uses the higher-level helpers that
now proxy cleanly.
"""
from __future__ import annotations

from stash_ai_server._errors import unsupported


def get_session():
    raise unsupported("a direct SQLAlchemy session", "stash_ai.db.query / execute")


def get_db():
    """FastAPI-style dependency.

    Returns a lightweight facade rather than raising, because several plugins
    declare `db: Session = Depends(get_db)` as a type hint and never touch it.
    Raising here would break them for no reason.
    """
    return _SessionFacade()


class _SessionFacade:
    """Fails only if actually used to query."""

    def execute(self, *_args, **_kwargs):
        raise unsupported("SQLAlchemy execute", "stash_ai.db.query / execute")

    def query(self, *_args, **_kwargs):
        raise unsupported("SQLAlchemy query", "stash_ai.db.query")

    def add(self, *_args, **_kwargs):
        raise unsupported("SQLAlchemy add", "stash_ai.db.execute")

    def commit(self) -> None:
        """No-op: each proxied statement is already committed."""

    def close(self) -> None:
        """No-op."""
