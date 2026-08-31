---
status: completed
spec: [002-consent-tristate-undecided]
summary: Replaced Candidate.AutoUpdate bool with tri-state filter.Consent throughout; rewrote NewAutoUpdateFilter as a fail-closed three-way switch (auto_update_disabled for refused, auto_update_undecided for undecided/invalid); proved GoBehindFilter short-circuits current-on-Go repos before the consent verdict; updated all fixtures, CHANGELOG, and watcher/filter tests.
execution_id: github-update-go-watcher-tristate-exec-008-spec-002-filter-verdict
dark-factory-version: dev
created: "2026-08-31T10:33:16Z"
queued: "2026-08-31T11:22:11Z"
started: "2026-08-31T11:33:48Z"
completed: "2026-08-31T11:39:52Z"
---

<summary>

- The consent gate now produces three distinct outcomes instead of a pass/fail boolean: granted, refused, or undecided.
- A repo whose owner has never answered is now told apart from a repo whose owner explicitly said no — today both look identical downstream.
- A repo that is already on the latest Go version never gets bothered about consent at all, decided or not — being current on Go still wins outright, unchanged from today.
- The filter chain's step order and every other skip reason stay exactly as they are today; only what step 5 can say about an undecided repo changes.
- Every place in the codebase that builds a test fixture with the old boolean consent field is mechanically updated to the new three-valued field — no test's actual expected outcome changes except the handful that specifically exist to prove the new third outcome.
- This prompt still does not make the watcher DO anything different when a repo is undecided (no new task is filed yet) — it only makes the filter correctly NAME that case. Wiring the response comes in the next prompt.

</summary>

<objective>

Replace `pkg.Candidate.AutoUpdate bool` / `filter.Candidate.AutoUpdate bool` with a `Consent filter.Consent` field throughout, and rewrite `filter.NewAutoUpdateFilter()` to a locked three-way switch that returns `"auto_update_disabled"` for `RefusedConsent`, `"auto_update_undecided"` for `UndecidedConsent` (and any other/invalid value, including the zero value — fail closed), and `""` only for `GrantedConsent`. Prove that `GoBehindFilter` (chain position 4) still short-circuits ahead of the consent verdict, so a repo already current on Go never reaches or reports a consent outcome at all, decided or not.

</objective>

<context>

Read before editing:
- `pkg/filter/filter.go` — `Candidate` struct (field to rename: `AutoUpdate bool` at line 41-43) and the package-doc chain-order table (line 8-19, step 5's comment needs updating).
- `pkg/filter/auto_update_filter.go` — full current file (27 lines), the function to rewrite.
- `pkg/candidate.go` — `pkg.Candidate` struct (field to rename: `AutoUpdate bool` at line 28), `FilterCandidate()` projection (line 46-55).
- `pkg/watcher.go` — `gatherCandidate`'s `Candidate{...}` literal (the interim `AutoUpdate: consent == filter.GrantedConsent` collapse introduced by the prior prompt in this sequence — becomes a direct passthrough here).
- `pkg/filter/filter_test.go` — full current file (274 lines): the `AutoUpdateFilter` `DescribeTable("consent matrix", ...)` (line 164-174), the `Closed set assertion` `Describe` (line 215-249, candidate literal at line 225-232), the `Full chain ordering` `Describe` (line 251-273, candidate literal at line 262-269).
- `pkg/taskbuilder_test.go` — candidate fixture (`AutoUpdate: true` at line ~47); this file currently imports only `"github.com/bborbe/github-update-go-watcher/pkg"`, no `filter` import.
- `pkg/taskpublisher_test.go` — candidate fixture (`AutoUpdate: true` at line ~53); this file currently imports `"context"`, `"errors"`, `"github.com/bborbe/agent/command/task"`, `mocks`, `pkg` — no `filter` import.
- `pkg/factory/factory_test.go` — full current file (83 lines); already imports `"github.com/bborbe/github-update-go-watcher/pkg/filter"`; three `filter.Candidate{..., AutoUpdate: true}` literals (in `"passes a fully-qualifying Candidate"`, `"skips a repo outside the allowlist for reason scope"`, `"passes a repo within the allowlist"`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega `DescribeTable`/`Entry` conventions already in use throughout this repo.

This prompt assumes the prior prompt in this sequence has already landed: `pkg/filter/consent.go` exists with `Consent`, `GrantedConsent`, `RefusedConsent`, `UndecidedConsent`, and `pkg.GitHubClient.GetMaintainerConfig` already returns `(filter.Consent, error)`. If any of those are missing, stop and report — do not re-derive them here.

</context>

<requirements>

## 1. `pkg/filter/filter.go` — rename the field

Replace:
```go
	// AutoUpdate is `.maintainer.yaml: goUpdate.autoUpdate`. True is the ONLY
	// value that passes the consent gate.
	AutoUpdate bool
```
with:
```go
	// Consent is the tri-state outcome of `.maintainer.yaml:
	// goUpdate.autoUpdate` (spec 002). Only GrantedConsent passes the
	// consent gate; RefusedConsent and UndecidedConsent are both skips, but
	// with distinct reasons (see AutoUpdateFilter).
	Consent Consent
```

Update the package-doc chain-order table's step 5 line from:
```
//  5. AutoUpdateFilter     -> "auto_update_disabled" — owner has not opted in
```
to:
```
//  5. AutoUpdateFilter     -> "auto_update_disabled" | "auto_update_undecided"
//                             — owner refused, or has not answered at all
```

## 2. `pkg/filter/auto_update_filter.go` — three-way switch

Replace the entire body of `NewAutoUpdateFilter` with:
```go
// NewAutoUpdateFilter is the per-repo trust gate sourced from
// `.maintainer.yaml: goUpdate.autoUpdate` (spec 002). It is POSITIVE
// OPT-IN: only Consent == GrantedConsent passes.
//
//   - GrantedConsent  -> "" (passes)
//   - RefusedConsent  -> "auto_update_disabled" — owner explicitly opted out
//   - UndecidedConsent, or any other/invalid Consent value (including the
//     zero value) -> "auto_update_undecided" — fails closed
//
// This gate is the only thing that turns this service's attention into
// agent action on somebody else's repository. There is deliberately no
// flag, env var, or code path that disables it or defaults any non-granted
// value to consent.
//
// An UNPARSABLE `.maintainer.yaml` never reaches this filter: the gatherer
// drops that repo from the cycle before evaluation, so a malformed file is
// a drop, not a consent verdict.
func NewAutoUpdateFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		switch candidate.Consent {
		case GrantedConsent:
			return ""
		case RefusedConsent:
			return "auto_update_disabled"
		default:
			// UndecidedConsent, and any other/invalid Consent value
			// (including the zero value), fails closed here.
			return "auto_update_undecided"
		}
	})
}
```

## 3. `pkg/candidate.go` — rename the field and update the projection

Replace:
```go
	AutoUpdate    bool    // .maintainer.yaml: goUpdate.autoUpdate — the consent gate
```
with:
```go
	Consent       filter.Consent // .maintainer.yaml: goUpdate.autoUpdate — the consent gate
```
Update the struct's build-order doc comment step 4 from:
```
//  4. AutoUpdate   (from GetMaintainerConfig — false when .maintainer.yaml is absent)
```
to:
```
//  4. Consent      (from GetMaintainerConfig — UndecidedConsent when .maintainer.yaml is absent)
```
Replace the `FilterCandidate` projection's:
```go
		AutoUpdate:    c.AutoUpdate,
```
with:
```go
		Consent:       c.Consent,
```

## 4. `pkg/watcher.go` — `gatherCandidate` direct passthrough

Replace:
```go
	candidate := Candidate{
		Repo:     repo,
		HeadSHA:  headSHA,
		LatestGo: latestGo,
		// Candidate.AutoUpdate stays a bool in this prompt (renamed to a
		// filter.Consent field in a later prompt). This collapse is a
		// deliberately temporary bridge so nothing observable changes yet.
		AutoUpdate: consent == filter.GrantedConsent,
	}
```
with:
```go
	candidate := Candidate{
		Repo:     repo,
		HeadSHA:  headSHA,
		LatestGo: latestGo,
		Consent:  consent,
	}
```

## 5. Mechanical test-fixture updates (8 call sites, 3 files)

Rename `AutoUpdate: true` -> `Consent: filter.GrantedConsent` and `AutoUpdate: false` -> `Consent: filter.RefusedConsent` (or `filter.UndecidedConsent` where noted) at every site below. No other change to any of these tests' assertions or outcomes.

- `pkg/taskbuilder_test.go`: add the import `"github.com/bborbe/github-update-go-watcher/pkg/filter"` to the existing import block; change the candidate fixture's `AutoUpdate: true` to `Consent: filter.GrantedConsent`.
- `pkg/taskpublisher_test.go`: add the import `"github.com/bborbe/github-update-go-watcher/pkg/filter"` to the existing import block; change the candidate fixture's `AutoUpdate: true` to `Consent: filter.GrantedConsent`.
- `pkg/factory/factory_test.go` (already imports `filter`, no import change needed): change all three `AutoUpdate: true` occurrences (in `"passes a fully-qualifying Candidate"`, `"skips a repo outside the allowlist for reason scope"`, `"passes a repo within the allowlist"`) to `Consent: filter.GrantedConsent`.

## 6. `pkg/filter/filter_test.go` — behavior-changing rewrites

Replace the `AutoUpdateFilter` `DescribeTable` (currently `func(autoUpdate bool, expected string)` with 2 `Entry`s) with a three-way version plus an explicit fail-closed proof for the zero value:
```go
var _ = Describe("AutoUpdateFilter", func() {
	DescribeTable("consent matrix",
		func(consent filter.Consent, expected string) {
			f := filter.NewAutoUpdateFilter()
			reason := f.Skip(filter.Candidate{Consent: consent})
			Expect(reason).To(Equal(expected))
		},
		Entry("granted passes", filter.GrantedConsent, ""),
		Entry("refused returns auto_update_disabled", filter.RefusedConsent, "auto_update_disabled"),
		Entry("undecided returns auto_update_undecided", filter.UndecidedConsent, "auto_update_undecided"),
		Entry("zero value fails closed to auto_update_undecided", filter.Consent(""), "auto_update_undecided"),
	)
})
```

In `Describe("Closed set assertion", ...)`, change the candidate literal's `AutoUpdate: true` to `Consent: filter.GrantedConsent`. Leave the local `validReasons` map (lines 217-224) untouched — it is not exercised by the `auto_update_undecided` reason in this test's fixture and adding an inert key would add doc surface with no test value.

In `Describe("Full chain ordering", ...)`, change the candidate literal's `AutoUpdate: false` to `Consent: filter.UndecidedConsent` (representative of "not yet decided" — this candidate's `RepoKey` is already outside the allowlist, so `RepoAllowlistFilter` still wins with `"scope"` before the consent filter is ever reached; the outcome is unchanged).

Add a new `It`, sibling to `Describe("Full chain ordering", ...)`, proving spec 002 Desired Behavior 9 directly at the filter-chain level — `GoBehindFilter` (chain position 4) short-circuits ahead of `AutoUpdateFilter` (chain position 5), so an undecided repo already current on Go reports `"go_current"`, never a consent-related reason:
```go
var _ = Describe("GoBehindFilter short-circuits before the consent verdict (DB9)", func() {
	It("reports go_current for an undecided repo already current on Go", func() {
		filters := filter.TaskCreationFilterList{
			filter.NewRepoAllowlistFilter(nil),
			filter.NewGoModPresentFilter(),
			filter.NewGoModParsableFilter(),
			filter.NewGoBehindFilter(),
			filter.NewAutoUpdateFilter(),
			filter.NewSHAUnchangedFilter(&fakeCursor{}),
		}
		candidate := filter.Candidate{
			RepoKey:       "github.com/bborbe/repo",
			HeadSHA:       "abc123",
			GoModPresent:  true,
			GoModParsable: true,
			GoBehind:      false,
			Consent:       filter.UndecidedConsent,
		}
		reason := filters.Skip(candidate)
		Expect(reason).To(Equal("go_current"))
	})
})
```

## 7. `pkg/watcher_test.go` — second restructure of the AC5 consent-gate table

This `DescribeTable` was already mechanically retargeted to `filter.Consent` in the prior prompt in this sequence, preserving today's exact behavior. Now that `AutoUpdateFilter` itself distinguishes refused from undecided, restructure it a second time to add an `expectedReason` dimension, replacing the current body inside `Context("AC5 consent gate")`:
```go
DescribeTable("consent matrix",
	func(consent filter.Consent, expectPublish bool, expectedReason string) {
		ghClient.GetMaintainerConfigReturns(consent, nil)
		publisher.PublishCreateReturns(true)
		metrics.IncFilterSkippedStub = func(string) {}

		err := watcher.Poll(context.Background(), false)
		Expect(err).NotTo(HaveOccurred())

		if expectPublish {
			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			return
		}
		Expect(publisher.PublishCreateCallCount()).To(Equal(0))
		Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
		arg := metrics.IncFilterSkippedArgsForCall(0)
		Expect(arg).To(Equal(expectedReason))
	},
	Entry("maintainer file absent",
		filter.UndecidedConsent, false, "auto_update_undecided"),
	Entry("goUpdate section absent",
		filter.UndecidedConsent, false, "auto_update_undecided"),
	Entry("autoUpdate key absent",
		filter.UndecidedConsent, false, "auto_update_undecided"),
	Entry("autoUpdate false",
		filter.RefusedConsent, false, "auto_update_disabled"),
	Entry("autoUpdate true",
		filter.GrantedConsent, true, ""),
)
```
This directly satisfies spec 002 AC1 and AC2 at the watcher level (in addition to the filter-level proof in `pkg/filter/filter_test.go`).

## 8. `pkg/watcher_test.go` — close the AC6 evidence gap

Spec AC6 requires a single test asserting **both** that the verdict is `go_current` **and** that the publisher call count is 0, for an undecided repo that is already current on Go. The filter-package test added in requirement 6 proves the verdict half but cannot reach a publisher mock; the AC5 table above uses a behind-stable fixture. Add one watcher-level `It` that asserts both halves together.

Add inside the same `Context` that hosts the AC5 consent-gate table:

```go
It("undecided repo already current on Go emits nothing (AC6, DB9)", func() {
	ghClient.ListReposReturns([]pkg.Repo{
		{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
	}, nil)
	ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
	ghClient.GetGoModReturns([]byte("module x\n\ngo 1.26.6\n"), nil)
	ghClient.GetMaintainerConfigReturns(filter.UndecidedConsent, nil)
	goDevClient.LatestStableReturns(pkg.Version{
		Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
	}, nil)
	buildWatcher()

	_ = watcher.Poll(context.Background(), false)

	// GoBehindFilter (chain position 4) short-circuits before the consent
	// filter (position 5), so the verdict is go_current, not
	// auto_update_undecided.
	Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
	Expect(metrics.IncFilterSkippedArgsForCall(0)).To(Equal("go_current"))
	Expect(publisher.PublishCreateCallCount()).To(Equal(0))
})
```

Adjust the `go.mod` byte fixture and the `LatestStable` return to whatever the surrounding tests already use for a "current on stable" repo, if that differs — the requirement is that the repo is **not** behind, its consent is `UndecidedConsent`, and both assertions live in the same `It`.

The `Context("forced cycle bypasses SHAUnchangedFilter")`'s `It("force=true still respects consent gate")` sub-test is unaffected by this prompt — it already uses `filter.RefusedConsent` (from the prior prompt's mechanical conversion) and `RefusedConsent` still maps to `"auto_update_disabled"` after this prompt's rewrite, so no further edit is needed there.

</requirements>

<constraints>

- Weakening the consent gate is out of scope. Absent consent must still never file an update task and never cause any write to the observed repo.
- Defaulting absent consent to true, under any flag, env var, or code path, is forbidden.
- Behaviour for `autoUpdate: true` and explicit `autoUpdate: false` must remain unchanged by this change — an explicit refusal must keep producing `auto_update_disabled`, not the new `auto_update_undecided` reason.
- No new configuration list, no flag, no env var. The set of repos evaluated is unchanged.
- Filter chain order stays frozen: 1 scope, 2 no_gomod, 3 gomod_unparsable, 4 go_current, 5 auto_update_disabled/auto_update_undecided, 6 sha_unchanged. Because `GoBehindFilter` (position 4) runs before the consent filter (position 5), an undecided repo that is already current on Go never reaches the consent verdict and files nothing — this is existing, correct short-circuit behavior that this prompt proves explicitly rather than changes.
- Skipping with a new reason must not itself publish any command — that pathway (skip-with-emit) is introduced in a later prompt. This prompt only changes what reason string is produced.
- The new skip reason string must be exactly `auto_update_undecided`, matching the metric-label pre-initialisation landing in a later prompt.
- Do not hand-edit any file under `mocks/` — it is fully regenerated by `make precommit`'s `generate` step.

</constraints>

<verification>

```
make precommit
```
must exit 0.

Confirm the old field name is fully gone:
```
grep -rn "AutoUpdate\b" pkg/filter/filter.go pkg/filter/auto_update_filter.go pkg/candidate.go pkg/watcher.go
```
must print nothing (the type name `filter.NewAutoUpdateFilter`/`AutoUpdateFilter` function/constructor name itself is expected to remain and is not a match for this grep since it greps the field name `AutoUpdate` as a whole word, not the substring inside `AutoUpdateFilter` — if the grep incidentally matches the constructor name, treat that as expected and verify manually that no `.AutoUpdate` field access remains).

</verification>
