# CLAUDE.md — stash (curated superfork)

## What this repo is

A **private, heavily-curated fork** of [stashapp/stash](https://github.com/stashapp/stash) — Go backend + React (TypeScript) frontend, GraphQL API. It is **not** vanilla upstream:

- Go module path is still `github.com/stashapp/stash` (upstream path retained); the real remote is **private** (`origin` → `git.s8n.is`). **Do not push or open PRs without explicit approval.**
- History is squashed to a single `initial` commit; primary branch is `feature/clips`. There is **no shared git history with upstream or community forks** → integrating outside changes means **hand-porting patches, not cherry-pick/merge**.
- It has already absorbed many community features (first-class Clips entity, favorites, autotag prefix-index + regexp cache, O-counter hook, scraper 429 backoff, ffmpeg stream limiter, `slowSeek` sprite/phash retry, HW-accel preview/decode). Before "adding" a feature, grep — it may already exist.

## Build / test / codegen

Backend (Go 1.25):
- `make build` — build the `stash` binary.
- `make generate` — full codegen: `generate-backend` (gqlgen via `go generate ./cmd/stash`) + `generate-ui` (`pnpm run gqlgen` in `ui/v2.5`). Run after any `graphql/**/*.graphql` change.
- `make fmt` · `make lint` · `make test` (unit) · `make it` (integration).
- Narrow iteration: `go build ./...`, `go vet ./pkg/...`, `go test ./pkg/<pkg>/...`.

Frontend (`cd ui/v2.5`, pnpm):
- `pnpm run start` (vite dev) · `pnpm run build`.
- `pnpm run check` (tsc `--noEmit`) · `pnpm run lint` (biome + stylelint) · `pnpm run validate` (lint + check + format-check) · `pnpm run format`.

## Architecture map

- `cmd/` entrypoints · `internal/api` GraphQL resolvers + HTTP routes · `internal/manager` jobs/tasks (generate, autotag, scan) · `pkg/models` domain types + interfaces · `pkg/sqlite` DB layer + migrations · `pkg/ffmpeg` transcode/codec/HW-accel · `pkg/match` + `internal/autotag` auto-tagging · `pkg/scene|image|gallery` services.
- Frontend `ui/v2.5/src`: `components/` (feature dirs), `models/list-filter` (filter criteria), `hooks/`, `core/` (generated GraphQL + client), `locales/` (i18n; `en-GB.json` is the source of truth).

## Critical conventions & gotchas (learned the hard way)

- **Filter/model input structs are HAND-WRITTEN** in `pkg/models/*.go` (e.g. `SceneFilterType` in `scene.go`); gqlgen *binds* to them. When adding a schema input field, add the struct field **by hand first**, then run codegen — otherwise gqlgen fails with a misleading `*Resolver does not implement ResolverRoot (missing method XFilterType)` because a schema field has no model field.
- **Generated files are gitignored** and regenerated, never committed: `internal/api/generated_exec.go`, `ui/v2.5/src/core/generated-graphql.ts`. Don't hand-edit them; don't try to commit them.
- **Codegen bootstrap order**: gqlgen typechecks the whole module, so it fails if any package references a not-yet-generated field. Order = edit schema + hand-written model fields → (temporarily neutralize any code referencing new generated fields if needed) → `make generate-backend` → restore → `make generate-ui`.
- **DB migrations**: `pkg/sqlite/migrations/NN_*.up.sql`, sequential; bump `appSchemaVersion` in `pkg/sqlite/database.go` to match the highest migration. Add favorite-style boolean columns as `ALTER TABLE ... ADD COLUMN x boolean not null default '0';`. Do not use ad-hoc startup `ALTER TABLE` (no `fork_schema.go` pattern).
- **i18n**: new UI labels need keys in `ui/v2.5/src/locales/en-GB.json` (alphabetically placed) or they render as the raw key.
- **HW-accel** (`pkg/ffmpeg`): `HardwareDecodeArgs` injects `-hwaccel auto` (opportunistic; ffmpeg falls back to software). Real GPU/NVENC behavior needs a live run to verify — build/tests don't exercise it.
- Verify a nontrivial change by building/typechecking the affected side, then running the narrowest relevant tests. When a schema changes, regenerate before building or the build will reference stale generated types.

## How to work here (operating rules)

**Lead with the answer** — conclusion, complete code, patch, or command first; then evidence, assumptions, material trade-offs, and uncertainty when they affect the decision. No sycophantic openings, no empty praise.

**Evidence over assertion.** Read the relevant code/config and use tools before asserting facts. Never fabricate tool results, source support, verification, or completion. State plainly which work was **performed vs verified vs inferred vs unverified**. Never call an unrun or failing gate "passing"; report unavailable, skipped, pre-existing, newly-introduced, and conflicting failures **separately**.

**Smallest complete change at the root cause** (KISS/YAGNI). No speculative abstractions or unrequested scope. Preserve unrelated user work and existing conventions — don't reformat/rewrite adjacent code without need.

**Verification proportional to risk.** Low-risk/reversible: proceed. Elevated (persistent behavior change, cross-component, public API, >5 files): state the key assumption + primary failure mode + mitigation. High-risk (destructive ops, secrets/auth/privacy, irreversible migrations, breaking changes, >10 files): verify assumptions first, warn clearly, give rollback steps, and **confirm before irreversible production actions**. Never fabricate; never expose secrets.

**Be a direct collaborator.** Correct material errors and false premises immediately and plainly; distinguish confidence from certainty; admit your own errors and state the corrected conclusion. Don't manufacture disagreement or agree to be agreeable — the goal is a better engineering decision, not winning.

**Ask sparingly.** Only when tools + context can't resolve a material trade-off; then state the conflict and ask for the one missing decision. Otherwise make a grounded best effort.

**Precedence** when rules conflict: current user task intent → repo instructions & pinned versions → scoped file/platform rules → global defaults. At the same level, the most recent specific instruction wins. Project conventions override style/tool defaults — never safety or integrity rules.

## Fork-integration scope

- VR/HereSphere/DeoVR integration is deferred — do not pull VR features from forks for now.
