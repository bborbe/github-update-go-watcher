---
status: completed
tags:
    - dark-factory
    - spec
approved: "2026-07-11T21:12:30Z"
generating: "2026-07-11T21:48:41Z"
prompted: "2026-07-11T21:52:23Z"
verifying: "2026-07-11T22:08:42Z"
completed: "2026-07-11T22:10:01Z"
branch: dark-factory/update-go-mod-deps
---

## Summary

- Update the Go module dependencies in `go.mod` to their latest compatible versions.
- Run `go get -u ./...` followed by `go mod tidy`, then refresh the vendored/checksum state so the build stays reproducible.
- Scope is dependency version bumps only — no source-code changes, no API migrations.

## Problem

Dependencies in `go.mod` drift behind upstream over time, accumulating missed bug fixes and security patches. Keeping them current is routine maintenance that should be a single mechanical pass, verified by the existing build.

## Goal

`go.mod` (and `go.sum`) reference the latest compatible versions of all direct dependencies, `go mod tidy` reports a clean state, and `make precommit` passes with no code changes required.

## Non-goals

- Major-version upgrades that require code migration (e.g. `v1` → `v2` import path changes).
- Adding or removing dependencies.
- Any change to application source code, tests, or configuration beyond `go.mod` / `go.sum`.
- Changing the Go language version in `go.mod`.

## Desired Behavior

1. Run `go get -u ./...` to bump direct (and transitively required) dependencies to their latest compatible minor/patch versions.
2. Run `go mod tidy` to prune unused requirements and reconcile `go.sum`.
3. If the repo vendors dependencies, run `go mod vendor` to refresh the vendor tree; otherwise skip.
4. Leave application source untouched — only `go.mod`, `go.sum` (and `vendor/` if present) change.
5. If a dependency's latest version introduces a compile break, pin that one dependency at the highest version that still builds and note it; do not modify source to accommodate it.

## Constraints

- No source-code edits outside `go.mod` / `go.sum` / `vendor/`.
- No major-version bumps requiring import-path changes.
- The Go directive version in `go.mod` is unchanged.
- `make precommit` must pass (format + lint + test + security).

## Acceptance Criteria

- [ ] `go get -u ./...` and `go mod tidy` have been run; `go.mod` / `go.sum` reflect latest compatible versions.
- [ ] `go mod tidy` leaves no pending changes (idempotent second run is a no-op).
- [ ] No files other than `go.mod`, `go.sum`, and `vendor/` (if vendored) are modified.
- [ ] `make precommit` passes.

## Verification

```
go mod tidy
git diff --name-only        # only go.mod, go.sum (and vendor/) appear
make precommit
```

## Do-Nothing Option

Dependencies stay where they are. The build keeps working today, but the module drifts further behind upstream, so the eventual catch-up upgrade is larger and riskier. For a github-update-go-watcher/template repo the cost is low, which is exactly why this is a safe fixture for exercising the pipeline end-to-end.
