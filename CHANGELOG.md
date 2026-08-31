# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

- feat: add a decision-task pathway for undecided-consent repos — an `auto_update_undecided` skip now additionally publishes one repo-keyed decision task (`DeriveDecisionTaskID`, `BuildDecisionCommand`, `TaskPublisher.PublishDecision`) with type `github-update-go-decision` assigned to `bborbe`, whose body tells the owner how to opt in/out via `goUpdate.autoUpdate` in `.maintainer.yaml`; identity derives from owner/repo only (never the HEAD SHA), so re-emission every cycle is a downstream no-op and a failed publish never fails the poll cycle
- feat: rename `Candidate.AutoUpdate bool` to `Candidate.Consent filter.Consent` and make `filter.NewAutoUpdateFilter` a tri-state gate — explicit `autoUpdate: false` still yields `auto_update_disabled`, while absent/undecided consent (and any invalid value) now yields a distinct `auto_update_undecided` reason (fails closed); `GoBehindFilter` still short-circuits repos already current on Go before the consent verdict is ever reached
- feat: Add tri-state `filter.Consent` (`granted`/`refused`/`undecided`) and `filter.ParseConsent` — `GetMaintainerConfig` now returns `filter.Consent` instead of `maintainerconfig.MaintainerConfig`, walking `.maintainer.yaml` as a raw yaml.Node so an absent or non-boolean `goUpdate.autoUpdate` no longer collapses with an explicit `false`
- feat: pre-initialise the `auto_update_undecided` skip-reason series on `filter_skipped_total` — `pkg.FilterSkipReasons` now includes it, so `NewMetrics` exposes `filter_skipped_total{reason="auto_update_undecided"}=0` from process start like every other skip reason, closing out the decision-task pathway (spec 002); `pkg/watcher_test` derives its label set from `pkg.FilterSkipReasons` instead of a second hardcoded mirror, and README's skip-reasons table/trust-gate text reflect the tri-state (`auto_update_disabled` is now the explicit-`false` case only) with a new "Decision task contract" section

## v0.4.1

- fix: detect merged update PRs via `merged_at`, not `merged` — the GitHub list API returns `merged: null` even for merged PRs (only the detail view populates it), so `GetMerged()` always read false and the merge-detection pass silently skipped every merged PR; checking `merged_at` (populated on list responses) makes complete-task actually fire

## v0.4.0

- feat: opt into `autoMerge.trivial` — mechanically trivial update PRs (go.mod / Dockerfile / CHANGELOG / workflow bumps) get the auto-merge label from github-pr-watcher, so GitHub-native auto-merge lands them once checks + review are green

## v0.3.0

- feat: merge-detection auto-completes update-go vault tasks — the watcher publishes a `complete-task` command (same pattern as github-build-watcher's red→green closure) when an emitted task's update PR (`fix/update-go-<sha>`) has merged, so the controller flips the task `human_review` → `done` without a manual close-sweep

## v0.2.3

- chore: update github.com/bborbe/agent to v0.83.0, github.com/bborbe/cqrs to v0.6.8, github.com/bborbe/errors to v1.5.21, github.com/bborbe/http to v1.26.24, github.com/bborbe/kafka to v1.25.9, github.com/bborbe/log to v1.6.25, github.com/bborbe/maintainer to v0.50.2, github.com/bborbe/metrics to v0.5.15, github.com/bborbe/run to v1.9.37, github.com/bborbe/sentry to v1.9.27, github.com/bborbe/service to v1.10.9, github.com/bborbe/time to v1.27.10

## v0.2.2

- chore: update Go to 1.27.0

## v0.2.1

- chore: Make build tooling compatible with Go 1.27 — run `gofmt -w` after golines in the `format` target so golines' wrapping is normalized before the gofmt lint check, bump golangci-lint to v2.13.1 (fixes staticcheck `buildir` panic), and bump errcheck to v1.20.0 (fixes `package "context" without types`)

## v0.2.0

- feat: emit an optional `update_scope` field (`golang` | `deps`) on every emitted task from the `UPDATE_SCOPE` env, so a fleet-wide sweep can be scoped Go-only or deps-only. Unset = omitted, agent defaults to `both` (byte-identical to before).

## v0.1.1

- fix(build): publish semver-tagged images and drop the in-repo k8s manifests. The scaffold's `Makefile.docker` tagged by `$(BRANCH)` (`:dev`/`:prod`) and `Makefile.k8s` applied `k8s/*.yaml` with `kubectl` — neither fits how this unit is actually deployed, which is a pinned OCI Helm chart (`watcher.tag: v0.1.0`) mirrored into the quant registry, matching the three sibling watchers. A branch-tagged image cannot be pinned by version, and the `k8s/` manifests would have deployed a second unmanaged copy alongside the Helm release. Also passes `BUILD_GIT_VERSION` to the build, which the sibling's Makefile omits despite the Dockerfile consuming it for `org.opencontainers.image.version`
- fix(build): bump the Dockerfile build stage `golang:1.26.5` → `golang:1.26.6` to match `go.mod`. Same defect that made github-releaser-agent v0.4.1 unbuildable — `make precommit` compiles with the host toolchain, so only the container pins a Go version and the drift is invisible until an operator runs `make buca`

## v0.1.0

- feat: add pkg/githubclient GitHubClient interface backed by go-github/v84, with ListRepos (paginated), GetHeadSHA, GetGoMod (1 MiB cap), and GetMaintainerConfig; ErrRateLimited sentinel for rate-limit signals
- feat: add pkg/auth ResolveGitHubClient for GitHub App installation token resolution with partial-config validation (PEM never logged)
- feat: add pkg/filter decision chain (RepoAllowlistFilter, GoModPresentFilter, GoModParsableFilter, GoBehindFilter, AutoUpdateFilter, SHAUnchangedFilter) with TaskCreationFilter interface and Candidate type
- feat: add pkg/cursor for persisted-memory LoadCursor/SaveCursor with atomic write via temp file + rename
- feat: add pkg/cursorreader adapter bridging Cursor to filter.CursorReader without import cycle
- feat: add pkg/candidate (per-repo observation), pkg/taskbuilder (frozen CreateTaskCommand contract), pkg/taskpublisher (send + counter), and pkg/watcher (full scan cycle orchestration)
- feat: wire watcher into binary with env binding, poll loop, single-cycle lock, HTTP surface (health, readiness, metrics, log level, forced cycle), and remove scaffold key-value-store endpoints
- feat: add POST /trigger endpoint for forced cycle with exactly-one-cycle gate
- feat: add pkg/cyclegate for non-blocking single-cycle enforcement shared by timer and endpoint
- refactor: move all composition into pkg/factory, remove libboltkv/libkv imports, remove /resetdb and /resetbucket endpoints
- chore: update Makefile run target flags and example.env
- docs: rewrite README with environment variables, metrics, skip reasons, and forced-emit verification procedure
- fix: run the forced cycle through run.BackgroundRunner + run.CatchPanic instead of a raw goroutine, so a panic in Poll no longer takes down the process
- feat: add paired ParseGoReleaseDefault, ParseGoDirectiveDefault, and ParseGoModVersionDefault variants
- refactor: rename TaskCreationFilters to TaskCreationFilterList per list-type naming convention
- chore: add counterfeiter:generate directive for TriggerHandler and lower two always-on logs to V(2)
- fix: recover from a corrupt cursor file by renaming it to .corrupt and cold-starting, instead of returning an error that wedged every subsequent poll cycle indefinitely

## v0.0.1

- initial scaffold from `go-skeleton`
