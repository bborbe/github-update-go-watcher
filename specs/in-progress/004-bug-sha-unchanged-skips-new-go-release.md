---
status: verifying
approved: "2026-10-09T14:27:19Z"
generating: "2026-10-09T15:04:51Z"
prompted: "2026-10-09T15:04:51Z"
verifying: "2026-10-09T15:20:41Z"
branch: dark-factory/bug-sha-unchanged-skips-new-go-release
---

## Summary

- The watcher skips a repo whenever its default-branch HEAD equals the HEAD recorded at its last emit — regardless of whether a newer stable Go release has appeared since.
- A new Go release moves no repo's HEAD, so a repo already in the cursor is never re-evaluated for that release.
- The task identity has the same blind spot: it is derived without the Go version, so lifting the skip on its own would re-emit the identical identifier and be absorbed downstream as a no-op.
- Fix: record the target Go version beside the HEAD in the cursor, skip only when both are unchanged, and fold the target Go version into the task identity.
- Measured 2026-10-09: five repos hold a completed Go-1.27.2 task, no open task, an unchanged HEAD and a `go.mod` still on `1.27.1` — permanently stalled, with no path back into the pipeline.

## Problem

The dedup contract is Go-version-blind. `SHAUnchangedFilter` (`pkg/filter/sha_unchanged_filter.go`) skips a repo when `Candidate.HeadSHA` equals `CursorReader.LastSeenSHA(repoKey)`, and `pkg/cursor.go`'s `RepoState` persists only `LastSeenHeadSHA` and `CompletedHeadSHA` — no Go version; `DeriveTaskID` (`pkg/taskid.go`) seeds the identity on `(owner, repo, headSHA)`, also without the Go version. A new stable Go release changes no repo's HEAD SHA, so every repo already in the cursor passes `go_current` (it is genuinely behind), then is killed by `sha_unchanged` (its HEAD never moved), and — because the identifier would be byte-identical — a forced re-emit would be absorbed downstream rather than producing work. This is the failure mode [[Watcher Task Dedup Seeds on Changed State]] names as "dedup plus event-only delivery is a permanent silent stall": the watcher's cursor is the single point of delivery, so a task that closes without the work landing is unrecoverable, and its symptom is indistinguishable from a slow queue. The blast radius compounds — any repo whose HEAD has not moved since its last emit is frozen out of the *next* release too.

## Goal

A repo is re-evaluated for a Go release whenever the target Go version changes, even if its HEAD has not moved. Re-evaluating it produces a task identity the downstream dedup has not already absorbed, so the repo re-enters the pipeline instead of being skipped forever. The existing per-commit in-flight suppression (the open `fix/update-go-*` PR gate) is unchanged.

## Non-goals

- The five repos currently stalled are NOT re-driven by hand — no `github-update-go-repo-trigger` run. The one-off re-emission this fix produces is the recovery path, and it is the observable the acceptance criteria check.
- The downstream consumer's failure to bump a repo to the release it was told about (`github-update-go-agent`'s bump target is the Go baked into its own image) is NOT fixed here — separate concern, separate task.
- The `sha_unchanged` Prometheus label is NOT renamed. It is an operational contract (dashboards, `README.md`, `pkg/metrics.go`), and a rename is a separate change from this fix.
- The decision-task identity (`DeriveDecisionTaskID`, seeded on `(owner, repo)` only) is NOT changed — it is deliberately SHA-free and re-emits every cycle as a documented no-op.
- The open-PR gate gets NO new opt-out knob.
- The merge-detection completion marker stays HEAD-only. Consequence: a release re-filed at an *unchanged* HEAD does not auto-complete — the task waits for the existing close-sweep — because the guard compares `LastSeenHeadSHA` to `CompletedHeadSHA` and both are the same unchanged HEAD. Making the marker version-aware means changing the merge-detection pass, which the Constraints freeze; that is a follow-up spec. A second, smaller window is also left open: on a cold start (missing cursor, the `.corrupt` path, or a lost cursor write) the marker is `""` rather than a stale HEAD, so the same-cycle premature-completion hazard that DB1 guards against is not fully closed there either.

## Do-Nothing Option

Every idle repo silently stops receiving Go updates. The five already-stalled repos stay stalled permanently, and each new release strands more repos whose HEAD happens not to have moved: a repo is only re-admitted to the pipeline by an unrelated commit. Detection is by hand — the symptom is an absence (no task, no PR, no error), visible only by cross-referencing each repo's `go.mod` against the current stable release.

## Reproduction

Version: prod image `v0.6.0` (deployed 2026-10-05, pre-fix), nuke prod, `sts/github-update-go-watcher`.

1. A repo R has `goUpdate.autoUpdate: true` and is behind stable Go. The watcher emits a task for R at HEAD `H`, recording `LastSeenHeadSHA: H` in the cursor.
2. R's update task closes without R's `go.mod` being bumped — the Go version in `go.mod` is unchanged, and R's HEAD is still `H`.
3. A new stable Go release appears. `go.dev` now reports a newer version; `LatestStable` advances.
4. Next poll: R is still behind, so `go_current` passes. `SHAUnchangedFilter` compares `H` against the cursor's `H`, returns `sha_unchanged`, and R is skipped.
5. Repeat every poll, forever. No task is filed for the new release.

Observed evidence (2026-10-09, nuke prod, read-only):

```
I1009 13:28:21.190189 1 watcher.go:179] repo skipped repo=github.com/bborbe/agent reason=sha_unchanged
```

`kubectlnukeprod -n prod logs sts/github-update-go-watcher --tail=3000` yields 63 distinct repos skipped with `reason=sha_unchanged` in one cycle; all 63 report `go 1.27.1` in `go.mod` while `https://go.dev/VERSION?m=text` returns `go1.27.2`.

Five of them hold a **completed** `Update Go` task for 1.27.2, no open task, and an unchanged HEAD:

| Repo | Completed 1.27.2 task ref | HEAD (unchanged since) |
|---|---|---|
| `bborbe/agent-gemini` | `ad2800d2` | `ad2800d2` (2026-09-25) |
| `bborbe/ctrader` | `0173c67b` | `0173c67b` (2026-09-25) |
| `bborbe/image-renamer` | `d7aa7f7b` | `d7aa7f7b` (2026-09-08) |
| `bborbe/lock` | `04ae3d47` | `04ae3d47` (2026-09-25) |
| `bborbe/recurring-task-creator` | `c5d4b47f` | `c5d4b47f` (2026-09-25) |

For each, the cursor's `LastSeenHeadSHA` equals HEAD, so the repo can never be re-emitted.

## Expected vs Actual

**Expected** (per `pkg/taskid.go`'s own contract comment — "Same repo at the same HEAD always yields the same identifier, so a re-emit is a downstream no-op; a new HEAD yields a new identifier, so a new commit correctly produces a fresh work item" — and spec 001 Desired Behavior 6, which states the shorter form "same repo at the same HEAD always yields the same identifier; a new HEAD yields a new one": the emit is one-shot per *unit of work*): a repo behind on the current stable Go receives a task for it, and a repo whose task closed without the bump is re-driven on the next release.

**Actual:** the emit is one-shot per `(repo, HEAD SHA)` pair. The unit of work is the Go release, but the dedup key is a commit SHA that a release never changes — so the key and the work are unrelated, and the release never re-drives a repo whose HEAD happens to be still. The `sha_unchanged` skip also fires for a repo that is *known* to be behind, which is precisely the case the skip must not cover.

## Acceptance Criteria

- [ ] The cursor persists the target Go version alongside the HEAD — evidence: `grep -n "LastSeenGoVersion" pkg/cursor.go` returns ≥1 AND a watcher test asserts the cursor JSON saved after a successful publish for a repo carries `"last_seen_go_version":"1.27.2"` (the cycle's resolved stable version), not an empty string.
- [ ] The cursor entry written on a successful publish records the cycle's resolved stable Go version — evidence: `grep -nE "LastSeenGoVersion:.*LatestGo" pkg/watcher.go` returns ≥1, so the value comes from the cycle's resolved version rather than a literal.
- [ ] The skip requires **both** inputs to be unchanged — evidence: `grep -c "LastSeenGoVersion" pkg/filter/sha_unchanged_filter.go` returns ≥1 AND `grep -n "LastSeenGoVersion" pkg/filter/sha_unchanged_filter.go` shows the comparison inside the same `if` that returns `"sha_unchanged"`.
- [ ] The task identity folds in the target Go version — evidence: a `go test ./pkg/...` assertion that `DeriveTaskID` differs when only the Go version differs and is identical when all inputs are equal, AND `grep -n "func DeriveTaskID" pkg/taskid.go` shows a signature carrying a Go-version argument.
- [ ] Merge-detection derives the identifier of the task the create pass actually filed — evidence: `grep -n "DeriveTaskID" pkg/watcher.go` returns ≥1 line inside `completeTask`, passing `state.LastSeenGoVersion`.
- [ ] The same repo, same HEAD, newer stable Go → **not** skipped, and publishes an identifier different from the one published before the advance — evidence: a `go test ./pkg/...` assertion comparing the two identifiers for inequality and asserting the publisher spy received a create command (exit 0).
- [ ] The same repo, same HEAD, same stable Go → skipped with reason `sha_unchanged` and **no** create command published — evidence: `go test ./pkg/...` assertion on the publisher spy recording zero publishes (exit 0).
- [ ] A cursor entry with no recorded Go version (the pre-fix on-disk shape) does not skip — evidence: a `go test ./pkg/...` assertion that a `RepoState{LastSeenHeadSHA: H}` with no Go version passes the filter for stable `X`.
- [ ] The open-PR gate is untouched — evidence: `git diff --name-only origin/master...HEAD -- pkg/githubclient.go` returns empty, and `grep -c "open_update_pr" pkg/watcher.go` returns the same count as `git show origin/master:pkg/watcher.go | grep -c "open_update_pr"`.
- [ ] `make precommit` exits 0 — evidence: exit code.
- [ ] **Post-Deploy (Rung-3):** in nuke prod, a repo whose 1.27.2 task closed without a bump receives a FRESH update task from the watcher, with no manual trigger — evidence: `kubectlnukeprod -n prod logs sts/github-update-go-watcher --since=20m | grep "published CreateTaskCommand repo=github.com/bborbe/lock"` returns ≥1 line whose `taskID=` is not `2215e298-7f8c-5735-97a1-755f5b5112c7` (the `task_identifier` of the completed `Update Go bborbe-lock 04ae3d4.md` task), AND a new `Update Go bborbe-lock *.md` file exists in `~/Documents/Obsidian/OpenClaw/tasks/` beyond the six present before the deploy.
  - `deploy_check:` `kubectlnukeprod -n prod get sts/github-update-go-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git tag --list 'v*' --sort=-v:refname | head -1)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — exits 0
- `grep -n "LastSeenGoVersion" pkg/cursor.go pkg/watcher.go pkg/filter/sha_unchanged_filter.go` — ≥1 per file
- `grep -n "func DeriveTaskID" pkg/taskid.go` — signature carries the Go version
- `git diff --name-only origin/master...HEAD -- pkg/githubclient.go` — empty

### Operator-executable (host, after PR merge + image publish + mirror + deploy)

- `cd ~/Documents/workspaces/nuke/github-update-go-watcher && make mirror BRANCH=dev && make apply BRANCH=dev` — dev rolls to the new tag
- `kubectlnukedev -n dev get sts github-update-go-watcher -o jsonpath='{.spec.template.spec.containers[0].image}'` — new tag
- `cd ~/Documents/workspaces/nuke/github-update-go-watcher && make apply BRANCH=master` — prod rolls to the new tag
- `kubectlnukeprod -n prod logs sts/github-update-go-watcher --since=20m | grep "published CreateTaskCommand repo=github.com/bborbe/lock"` — ≥1 line with a taskID differing from the completed 1.27.2 task's identifier
- `ls ~/Documents/Obsidian/OpenClaw/tasks/ | grep "Update Go bborbe-lock"` — a new file beyond the six pre-existing ones (`04ae3d4`, `41e7620`, `4a141e8`, `660616e`, `eef504e`, `eef504e - c90da969`)

## Desired Behavior

1. **The cursor records the target Go version.** `pkg.RepoState` gains a third field, `LastSeenGoVersion string`, serialised as `last_seen_go_version,omitempty` so pre-fix cursor files still load. It holds the cycle's resolved stable Go version in the same three-part form the emitted task carries as `latest_go` (`1.27.2`). It is written beside `LastSeenHeadSHA` on a successful `PublishCreate`, and the existing `CompletedHeadSHA` marker is carried forward rather than dropped: `Poll` runs the merge-detection pass immediately after `processRepos` over the same cursor object, and that pass is suppressed only by `LastSeenHeadSHA == CompletedHeadSHA`, so a fresh `RepoState` literal would defeat the guard and publish a `CompleteCommand` for the task the same cycle just filed. Writing only on a successful `PublishCreate` means a failed publish leaves the repo re-evaluable next cycle.
2. **The skip is two-key.** The skip reason is returned only when the candidate's HEAD equals the cursor's recorded HEAD **and** the cycle's stable Go version equals the cursor's recorded Go version. A repo whose HEAD is unchanged but whose stable Go advanced is NOT skipped — it proceeds to emit.
3. **The Go version reaches the filter.** `filter.Candidate` gains the cycle's stable Go version as a plain string, populated from `pkg.Candidate.FilterCandidate()`; the local `filter.CursorReader` interface gains `LastSeenGoVersion(repoKey string) string`, implemented by `pkg.NewCursorReader`. The `filter` package must not import `pkg` (the existing import-cycle constraint in `pkg/filter/filter.go`).
4. **The task identity folds in the Go version.** `DeriveTaskID` takes the target Go version in addition to `(owner, repo, headSHA)` and seeds `update-go-<owner>-<repo>-<goVersion>-<headSHA>`. A new Go release therefore yields a new identifier even at an unchanged SHA, while a new commit still yields a new identifier as before.
5. **Completion still matches creation.** `completeTask` derives the identifier with the same Go version it recorded at create time (`state.LastSeenGoVersion`), so a merged update PR closes the exact task the watcher filed.
6. **Forced cycles are unchanged.** A forced cycle still omits the skip filter entirely; every other gate still applies.
7. **The emitted command shape is unchanged** apart from the identifier's derivation — same twelve keys, same values, same body. Two documented rows must be updated in the same change so the docs do not drift from the code: `README.md`'s `task_identifier` row (derivation now names the Go version) and `README.md`'s `sha_unchanged` skip-reason row (currently "Repo HEAD SHA has not changed since last successful cycle" — now both the HEAD and the target Go version).
8. **Tests.** Unit coverage for: same HEAD + advanced Go → not skipped, and the published identifier differs from the pre-advance identifier; same HEAD + same Go → skipped, zero publishes; legacy cursor entry (HEAD recorded, no Go version) → not skipped; `DeriveTaskID` differs when only the Go version differs and is stable when all inputs are equal; `completeTask` derives the identifier the create pass published. Counterfeiter mocks regenerated for the extended `CursorReader`.

## Constraints

- Errors follow `github.com/bborbe/errors` wrapping; info logs at glog V(2) (project DoD: `docs/dod.md`).
- Tests use Ginkgo v2 / Gomega; mocks via Counterfeiter in `mocks/` — never hand-edited.
- The `sha_unchanged` label string and the `NewSHAUnchangedFilter` constructor name are retained; only their doc comments change to name both inputs. Renaming either is out of scope (see Non-goals).
- The open `fix/update-go-*` PR gate, the merge-detection pass, the allowlist, `go.mod` parsing, the version comparison, and the consent gate are unchanged.
- The watcher stays read-only against observed repos and gains no new external dependency.
- `docs/dod.md`'s "In-flight signal contract" section states the identifier derives from `(owner, repo, head_sha)`; it must be updated in the same change, since the SHA remains part of the seed and the contract's rationale (a new commit produces a new identifier, hence the open-PR gate) still holds.
- `README.md` carries two rows this change invalidates — the `task_identifier` derivation row and the `sha_unchanged` skip-reason row (currently "Repo HEAD SHA has not changed since last successful cycle"). Both must be updated in the same change.
- **Assumptions:** a pre-fix cursor file on the PVC loads unchanged (the new field is `omitempty`, so absent deserialises to `""`); forced cycles keep omitting the skip filter; `LatestStable` resolves to the same version for every candidate within one cycle, so a single recorded value per repo is sufficient.
- **Frozen surface** (the only new symbols downstream artifacts and prompts may pin): `pkg.RepoState.LastSeenGoVersion` with JSON tag `last_seen_go_version,omitempty`; `filter.CursorReader.LastSeenGoVersion(repoKey string) string`; the target Go version carried on `filter.Candidate`; the `DeriveTaskID` parameter list and its seed format `update-go-<owner>-<repo>-<goVersion>-<headSHA>`. Everything else is the implementer's choice.
- `CHANGELOG.md` gains an `## Unreleased` section with a `fix:` bullet — the file currently has none.

## Failure Modes

| Trigger | Expected behavior | Detection | Reversibility | Recovery |
|---------|-------------------|-----------|---------------|----------|
| Pre-fix cursor file on the PVC (entries with no `last_seen_go_version`) | Every behind repo fails the two-key skip once and is re-emitted with a new identifier — a single one-off fleet re-emission | A one-cycle burst of `published CreateTaskCommand` lines far above the steady rate; `github_update_go_watcher_filter_skipped_total{reason="sha_unchanged"}` drops sharply | Partial — the emitted tasks can be closed by the existing sweeps, but the cursor entries now carry the version, so the re-emission itself does not repeat | Expected and intended: it is how the stalled repos re-enter the pipeline. Documented as a signal change per [[Watcher Task Dedup Seeds on Changed State]] |
| The one-off re-emission produces a second open task for a repo that already holds an open 1.27.2 task | Two open tasks for the same repo and release, with different identifiers | Two open `github-update-go` tasks naming the same `repo` with the same `latest_go` in `~/Documents/Obsidian/OpenClaw/tasks/` | Reversible — the duplicate is a vault file with no external side effect | The live agent-task-controller drain and the `close-obsolete-tasks` sweep collapse duplicates; no new mechanism is added here |
| A legacy cursor entry is used to derive a completion identifier (`LastSeenGoVersion` empty) | The derived identifier matches no filed task; the controller absorbs the completion as a no-op | `complete-task: published … taskID=` line whose identifier matches no task file | Reversible — a no-op publish, nothing is mutated | Self-healing: the next successful emit writes the Go version, after which completions match |
| `LatestStable` fails for a cycle | Unchanged — cycle aborts with `go_version_error`, cursor not advanced | `poll_cycle_total{result="go_version_error"}` increments | Reversible — no state written | Next poll retries; no cursor mutation occurred |
| A rate limit during the cycle | Unchanged — cycle aborts with `rate_limited`; the cursor is not saved | `poll_cycle_total{result="rate_limited"}` increments | Reversible — no state written | Next poll retries from the last saved cursor |
| Crash between publish and cursor save | The repo is re-evaluated next cycle and re-emits the same identifier | A repeated identical `published CreateTaskCommand … taskID=` line across two cycles | Reversible — downstream dedup absorbs the repeat | Downstream dedup absorbs the repeat — the pre-existing contract for a lost cursor write |

## Security / Abuse Cases

No new surface. The change reads one additional value from the same `go.dev` response the watcher already fetches and writes it to the existing PVC-mounted cursor file. The Go version string is already validated against `^go\d+\.\d+(\.\d+)?$` before use, and the value placed in the cursor and the identifier seed is the parsed three-part form produced by `Version.Number()`, never raw upstream text. No credentials, no new network calls, no new files.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Two-key dedup: cursor field + filter condition + identity seed + completion match + mock regen + README/dod doc updates + tests | 1-8 | 1-10 | — |
| — | Post-deploy verification (operator, after merge + image publish + mirror + deploy) | — | 11 | prompt 1 |

Rationale: kept atomic across four code surfaces (cursor schema + persistence, filter chain + `CursorReader`, identity derivation, watcher wiring + completion) plus mock regeneration and two doc contracts. That layer count is deliberate, not an oversight: a partial landing is worse than the bug. The cursor field without the filter change skips a repo that should emit; the filter change without the identity change re-emits into an identifier the downstream dedup already absorbed — which *looks* like progress (the log line appears) while producing no work. The two must land in one commit, so the prompt-creator should expect to research all four surfaces together. AC 11 is operator-only post-deploy observation and is not a prompt deliverable.
