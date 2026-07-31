#!/bin/sh
# Provisions the shared Python venv used by stash's community scrapers/
# plugins (the interpreter pointed at by the `python_path` config option).
#
# Reproduces, as a script, what was done by hand on d3ll on 2026-07-27 -
# see README.md for the reasoning (why 3.13, why in place, why this exact
# package list).
#
# Usage (as the stash user, or via `sudo -u stash -H env HOME=<stash home>`):
#   VENV_PATH=/var/lib/stash/stsh ./provision.sh
#
# Requires `uv` (https://docs.astral.sh/uv/). Safe to re-run: backs up an
# existing venv before replacing it.

set -eu

VENV_PATH="${VENV_PATH:-/var/lib/stash/stsh}"
PYTHON_VERSION="${PYTHON_VERSION:-3.13}"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

# uv's config-file discovery walks up from the current directory; run from a
# neutral directory so it doesn't stumble into an unrelated/inaccessible
# uv.toml (observed when invoked from another user's home directory).
cd /tmp

echo "==> Installing Python ${PYTHON_VERSION} via uv"
uv python install "${PYTHON_VERSION}"

if [ -e "${VENV_PATH}" ]; then
    backup="${VENV_PATH}.bak.$(date +%Y%m%d)"
    echo "==> Backing up existing venv to ${backup}"
    cp -a "${VENV_PATH}" "${backup}"
    rm -rf "${VENV_PATH}"
fi

echo "==> Creating venv at ${VENV_PATH} (Python ${PYTHON_VERSION})"
uv venv --python "${PYTHON_VERSION}" "${VENV_PATH}"

echo "==> Installing dependencies"
uv pip install --python "${VENV_PATH}/bin/python" -r "${SCRIPT_DIR}/requirements.txt"

echo "==> Verifying key imports"
"${VENV_PATH}/bin/python" -c "
import lxml
import stashapi.log
import bs4
import requests
import mechanicalsoup
from algoliasearch.search.client import SearchClientSync
import pycountry
from fastbencode import bdecode
from fp.fp import FreeProxy
print('all imports ok')
"

echo "==> Done. python_path should point at: ${VENV_PATH}/bin/python"
