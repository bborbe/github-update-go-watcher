---
status: approved
approved: "2026-11-12T18:23:58Z"
branch: dark-factory/spec-007
---

## Summary

- The emission claim (`InFlightTarget` / `InFlightSince`, spec 005) stops suppressing only on four events, and **none of them is "the emission's task can no longer progress"**. A task that reaches a terminal status, or is parked at `phase: human_review` where no Config dispatches it, leaves a live claim behind — and the claim then suppresses every re-emit for `InFlightTTL` (14 days).
- Measured 2026-11-12 on prod (`github-update-go-watcher-0`, ns `agent`): **all 42 repos behind `go 1.27.4` were skipped `reason=emission_in_flight`, and that set is exactly the behind-set** in both directions. `bborbe/auth-http-proxy` (newest record `status: completed`, earlier record `aborted`) and `bborbe/obsidian-redirector` (`status: completed`) both still carry live claims, which proves the claim is cursor-side and outlives the record.
- Fix: the watcher subscribes to the task lifecycle event topic (the same stream `github-pr-watcher` already consumes) and ends a claim whose emitted task reached a terminal status, once, dropping the dedup keys so the unchanged-HEAD gate cannot re-suppress the re-emit.

## Problem

`emissionInFlight` (`pkg/watcher.go:390-414`) ends a claim on two conditions — `!candidate.GoBehind()` (`:405`) and `inFlightExpired` (`:409`) — and `gateRepo` (`:339-356`) on one more, an observed open `fix/update-go-*` PR (`:351`). A fourth condition, a changed target version (`:398`), stops the claim *matching* without clearing it. For a repo whose emission task died, those resolve as follows:

| # | condition that ends suppression | clears the cursor record? | fires on a failed emission? |
|---|---|---|---|
| 1 | the repo converges — `!candidate.GoBehind()` | yes | **No** — the repo is still behind by definition |
| 2 | `InFlightTTL` (336h) elapsed | yes | **Yes, in 14 days** — the only signal the case has |
| 3 | a new stable release moves `LatestGo`, so `InFlightTarget != target` | no — the stale claim stays on disk | **Only if a new Go release lands** |
| 4 | an open `fix/update-go-*` PR is observed | yes | **No** — a failed emission opens no PR |
| **5** | **the emitted task can no longer progress** | **— absent —** | **this is the defect** |

"Can no longer progress" is a **dispatchability** question, not a status string. A record is progressing only if a Config will still pick it up; two populations cannot: a terminal status (`completed`, `aborted`), and `phase: human_review`, which no Config dispatches and which a record cannot leave on its own. Measured 2026-11-12 in `private-agent/tasks/`: **36 records at `status: in_progress` + `phase: human_review`**, plus 3 at `status: hold` + `human_review` — 39 non-terminal, parked, and invisible to every existing clearing path.

The genuinely in-flight case is protected by ordering, not by a status test: `gateRepo` runs **before** `emissionInFlight`, so a `human_review` row whose update PR is open is already skipped and its claim already cleared by the PR gate. What reaches the new condition is a parked row with **no** PR — the dead case.

The TTL's own comment (`:250-257`) calls it "the backstop for an emission claim whose task never opened a pull request" — which is exactly the broken case, and it is the case's only signal.

The cost is a 14-day freeze on repos that need no code change: 31 of the 42 carry only the image-target cause and would move on a single re-emit.

**Constraint that shapes the fix.** `docs/dod.md:30` freezes that the watcher runs in a pod with no vault filesystem access and cannot query the task store. A "check the task's status" read is therefore unavailable — but the task lifecycle **event stream** is not a store query, and `github-pr-watcher` already consumes it for the same purpose (release a suppression when a task reaches a terminal status).

## Reproduction

Cursor state for a repo whose emission task has ended: `last_seen_head_sha = <sha>`, `last_seen_go_version = "1.27.4"`, `in_flight_target = "1.27.4"`, `in_flight_since = <now-3d>`. The repo's default-branch `go.mod` still reads `go 1.27.1`; no `fix/update-go-*` PR is open; the task filed for `(repo, 1.27.4, <sha>)` reached `status: aborted`.

The reproduction is one poll cycle against that fixture. **It is driven through the Ginkgo suite, not by `go run .`** — the binary requires `APP_ID` / `INSTALLATION_ID` / `PEM_KEY` and exits at `auth.ResolveGitHubClient` before any cycle when they are empty. The suite constructs the watcher with mocked `GitHubClient` / `GoDevClient` / `TaskPublisher`, writes the fixture cursor, and asserts the counters:

```bash
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='emission'
```

- `openUpdatePRGate`: no open PR → passes.
- `emissionInFlight`: target matches, `GoBehind()` is true, `inFlightExpired` is false → **suppresses**.
- `markPublished`: never runs, so the claim is never replaced.

**Observed:** the cycle logs `repo skipped repo=<owner>/<repo> reason=emission_in_flight` and `github_update_go_watcher_filter_skipped_total{reason="emission_in_flight"}` increments.
**Expected:** the claim is cleared, the repo is emitted, and the metric stays flat.

The same shape reproduced at fleet scale on 2026-11-12: 42 repos skipped, every cycle.

## Goal

A repo whose emission task can no longer progress — terminal, or parked where nothing dispatches it — is re-emittable on the next watcher cycle, while duplicate suppression for a genuinely live emission is unchanged, including the two negative controls spec 005 established (a GitHub error never clears; a forced cycle never bypasses).

## Non-goals

- **`InFlightTTL` is not shortened.** It stays the 336h backstop for a claim whose task is still running (or whose non-progressing event was missed). This adds the missing signal; it does not retune the fallback.
- **`status: hold` ends a claim.** A hold is a block with no scheduled resume date — nothing dispatches it and no clock brings it back — so a claim covering it freezes the repo for the whole `InFlightTTL`. Three repos (`backup`, `crypto`, `pr-review-fixtures`) were stuck on exactly this, measured 2026-11-13. Added after v0.9.1 shipped with terminal + `human_review` only.
- **The once-per-task bound is per claim generation, not per process lifetime.** The clear re-arms a claim with the SAME identifier (a failed emission never moved HEAD), so a permanent bound froze the repo until restart: `lock` and `github-vuln-watcher` were skipped `emission_in_flight` 33 times in 3h with their records already observed and zero publishes. `markPublished` re-arms the entitlement for the identifier it writes, and drops it from that cycle's pending consume list — the re-arm would otherwise be undone by `consumePending`, which runs after `SaveCursor`.
- **`phase: human_review` DOES end a claim, and that is deliberate.** A record parked there is owned by nobody ([[A github-update-go Record Routed to human_review Is Owned by Nobody]] closed, and 39 records are still parked), so treating it as live is what freezes the pipeline. It is not "terminal" in the state-machine sense — the distinction this spec draws is **dispatchability**, and the open-PR gate above it keeps a genuinely in-flight review suppressing.
- **The open-PR gate, the `sha_unchanged` two-key rule, the task identity seed and the consent filters are unchanged.**
- **No vault read, no task-store query, no new chart value, no new PVC.**
- **The controller-side supersede refinement and the repo-trigger's identifier seed are untouched.**

## Do-Nothing Option

42 repos stay frozen for up to 14 days per failed emission, and the same freeze recurs for every future emission whose task dies. Recovery requires either waiting out the TTL or hand-opening a PR per repo to clear its claim. Pipeline throughput becomes a function of how often tasks fail, not of how often Go releases.

## Desired Behavior

1. **A task lifecycle consumer runs beside the poll loop.** A new `run.Func` is passed to `service.Run` in `main.go` alongside the existing poll loop and HTTP server. It subscribes to `agentlib.TaskV1SchemaID.EventTopic(prefix)` — derived from the schema id and the configured prefix, never a bare literal, or it subscribes to the wrong topic under a prefix — and it never returns on a recoverable error: `service.Run` wraps every func in `run.CancelOnFirstFinishWait` (`github.com/bborbe/service` `service_run.go:29`), so a return would tear down the poll loop and the HTTP server.

   **Offset state is in-process, so a restart replays the topic from its oldest offset.** That is the `github-pr-watcher` precedent, and here the replay is a feature rather than a cost: it rebuilds the non-progressing set from history, so the first deploy ends the pre-existing frozen claims without an operator sweep. The prompt carries the concrete constructor wiring; this spec fixes only the behaviour.

2. **The handler records identifiers whose task can no longer progress.** The event payload decodes to `agentlib.Task`; the task identifier is the message key. An empty payload is a delete event and is ignored. **Non-progressing** is `status ∈ {completed, aborted}` **or** `phase == human_review` — the two populations no Config will dispatch. `Frontmatter.Phase()` returns a pointer, so an absent phase is neither terminal nor parked and is ignored. Every other status (`in_progress`, `next`, `backlog`, `hold` at a dispatchable phase) is ignored: a queued or running task must keep its claim.

3. **A non-progressing identifier is stored as observed, with a consumed flag.** The store answers two questions: *has this identifier been observed non-progressing*, and *may this cycle consume the entitlement to end a claim for it*. Both are local reads — no clock, no GitHub call, no cursor access. No sighting counter: the second question is a boolean, and nothing reads a count.

4. **The claim is matched by REPO, not by a derived task identifier.** A derived identifier cannot be made to agree: the watcher derives it from its cursor (`InFlightTarget` + `LastSeenHeadSHA`) while a record's `task_identifier` is fixed at the SHA it was minted at, and the two diverge whenever the record was re-pointed or created by another path. `bborbe/backup` was skipped `emission_in_flight` with its record already observed (`observed=1`) for exactly this reason — the derived identifier never equalled `b220e13f…`. Every record carries `repo: <owner>/<name>` in frontmatter, so the consumer keys on that and the watcher passes `repo.String()`. The `task_identifier` derivation was the original design and is recorded here as the defect it proved to be.

4b. **`emissionInFlight` gains the fifth condition, evaluated after the target-match guard and after the convergence branch, before the TTL branch.** The claim's task identifier is derived, not stored: `DeriveTaskID(repo.Owner, repo.Name, state.InFlightTarget, state.LastSeenHeadSHA)`. This is exact by construction — `markPublished` writes `LastSeenHeadSHA`, `LastSeenGoVersion` and `InFlightTarget` in one assignment from the same candidate, and a live claim suppresses every later publish, so no later write can move `LastSeenHeadSHA` out from under a live claim. No new cursor field is added and no live claim needs a migration. An empty `LastSeenHeadSHA` yields no identifier and therefore no clear.

5. **Ending a failed emission drops the claim, and the dedup keys only when the re-emit would carry a new identifier.** The `CompletedHeadSHA` merge marker always survives. The re-emit is worth making only when HEAD moved: then the new `(target, HEAD)` derives a new identifier, no record exists at it, and the task it files will run and be re-observed — so the dedup keys drop (`clearFailedEmission`) and the repo is emitted. When HEAD has **not** moved the re-emit is identity-identical: the controller finds the existing record and does nothing, no new run exists to re-observe the repo, and a re-armed claim could only be cleared by `InFlightTTL`. That case keeps the `sha_unchanged` dedup keys (`clearInFlight`) and is skipped as `sha_unchanged` — bounded, and true, since an emission did happen at this HEAD. Measured 2026-11-14: `pr-review-fixtures`, `claude-wrapper` and both `build-fix-canary` repos sat frozen on exactly this, their `hold` records' mtimes all predating their publish. `completeMergedUpdates` skips a repo with an empty `LastSeenHeadSHA` for one cycle, which is harmless — a failed emission has no merged PR to complete.

6. **The re-arm fires only when the emission carries a new identifier, and the clear fires once per claim generation.** `markPublished` re-arms the entitlement only for a publish whose `(target, HEAD)` differs from the one the cursor already records; an identity-identical publish writes the dedup key and no claim at all. That is what stops a forced (trigger) cycle — which omits `SHAUnchangedFilter` and can therefore re-publish the same HEAD — from re-creating a claim the ordinary cycle would never write. Together with DB5's same-HEAD branch this makes the bound real rather than nominal: a claim is only ever re-armed for a record that does not yet exist, so a repo whose parked record never re-runs cannot be re-armed into a freeze. The entitlement is still consumed by the first cycle that uses it, so a repeat non-progressing sighting of the same repo does not clear again. ⚠️ The earlier form of this box said a same-identifier re-emit "keeps its claim and waits for `InFlightTTL`, exactly as today" — that is the defect, not the bound: against a `hold` record nothing ever re-runs, so the wait is a pure 14-day freeze.

   **The bound is process-scoped, and that is stated rather than hidden:** the entitlement lives in the in-process store, and a restart replays the topic from the oldest offset, which rebuilds it. A deterministically failing repo therefore re-emits **once per pod restart**, not once per task lifetime. That is bounded and visible (one extra emission per failed repo per restart), and it is accepted in exchange for a store that needs no file, no PVC and no clock. Persisting the consumed flag would need all three.

7. **Suppression is otherwise unchanged.** A live claim with no terminal observation, no convergence, no open PR and no TTL expiry still suppresses with `reason=emission_in_flight`; a different target version still stops matching; a GitHub error still never clears; a forced cycle still does not bypass; a store error leaves the claim intact.

8. **Documentation in the same change.** `docs/dod.md`'s in-flight paragraph names the fifth condition, defines it as *dispatchability* (terminal **or** `phase: human_review`), and states the once-per-task bound and that the set is rebuilt from the event topic on restart. `README.md`'s `emission_in_flight` row (`:82`) changes "the three clearing events" to four and names the new one. `specs/in-progress/005-…md` Desired Behavior 3 (`:62-65`) lists five conditions with a pointer to this spec, and its Failure Modes row at `:84` changes its recovery from "TTL expires after 336h" to "the non-progressing event ends the claim on the next cycle; the TTL remains the backstop".

## Constraints

- No vault, task-store or filesystem read. The non-progressing set lives in the process; the event stream is subscribed, not queried. `docs/dod.md:30` is a durable contract.
- No new cursor field, no new chart value, no new PVC, no new config knob, no new GitHub API call.
- **One new module dependency:** `github.com/bborbe/kv`, for the in-process offset store — the upstream package ships no public memory constructor, so the repo carries a minimal implementation. It is already in the module graph; `go mod tidy` moves it out of the indirect block.
- Do not change `InFlightTTL`, `DeriveTaskID`, `openUpdatePRGate`'s semantics, or `SHAUnchangedFilter`'s rule. The dedup-key drop happens in the cursor, not in the filter.
- The cursor file must stay readable by an older binary — this change adds no field.
- The consumer must never return on a recoverable error.
- The non-progressing set is exactly three populations: a terminal status, `status: hold` (a block with no scheduled resume date), and `phase: human_review`. Do not widen it to `next` or `backlog` — those are queued waits the claim exists to cover.
- Ending a failed emission and clearing on convergence stay separate code paths: they differ in whether the dedup keys survive, and collapsing them would either lose the re-emit or clear dedup state on convergence.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Task ends `aborted`/`completed`, repo still behind | Claim ended on the next cycle; one `PublishCreate`; dedup keys dropped so the unchanged-HEAD gate passes | Normal emission path; a fresh claim is written |
| The re-emitted task stops progressing again at the same HEAD | Second sighting — entitlement already consumed, claim kept, TTL governs | 14-day backstop, as today; a HEAD move produces a new identifier and a fresh entitlement |
| Terminal event arrives while a poll cycle is mid-flight | The end applies to the next cycle; the current cycle already evaluated its claim | Bounded to one cycle |
| Consumer restarts / pod restarts | Replay from the oldest offset rebuilds the non-progressing set and re-arms every consumed entitlement; offsets are in-process by design | Self-healing — the replay also ends claims left by pre-fix failures. Cost: a deterministically failing repo re-emits once per restart (bounded, visible) |
| Event topic unavailable | The consumer logs and retries; the poll loop keeps running with the TTL as its only backstop | Fix the broker; no state is lost because replay is from the oldest offset |
| Malformed event payload | The handler is wrapped in a skip-errors sampler; the consumer keeps consuming | Nothing — one skipped event cannot wedge the loop |
| Non-progressing event (`completed`, `aborted`, or `phase: human_review`) | Recorded; the next cycle may end the claim once | — |
| Status event at a dispatchable phase (`in_progress`, `next`, `backlog` at `planning`/`execution`/`ai_review`) | Ignored — the claim must survive a queued or running task | — |
| A `human_review` row whose update PR is open | The PR gate runs first: repo skipped `reason=open_update_pr`, claim cleared there; the new condition never sees it | — |
| Delete event (empty payload) | Ignored explicitly | — |
| A `- reemit` hand-authored task stops progressing | Its identifier is not a `DeriveTaskID` seed, so it can never match a claim | — |
| Forced cycle with a live claim and no terminal observation | Still suppressed — the new condition ends a claim only on evidence, and a forced cycle supplies none | — |
| Store read fails | Claim left intact, repo suppressed, error logged at warning | Next cycle retries the read |
| **The in-process store grows with topic history** | Bounded by the number of non-progressing task identifiers the topic has ever carried — one short UUID each, replayed in full on every restart. **Accepted rather than evicted, and this is a new choice, not an inherited one:** the sibling watcher's in-process DB holds offsets and a *pruned* guard ledger, so it is not a precedent for unbounded growth here. Eviction would need a clock the store deliberately does not have | Revisit if the replay becomes a startup-time problem; the ceiling is the topic's non-progressing-task cardinality |

## Acceptance Criteria

- [ ] **A non-progressing task ends the claim and the repo is emitted.** Fixture: cursor with `in_flight_target: "1.27.4"`, `in_flight_since: <now-1h>`, `last_seen_head_sha: <sha>`, `last_seen_go_version: "1.27.4"`; `go.mod` at `1.27.1`; no open PR; the store reports `DeriveTaskID(owner, repo, "1.27.4", <sha>)` non-progressing with the entitlement available. → exactly **1** `PublishCreate` for that repo, `filter_skipped_total{reason="emission_in_flight"}` stays **0**, and the decoded cursor's `InFlightSince` is **the current cycle's time**, not the stale hour-old value. — evidence: publisher spy call count + metric counter + decoded-struct assertion.

  ⚠️ **The cursor is not left empty, and asserting that would fail against a correct implementation.** A successful publish re-arms a fresh claim via `markPublished`, so `InFlightTarget` reads the target again and `LastSeenHeadSHA` advances to the current HEAD. The discriminating evidence is the **publish count** (1 versus 0 when suppressed) plus the **re-stamped `InFlightSince`**; "empty claim" is the shape only when the publish itself fails.
- [ ] **A `human_review`-parked task ends the claim too.** Same fixture, but the observed task is `status: in_progress` + `phase: human_review`, no open PR → exactly **1** `PublishCreate`, counter **0**, `InFlightTarget` empty. — evidence: spy count + metric counter + decoded struct. This is the population a terminal-only predicate would miss (39 records measured).
- [ ] **A queued or running task does not.** Same fixture, the observed task is `status: in_progress` + `phase: execution` (or `next` / `backlog`) → **0** `PublishCreate`, `{reason="emission_in_flight"}` increments, `InFlightTarget` intact. — evidence: spy count + metric counter + decoded struct. Without this the widening turns the claim off.
- [ ] **The fix is failing-first.** The same fixture against pre-fix code (no terminal store wired) yields **0** `PublishCreate` and `filter_skipped_total{reason="emission_in_flight"}` = **1**. — evidence: the RED run's own assertion output, recorded in the prompt log before the implementation (the build container masks `.git`, so a `git log` proof is not runnable there).
- [ ] **The consumer is actually composed, not merely declared.** `main_test.go` extracts the `service.Run(...)` call block from `main.go` and asserts it contains `NewTerminalClaimsConsumer` — scoped to the call block, not a file-wide count, so a constructor that is declared and tested but never passed cannot pass. — evidence: the test green, and red when the argument is replaced by a no-op `run.Func` (verified: it fails with "the terminal-claims consumer must be a run.Func passed to service.Run").
- [ ] **A second sighting does not end the claim.** Same fixture, the entitlement already consumed → **0** `PublishCreate`, `{reason="emission_in_flight"}` increments, and the decoded cursor still carries `InFlightTarget`. — evidence: spy count + metric counter + decoded struct.
- [ ] **A live claim with no non-progressing observation still suppresses.** Claim live, repo behind, store reports nothing non-progressing → **0** `PublishCreate`, `{reason="emission_in_flight"}` increments. — evidence: spy count + metric counter.
- [ ] **The four existing conditions still work.** Convergence (`go.mod` at `1.27.4`), an observed open `fix/update-go-*` PR, TTL expiry (`in_flight_since = <now-337h>` with a HEAD differing from `last_seen_head_sha`), and a different target version each still stop suppression, with the pre-existing assertions unchanged. — evidence: the existing `emissionInFlight` / `gateRepo` suite green, plus one decoded-struct assertion per condition.
- [ ] **Negative control — a GitHub error still does not clear.** `HasOpenUpdatePR` stubbed to a non-fatal error → repo dropped, `InFlightTarget` unchanged, 0 `PublishCreate`. — evidence: decoded struct + spy count.
- [ ] **Negative control — a forced cycle does not bypass.** Live claim, `force=true`, no terminal observation → 0 `PublishCreate` and `{reason="emission_in_flight"}` increments. — evidence: spy count + metric counter.
- [ ] **A store error does not clear.** The store returns an error → the claim is unchanged and the repo is suppressed. — evidence: decoded struct + spy count 0.
- [ ] **The consumer records only non-progressing tasks.** Handler fed six events — `completed`, `aborted`, `in_progress`+`human_review`, `in_progress`+`execution`, `next`+`planning`, and an empty-payload delete — records the first three and ignores the last three, and the entitlement is available exactly once for a single-observation identifier. — evidence: store read after each event.
- [ ] **The consumer never returns on a recoverable error.** A handler error is skipped and the loop continues until `ctx` is cancelled. — evidence: the consumer test cancels the context and asserts a `nil` return with a non-zero handled-message count.
- [ ] **Docs updated in the same change.** `docs/dod.md` names the fifth condition and the once-per-task bound (`grep -c 'terminal' docs/dod.md` is ≥ 1 **and** `grep -c 'once-per-task' docs/dod.md` is ≥ 1); `README.md`'s row no longer says three (`grep -c 'four clearing events' README.md` is ≥ 1); spec 005 lists five (`grep -c 'terminal status' specs/in-progress/005-*.md` is ≥ 1). — evidence: the four counts quoted. Each count is 0 today, so a stray mention of the word "terminal" cannot satisfy it.

## Suggested Decomposition

The change spans a new consumer, a new store, a cursor-clearing path, a wiring change and three docs — more than one safe change. It stays one spec **and one prompt** because the parts are not independently safe: a consumer with no clearing path observes and does nothing; a clearing path with no consumer ends claims on nothing; a store whose entitlement is never consumed re-emits every cycle. Splitting them would ship intermediate states that compile and suppress nothing.

The repo's own precedent is prompt 014, which carried spec 005's guard, its three clears, the new skip-reason label and twelve specs in one prompt. This spec follows it.

The table is therefore the **implementation order and review sequence within one prompt**, not a prompt plan — each row is a checkpoint a reviewer walks, in this order:

| # | Implementation step | Covers DBs | Covers ACs |
|---|---|---|---|
| 1 | In-process kv DB + terminal-claims store (observed non-progressing, entitlement) | 3 | 10 |
| 2 | Task-lifecycle consumer + handler (topic, non-progressing predicate, ignore rules) | 1, 2 | 10, 11 |
| 3 | The fifth condition in `emissionInFlight` + failed-emission clear + once-per-task bound | 4, 5, 6 | 1–9 |
| 4 | `main.go` composition (third `run.Func`) | 1 | 3 |
| 5 | Docs: `docs/dod.md`, `README.md`, spec 005 amendment | 8 | 12 |

Review checkpoints, in order: store semantics → the handler's non-progressing predicate and ignore rules → the clearing path and dedup-key drop → the composition AC (a consumer that exists but is never wired must fail) → docs.

## Verification

### Container-executable

```bash
ROOTDIR=/workspace make precommit    # format + lint + test + security; must exit 0
go test ./pkg/... -count=1           # the Ginkgo suites behind the acceptance criteria
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='emission'   # the claim suite alone
grep -n "terminal\|once-per-task" docs/dod.md
grep -n "four clearing events" README.md
awk '/service.Run\(/,/\)/' main.go
```

`ROOTDIR=/workspace` is required, not cosmetic: `Makefile.variables` derives `ROOTDIR` from `git rev-parse --show-toplevel` and the build container masks `.git`, so an unqualified `make` dies before running any target. Never `cd` to a host path — the repo is `/workspace` in-container.

The failing-first evidence is the RED run's own output: the non-progressing-observation fixture asserts 1 publish and reads 0, with `filter_skipped_total{reason="emission_in_flight"}` at 1. The RED output is recorded in the prompt log, not committed — dark-factory owns the commit and `.git` is masked in this container.

### Operator-executable

Deployment is not in this repo: `Makefile.docker:61-63` makes `apply` a no-op (`skip apply — publish-only`), so `BRANCH=dev make buca` publishes an image and moves no pod. The watcher runs as a StatefulSet named `github-update-go-watcher` in namespace `agent`, pinned by the Helm release after the image is mirrored. Prod was verified live on 2026-11-12 at `v0.8.0`, ready 1/1.

After the pin is bumped, the live observation is:

```bash
kubectlnukeprod -n agent get statefulset github-update-go-watcher \
  -o jsonpath='{.status.readyReplicas} {.spec.template.spec.containers[0].image}'
kubectlnukeprod -n agent logs github-update-go-watcher-0 --since=30m | grep -c 'reason=emission_in_flight'
kubectlnukeprod -n agent logs github-update-go-watcher-0 --since=30m | grep -E 'published CreateTaskCommand.*latest_go=1\.27\.4'
```

The skip count must fall below the pre-fix 42 for the repos whose tasks can no longer progress, and the emission grep must name at least one previously-frozen repo. This observation is the post-deploy confirmation, not a `spec-verifier` gate — the gating evidence for every AC above is the in-container suite.

## Design Decisions

- **Event stream, not a store read.** `docs/dod.md:30` forbids the vault read; the task lifecycle topic is already consumed by `github-pr-watcher` for exactly this purpose (release a suppression when a task reaches a terminal status), so this is a proven shape rather than a new coupling.
- **In-process store, replay on restart.** The sibling watcher accepts replay-from-oldest because the downstream effect is idempotent. Here the replay is a feature: it reconstructs the non-progressing set from history, so the first deploy ends the pre-existing 42 claims without an operator sweep.
- **The identifier is derived, not stored.** An `InFlightTaskID` cursor field would duplicate information `LastSeenHeadSHA` + `InFlightTarget` already carry exactly, and would need a migration for every live claim.
- **Drop the dedup keys when ending a failed emission.** Without it the re-emit is suppressed by `SHAUnchangedFilter` and the whole path is inert — the failure mode a naive implementation would ship.
- **Once-per-task bound.** The alternative — end the claim on every non-progressing sighting — turns a 14-day re-emit period into a 10-minute one for any repo whose task deterministically completes without converging. Bounding at one attempt per identifier keeps the blast radius to "one extra emission per failed emission".
- **`completed` ends a claim as well as `aborted`.** A `completed` task that left the repo behind did not achieve the bump (deps-only PR, `no_update_needed`), so its emission failed in the only sense that matters here: the repo did not converge.
- **Not the executor ending the claim.** That needs `agent-task-controller` to learn the watcher's address and a second deploy; the event already exists.

## Security / Abuse Cases

No new surface. The consumer reads a topic the watcher's own cluster already publishes, and the store holds one UUID per non-progressing task. No credential, no user input, no new network egress. The one abuse-shaped case — forcing a cycle to force an emission — remains refused, because the new condition ends a claim only on an observed non-progressing event.
