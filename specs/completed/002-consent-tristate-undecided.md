---
status: completed
tags:
    - dark-factory
    - spec
approved: "2026-08-31T10:02:36Z"
generating: "2026-08-31T11:01:47Z"
prompted: "2026-08-31T11:01:47Z"
verifying: "2026-08-31T12:52:45Z"
completed: "2026-08-31T12:52:53Z"
branch: dark-factory/consent-tristate-undecided
---

## Summary

- Today the consent gate collapses four different states into one verdict. `.maintainer.yaml` absent, `goUpdate` section absent, `autoUpdate` key absent, and an explicit `autoUpdate: false` all produce the identical skip reason `auto_update_disabled`.
- That means "the owner decided no" is indistinguishable from "nobody has ever decided". On 2026-08-30 five actively-maintained repos sat on Go 1.26.6 for days inside the second state while every dashboard stayed green, because a consent skip is a clean no-op — not a failure, not a park, not an abort.
- This spec splits the two apart: an **explicit** `false` stays silent, while an **absent** decision becomes visible as its own skip reason and files one task asking a human to decide.
- The gate itself does not weaken. Absent consent still never produces an update task and still never leads to any action on the repo. The only new output is a request for a decision.

## Problem

`NewAutoUpdateFilter` tests one boolean: `if !candidate.AutoUpdate { return "auto_update_disabled" }`. `Candidate.AutoUpdate` is built as "false when `.maintainer.yaml` is absent", so absence and refusal are the same value by construction.

The consequence is not theoretical. As of 2026-08-30, all ten repos then skipped as `auto_update_disabled` had no `.maintainer.yaml` at all — byte-identical to the state the five laggards were in. Repository activity does not separate them either: two of the deliberately-excluded sample repos were pushed the same week as the maintained laggards. There is no signal anywhere, in logs, metrics or tasks, that distinguishes a repo the operator excluded on purpose from one that fell out by accident.

## Goal

Make "nobody has decided" a distinct, visible state, so a repo can no longer leave the fleet silently.

## Non-goals

- Weakening the consent gate. Absent consent must still never file an update task and never cause any write to the observed repo. Positive opt-in remains the only path to action.
- Defaulting absent consent to true, under any flag, env var, or code path.
- Changing behaviour for `autoUpdate: true` (unchanged) or explicit `autoUpdate: false` (unchanged, still silent).
- Reviving or extending `updater digest`. It has no recipient, no webhook and no scheduler, and is out of scope here.
- Deciding which repos *should* be opted in. That is the operator's call; this spec only surfaces the question.

## Assumptions

- The decision task's consumer is a human reading the vault, reached via assignee `bborbe` — believed to be the same path `go-version-watcher` uses for its release-trigger task *(unverified from this worktree)*; the assignee freeze stands on its own regardless. This spec does not assume anything about how `github-update-go-agent` handles an unknown `task_type`, because a decision task is never addressed to it.
- `pkg/taskid.go`'s documented property — same seed yields the same identifier, so a re-emit is a downstream no-op — holds for a repo-keyed decision seed as it does for the SHA-keyed update seed. DB 8 depends on this; if it does not hold, a suppression store becomes necessary and the size of this change grows by one layer.

## Desired Behavior

Consent becomes three-valued rather than boolean:

| `.maintainer.yaml` state | Consent | Skip reason | Task emitted |
|---|---|---|---|
| `goUpdate.autoUpdate: true` | granted | — (passes gate) | update task, as today |
| `goUpdate.autoUpdate: false` | refused | `auto_update_disabled` | none — silent, as today |
| file absent | undecided | `auto_update_undecided` | one decision task |
| `goUpdate` section absent | undecided | `auto_update_undecided` | one decision task |
| `autoUpdate` key absent | undecided | `auto_update_undecided` | one decision task |

1. Consent parsing distinguishes three outcomes — granted, refused, undecided — where refused requires the `autoUpdate` key to be present and explicitly `false`, and undecided covers an absent file, an absent `goUpdate` section, and an absent `autoUpdate` key.
2. An unparsable `.maintainer.yaml` keeps today's behaviour exactly: the gatherer drops the repo before evaluation, so it is a drop, not a consent verdict, and must never be reported as undecided.
3. Granted behaves exactly as today — passes the gate, update task filed, no change to task shape, identity or assignee.
4. Refused behaves exactly as today — skipped with reason `auto_update_disabled`, nothing emitted, nothing logged beyond the existing skip line.
5. Undecided is skipped for update purposes **and** emits one decision task. This is a new pathway: today a filter verdict is terminal — the watcher increments the skip metric and `continue`s, so nothing downstream of the chain is reachable for a skipped repo. This spec requires a repo to be simultaneously "not updated" and "reported". The implementation must introduce an explicit shape for skip-with-emit rather than smuggling the task out of the filter chain; whether the chain returns a richer verdict, or the watcher branches on the specific reason before continuing, is an implementation choice, but it must be a named one.
6. A decision task is a distinct task **type** from an update task — not merely a differently-worded body. The type is frozen as `github-update-go-decision`. Its **assignee is frozen as `bborbe`**, not `github-update-go-agent`. Both discriminators are deliberate and independent: the type distinguishes the payload, and the human assignee means the decision task is never routed to the update agent in the first place. This is stronger than relying on that agent to ignore an unknown type, which is behaviour of a separate binary in a separate repo that this spec does not control and must not assume.
7. Decision-task identity is **repo-keyed, not SHA-keyed**: the seed is derived from owner and repo only, deliberately excluding HEAD. An undecided repo that receives commits while awaiting a decision must not produce a second task. This is a deliberate departure from update-task identity, which is SHA-keyed because a new commit genuinely warrants a fresh bump.
8. No new suppression state is introduced. A repo-keyed decision identity inherits the dedup contract `pkg/taskid.go` already documents — the same seed always yields the same identifier, so a re-emit is a downstream no-op. Re-publishing on each cycle is therefore acceptable and correct; a local "already filed" store would buy reduced wire chatter that no consumer in the Problem section asked for, at the cost of an extra persistent layer and a crash-between-publish-and-persist failure mode. If the implementation nonetheless needs local state, it must not cause `completeMergedUpdates` to poll for an update PR on a repo that never received one.
9. Because `GoBehindFilter` (position 4) runs before the consent filter, an undecided repo that is already current on Go never reaches the consent verdict and files nothing. The question is raised only when the repo is actually falling behind.
10. The decision task states the situation plainly: this repo has Go, it is behind stable, and nobody has recorded whether it should be updated. It asks for one of two edits to `.maintainer.yaml` — `autoUpdate: true` to opt in, `autoUpdate: false` to record the exclusion — and states that either answer makes the repo silent again.

## Constraints

- Decision-task identity is repo-keyed (DB 7). Reusing the SHA-keyed update seed is a defect, not a shortcut: these repos are actively pushed, so a SHA-keyed decision task refiles on every commit. Repo-keying is also what makes re-emit a downstream no-op, which is why no suppression store is needed (DB 8).
- A decision task carries type `github-update-go-decision` **and** assignee `bborbe` (DB 6). The update agent's assignee is never used for a decision task.
- The new skip reason must appear in the existing skip-reason metric alongside the current ones, so the undecided count is observable without reading logs.
- `README.md`'s existing `auto_update_disabled` row reads "Repo has no goUpdate.autoUpdate: true" — after this change that description is factually the *undecided* case and must be rewritten, not merely appended to. README's "Emitted task contract" section is declared frozen against the shipped agent consumer; a second emitted task type is a change to that contract and must be documented there.
- `FilterSkipReasons` is a closed, exported label set kept in lockstep with `README.md` by an explicit contract in `pkg/metrics.go`. The README must document the new reason in the same change.
- Filter chain order stays frozen. This change alters one verdict branch; it does not reorder, add or remove filters.
- No new configuration list, no flag, no env var. The set of repos evaluated is unchanged.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery |
|---|---|---|---|
| Malformed `.maintainer.yaml` misread as undecided | Repo is dropped with step `maintainer_config` before the consent filter; no verdict, no task | A decision task naming a repo whose config is known-broken | Treat unparsable as a drop ahead of any emit; delete wrongly-filed tasks (repo-keyed, enumerable by name) |
| Explicit `false` misread as absent | The ten repos marked out on 2026-08-31 stay silent | `filter_skipped_total{reason="auto_update_undecided"}` non-zero for a repo known to be explicitly `false` | Revert the release; the explicit-false branch is inverted. Delete the wrongly-filed tasks by name |
| Decision task routed to the update agent | Never occurs — assignee is `bborbe`, not the update agent (DB 6) | A bump PR appears on a repo with no `autoUpdate: true` | Close the PR unmerged, revert the release, treat the assignee freeze as breached |
| Decision task refiled per push | Repo-keyed identity yields one identity regardless of HEAD; re-emit is a downstream no-op | More than one decision task file for a single repo | Duplicates mean the seed still includes HEAD — fix the seed, delete duplicates by name |
| Kafka publish of a decision task fails | Absorbed by the existing publish error path; the repo stays undecided and is re-emitted next cycle, since identity is repo-keyed | `published_total{status="error"}` non-zero | None needed — the next cycle retries; a persistently non-zero error count means the publish path itself is broken |
| Human closes a decision task without editing `.maintainer.yaml` | Refiled on the next cycle — deliberate; the question is unanswered until the config records an answer | A repeatedly-reappearing decision task for one repo | Answer it (`true` or `false`); closing alone is not an answer |
| Undecided repo that is current on Go files a task | Never reaches the consent verdict — `GoBehindFilter` short-circuits at position 4 | Any decision task for a repo already on stable | Chain order was altered; restore the frozen order |

## Security / Abuse Cases

`.maintainer.yaml` is attacker-controlled: any observed repo can write any content. Absence of a key must never be interpretable as consent. The tri-state adds a new reading of absence, so the test suite must prove that neither an absent file, an absent section, an absent key, nor any malformed content can produce the `granted` verdict.

## Acceptance Criteria

- [ ] **AC1 (DB3)** — `goUpdate.autoUpdate: true` passes the gate and files an update task unchanged. Evidence: `make precommit` green; unit test asserts the consent filter returns `""` for a granted candidate.
- [ ] **AC2 (DB4)** — explicit `goUpdate.autoUpdate: false` is skipped with reason exactly `auto_update_disabled` and emits nothing. Evidence: unit test asserts the reason string, and asserts the task-publisher mock records **zero** calls.
- [ ] **AC3 (DB1, DB5)** — absent file, absent `goUpdate` section, and absent `autoUpdate` key each yield reason exactly `auto_update_undecided` and emit exactly one task. Evidence: table test over the three input classes; publisher mock records exactly 1 call per case.
- [ ] **AC4 (DB6)** — the emitted decision task carries type `github-update-go-decision`, and **no** command bearing the update task type is emitted for an undecided repo. Evidence: unit test asserts the built command's type field, plus a negative assertion that zero update-type commands are published across a full undecided cycle.
- [ ] **AC5 (DB7, DB8)** — decision-task identity is repo-keyed: two cycles against the same undecided repo with **different** HEAD SHAs produce the **same** task identifier. Evidence: unit test varying HEAD across cycles, asserting the derived identifier is byte-identical across both and contains no SHA substring; **and** that two different undecided repos yield different identifiers (`id(ownerA/repoA) != id(ownerB/repoB)`). Without the second clause a single hardcoded constant satisfies the AC, collapsing every undecided repo onto one identity so the controller dedups them into a single task naming one repo — the exact invisibility this spec abolishes.
- [ ] **AC6 (DB9)** — an undecided repo already current on Go emits nothing. Evidence: unit test asserts verdict is `go_current` and publisher call count is 0.
- [ ] **AC7 (DB2)** — an unparsable `.maintainer.yaml` is dropped before evaluation with step `maintainer_config`, producing neither an `auto_update_undecided` verdict nor a task. Evidence: assert the drop step is `maintainer_config` (not `gomod_unparsable`, which is the unreadable-`go.mod` path), `IncFilterSkipped` call count is 0 for that repo, and no command is published.
- [ ] **AC8 (Security)** — no consent input yields `granted` except an explicit boolean `true`. Evidence: table test enumerating absent file, absent section, absent key, string `"true"`, integer `1`, `yes`, null, and malformed YAML; each asserts the outcome is never `granted` — the seven non-malformed rows via a nil-error non-granted verdict, the malformed row via a non-nil parse error consistent with AC7's drop-before-evaluation contract.
- [ ] **AC9 (DB6, negative evidence)** — across a full cycle over undecided repos, **zero** published commands carry the update task type and **zero** carry assignee `github-update-go-agent`. Evidence: publisher mock inspected for both fields; counts are 0. (Asserting "no writes to the observed repo" would be vacuous — the GitHub client exposes no write methods.)
- [ ] **AC10 (DB10)** — the decision task body names the repo's current Go version and the stable version, and contains both literal strings `autoUpdate: true` and `autoUpdate: false` so the reader knows the two available answers. Evidence: `grep`-shaped assertions on the built body, following the existing `buildTaskBody` test precedent.
- [ ] **AC11 (Constraint)** — `auto_update_undecided` is pre-initialised in the skip-reason metric and documented in `README.md`. Evidence: `grep -q 'auto_update_undecided' README.md`, and a metrics test asserting the series exists at zero before any cycle.

## Verification

```
make precommit
```

must exit 0. The acceptance criteria above are all satisfiable by unit tests in `pkg/filter`, `pkg/taskbuilder` and `pkg/metrics` — no runtime double is faked at a boundary that would require a scenario.

Deterministic gate: AC2 and AC8 together are the real proof of the explicit-false branch. They do not depend on live fleet state and cannot rot.

Diagnostic only, not a gate: running the built watcher against live fleet state should report an undecided count of zero. The two populations were separated on 2026-08-31 — the five maintained laggards were opted **in** (`autoUpdate: true`, so they now pass the gate and never reach the consent verdict), and the ten deliberate exclusions were marked **out** (`autoUpdate: false`, so they reach the verdict and are refused). Nothing should remain in the undecided branch. Treat a non-zero count as a signal to inspect, not as a failure — a new repo created without a `.maintainer.yaml` between now and verification would legitimately raise it, and that is the feature working.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Tri-state consent parsing; drop-before-evaluate preserved | 1, 2 | AC7, AC8 | — |
| 2 | Consent filter emits the third verdict; chain order frozen | 3, 4, 9 | AC1, AC2, AC6 | 1 |
| 3 | Skip-with-emit pathway named and implemented in the watcher | 5 | AC3 | 2 |
| 4 | Decision task type, frozen assignee, repo-keyed identity, body copy | 6, 7, 8, 10 | AC4, AC5, AC9, AC10 | 3 |
| 5 | Metric label pre-initialisation; README row rewrite + task-contract docs | — | AC11 | 2 |

## Do-Nothing Option

Leave consent boolean. The ten deliberate exclusions were made explicit on 2026-08-31, so today's state is clean and nothing is currently hidden. The cost is that the cleanliness is a snapshot maintained by hand: the next repo created without a `.maintainer.yaml`, or any repo whose config is deleted, silently leaves the fleet again with no signal — the exact failure this spec exists to make impossible.
