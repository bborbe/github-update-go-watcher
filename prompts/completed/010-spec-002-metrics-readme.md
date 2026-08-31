---
status: completed
spec: [002-consent-tristate-undecided]
summary: Added auto_update_undecided to FilterSkipReasons so filter_skipped_total pre-initialises the new series to 0, with a pre-init test, README tri-state docs (skip table, trust gate, decision task contract), watcher_test label derivation from the real slice, and a CHANGELOG entry
execution_id: github-update-go-watcher-tristate-exec-010-spec-002-metrics-readme
dark-factory-version: dev
created: "2026-08-31T10:33:16Z"
queued: "2026-08-31T11:22:11Z"
started: "2026-08-31T11:47:03Z"
completed: "2026-08-31T11:51:19Z"
---

<summary>

- The Prometheus scrape endpoint now exposes the new "undecided" skip reason as a zero-valued series from the moment the process starts, exactly like every other skip reason — an operator dashboard never shows a gap for it before the first repo actually hits it.
- The README's skip-reasons table is corrected: the existing row describing "no consent" is split into "explicitly refused" and "never answered", matching the behavior that landed in the two prior prompts.
- A new README section documents the decision task's shape (frontmatter keys, body) the same way the existing update-task contract is documented, so a future reader can see both task shapes side by side.
- No code behavior changes in this prompt — this is metric label bookkeeping and documentation only.

</summary>

<objective>

Close out spec 002 by adding `"auto_update_undecided"` to the closed, exported `FilterSkipReasons` label set (so it is pre-initialised to 0 at startup, matching the existing pattern for every other label), and bring `README.md` in line with the tri-state consent behavior implemented in the three prior prompts: the `auto_update_disabled` row's description, a new `auto_update_undecided` row, a new "Decision task contract" section, and one clarifying sentence in "Scope — two gates".

</objective>

<context>

Read before editing:
- `pkg/metrics.go` — full current file (159 lines): the `FilterSkipReasons` slice (line 47-54) and the pre-initialisation loop in `NewMetrics` (line 125-127) that ranges over it.
- `pkg/metrics_test.go` — full current file (163 lines): the exact `Fail("filter_skipped_total metric or scope label not found")` pattern used by `"IncFilterSkipped moves the correct series to 1"`, and the `"pre-initialises all FilterSkipReasons label values to 0"` test's structure to mirror.
- `README.md` — full current file (142 lines): the "Scope — two gates, both must pass" section (line 15-24), the "Skip reasons" table (line 70-79, the `auto_update_disabled` row is line 78), and the "Emitted task contract" section (line 81-109) whose table/body-block style the new "Decision task contract" section must match.

This prompt assumes the three prior prompts in this sequence have already landed: `filter.NewAutoUpdateFilter()` returns `"auto_update_undecided"` for an undecided repo, and `pkg.BuildDecisionCommand`/`pkg.TaskPublisher.PublishDecision` exist and are wired into the watcher's skip-with-emit branch. If any of those are missing, stop and report — do not re-derive them here.

</context>

<requirements>

## 1. `pkg/metrics.go` — add the new label to the closed set

Replace:
```go
	FilterSkipReasons = []string{
		"scope",
		"no_gomod",
		"gomod_unparsable",
		"go_current",
		"auto_update_disabled",
		"sha_unchanged",
	}
```
with:
```go
	FilterSkipReasons = []string{
		"scope",
		"no_gomod",
		"gomod_unparsable",
		"go_current",
		"auto_update_disabled",
		"auto_update_undecided",
		"sha_unchanged",
	}
```
Update the `Metrics` interface's `IncFilterSkipped` doc comment from:
```go
	// IncFilterSkipped — reason: "scope" | "no_gomod" | "gomod_unparsable" |
	// "go_current" | "auto_update_disabled" | "sha_unchanged"
	IncFilterSkipped(reason string)
```
to:
```go
	// IncFilterSkipped — reason: "scope" | "no_gomod" | "gomod_unparsable" |
	// "go_current" | "auto_update_disabled" | "auto_update_undecided" |
	// "sha_unchanged"
	IncFilterSkipped(reason string)
```
No other change to this file — the pre-initialisation loop in `NewMetrics` already ranges generically over `FilterSkipReasons`, so the new label is picked up automatically with zero additional code.

## 2. `pkg/metrics_test.go` — prove the new label is pre-initialised to 0

Add a new `It`, sibling to the existing `"IncFilterSkipped moves the correct series to 1"` and `"all other filter skip reasons remain at 0 after IncFilterSkipped(scope)"` tests, inside the existing `Describe("NewMetrics", func() { ... })` block:
```go
		It("pre-initialises auto_update_undecided to 0", func() {
			pkg.NewMetrics(reg)

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_filter_skipped_total" {
					for _, metric := range f.GetMetric() {
						for _, label := range metric.GetLabel() {
							if label.GetName() == "reason" && label.GetValue() == "auto_update_undecided" {
								Expect(metric.GetCounter().GetValue()).To(Equal(0.0))
								return
							}
						}
					}
				}
			}
			Fail("filter_skipped_total metric or auto_update_undecided label not found")
		})
```
This must be added as an additional nested `It` inside the existing `Describe("NewMetrics", func() { ... })` block (which is itself nested inside the outer `Describe("Metrics", func() { ... })` block, sharing the `reg *prometheus.Registry` fixture from the outer `BeforeEach`) — not as a new top-level `var _ = Describe(...)` statement, which would not have `reg` in scope.

## 3. `README.md` — Skip reasons table

Replace the `auto_update_disabled` row:
```
| `auto_update_disabled` | Repo has no `goUpdate.autoUpdate: true` in `.maintainer.yaml` |
```
with the corrected description (an explicit refusal, not merely "absent") plus a new row immediately after it for the new outcome:
```
| `auto_update_disabled` | Repo has `goUpdate.autoUpdate: false` explicitly set in `.maintainer.yaml` |
| `auto_update_undecided` | `.maintainer.yaml` is absent, has no `goUpdate:` section, has no `autoUpdate` key, or the key holds a non-boolean value — the owner has never answered. A decision task is filed once per repo (see [Decision task contract](#decision-task-contract)) |
```

## 4. `README.md` — "Scope — two gates" clarifying sentence

In the "Scope — two gates, both must pass" section, after the existing sentence:
```
The second is a **trust gate**: a repo with no `.maintainer.yaml`, no `goUpdate:` section, or `autoUpdate` absent/false is skipped. Opt-in, never opt-out — this watcher causes code to be committed to the target repo, so it does not act without explicit consent.
```
replace it with (correcting "absent/false" now that absent and false are distinct outcomes, and adding the decision-task sentence):
```
The second is a **trust gate**: a repo with `autoUpdate: false`, explicitly set, is skipped and stays silent. A repo whose owner has never answered at all — no `.maintainer.yaml`, no `goUpdate:` section, no `autoUpdate` key, or a non-boolean value — is also skipped, but additionally gets a one-time decision task filed asking the owner to answer (see [Decision task contract](#decision-task-contract)). Opt-in, never opt-out — this watcher causes code to be committed to the target repo, so it does not act without explicit consent, and it never defaults an unanswered repo to consent.
```

## 5. `README.md` — new "Decision task contract" section

Add a new `##` section immediately after the existing "## Emitted task contract" section (after its closing body-block and before "## Endpoints"):
```markdown
## Decision task contract

When a repo's consent is undecided (see [Skip reasons](#skip-reasons)), the watcher publishes a second, distinct `CreateTaskCommand` shape — a decision task addressed to a human, not to the update agent. Re-emitted every cycle the repo stays undecided; the task identifier is derived from `(owner, repo)` only (no HEAD SHA), so a re-emit is a downstream no-op, not a duplicate.

The command's frontmatter carries exactly these ten keys:

| Key | Value |
|---|---|
| `task_type` | `github-update-go-decision` |
| `assignee` | `bborbe` (never `github-update-go-agent` — this is a human decision, not agent work) |
| `phase` | `planning` |
| `status` | `in_progress` |
| `stage` | `<STAGE>` (`dev` or `prod`) |
| `task_identifier` | deterministic UUID5 derived from `(owner, repo)` only |
| `title` | `Go Update Decision <owner>-<repo>` (no HEAD SHA) |
| `repo` | `<owner>/<repo>` |
| `current_go` | normalised three-part current Go version (e.g. `1.24.0`) |
| `latest_go` | current stable three-part Go version (e.g. `1.26.6`) |

Body: a short explanation plus both `.maintainer.yaml` answers (`autoUpdate: true` to opt in, `autoUpdate: false` to opt out) — either answer stops future decision tasks for that repo.
```

## 6. `pkg/watcher_test.go` — remove the duplicated skip-reason set

`pkg/watcher_test.go`'s `It("IncFilterSkipped labels are in FilterSkipReasons")` hardcodes its own six-entry copy of the closed skip-reason set instead of deriving it from `pkg.FilterSkipReasons`:

```go
validLabels := map[string]bool{
	"scope":                true,
	"no_gomod":             true,
	"gomod_unparsable":     true,
	"go_current":           true,
	"auto_update_disabled": true,
	"sha_unchanged":        true,
}
```

This does not fail today (that test's fixture only ever emits `no_gomod`), but it is a second, un-synced mirror of the very label set this prompt exists to keep in lockstep — a trap for the next test that emits `auto_update_undecided`. Replace the literal map with a lookup against the real slice so it can never drift again:

```go
validLabels := map[string]bool{}
for _, reason := range pkg.FilterSkipReasons {
	validLabels[reason] = true
}
```

Leave the assertion loop below it unchanged. Do **not** simply add a seventh literal entry — that reproduces the drift this requirement removes.

</requirements>

<constraints>

- Weakening the consent gate is out of scope. Absent consent must still never file an update task and never cause any write to the observed repo.
- Defaulting absent consent to true, under any flag, env var, or code path, is forbidden.
- The new skip reason must appear in the existing skip-reason metric (`filter_skipped_total{reason="auto_update_undecided"}`), not a new metric or a new label name.
- `README.md`'s existing `auto_update_disabled` row must be rewritten, not merely appended to — the row's meaning changed (it is now the explicit-refusal case only).
- `FilterSkipReasons` is a closed, exported label set kept in lockstep with `README.md` — every value in the Go slice must have a corresponding row in the Skip reasons table, and vice versa.
- No new configuration list, no flag, no env var.
- The emitted update-task contract (frontmatter keys, body template) documented under "Emitted task contract" is unchanged and frozen — this prompt does not touch it, only adds a sibling section for the decision task.

</constraints>

<verification>

```
make precommit
```
must exit 0.

Confirm the label set and the README table stay in lockstep (every `FilterSkipReasons` entry has a matching README row):
```
sed -n '/FilterSkipReasons = \[\]string{/,/^\t}/p' pkg/metrics.go | grep -oP '"\K[a-z_]+(?=")' | sort -u
grep -oP '^\| `\K[a-z_]+(?=`)' README.md | sort -u
```
Every label printed by the first command must appear in the second command's output (README rows may additionally list frontmatter-key names from the task-contract tables, which is expected — only confirm the six/seven skip-reason labels are a subset).

</verification>
