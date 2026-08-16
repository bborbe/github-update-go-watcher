---
status: completed
spec: [001-update-go-mod-deps]
summary: Bumped all Go module dependencies to latest compatible versions via go get -u ./... + go mod tidy; go.mod/go.sum updated with no source changes
execution_id: github-update-go-watcher-update-deps-exec-005-spec-001-update-go-mod-deps
dark-factory-version: v0.191.4
created: "2026-07-11T21:50:00Z"
queued: "2026-07-11T21:54:04Z"
started: "2026-07-11T22:06:34Z"
completed: "2026-07-11T22:08:42Z"
branch: dark-factory/update-go-mod-deps
---

<summary>
- Bumps all Go module dependencies in `go.mod`/`go.sum` to their latest compatible minor/patch versions
- No application source, test, or config changes — dependency versions only
- Leaves the Go language directive in `go.mod` unchanged
- Does not perform major-version (v1→v2) upgrades that would need import-path changes
- Skips vendoring — this repo is not vendored
- Confirms the module is clean and the full build/lint/test/security suite still passes
- If a single dependency's newest version breaks the build, that one dependency is held at its highest building version and the hold is reported
</summary>

<objective>
Bring every direct (and transitively required) dependency in `go.mod`/`go.sum` up to its
latest compatible version so the module stops drifting behind upstream, with the existing
build proving correctness and zero source-code changes.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` entry format.

Facts about this repo (verified):
- Module: `github.com/bborbe/github-update-go-watcher`, Go directive `go 1.26.5` in `/workspace/go.mod`.
- The repo is NOT vendored. There is no `vendor/` directory, and `make precommit`'s
  `ensure` target explicitly runs `rm -rf vendor`. Therefore do NOT run `go mod vendor`.
- `make precommit` expands to `ensure format generate test check addlicense`
  (see `/workspace/Makefile.precommit` line 5). The `ensure` target itself runs
  `go mod tidy` then `go mod verify`, so precommit will fail loudly if `go.sum` is stale.
- `make precommit` regenerates mocks (`generate` target does `rm -rf mocks` then
  `go generate ./...`). Regenerated mock churn under `/workspace/mocks/` is expected and
  acceptable output of the update; it is NOT a hand-edited source change.

Spec 001 — Desired Behavior (verbatim):
1. Run `go get -u ./...` to bump direct (and transitively required) dependencies to their
   latest compatible minor/patch versions.
2. Run `go mod tidy` to prune unused requirements and reconcile `go.sum`.
3. If the repo vendors dependencies, run `go mod vendor`; otherwise skip. (This repo does
   NOT vendor — skip.)
4. Leave application source untouched — only `go.mod`, `go.sum` change.
5. If a dependency's latest version introduces a compile break, pin that one dependency at
   the highest version that still builds and note it; do not modify source to accommodate it.
</context>

<requirements>
1. From `/workspace`, run `go get -u ./...` to bump direct and transitively required
   dependencies to their latest compatible minor/patch versions.
2. From `/workspace`, run `go mod tidy` to prune unused requirements and reconcile `go.sum`.
3. Do NOT run `go mod vendor` — this repo has no `vendor/` directory and `make precommit`
   deletes any that appears.
4. Do NOT change the `go 1.26.5` directive line in `/workspace/go.mod`. If `go get -u` or
   `go mod tidy` raises it, revert only that line back to `go 1.26.5` before proceeding.
5. Do NOT perform major-version upgrades that require import-path changes (e.g. a module
   moving from `/v2` to `/v3`). `go get -u ./...` will not do this automatically; if any tool
   suggests it, skip that bump and leave the module at its current major version.
6. Make NO edits to application source, tests, or configuration. The only intended file
   changes are `/workspace/go.mod` and `/workspace/go.sum`. (Mock regeneration under
   `/workspace/mocks/` performed by `make precommit`'s `generate` target is a build artifact,
   not a source edit, and is acceptable.)
7. Failure handling — compile break from a new dependency version: if the latest version of
   a single dependency breaks the build (compile error or test failure that is clearly a
   version incompatibility, not a flake), pin that one dependency at the highest version that
   still builds using `go get <module>@<version>`, re-run `go mod tidy`, and record the held
   dependency and the version you pinned it to in the completion report and in the
   `## Unreleased` changelog entry. Do NOT modify any source file to accommodate a new
   dependency version — pinning is the only permitted remedy.
8. Failure handling — `go mod tidy` not idempotent: after step 2, run `go mod tidy` a second
   time. It must be a no-op (no further changes to `go.mod`/`go.sum`). If the second run
   produces changes, keep running `go mod tidy` until it stabilizes.
9. Add a `## Unreleased` entry to `/workspace/CHANGELOG.md` following
   `changelog-guide.md`. Use prefix `chore:`. Example:
   `- chore: Update Go module dependencies to latest compatible versions via go get -u ./... + go mod tidy`.
   If a dependency was held back per requirement 7, add a second bullet naming it and the
   pinned version, e.g. `- chore: Pin <module> at <version> (latest breaks build)`.
   If `## Unreleased` already exists, append to it rather than replacing it.
</requirements>

<constraints>
- No source-code edits outside `go.mod` / `go.sum` (this repo is not vendored, so no `vendor/`).
- No major-version bumps requiring import-path changes.
- The Go directive version in `go.mod` (`go 1.26.5`) is unchanged.
- `make precommit` must pass (format + lint + test + security).
- Do NOT commit — dark-factory handles git.
- Do NOT add features or refactor — this is a dependency-only update.
</constraints>

<verification>
Run from `/workspace`:

1. `go mod tidy` — second run must be a no-op (no changes to `go.mod`/`go.sum`).
2. `git diff --name-only` — expect ONLY `go.mod`, `go.sum`, and `CHANGELOG.md` (plus any
   regenerated files under `mocks/` produced by `make precommit`'s `generate` target).
   No hand-edited `.go` source, test, or config files may appear.
3. `make precommit` — must exit 0.

If `make precommit` exits non-zero, the run is `failed` (per the completion protocol) unless
the failure was resolved by pinning a single dependency per requirement 7 and precommit then
passes on re-run.
</verification>

<!--
OPEN QUESTIONS (surfaced for human reviewer; resolved in-prompt with the safest default):
- The spec's Desired Behavior step 3 conditionally runs `go mod vendor`. This repo is NOT
  vendored (no vendor/ dir; `make precommit` does `rm -rf vendor`), so the step is skipped.
  If the repo is later vendored, this prompt must be updated to add `go mod vendor`.
- Prior completed prompt `002-update-go-deps.md` used an `updater go` CLI instead of raw
  `go get -u ./...`. The spec explicitly prescribes `go get -u ./...` + `go mod tidy`, so this
  prompt follows the spec's mechanical steps rather than the `updater` tool.
-->
