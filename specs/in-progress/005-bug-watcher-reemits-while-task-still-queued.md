---
status: verifying
approved: "2026-10-27T22:21:46Z"
generating: "2026-10-27T22:35:12Z"
prompted: "2026-10-27T22:35:12Z"
verifying: "2026-10-27T22:53:14Z"
branch: fix/update-go-emission-claim
---

## Summary

- The watcher's only repo-level in-flight signal is an **open `fix/update-go-*` PR**, and no PR exists while the emitted task is still sitting in the queue. Between emission and the agent's first push, a repo is indistinguishable from an idle repo.
- So a repo that receives a second commit in that window is emitted again: two task records, two agent runs, two PRs for one bump.
- Measured on the Go 1.27.2 rollout: `bborbe/agent-code` emitted at `810ce8b` (2026-10-08T15:59Z, PR #18 opened 2026-10-15T13:01Z) and again at `ec0bf41` (2026-10-14T14:54Z, PR #19 opened 2026-10-16T05:14Z); `bborbe/agent-gemini` emitted at `89a427d` (2026-10-13T11:04Z, PR #18 opened 2026-10-16T20:49Z) and again at `b7ab537` (2026-10-14T15:33Z, PR #17 opened 2026-10-15T22:05Z). Both pairs were closed by hand as duplicates.
- Fix: the watcher records its own emission in the cursor and refuses to re-emit for the same `(repo, target Go version)` until one of three observable events clears the claim — convergence, the appearance of the update PR, or a TTL backstop set above the measured queue latency.

## Problem

`processRepos` (`pkg/watcher.go:144-209`) runs, per repo: `gatherCandidate` (`:167`) → `openUpdatePRGate` (`:170-175`) → the cycle filter chain (`:177`) → `publisher.PublishCreate` (`:201`), then `markPublished` (`:216-230`) advances `LastSeenHeadSHA`/`LastSeenGoVersion`.

Neither repo-level guard spans the window between emission and PR creation:

- **The open-PR gate** (`pkg/githubclient.go:416`, `strings.HasPrefix(pr.GetHead().GetRef(), updateBranchPrefix)`) reads GitHub PRs only. A task that is queued, claimed, or running has no PR, so the gate cannot see it. The gap is not minutes: it is the agent's queue latency, measured at **7 days** for `agent-code` (emitted 2026-10-08T15:59Z, PR 2026-10-15T13:01Z) and **3.5 days** for `agent-gemini` (2026-10-13T11:04Z → 2026-10-16T20:49Z).
- **`sha_unchanged`** (`pkg/filter/sha_unchanged_filter.go:28-36`) is a new-commit gate, not a duplicate gate: `HeadSHA != "" && HeadSHA == cursor.LastSeenSHA(repoKey) && LatestGoVersion == cursor.LastSeenGoVersion(repoKey)`. A second commit is exactly the input that makes it pass.

The identity seed (`pkg/taskid.go:26-29`, `update-go-<owner>-<repo>-<goVersion>-<headSHA>`) makes the two emissions distinct by construction, so nothing downstream collapses them: `agent-task-controller`'s supersede (`pkg/command/supersede_update_go.go:158-176`) only aborts a prior at create time, and it shipped after these emissions (v0.15.1, 2026-10-21).

`docs/dod.md:30` freezes the constraint that shapes any fix: the watcher runs in a pod with **no vault access** and cannot query the task store. A "check for an existing open task" is therefore not available.

## Goal

A repo already emitted for a target Go version produces **no second task** while that emission is in flight, and the emission is re-armed when it demonstrably did not land — with the re-arm triggered by an observable event, not by a guess about how long a task "should" take. Both properties are readable from the cursor file and the publish log, with no vault read.

## Non-goals

- **The task identity seed is NOT changed.** Dropping `headSHA` from `DeriveTaskID` (and from the title) would make a re-emit a downstream no-op, but the controller *reopens* a terminal record at the same title path in place (`agent-task-controller/pkg/command/task_create_task_executor.go:209-215`), which `Agent - Re-Drive Parked Tasks.md:313-315` flags as destroying agent-written analysis. Rejected deliberately.
- **The open-PR gate is NOT removed or weakened.** It stays the in-flight signal once a PR exists; the claim covers only the window before that.
- **No per-repo lock and no "one open record per repo" rule.** A repo-level lock measured on 2026-10-27 would suppress five legitimate hand-authored repair tasks and hold thirteen genuinely-behind repos at zero live tasks.
- **The `- reemit` repair traffic is NOT suppressed.** Those records are hand-authored by a session, carry non-seed identifiers, and exist to re-drive a repo whose previous task died. The claim is written only by `markPublished`, so it never covers them.
- **The TTL is NOT operator-tunable.** One constant, with the value and its derivation written down (DB6). A knob with no consumer adds a flag, a chart value and a README row.
- The `github-update-go-repo-trigger` slash command's identifier seed is not fixed here (separate task).

## Do-Nothing Option

Every repo that receives a second commit while its update task is queued produces a duplicate task, a duplicate agent run and a duplicate PR, and the duplicate is closed by hand. The rate is proportional to how often repos are pushed during queue latency — the common case for actively developed repos. The cost per occurrence is one full agent invocation plus one reviewer round, and it makes PR count an unusable proxy for task count.

## Reproduction

Cursor state before the second emission: `last_seen_head_sha = 810ce8b…`, `last_seen_go_version = 1.27.2`, no open `fix/update-go-*` PR. A commit lands making the repo's HEAD `ec0bf41…`; `go.mod` still reads `1.27.1` and the target is `1.27.2`.

- `sha_unchanged`: HEAD differs from `LastSeenHeadSHA` → **passes** (by design).
- `openUpdatePRGate`: no open PR exists → **passes**.
- `DeriveTaskID(owner, repo, "1.27.2", "ec0bf41…")` → a new identifier → a second record.
- `markPublished` overwrites `LastSeenHeadSHA` with `ec0bf41…`, losing the fact that `810ce8b…` was already emitted and never converged.

**Expected:** the second emission is suppressed because the watcher already emitted for `(repo, 1.27.2)` and none of the three clearing events has occurred.

## Desired Behavior

1. **Schema and write.** `pkg/cursor.go`'s `RepoState` gains two persisted fields, both tagged `,omitempty` so an empty claim serialises to nothing: `InFlightTarget` (the target Go version string the watcher last emitted for) and `InFlightSince` (RFC3339, the emission time). `markPublished` sets both on a successful publish, alongside the existing `LastSeenHeadSHA`/`LastSeenGoVersion` writes; the `CompletedHeadSHA` carry-forward is unchanged. Empty means "no claim"; an existing cursor file needs no migration. `InFlightSince` persists as a plain RFC3339 `string`, not `libtime.DateTime`: `,omitempty` does not omit struct-typed fields and a zero `libtime.DateTime` marshals to `null`, which would violate "an empty claim serialises to nothing"; parsing goes through `libtime.ParseDateTime`. **The clock is injected, not read:** `pkg/` has no time source and the coding plugin's `go-time/no-time-now-direct` forbids `time.Now()` there, so `libtime.CurrentDateTimeGetter` threads through `NewWatcher` → `factory.CreateWatcher` → `main.go`, plus the `buildWatcher` closure in `pkg/watcher_test.go` (three call sites, verified by grep).
2. **Ordering: two distinct insertion points, and the gate's signature changes.** (a) **The PR-clear runs inside the gate's skip branch.** `openUpdatePRGate`'s `openUpdate == true` path returns `"skip"` and the caller `continue`s (`pkg/watcher.go:170-175`), so a check placed *after* the switch would never observe it — the branch clears the claim, then returns. (b) **The suppression check runs after the switch**, before `cycleFilter.Skip` (`:177`), where `candidate.GoBehind()` is available (`gatherCandidate` is called at `:162`). The guard is **not** appended to the cycle filter chain: a `TaskCreationFilter` receives only `FilterCandidate`, so it cannot see the gate's result. `openUpdatePRGate` returns `(action string, openUpdate bool)` instead of a bare `string`: today `"skip"` covers both the `openUpdate == true` branch and the `dropRepo(repo, "open_update_pr", err)` error branch (`pkg/watcher.go:245-262`), so the string alone cannot express the clearing signal DB3 needs — and a second API call is forbidden by the Constraints.
3. **Clearing — evaluated before the skip decision.** A claim for the current target is cleared when any one holds:
   - **Convergence:** `!candidate.GoBehind()` — the *same* predicate the `go_current` filter uses (`pkg/candidate.go:41`: `GoModParsable && CurrentGo.Less(LatestGo)`), so equal-to, ahead-of and unparsable all clear. Comparing `CurrentGo` to `LatestGo.Number()` by equality would leave the claim live on the ahead-of and unparsable cases while the filter still drops the repo.
   - **The PR exists:** the gate's `openUpdate` result is `true` (`HasOpenUpdatePR`, `pkg/githubclient.go:387-420`). This is the `openUpdate == true` branch only — the error branch also returns `"skip"` from `openUpdatePRGate` (`pkg/watcher.go:245-262`, `dropRepo(repo, "open_update_pr", err)`) and **must not** clear the claim, or a transient GitHub error re-emits the next cycle — the defect this spec exists to fix. This is why DB2 changes the signature rather than inferring the branch from the action string.
   - **TTL backstop:** `now - InFlightSince > InFlightTTL` — the task never produced a PR and is presumed dead.
4. **Skip, and the observable is the metric — not the log.** When the claim is live for the current target and no clearing condition held, the repo is skipped with reason `emission_in_flight`, logged at `glog.V(2)` in the existing skip-line shape (`repo skipped repo=%s reason=%s`, `pkg/watcher.go:178-182`) and counted on `github_update_go_watcher_filter_skipped_total{reason="emission_in_flight"}` via `IncFilterSkipped`. **V(2) is deliberate, not an oversight**: a skip is routine and fires once per claimed repo per cycle, whereas `dropRepo` (`pkg/watcher.go:418-422`) logs at Warning precisely because a drop is an anomaly. The consequence is that the Ginkgo suites run at V(0) and never see the line, so **every AC below asserts the metric counter or the cursor, never the log text**. `emission_in_flight` is added to `FilterSkipReasons` (`pkg/metrics.go:48-57`), to the pre-initialisation test (`pkg/metrics_test.go:52`) and to the membership test (`pkg/watcher_test.go:652`) — a label outside that list silently breaks pre-initialisation.
5. A **different** target Go version does not match the claim: a new stable release is emitted immediately, exactly as `sha_unchanged`'s two-key behaviour already allows.
6. **`InFlightTTL` is a package constant, `336h` (14 days)** — twice the maximum queue latency measured in the Problem section (7 days). The derivation is part of the value: the TTL is a backstop for a task that never opened a PR, not the normal clearing path, so it must be longer than a *healthy* queue wait or it re-creates the duplicate. Revisit when the dispatch-latency work lands (`Find Why New Agent Tasks Wait Tens of Minutes…`), because a shorter healthy latency permits a shorter backstop. A forced cycle does **not** bypass the guard — spec 003 established the always-on precedent, and a forced cycle is an operator "poll now", not an "emit regardless".
7. **Persistence.** The claim is per repo, lives in the cursor file that already persists across pod restarts, and adds no store, no PVC and no vault access.
8. **Documentation in the same change.** `docs/dod.md`'s in-flight-signal paragraph names the claim as the pre-PR window, and `README.md`'s skip-reasons table gains the `emission_in_flight` row.

## Constraints

- No vault, task-store or filesystem read outside the existing cursor file — `docs/dod.md:30` is a durable contract.
- The open-PR gate, the `sha_unchanged` filter and the task identity seed keep their current semantics; this adds a layer, it does not replace one.
- The cursor file stays readable by an older binary (additive change only). An older pod reading a newer cursor must ignore the new fields, not fail.
- The guard must not add a GitHub API call per repo per cycle: the PR list it consults is the one `openUpdatePRGate` already fetches.
- Rate limiting: the guard performs no API call of its own, so `ErrRateLimited` keeps its existing behaviour (abort the cycle) and the claim is neither written nor cleared on an aborted cycle.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Task dies before opening a PR; claim holds the repo | No emission while the claim is live | TTL expires after `336h` and the next cycle emits; the cursor's `InFlightSince` is refreshed on that emit |
| `HasOpenUpdatePR` returns a non-fatal error | Repo dropped from this cycle (`step=open_update_pr` WARNING), claim unchanged | Next cycle re-checks; no emission and no clear happened |
| `HasOpenUpdatePR` is rate limited | Cycle aborts (`ErrRateLimited`), claim unchanged | Next cycle re-runs |
| Claim written, cursor write lost (crash between publish and save) | Two identical `published CreateTaskCommand … taskID=` lines across cycles | The re-emit produces the same identifier and is absorbed downstream — the pre-existing contract for a lost cursor write |
| Pod clock jumps forward past `InFlightTTL` | Every live claim expires in one cycle and the fleet re-emits once | Bounded: one emission per repo, each absorbed downstream if the identifier is unchanged; the cursor is rewritten with the new `InFlightSince`. Tolerance is the TTL itself; no separate clock source |
| Repo converges but the cycle is dropped before the cursor is saved | Claim survives one cycle past convergence | Next cycle sees `!GoBehind()`, clears it, and the `go_current` filter drops the repo; no emission |
| PR is opened by the agent, then closed unmerged, repo still behind | Claim already cleared by the `HasOpenUpdatePR == true` path; the repo is re-evaluated normally | The next cycle emits, because no claim and no open PR remain |

## Acceptance Criteria

- [ ] **Claim suppresses a second emission.** Cursor fixture with `in_flight_target: "1.27.2"` and `in_flight_since: <now-1h>`, repo HEAD moved, no open PR → the publisher spy records **0** `PublishCreate` calls for that repo, and `github_update_go_watcher_filter_skipped_total{reason="emission_in_flight"}` reads **1**. — evidence: spy call count + the metric counter read through the metrics registry (not the log, which is V(2) and invisible under `go test`).
- [ ] **Convergence clears the claim.** Same fixture, `go.mod` at `1.27.2` → the decoded cursor for **that repo key** has an empty `InFlightTarget` (the serialised JSON has no `in_flight_target` on its object), `filter_skipped_total{reason="go_current"}` increments and `{reason="emission_in_flight"}` stays **0**. — evidence: decoded-struct assertion on the repo key (not a file-wide grep) + both metric counters.
- [ ] **Ahead-of and unparsable also clear.** Fixture repo whose `CurrentGo` is ahead of the target, and one whose `go.mod` is unparsable → in both, `InFlightTarget` is empty after the cycle. — evidence: two decoded-struct assertions.
- [ ] **An open update PR clears the claim.** Claim live, `fix/update-go-<sha7>` PR open, `go.mod` behind → `filter_skipped_total{reason="open_update_pr"}` increments and `{reason="emission_in_flight"}` stays **0**, and the decoded cursor has an empty `InFlightTarget`. — evidence: both metric counters + decoded-struct assertion.
- [ ] **A GitHub error does NOT clear the claim.** `HasOpenUpdatePR` stubbed to return a non-fatal error → the repo is dropped this cycle, `InFlightTarget` is unchanged, and 0 `PublishCreate` calls. — evidence: decoded-struct assertion + spy count.
- [ ] **TTL re-arms.** Claim with `in_flight_since = <now-337h>`, no PR, still behind, **and a HEAD that differs from `last_seen_head_sha`** (otherwise `sha_unchanged` suppresses the emit and the assertion never fires) → exactly **1** `PublishCreate` and the saved cursor's `in_flight_since` is the current cycle's time. — evidence: spy count 1 + decoded field value.
- [ ] **Positive control: a behind repo with no claim still emits.** Fixture repo behind target, no claim, no PR → exactly 1 `PublishCreate`; a second cycle at the same HEAD → 0. The binding evidence is the spy count (1 then 0), **not** the skip reason: the first cycle's publish writes a claim for the same target, so the second cycle is skipped by `emission_in_flight` before the filter chain ever sees it — `sha_unchanged` is not the reason, and asserting it would fail against a correct implementation.
- [ ] **Forced cycle does not bypass.** Live claim, `force=true` → 0 `PublishCreate` calls and `filter_skipped_total{reason="emission_in_flight"}` increments. — evidence: spy count + the metric counter (the `published CreateTaskCommand` line is `glog.V(2)` in `pkg/taskpublisher.go:60` and is invisible at the suite's verbosity, so it cannot carry this AC).
- [ ] **Cursor compatibility is additive.** A JSON fixture carrying `in_flight_target`/`in_flight_since` decodes into a struct without those fields without error, and re-encoding preserves the pre-existing fields. — evidence: `go test ./pkg/... -run TestSuite -ginkgo.focus='cursor'` green (the suite's only entry point is `TestSuite`, `pkg/pkg_suite_test.go:17` — a `-run Cursor` selector matches nothing and exits 0).
- [ ] **No new read path.** The change adds no vault/task-store reference and no new file or exec read outside `pkg/cursor.go`. — evidence: `git diff -U0 origin/master...HEAD -- pkg/ | grep -c '^+.*\(obsidian\|task_store\|vault task\)'` prints **0**. The base must be pinned to `origin/master`: a bare `git diff` is empty once the work is committed and passes vacuously. Read the printed count, not the exit code — `grep -c` exits 1 at zero. An absolute `grep` over the tree is not usable either: two pre-existing comment lines already match (`pkg/watcher.go:121`, `pkg/githubclient.go:90`).
- [ ] **Docs updated in the same change.** `docs/dod.md`'s in-flight paragraph names the claim; `README.md`'s skip-reasons table has an `emission_in_flight` row. — evidence: `grep -n 'emission_in_flight' docs/dod.md README.md`.

## Verification

```bash
cd ~/Documents/workspaces/github-update-go-watcher-emission-claim
make precommit                     # format + lint + tests + security; must exit 0
go test ./pkg/... -count=1         # the Ginkgo suites behind the ACs above
git diff -U0 origin/master...HEAD -- pkg/ | grep -c '^+.*\(obsidian\|task_store\|vault task\)'   # must print 0; the pinned base matters
grep -n "emission_in_flight" docs/dod.md README.md
```

Deployment is **not** in this repo: `Makefile.docker:61-63` makes `apply` a no-op (`skip apply — publish-only`), so `BRANCH=dev make buca` publishes an image and moves no pod. The watcher runs as a StatefulSet named `github-update-go-watcher` in namespace **`agent`** on **both** clusters — dev (`STAGE=dev`, `TOPIC_PREFIX=develop`, allowlist `github.com/bborbe/go-skeleton`) and prod (`STAGE=prod`, `TOPIC_PREFIX=master`) — each pinned by the Helm release after the image is mirrored. Both were verified live on 2026-10-27 at `v0.6.5`, ready 1/1.

Dev is the cheapest post-pin check because its allowlist is one repo, and it is the first step after the chart tag is bumped:

```bash
kubectlnukedev -n agent get statefulset github-update-go-watcher \
  -o jsonpath='{.status.readyReplicas} {.spec.template.spec.containers[0].image}'   # pinned is not deployed — read the pod
kubectlnukedev -n agent logs github-update-go-watcher-0 --since=24h | grep -c 'reason=emission_in_flight'
```

Then prod:

```bash
kubectlnukeprod -n agent get statefulset github-update-go-watcher \
  -o jsonpath='{.spec.template.spec.containers[0].image}'
kubectlnukeprod -n agent logs github-update-go-watcher-0 --since=24h | grep -c 'reason=emission_in_flight'   # ≥1 once a repo is claimed
```

## Design Decisions

- **Cursor, not a vault check.** `docs/dod.md:30` forbids the vault read, and a task-store check would couple the watcher to a store it cannot reach from the pod.
- **Three clearing events, not one.** Convergence alone cannot clear a claim whose task died before opening a PR — that is precisely the population the `- reemit` repairs exist for. The PR-existence clear is the normal path (a healthy task opens its PR within hours); the TTL is the backstop for the pathological one.
- **Clear on "an update PR exists", not on "a closed PR whose bump is missing".** The latter cannot be implemented here: `completeMergedUpdates` runs after `processRepos` (`pkg/watcher.go:120-132`), `GetMergedUpdatePR` (`pkg/githubclient.go:342-380`) matches an exact head branch and returns false for a closed-unmerged PR, and the claim stores a Go version rather than the emission SHA — so there is no key to associate a closed PR with the claim. Clearing on existence needs no association and no extra call.
- **Guard after the PR gate.** The gate's answer is the claim's primary clearing signal and the PR list is already fetched; checking the claim first would require either a second list call or a claim that never clears when a PR appears.
- **Forced cycles do not bypass.** Spec 003 established that the gate is always-on with no opt-out; this guard inherits that property rather than becoming the first exception.

## Suggested Decomposition

The change spans five code surfaces plus two docs, which exceeds the usual single-change budget. It stays one change because the parts are not independently safe (a claim written but never cleared suppresses a repo permanently; a guard without clearing *is* the suppression defect). The mapping a prompt-creator needs:

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Cursor schema + `markPublished` write + round-trip test | 1, 7 | 9 | — |
| 2 | `processRepos` guard, ordering, and the three clears | 2, 3, 5, 6 | 1–8 | 1 |
| 3 | Skip reason, `FilterSkipReasons` label, pre-init test | 4 | 1, 4, 8 | 2 |
| 4 | `docs/dod.md` + `README.md` skip-reason row | 8 | 11 | 3 |

Review checkpoints, in order: schema round-trip → clearing paths (including the error path) → skip ordering → metric label → docs.

## Security / Abuse Cases

No new surface. The cursor file is already the watcher's own trust boundary and is written only by the watcher; the guard reads it and adds no network call, no credential and no user input. The one abuse-shaped case — an operator forcing a cycle to force an emission — is refused by DB6.

## Out of Scope

- The controller-side supersede refinement (do not abort a prior whose successor is not live and whose repo is still behind).
- The repo-trigger's identifier seed.
- The 7-day fleet-wide observation window that closes the originating task; it is measured by a successor task.
