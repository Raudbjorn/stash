# packaging/archlinux

Arch Linux packaging for this repository's `stash` fork, as an in-tree
alternative to the AUR `stash` package (which builds a different fork).

## What's here

- `PKGBUILD` — builds and packages the `stash` binary plus the systemd
  integration files below.
- `stash.service` — system-wide unit (runs as the `stash` user, reads
  `/etc/conf.d/stash`, config at `/var/lib/stash/config.yaml`).
- `stash-user.service` — user-session unit, for running Stash under a
  regular user's systemd instance instead of system-wide.
- `stash.sysusers` — `systemd-sysusers` snippet that creates the `stash`
  system user and its home (`/var/lib/stash`).
- `stash.tmpfiles` — `systemd-tmpfiles` snippet that ensures
  `/var/lib/stash` exists with the right owner/mode.
- `stash.env` — default `/etc/conf.d/stash` environment file (host/port/
  external-host overrides), installed as a `backup=()` config so local
  edits survive upgrades.

## How the build works

This `PKGBUILD` is meant to be built **from within a checkout of this
repository** — it packages whatever commit is currently checked out, not
a fixed upstream release. Its `source` array points at the repo root via
`git+file://${startdir}/../..`, i.e. a fast local clone two directories
up, so:

- No network access to `git.s8n.is` is required to build.
- `pkgver()` derives a version from the clone's commit count and short
  hash (`rN.g<hash>`), since this fork carries no version tags.
- Building from a dirty working tree only picks up **committed**
  changes — `git clone` only sees what's been committed, not uncommitted
  edits.

The actual build follows the same steps as CI/the release Docker image
(`.forgejo/workflows/ci.yml`, `docker/compiler/Dockerfile`): `make
pre-ui` + `make generate` (codegen) in `prepare()`, then `make ui` +
`make build-release` (PIE release binary) in `build()`.

## Usage

```sh
cd packaging/archlinux
makepkg -si          # build + install, will prompt for sudo
# or, non-interactively once dependencies are already installed:
makepkg --nocheck --noconfirm --clean
sudo pacman -U stash-*.pkg.tar.zst
```

After install, enable/start the service:

```sh
sudo systemctl enable --now stash.service
```

Edit `/etc/conf.d/stash` to set `STASH_HOST`/`STASH_PORT`/
`STASH_EXTERNAL_HOST` if the defaults don't fit your setup, then
`sudo systemctl restart stash.service`.
