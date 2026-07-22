# Stash Desktop Client (Tauri)

Optional native desktop wrapper around the Stash web UI. Purely additive — the
normal `pnpm install && pnpm build` web workflow never touches Rust, and nothing
under `src/` imports this. Tauri only *consumes* the built `../build` output.

## Status

Already done: `package.json` has the `tauri`/`tauri:dev`/`tauri:build` scripts and
the `@tauri-apps/cli` + `@tauri-apps/plugin-http` deps (installed); icons are
generated in `icons/`; `pnpm tauri info` validates the config. The **only**
remaining prerequisite is a system library (below) — a full build/run needs it.

## Prerequisites (per platform)

- Rust stable via [rustup](https://rustup.rs) (`cargo`) — present.
- **Linux (this machine, Arch):** install `webkit2gtk-4.1` — currently MISSING.
  `sudo pacman -S webkit2gtk-4.1` (plus `gtk3`, `libsoup3` if not already present).
  `rsvg2` is already installed.
- **macOS:** Xcode Command Line Tools.
- **Windows:** WebView2 runtime, MSVC build tools, NSIS.

## Smoke test (Linux)

Once `webkit2gtk-4.1` is installed:
- `cargo check` inside `src-tauri/` (compiles Rust without bundling).
- `pnpm tauri:dev` — launches the web dev server and opens the 1280×800 window
  pointing at `http://localhost:3000`.

Config validity can be checked right now without the system lib: `pnpm tauri info`.

## Notes

- `bundle.targets` is `"all"` (Tauri picks the appropriate bundler per OS).
- `capabilities/default.json` intentionally has NO hardcoded server URL — the
  broad `http(s)://*` allowances cover reaching any Stash backend over HTTP.
