---
status: draft
created: "2026-11-24"
---

# Summary

- Every repo that passes the allowlist and has granted update consent receives one full dependency-update task per calendar month.
- The monthly task is emitted even when the repo's Go directive is current and its HEAD has not moved — the cheap Go-version signal is bypassed by design.
- A second emission for the same repo in the same month is a no-op, and the no-op is attributed to the month, not to an unchanged HEAD.
- The next calendar month emits again.
- The forced HTTP trigger can run a monthly pass for an explicit month, so behaviour is observable without waiting for a boundary.
- Existing Go-version emission and its dedup behaviour are unchanged.

# Problem

Today the only emission trigger is "the repo's declared Go version is behind current stable". A repo that is current on Go but has months of dependency drift is invisible to the fleet until someone runs the watcher by hand; the monthly sweep currently depends on an operator reading a recurring task and emitting per-repo tasks themselves. That keeps the dependency graph drifting between Go releases and makes the monthly cadence a human responsibility.

# Goal

The watcher, unattended, files exactly one `github-update-go` task per repo per calendar month for every repo that passes both gates — the operator allowlist and the repo's own `.maintainer.yaml: goUpdate.autoUpdate: true` — regardless of Go version or HEAD movement. The existing Go-version pass keeps working exactly as it does today.

# Non-goals

- No new deployment unit, CronJob or second watcher.
- No change to the agent that consumes the tasks (`github-update-go-agent` already does toolchain + deps + vuln scan + `make precommit`).
- No emission for repos that refused or never gave consent.
- No retry of a failed monthly task inside the same month.
- No change to the frozen task-identity namespaces or the existing `Derive*TaskID` seeds.

# Acceptance Criteria

- [ ] A monthly pass emits exactly one task for a repo that is Go-current and whose HEAD is unchanged. Evidence: the new test for this case passes and asserts one published command whose frontmatter carries `update_month: <YYYY-MM>` and a `task_identifier` equal to `DeriveMonthlyTaskID(owner, repo, month)`.
- [ ] A second monthly pass in the same month publishes nothing for that repo and logs the literal `month_unchanged`. Evidence: the test asserts zero publishes and the captured log contains the line `month_unchanged`; `grep -c month_unchanged <captured log>` returns ≥ 1.
- [ ] A monthly pass for the next month emits again for the same repo. Evidence: the test runs the pass for `M` then `M+1` with HEAD unchanged and asserts one publish in each, with distinct `task_identifier`s.
- [ ] `DeriveMonthlyTaskID` is stable for the same (owner, repo, month) and differs across months. Evidence: table test asserts equality within a month and inequality across two months.
- [ ] A malformed `month` query parameter is rejected. Evidence: handler test asserts HTTP 400 with body `{"status":"error","error":"invalid month, want YYYY-MM"}`.
- [ ] The normal cycle is unchanged. Evidence: `Poll(ctx, force, "")` still skips a Go-current repo — the regression test asserts zero publishes, and the pre-existing suite stays green.
- [ ] **Post-Deploy (Rung-2):** on dev, one forced monthly pass emits one task per gate-passing repo and a second pass in the same month emits none. Evidence: task count in the `openclaw` vault for the month equals the expected set after pass 1 and is unchanged after pass 2.
  - `deploy_check:` `kubectlnukedev -n agent get statefulset github-update-go-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)` at the deployed release

# Verification

## Container-executable

- `ROOTDIR=/workspace make precommit` exits 0.

## Operator-executable

- After the dev deploy, `curl -sS -X POST 'http://<dev-watcher>/trigger?monthly=1&month=<YYYY-MM>'` returns 202, and the `openclaw` task store gains one `github-update-go` task per gate-passing repo carrying `update_month: <YYYY-MM>`.
- A second call with the same month returns 202 and the task count is unchanged.
- A call with `month=<next YYYY-MM>` emits again.

# Desired Behavior

1. The watcher's poll cycle accepts a month. An empty month preserves today's behaviour exactly; a non-empty month additionally runs a monthly pass over the same candidate list.
2. The monthly pass keeps the allowlist, `go.mod` present/parsable and consent gates, and does not use the Go-behind filter or the HEAD-unchanged filter.
3. A repo whose cursor already records the current month is skipped with the reason `month_unchanged`; the reason surfaces through the existing skip-log path, once per repo per cycle.
4. A repo that passes the monthly chain gets a task titled `Monthly Deps Update <owner>-<repo> <YYYY-MM>` with `update_month` frontmatter, and the cursor records the month after a successful publish.
5. The monthly task identifier is a UUID5 over the month in its own namespace, so the same repo+month is always the same identity and never collides with the Go-version, decision or stale-ignore identities.
6. `POST /trigger?monthly=1[&month=YYYY-MM]` runs a monthly pass; `month` defaults to the current calendar month; a malformed month returns 400 and runs nothing.
7. A repo may legitimately receive both a Go-version task and a monthly task in the same month.

# Constraints

- The existing task-identity namespaces and seeds are frozen; the monthly namespace is derived from the frozen one and added, never substituted.
- The monthly pass must not emit for a repo without granted consent.
- No new environment variable, deployment unit or external dependency.
- The cursor file must remain backward compatible: a cursor written by the previous binary loads, and the new field is omitted when empty.
- The monthly skip reason is exactly `month_unchanged`.
- Existing tests must pass unchanged.

# Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Cursor file lost or reset | The month gate reads empty, so the next cycle re-emits once for every repo | Downstream dedup on the month-keyed identifier makes the re-emit a no-op; no action needed |
| Two watcher pods run concurrently | Both may emit; the deterministic identifier dedups downstream | Restore single-replica operation; the store keeps one task |
| GitHub API failure mid-cycle | The cycle degrades as it does today; no partial month is recorded for the failed repo | The next cycle retries; the month is recorded only after a successful publish |
| Malformed month in a forced trigger | 400, no cycle started | Caller fixes the parameter |
| Cursor write fails after a publish | The task exists but the month is not recorded, so the next cycle re-emits | Downstream dedup absorbs it; investigate the PVC |

# Security / Abuse

- The trigger endpoint takes no owner, repo or scope parameter: a forced monthly pass can only re-examine repos that already pass the allowlist and the consent gate.
- The month parameter is validated against `YYYY-MM` before any cycle starts, so it cannot be used to widen the candidate set.
- The consent gate is preserved for the monthly path; a repo that refused consent is never emitted for.

# Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Monthly task identity + task builder + `update_month` frontmatter | 4, 5 | 1, 4 | — |
| 2 | Cursor month state + `month_unchanged` filter | 3, 4 | 2, 3 | prompt 1 |
| 3 | Watcher `Poll` month parameter + poll-loop wiring | 1, 2, 7 | 1, 2, 3, 6 | prompts 1, 2 |
| 4 | Trigger handler `monthly`/`month` parameters + validation | 6 | 5 | prompt 3 |
| 5 | Regression coverage for the unchanged normal cycle | — | 6 | prompt 3 |

Rationale: prompt 1 establishes the identity and envelope the rest builds on; prompt 2 adds the state and the skip reason; prompt 3 wires the cycle; prompt 4 exposes it; prompt 5 locks the unchanged path.

# Do-Nothing Option

The monthly sweep stays an operator task: a recurring vault task instructs a human to emit per-repo update tasks by hand each month. Dependency drift accumulates between Go releases, the sweep happens only when someone remembers, and every missed month widens the next diff.
