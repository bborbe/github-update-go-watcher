---
status: completed
spec: [005-bug-watcher-reemits-while-task-still-queued]
summary: Rewrote the docs/dod.md in-flight-signal contract to name both the emission window and the open-PR window, added the emission_in_flight row to the README skip-reason table, and appended the fix entry to the existing Unreleased changelog section.
execution_id: github-update-go-watcher-emission-claim-exec-015-spec-005-docs-skip-reason
dark-factory-version: v0.198.7
created: "2026-10-27T22:25:43Z"
queued: "2026-10-27T22:35:07Z"
started: "2026-10-27T22:49:43Z"
completed: "2026-10-27T22:53:13Z"
---

<summary>
- The durable in-flight contract now names two windows — before the update pull request exists and after it does — and says which signal covers each.
- It names the record that covers the pre-pull-request window, the three events that clear it, and the fourteen-day backstop with its derivation.
- The operator-facing skip-reason table gains a row for the new reason, including the fact that it is evaluated before the other filters.
- A changelog entry records the fix, so the release notes do not have to be reconstructed from the diff.
- No code changes: this prompt only brings the two documented contracts and the changelog in line with the shipped behaviour.
</summary>

<objective>
Keep the two documented contracts in lockstep with the code landed by the previous two prompts. The definition of done's in-flight-signal section currently states that the open pull request is the only in-flight signal — which is now false for the window between emission and the first push — and the README's skip-reason table is missing the new label. Document both in the same change, plus the changelog entry.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:
- `docs/dod.md` — the file ends with the `## In-flight signal contract (durable — do not break)` section: a single paragraph beginning "The watcher runs in a k8s pod with **no vault filesystem access**; it cannot query the task store. The **open update PR on GitHub (`fix/update-go-*` head branch) IS the in-flight signal** for a repo…" and ending "The gate is always-on (including forced cycles) and has no opt-out." That paragraph is what this prompt rewrites.
- `README.md` — the `## Skip reasons` table (rows `scope`, `no_gomod`, `gomod_unparsable`, `go_current`, `auto_update_disabled`, `auto_update_undecided`, `sha_unchanged`, `open_update_pr`). The `## Metrics` table above it points at that table for the `filter_skipped_total` labels, so it needs no edit.
- `CHANGELOG.md` — its first release section today is `## v0.6.8`; there is no `## Unreleased` section. Entries are `- fix:` / `- chore:` bullets under a `## vX.Y.Z` heading, in the style of the v0.6.5 `- fix:` bullet.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the Unreleased convention.
- `/home/node/.claude/plugins/marketplaces/coding/docs/readme-guide.md` — table style.

Read the code landed by prompts 1 and 2 of this batch (this prompt must run after both; if any symbol below is missing, stop and report instead of writing docs for code that does not exist):
- `pkg/cursor.go` — `RepoState.InFlightTarget` / `RepoState.InFlightSince` and their doc comments.
- `pkg/watcher.go` — `InFlightTTL`, `clearInFlight`, `inFlightExpired`, `emissionInFlight`, and the `emission_in_flight` skip in `processRepos`.
</context>

<requirements>

### 1. `docs/dod.md` — rewrite the in-flight-signal paragraph

Replace the whole body of the `## In-flight signal contract (durable — do not break)` section (keep the heading verbatim) with a version that names two windows. It must contain the literal token `emission_in_flight` (the acceptance criterion greps for it) and must keep the existing facts: no vault access from the pod, the branch prefix constant `updateBranchPrefix` in `pkg/githubclient.go`, the spec-003 origin of the open-PR gate, and that the gate is always-on with no opt-out.

```markdown
## In-flight signal contract (durable — do not break)

The watcher runs in a k8s pod with **no vault filesystem access**; it cannot query the task store. A repo is in flight in one of two windows, and each window has its own signal.

**Before the update PR exists (the emission window).** The watcher records its own emission in the cursor: `in_flight_target` (the target Go version it filed for, the same string the emitted task carries as `latest_go`) and `in_flight_since` (RFC3339, when it filed). While a claim still targets the cycle's target version, the repo is skipped with reason `emission_in_flight` and no second task is filed. The claim is cleared by exactly three observable events: the repo converges (`!GoBehind()` — the same predicate the `go_current` filter uses, so equal-to, ahead-of and unparsable all clear), an open `fix/update-go-*` PR is observed, or the `InFlightTTL` backstop (336h / 14 days, twice the longest measured queue latency) expires. A GitHub error while checking for the PR never clears the claim — only a positive "a PR exists" does — and a rate-limited cycle neither writes nor clears it. The claim is written only by `markPublished`, so hand-authored `- reemit` repair records never carry one.

**Once the update PR exists.** The **open update PR on GitHub (`fix/update-go-*` head branch) IS the in-flight signal** for a repo: an open update PR means an update task is already underway, so the watcher must NOT emit another (spec 003, bug fix 2026-09-05 — task IDs derive from (owner, repo, target Go version, head_sha), so a new commit otherwise re-emits on undrained work). The branch prefix lives in one constant (`updateBranchPrefix` in `pkg/githubclient.go`) shared by the open-PR gate and the merged-PR completion pass. The gate is always-on (including forced cycles) and has no opt-out.

Both signals are readable without a vault read: the claim lives in the cursor file the watcher already persists, and the PR list is one the gate already fetches. The claim adds no store, no PVC, no new API call and no config knob.
```

Do not reflow, reorder or delete any other section of `docs/dod.md`.

### 2. `README.md` — one row in the Skip reasons table

Append this row immediately after the `open_update_pr` row, keeping the table's existing two-column shape:

```markdown
| `emission_in_flight` | Repo was already emitted for the current target Go version and none of the three clearing events has occurred (convergence, an open `fix/update-go-*` PR, or the 14-day backstop). Evaluated before the filter chain, so a claimed repo reports this reason rather than `sha_unchanged` |
```

No other README change: the `## Metrics` table already points at this table for the `filter_skipped_total` labels.

### 3. `CHANGELOG.md` — one `- fix:` entry under a new `## Unreleased`

There is no `## Unreleased` section today; add one directly after the intro block (after the SemVer bullet list, before `## v0.6.8`), separated by a blank line, with a single bullet in the style of the existing `- fix:` entries:

```markdown
## Unreleased

- fix: stop the watcher filing a second update task for a repo whose first task is still queued. The cursor now carries an emission claim (`in_flight_target` + `in_flight_since`) written by a successful publish, and a repo whose claim still targets the cycle's Go version is skipped with reason `emission_in_flight` — closing the window between emission and the agent's first push, where no `fix/update-go-*` PR exists yet and a second commit re-emitted (two task records, two agent runs, two PRs for one bump; measured on the Go 1.27.2 rollout for `bborbe/agent-code` and `bborbe/agent-gemini`). The claim clears on convergence, on an observed open update PR, or on a 14-day `InFlightTTL` backstop; a GitHub error never clears it, a rate-limited cycle leaves it untouched, and a forced cycle does not bypass it. The open-PR gate, the `sha_unchanged` filter and the task identity seed are unchanged.
```

Do not rename or reorder existing sections. Do not add a version heading — the release process assigns it.

</requirements>

<constraints>
- Documentation only: no `.go` file changes in this prompt. If the landed code disagrees with any sentence above, fix the SENTENCE to match the code — never change the code to match the prose.
- `docs/dod.md`'s section heading `## In-flight signal contract (durable — do not break)` stays byte-identical.
- `docs/dod.md` must contain the literal string `emission_in_flight`; `README.md` must contain it in the skip-reasons table.
- The README table stays a two-column markdown table; do not renumber or reword the existing rows.
- Do not touch `docs/dod.md`'s Build / Code Quality / Tests / Style sections, and do not touch `README.md`'s Emitted task contract or Decision task contract sections.
- No vault/task-store read is implied by the wording — the contract must keep saying the pod has no vault access.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run from the repo root:

```
grep -n "emission_in_flight" docs/dod.md README.md
```

Must print at least one match in each file.

```
grep -n "In-flight signal contract" docs/dod.md
```

Must print exactly one match — the heading was not duplicated.

```
grep -c "The watcher runs in a k8s pod" docs/dod.md
grep -c "IS the in-flight signal" docs/dod.md
```

Each must print exactly `1` — the old single-paragraph body was replaced, not left alongside the new one.

```
grep -n "in_flight_target\|in_flight_since\|InFlightTTL\|updateBranchPrefix" docs/dod.md
```

Must match all four terms — the paragraph names the record, the backstop and the shared branch prefix.

```
grep -c "in_flight_target" pkg/cursor.go
grep -c "InFlightTTL" pkg/watcher.go
grep -c "emission_in_flight" pkg/watcher.go
```

Each must print at least `1` — the code this prompt documents landed in the two preceding prompts of this batch. A zero means the docs would describe behaviour that does not exist: stop and report instead of writing them.

```
grep -n "^## Unreleased" CHANGELOG.md
```

Must print exactly one match.

```
awk '/^## /{print NR": "$0}' CHANGELOG.md | head -2
```

The first two `## ` headings must be `## Unreleased` then `## v0.6.8` — Unreleased sits above the newest release, not below it.

```
test "$(grep -c '^## v' CHANGELOG.md)" = "$(git show HEAD:CHANGELOG.md | grep -c '^## v')" && echo SAME_SECTION_COUNT
```

Must print `SAME_SECTION_COUNT` — no release section was added, removed or renamed.

```
make precommit
```

Must exit 0 (the doc edits must not disturb the Go build, the license headers or the security scans).
</verification>
