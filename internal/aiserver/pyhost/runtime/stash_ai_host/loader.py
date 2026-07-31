"""Importing plugin modules and collecting what they register.

This is the irreducible part of the host: everything else about a plugin -
parsing its manifest, resolving dependencies, downloading it, storing its
settings - happens in Go. Only executing its code has to happen here.

Compared with the standalone server's loader this is much smaller, because it no
longer owns the catalog, pip, migrations, HTTP routing or the database.
"""
from __future__ import annotations

import importlib
import importlib.util
import logging
import sys
import traceback
from typing import Any

from .dispatch import Dispatcher
from .registry import Registry

_log = logging.getLogger("stash_ai_host.loader")

# Plugin modules are imported under a synthetic package so their names cannot
# collide with anything installed, and so unloading can find them again.
NAMESPACE = "stash_ai_plugins"


class PluginLoader:
    """Loads plugin modules and tracks what they registered."""

    def __init__(self, plugins_dir: str = ""):
        self.plugins_dir = plugins_dir
        self.registry = Registry()
        self.dispatcher = Dispatcher(self.registry)
        self._loaded: dict[str, list[str]] = {}

    def loaded_names(self) -> list[str]:
        return sorted(self._loaded)

    def load(
        self, name: str, directory: str, manifest: dict[str, Any], bridge
    ) -> tuple[str, str, dict]:
        """Import a plugin's modules and return (status, error, registry).

        A failure is reported rather than raised: one broken plugin must not
        prevent the others loading, and the reason needs to reach the UI.
        """
        if name in self._loaded:
            self.unload(name)

        # The SDK is configured before any plugin code runs, so a module-level
        # registration during import already has somewhere to go.
        from stash_ai import runtime as sdk_runtime

        sdk_runtime.configure(
            plugin=name,
            registry=self.registry,
            bridge=bridge,
            dispatcher=self.dispatcher,
            settings=manifest.get("settings") or [],
        )

        files = manifest.get("files") or []
        if not files:
            # A plugin with no declared modules still gets a package import, so
            # the common single-module layout works without ceremony.
            files = ["plugin"]

        imported: list[str] = []
        if directory and directory not in sys.path:
            sys.path.insert(0, directory)

        try:
            for module_name in files:
                full = f"{NAMESPACE}.{name}.{module_name}"
                spec_path = f"{directory}/{module_name}.py"

                spec = importlib.util.spec_from_file_location(full, spec_path)
                if spec is None or spec.loader is None:
                    raise ImportError(f"cannot import {spec_path}")

                module = importlib.util.module_from_spec(spec)
                sys.modules[full] = module
                spec.loader.exec_module(module)
                imported.append(full)

                # A plugin may expose register() for explicit setup rather than
                # relying on import side effects.
                register = getattr(module, "register", None)
                if callable(register):
                    register()

        except Exception as exc:  # noqa: BLE001
            _log.exception("failed to load plugin %s", name)
            for full in imported:
                sys.modules.pop(full, None)
            self.registry.unregister_plugin(name)
            sdk_runtime.reset()
            return "error", f"{exc!r}\n{traceback.format_exc()}", self.registry_snapshot()

        finally:
            sdk_runtime.reset()

        self._loaded[name] = imported
        _log.info("loaded plugin %s (%d modules)", name, len(imported))
        return "ok", "", self.registry_snapshot()

    def unload(self, name: str) -> None:
        """Drop a plugin's registrations and modules.

        The caveats are the same as they always were: threads a module started
        keep running, and C-extension globals are not reset. That is why the
        supervisor prefers recycling the whole host for anything important - it
        holds no durable state, so throwing it away is always correct.
        """
        for full in self._loaded.pop(name, []):
            module = sys.modules.pop(full, None)
            unregister = getattr(module, "unregister", None)
            if callable(unregister):
                try:
                    unregister()
                except Exception:
                    _log.exception("error unregistering %s", full)

        self.registry.unregister_plugin(name)
        importlib.invalidate_caches()
        _log.info("unloaded plugin %s", name)

    def registry_snapshot(self) -> dict:
        return self.registry.snapshot()
