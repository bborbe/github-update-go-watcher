---
status: completed
summary: Filed Go-update decision records at phase human_review (code, README contract, test, changelog) while leaving the update task at planning
execution_id: github-update-go-watcher-decision-phase-exec-019-decision-record-human-review-phase
dark-factory-version: v0.198.17
created: "2026-11-14T15:55:00Z"
queued: "2026-11-14T15:55:44Z"
started: "2026-11-14T15:59:21Z"
completed: "2026-11-14T16:00:52Z"
---

# Rest decision records at human_review so UnreachablePhase stops firing

<summary>
- A repo that has not yet answered the Go-update decision question stops tripping the `UnreachablePhase` warning on every cluster
- Decision records rest at the phase the board already treats as "a human must act"
- Nothing changes in the task controller, its gauge, the alert expression, or any executor Config
- The record stays visible, still assigned to the human who must answer it
- Existing tests keep passing; one new assertion locks the emitted phase
</summary>

<objective>
File Go-update decision records at `phase: human_review` instead of `phase: planning`, so a record that no executor Config can ever claim stops producing a permanent `UnreachablePhase` alert on nukedev and nukeprod.
</objective>

<context>
Read `README.md` — it carries the two emitted-task contracts this change touches. Read `docs/dod.md` for the emission-claim contract.

The `UnreachablePhase` rule lives in `bborbe/nuke` at `agent/alerts/unreachable-phase-alert.yaml`. It joins the controller's `agent_controller_open_tasks` gauge against the executor's `executor_config_accepted`, and its expression excludes `phase="human_review"`, `phase="done"` and `phase=""`. No Config CR lists `task_type: github-update-go-decision`, so a decision record resting at `planning` fires the alert for as long as the record exists. The rule's own comment says such records "need Config coverage or a phase move, not a quieter expression" — this prompt is the phase move.

`pkg/taskbuilder.go` — `buildDecisionFrontmatter` is the only place a decision record's frontmatter is assembled; `buildFrontmatter` is its sibling for the update task and must not change.
`pkg/taskbuilder_test.go` — the `BuildDecisionCommand` describe block is where its fields are asserted; read it before editing.
</context>

<requirements>
1. Record the baseline before editing: run `go test -mod=mod ./pkg/ -run TestSuite -count=1 -v 2>&1 | awk '/Ran [0-9]+ of [0-9]+ Specs/{print; exit}'` and note the count. It is **293** on the unmodified tree.
2. In `pkg/taskbuilder.go`, change `buildDecisionFrontmatter` so the `"phase"` key is `"human_review"` instead of `"planning"`. Change nothing else in that function — every other key, including `"status": "in_progress"` and `"assignee": "bborbe"`, stays as it is. Leave `buildFrontmatter` untouched: the update task keeps `planning`.
3. In `pkg/taskbuilder_test.go`, add one expectation to the existing `BuildDecisionCommand` describe block asserting that the built command's `Frontmatter["phase"]` equals `"human_review"`. Follow the shape of the neighbouring `sets task_type to …` and `sets assignee to …` expectations.
4. Leave the existing `has exactly 10 frontmatter keys` expectation untouched — this change alters a value, not the key set. Confirm it still passes.
5. In `README.md`, change the `phase` row of the **"Decision task contract"** section (the ten-key table, currently `| `phase` | `planning` |`) to `| `phase` | `human_review` |`. Change nothing else in that table, and do **not** touch the twelve-key "Emitted task contract" table above it — the update task keeps `planning`.
6. Add a `## Unreleased` section at the top of `CHANGELOG.md`, above `## v0.11.1`, containing exactly one bullet: `- fix: file Go-update decision records at phase human_review so UnreachablePhase stops firing on records no Config can claim`
7. Before finishing, re-run `<verification>` and confirm every check passes; walk each numbered requirement against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Change no other frontmatter key, no other function, and no other task type — the update task (`github-update-go`) keeps `phase: planning` in both code and README
- Do NOT touch the task controller, the alert expression or any Config CR; the fix is confined to what this repo emits
</constraints>

<verification>
```
ROOTDIR=/workspace make precommit
```

Must exit 0.

```
awk '/^func buildDecisionFrontmatter/,/^}/' pkg/taskbuilder.go | grep -c '"phase": *"human_review"'
```

Must print exactly `1` — the decision record's phase is `human_review`.

```
awk '/^func buildFrontmatter/,/^}/' pkg/taskbuilder.go | grep -c '"phase": *"planning"'
```

Must print exactly `1` — the update task still rests at `planning`. This is the sibling the change must not move.

```
go test -mod=mod ./pkg/ -run TestSuite -count=1 -v 2>&1 | awk '/Ran [0-9]+ of [0-9]+ Specs/{print $2; exit}'
```

Must print a count strictly greater than the `293` baseline — expect `294`. An exit-code-only check would still pass if the new expectation never registered.

```
awk '/^## /{print NR": "$0}' CHANGELOG.md | head -2
```

The first two `## ` headings must be `## Unreleased` then `## v0.11.1` — Unreleased sits above the newest release.

```
grep -c '^## v' CHANGELOG.md
```

Must print exactly `32` — the same count as before the change, so no release section was added, removed or renamed (`## Unreleased` does not match `^## v`).

```
grep -c '^| `phase` | `human_review` |$' README.md
```

Must print exactly `1` — the decision task's row. The update task's row must still read `planning`.

```
grep -c '^| `phase` | `planning` |$' README.md
```

Must print exactly `1` — the update task's row, unchanged.
</verification>
