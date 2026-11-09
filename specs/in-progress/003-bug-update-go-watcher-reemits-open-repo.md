---
status: approved
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-09-05T11:58:46Z"
branch: dark-factory/bug-update-go-watcher-reemits-open-repo
---

## Summary

- The github-update-go-watcher re-emits a github-update-go task for a repo on EVERY new commit, even while that repo already has an open (in-flight) update.
- Measured 2026-09-05: 216 open github-update-go tasks across only 81 unique repos — bborbe/agent-task-executor holds 13 open tasks (one per commit) while 3 of its `fix/update-go-*` PRs are still open.
- The watcher runs in a k8s pod with no vault access, so it cannot query the task store. The GitHub open-PR state IS the in-flight signal: a repo with an open `fix/update-go-*` PR has an update already underway.
- Fix: a new always-on gate — a repo with an open `fix/update-go-*` PR is never emitted, on any cycle, regardless of head_sha.

## Problem

Task identity is a deterministic UUID5 of (owner, repo, head_sha), and the filter chain dedups only on head_sha. A new commit produces a new SHA, a new task id, and a re-emit — no check asks whether an update task is already in flight for the repo. Result: the OpenClaw queue grows monotonically per commit while the agent drains it. The OpenClaw task store (`~/Documents/Obsidian/OpenClaw/tasks`) shows 216 open github-update-go tasks across 81 repos; bborbe/agent-task-executor has 13 (all at different refs, each a separate commit), bborbe/vault-cli 12, bborbe/agent-task-controller 7, bborbe/agent-sentry-issue-analyzer 7.

## Goal

At most one open github-update-go task per repo. A repo with an update already in flight is never re-emitted, regardless of head_sha. New commits to a repo holding an open update PR add no tasks.

## Non-goals

- The existing-duplicate backlog (216 open tasks) is NOT collapsed by this spec — the live agent-task-controller drain collapses duplicates as jobs complete; this spec only stops new growth.
- The slash-command watcher (`~/.claude/commands/github-update-go-repo-watcher.md`) is NOT changed — already fixed separately (guard B, 2026-09-05).
- The gate gets NO opt-out knob — a per-repo or global disable would re-introduce the bug; if a future consumer demands variation, that is a separate spec.

## Do-Nothing Option

Not fixing leaves the OpenClaw queue growing monotonically per commit while the agent drains it. The drain is structurally outpaced: every commit to an actively-developed repo adds a task, while the agent completes tasks one at a time (`maxConcurrentJobs: 1`). Growth is unbounded for the most-active repos (13 for `agent-task-executor`), drowning the board and forcing every WIP review to re-litigate the same group.

## Reproduction

Version: prod image deployed 2026-09-05 (pre-fix).

1. Watcher polls the org; repo R has `goUpdate.autoUpdate: true`, is behind stable Go, and already holds an open `fix/update-go-*` PR (update in flight).
2. Any new commit to R's default branch advances head_sha.
3. Next poll: the chain dedups only on head_sha; the new SHA passes `sha_unchanged`; the watcher publishes a create-task command for `DeriveTaskID(owner, repo, new_head_sha)`.
4. Repeat per commit — one new open task per commit while the PR stays open.

Observed evidence (2026-09-05): 216 open tasks / 81 unique repos; bborbe/agent-task-executor 13 open tasks (all different refs/shas, one per commit) while holding 3 OPEN `fix/update-go-*` PRs (#41-43); bborbe/vault-cli 12; bborbe/agent-task-controller 7; bborbe/agent-sentry-issue-analyzer 7.

## Expected vs Actual

**Expected** (per spec 001 Desired Behavior 6, deterministic identity — same repo at same HEAD always yields the same id; the design assumes in-flight duplicates collapse): a repo with an update already in flight never receives a second open task.

**Actual:** one new open task per distinct head_sha. The dedup argument only holds within a single SHA — per-commit re-emission produces fresh ids the downstream dedup cannot absorb, so the queue grows unboundedly.

## Acceptance Criteria

- [ ] `grep -n "HasOpenUpdatePR" pkg/githubclient.go` returns ≥1 — evidence: file grep.
- [ ] `grep -rEn '"fix/update-go-' pkg --include='*.go' | grep -v '_test.go'` returns exactly 1 line (the single-source prefix constant) — evidence: file grep count.
- [ ] `grep -n "open_update_pr" pkg/watcher.go` returns ≥1 — evidence: file grep.
- [ ] `grep -n "HasOpenUpdatePR" mocks/github_client.go` returns ≥1 (counterfeiter mock regenerated) — evidence: file grep.
- [ ] Watcher gate is behavioral, not decorative: `go test ./pkg/...` passes tests asserting (a) a repo with an open update PR publishes NO create-task command (publisher spy), (b) a repo without one still emits, (c) a rate-limited open-PR check aborts the cycle with result `rate_limited` — evidence: exit code 0 AND `grep -nE "open update PR|rate limit" pkg/watcher_test.go` returns ≥1.
- [ ] `make precommit` exits 0 — evidence: exit code.
- [ ] **Operator-only (not container-runnable — `~/Documents/Obsidian/OpenClaw/tasks` and `sweep.sh` exist only on the host; the verifier skips this AC in-container). Post-merge + prod deploy:** a new commit to a repo holding an open `fix/update-go-*` PR produces no new open github-update-go task — evidence (negative): two consecutive `bash ~/.claude/skills/close-obsolete-tasks/scripts/sweep.sh --only github-update-go` runs spanning such a commit show the no-pr-planning-failed group for that repo without an added task (pre-fix it grew by one per commit).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — exits 0
- `grep -n "HasOpenUpdatePR" pkg/githubclient.go pkg/watcher.go mocks/github_client.go` — ≥1 per file
- `grep -rEn '"fix/update-go-' pkg --include='*.go' | grep -v '_test.go'` — exactly 1 line
- `grep -n "open_update_pr" pkg/watcher.go` — ≥1

### Operator-executable (host, after PR merge + `make buca` on the prod worktree per [[Rebuild Dev and Prod]])

- Replay the Reproduction: push a commit to a repo holding an open `fix/update-go-*` PR, run `bash ~/.claude/skills/close-obsolete-tasks/scripts/sweep.sh --only github-update-go` before and after the commit, and confirm no task is added to the no-pr-planning-failed group.

## Desired Behavior

1. **Read-only in-flight query.** The watcher gains a read-only check: does the repo currently have an open pull request whose head branch carries the update prefix? Open PRs are listed (state=open; prefix match is client-side) and any matching head branch yields true. Error contract mirrors the existing merged-PR check exactly: rate limit → `ErrRateLimited`; every other failure → wrapped `github.com/bborbe/errors` error.
2. **Single-source prefix.** The update branch prefix lives in one constant; both the new open-PR check and the existing merged-PR check build branches from it.
3. **Always-on gate.** After a repo passes candidate gathering and before the cycle-filter skip, the open-PR check runs:
   - open update PR exists → metric `IncFilterSkipped("open_update_pr")` + glog V(2) line + skip the repo;
   - non-rate-limit error → log + drop the repo this cycle, emitting nothing on uncertainty; retried next poll;
   - rate limit → abort the whole cycle with result `"rate_limited"` (identical to the existing merge-detection pass).
4. **Applies on every cycle**, including forced ones — the gate is not the SHA cursor; only `sha_unchanged` is force-omitted.
5. **Tests + mock.** Counterfeiter mock regenerated (existing tests keep passing — mock default zero values false/nil skip nothing). New tests: open update PR → true; only non-update open PRs → false; rate limit → `ErrRateLimited`; watcher level: open update PR → repo skipped with no create-task command published; none → emitted; rate limit → cycle aborts.

## Constraints

- Branch prefix stays in sync with the agent's branchPrefix — the single-source constant is the only place the literal appears in production code.
- `GitHubClient` stays read-only — nothing writes to observed repos.
- No new external dependencies (go-github already used).
- Errors follow `github.com/bborbe/errors` wrapping; info logs at glog V(2) (project DoD: docs/dod.md).
- The existing merged-PR detection pass (`GetMergedUpdatePR` / `completeMergedUpdates`) is unchanged.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Rate limit on the open-PR query | Whole cycle aborts with result `"rate_limited"`; no emits this cycle | Next scheduled poll retries; operator sees `poll_cycle_total{result="rate_limited"}` |
| Non-rate-limit error (network, 5xx) on the open-PR query | Repo dropped this cycle, nothing emitted | Automatic retry next poll; "repo dropped from cycle" warning log |
| Open PR opened between guard check and emit | A task is emitted while a PR is already in flight | Same pre-fix duplicate condition; the open PR suppresses all further emits from the next poll |
| Unrelated open PR with a spoofed `fix/update-go-*` branch | Update suppressed while no agent PR exists (false-positive skip) | Next poll re-checks; once the PR closes unmerged the repo emits normally — same trust model as the existing merged-PR check |
| Backlog not yet drained after deploy | Pre-existing duplicates still present | Live agent-task-controller drain collapses them (out of scope) |

## Security / Abuse Cases

The watcher performs read-only network I/O to the GitHub API. The only adversarial surface is a repo (or attacker with push rights) spoofing an unrelated `fix/update-go-*` branch head — that suppresses emission while the PR is open, a self-healing false-positive (next poll re-checks once the PR closes). Accepted risk: identical trust model to the existing merged-PR check, which already trusts the branch name.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Open-update-PR guard: interface method + prefix constant + watcher gate + mock regen + unit tests | 1-5 | 1-7 | — |

Rationale: one code layer, one atomic change — the interface, gate, and mock must land together (the counterfeiter mock breaks the moment the interface grows), so a single prompt keeps the change coherent.
