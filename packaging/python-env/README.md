# packaging/python-env

Reproducible provisioning for the shared Python venv that stash's
community scrapers/plugins run under (the interpreter configured as
`python_path` in Settings/`config.yaml`). This isn't part of the Arch
package build (`packaging/archlinux/`) — it's a separate, host-level
runtime dependency that this directory documents and scripts rather than
leaving as ad hoc shell history.

## Why this exists

On 2026-07-27, community scrapers started failing with missing-module
errors (`lxml`, then later `mechanicalsoup` and others) under the venv
that had been in place. The fix was to rebuild the venv on a newer Python
with a verified-working package set. This directory captures that as a
script + pinned requirements file instead of one-off SSH commands, so it
can be re-run and reasoned about later.

## Why Python 3.13, not 3.12

Stash's own migration guidance called for "3.12 or newer." 3.13 was
picked specifically, not just "newer than 3.11":

`plugins/community/PythonToolsInstaller/PythonToolsInstaller.py` (a
vendored community plugin, not code in this repo) installs
`stashapp-tools` by creating a throwaway venv with whatever interpreter
is running the script, then copies its site-packages into the configured
interpreter's own site-packages. The copy source path is **hardcoded**:

```python
src = f"{used_dir}/venv/lib/python3.13/site-packages"
```

If `python_path` points at anything other than 3.13, that copy step
looks for a directory that doesn't exist and fails. Using 3.13 sidesteps
this landmine entirely rather than requiring a patch to vendored
third-party plugin code.

## Why the venv is replaced in place

`python_path` in `config.yaml` conventionally points at
`/var/lib/stash/stsh/bin/python`. The venv directory contents get
replaced (with a timestamped backup first, matching the
`stsh.bak.YYYYMMDD` convention already present on the host from prior
admin work) rather than provisioning a new venv at a new path and
repointing the config - this keeps the config value stable across
re-provisions.

**Gotcha:** deleting the live venv directory while `stash.service` is
still running on the *old* process triggers stash's own "resolve
`python_path` via `$PATH` if the configured value becomes invalid"
fallback - which silently rewrites `config.yaml` to point at the system
Python instead. `provision.sh` doesn't stop the service itself (that's
an operational decision, not something to bake into a provisioning
script), so **stop `stash.service` before running this**, or otherwise be
prepared to re-check `python_path` in `config.yaml` afterward and reset
it explicitly if it changed underneath you.

## Package list

`requirements.txt` reconstructs the full previously-working package set:
mostly consolidated from the individual `requirements.txt` files shipped
by scrapers under `scrapers/community/*/` and plugins under
`plugins/community/*/`, plus a handful of packages that scrapers import
directly with **no accompanying requirements.txt at all** (see the
comments in that file for the current known list - `mechanicalsoup`,
`algoliasearch`, `pycountry`, `fastbencode`, `free-proxy`). Those only
ever surface as a runtime `ImportError` from the specific scraper that
needs them, so if another one turns up, add it to `requirements.txt`
with a comment noting which scraper needed it.

Deliberately excluded: `plugins/community/LocalVisage`, which manages
its **own** dedicated venv (`LocalVisage/venv/`) with heavy ML
dependencies (torch, tensorflow, gradio, deepface) - its
`requirements.txt` says as much ("Don't install this manually"). Never
fold those into the shared venv.

## Known follow-up (not yet addressed)

`scrapers/community/stash-sqlite/stash-sqlite.py` imports the stdlib
`imghdr` module, which Python 3.13 removed entirely (PEP 594). That
scraper will fail under this venv if it's actually invoked. Not fixed
here since it requires patching vendored scraper source and it's unclear
whether that scraper is in active use - flagging it for whoever picks
this up next.

## Usage

```sh
sudo systemctl stop stash.service
sudo -u stash -H env HOME=/var/lib/stash sh packaging/python-env/provision.sh
sudo systemctl start stash.service
# then confirm python_path in config.yaml still points at the venv
```
