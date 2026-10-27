---
status: approved
spec: [005-bug-watcher-reemits-while-task-still-queued]
created: "2026-10-27T22:25:43Z"
queued: "2026-10-27T22:35:04Z"
---

<summary>
- A repo that already had an update task filed for the current Go release is skipped instead of being filed a second time while that task is still queued.
- The skip clears itself on three observable events: the repo is already current, an update pull request appears, or a fourteen-day backstop expires.
- A GitHub error while checking for the pull request never clears the claim, so a transient failure cannot re-create the duplicate it was meant to prevent.
- A rate-limited cycle leaves the claim untouched — nothing is written, nothing is cleared.
- A forced cycle does not bypass the skip; a forced cycle means "poll now", not "emit regardless".
- A new Go release is still emitted immediately, because the claim is keyed to the release it was filed for.
- The skip is observable as a counter label rather than a log line, so it is visible in metrics at the suite's default verbosity and in the pod's `/metrics`.
- The pull-request gate keeps its current meaning; it now additionally reports whether it saw a pull request, which is what clears the claim.

</summary>

<objective>
Close the window between emission and the agent's first push, where a repo currently looks idle because no `fix/update-go-*` PR exists yet. Add a guard in the per-repo cycle that consults the persisted emission claim (written by the previous prompt) and skips the repo while that claim is live for the cycle's target Go version — clearing it only on convergence, on the appearance of an open update PR, or on the fourteen-day backstop. Land the guard, the three clears, the new skip-reason label, and the tests that prove all eight behaviours together: a partial landing either suppresses a repo forever (claim never cleared) or re-creates the duplicate (guard without clearing).
</objective>

<context>
Read `CLAUDE.md` (if present) and `docs/dod.md` — its "In-flight signal contract" section is the durable contract this prompt extends (the docs prompt updates its wording; do not edit it here).

Read these coding plugin docs before writing code (in-container paths, not host paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — the injected clock this prompt reads (`libtime.CurrentDateTimeGetter`, already wired by the previous prompt).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`

Read these repo files before writing code — every anchor below was verified against them:
- `pkg/watcher.go` — `processRepos` (the `gatherCandidate` call, the `switch w.openUpdatePRGate(ctx, repo)` block, the `cycleFilter.Skip` block with its `"repo skipped repo=%s reason=%s"` V(2) line, the `publisher.PublishCreate` + `markPublished` tail), `openUpdatePRGate` (returns `""` / `"skip"` / `"rate_limited"`; the error branch calls `dropRepo(repo, "open_update_pr", err)` and returns `"skip"`; the `openUpdate == true` branch calls `w.metrics.IncFilterSkipped("open_update_pr")` and returns `"skip"`), `markPublished` (already writes `InFlightTarget` / `InFlightSince` from `w.currentDateTimeGetter`), and `dropRepo(repo Repo, step string, err error) (Candidate, string, bool)`.
- `pkg/cursor.go` — `RepoState.InFlightTarget` (`json:"in_flight_target,omitempty"`) and `RepoState.InFlightSince` (`json:"in_flight_since,omitempty"`), written by the previous prompt.
- `pkg/candidate.go` — `Candidate.GoBehind() bool` is `c.GoModParsable && c.CurrentGo.Less(c.LatestGo)` — the SAME predicate `filter.NewGoBehindFilter` uses, so equal-to, ahead-of and unparsable all report `false`.
- `pkg/metrics.go` — `FilterSkipReasons` (closed label set, pre-initialised to 0 by `NewMetrics`), `IncFilterSkipped(reason string)`, and its doc comment listing the reason values.
- `pkg/filter/go_behind_filter.go` and `pkg/filter/filter.go` — the `go_current` verdict and the frozen chain order.
- `pkg/metrics_test.go` — the "pre-initialises auto_update_undecided to 0" test (the exact shape to copy for the new label) and the `reg.Gather()` + label-walk idiom.
- `pkg/watcher_test.go` — the `Describe("Poll")` contexts, `metrics.IncFilterSkippedStub = func(string) {}`, `publisher.PublishCreateCallCount()`, `ghClient.HasOpenUpdatePRReturns(bool, error)`, and the "metric label containment" test that derives its valid set from `pkg.FilterSkipReasons`.
- `pkg/pkg_suite_test.go` — `TestSuite` is the only entry point; it sets `time.Local = time.UTC`.

Verified library API facts (do not re-derive from memory):
- `libtime "github.com/bborbe/time"` — `DateTime.Time() stdtime.Time`, `DateTime.Sub(time HasTime) Duration`, `DateTime.Add(duration HasDuration) DateTime`, `DateTime.Format(layout string) string`, and `libtime.ParseDateTime(ctx context.Context, value interface{}) (*DateTime, error)` (accepts RFC3339 and RFC3339Nano). `libtime.Duration` constants: `Hour`, `Day`, `Week`; `Duration.Duration() stdtime.Duration`.
- `github.com/prometheus/client_golang/prometheus` — `NewRegistry()`, `(*Registry).Gather() ([]*dto.MetricFamily, error)`; the counter value is `metric.GetCounter().GetValue()`, and a label is `metric.GetLabel()` → `label.GetName()` / `label.GetValue()`. `testutil.ToFloat64` is NOT usable here: the collectors are unexported fields of the `metricsImpl` struct, so read through `reg.Gather()` as `pkg/metrics_test.go` already does.
- `github.com/bborbe/errors` — `errors.Wrap`, `errors.Wrapf`, `errors.Errorf`. Never `fmt.Errorf`.
</context>

<requirements>

### 1. The claim constant — `pkg/watcher.go`

Declare the backstop next to the guard helpers (same package, exported so tests derive their fixtures from it instead of hard-coding `337h`):

```go
// InFlightTTL is the backstop for an emission claim whose task never opened a
// pull request. It is not the normal clearing path — convergence and the
// appearance of the update PR are — so it must be longer than a HEALTHY queue
// wait or it re-creates the duplicate it exists to prevent. Fourteen days is
// twice the maximum queue latency measured on the Go 1.27.2 rollout (7 days for
// bborbe/agent-code, 3.5 days for bborbe/agent-gemini). Revisit when the
// dispatch-latency work lands: a shorter healthy latency permits a shorter
// backstop.
const InFlightTTL = 14 * libtime.Day
```

`InFlightTTL` must NOT become configurable — no flag, no env var, no struct field, no chart value (spec Non-goal: a knob with no consumer adds a flag, a chart value and a README row).

### 2. The gate reports the branch — `pkg/watcher.go` `openUpdatePRGate`

Change the signature to `(action string, openUpdate bool)` and return the second value only from the `openUpdate == true` branch. Everything else about the gate keeps its current semantics.

```go
// openUpdatePRGate applies the always-on open-PR in-flight gate (spec 003) to
// repo: an open update PR (fix/update-go-* head branch) means an update task is
// already underway, so the repo must not be emitted no matter how far head_sha
// has moved. Returns the repo's cycle action and, separately, whether an open
// update PR was actually observed:
//
//   - ("", false) — no open update PR, continue normal processing.
//   - ("skip", true) — repo has an open update PR: drop the repo from this
//     cycle AND clear the emission claim, because the emission demonstrably
//     landed (spec 005 DB3).
//   - ("skip", false) — the in-flight check failed non-fatally: drop the repo
//     from this cycle (nothing is emitted on uncertainty; the next poll
//     re-checks). The claim is deliberately NOT cleared here — a transient
//     GitHub error must not re-arm the emission this spec exists to suppress.
//   - ("rate_limited", false) — the in-flight check was rate limited: abort the
//     cycle; the claim is neither written nor cleared.
func (w *watcher) openUpdatePRGate(
	ctx context.Context,
	repo Repo,
) (string, bool) {
```

Body: return `("rate_limited", false)` from the rate-limit branch, `("skip", false)` after `dropRepo(repo, "open_update_pr", err)`, `("skip", true)` from the `openUpdate` branch, `("", false)` at the end. Keep the existing log lines and the existing `w.metrics.IncFilterSkipped("open_update_pr")` call byte-for-byte.

The second return value exists because `"skip"` alone cannot distinguish the two branches, and a second `HasOpenUpdatePR` call is forbidden by the Constraints (no extra API call per repo per cycle).

### 3. The guard and the two clear helpers — `pkg/watcher.go`

Add three package-level functions/methods below `openUpdatePRGate`.

```go
// clearInFlight drops the emission claim for repoKey when it targets target.
// It mutates the existing RepoState in place — like completeTask does — so
// LastSeenHeadSHA, LastSeenGoVersion and CompletedHeadSHA survive untouched.
func clearInFlight(cursorState *Cursor, repoKey, target string) {
	state := cursorState.Repos[repoKey]
	if state == nil || state.InFlightTarget != target {
		return
	}
	state.InFlightTarget = ""
	state.InFlightSince = ""
}

// inFlightExpired reports whether the claim is past InFlightTTL. An unparsable
// InFlightSince counts as expired: a bad timestamp must not suppress a repo
// forever. Expiring fails open — the repo is re-emitted and a duplicate is
// absorbed downstream by the deterministic task identifier — which is the
// pre-fix behaviour, not a permanent stall.
func inFlightExpired(ctx context.Context, state *RepoState, now libtime.DateTime) bool {
	since, err := libtime.ParseDateTime(ctx, state.InFlightSince)
	if err != nil {
		glog.Warningf(
			"emission claim has unparsable in_flight_since value=%q err=%v",
			state.InFlightSince,
			err,
		)
		return true
	}
	return now.Sub(since) > InFlightTTL
}

// emissionInFlight reports whether a live emission claim must suppress this
// cycle's emission for repo, clearing the claim when it is no longer valid.
// Evaluated after openUpdatePRGate and before the cycle filter chain: the
// gate's answer is the claim's primary clearing signal and its PR list is
// already fetched, while the filter chain cannot see the gate's result (a
// TaskCreationFilter receives only FilterCandidate).
//
// A claim only suppresses when it targets the cycle's current target version —
// a new stable Go release is emitted immediately, exactly as the two-key
// sha_unchanged gate already allows.
func (w *watcher) emissionInFlight(
	ctx context.Context,
	cursorState *Cursor,
	repo Repo,
	candidate Candidate,
) bool {
	target := candidate.LatestGo.Number()
	state := cursorState.Repos[repo.Key()]
	if state == nil || state.InFlightTarget == "" || state.InFlightTarget != target {
		return false
	}
	// Convergence: the same predicate the go_current filter uses, so equal-to,
	// ahead-of and unparsable all clear the claim exactly as that filter drops
	// the repo. Comparing CurrentGo to LatestGo by equality would leave the
	// claim live on the ahead-of and unparsable cases.
	if !candidate.GoBehind() {
		clearInFlight(cursorState, repo.Key(), target)
		return false
	}
	if inFlightExpired(ctx, state, w.currentDateTimeGetter.Now()) {
		clearInFlight(cursorState, repo.Key(), target)
		return false
	}
	return true
}
```

### 4. Wire the guard into `processRepos` — `pkg/watcher.go`

Replace the existing gate switch with the two-value form and the PR-clear, then insert the suppression check immediately after it and BEFORE the `if reason := cycleFilter.Skip(...)` block:

```go
		action, openUpdate := w.openUpdatePRGate(ctx, repo)
		switch action {
		case "rate_limited":
			return "rate_limited"
		case "skip":
			// An observed open update PR means the emission landed, so the
			// pre-PR claim is spent. The error branch also returns "skip" and
			// must leave the claim alone (spec 005 DB3).
			if openUpdate {
				clearInFlight(cursorState, repo.Key(), candidate.LatestGo.Number())
			}
			continue
		}

		if w.emissionInFlight(ctx, cursorState, repo, candidate) {
			w.metrics.IncFilterSkipped("emission_in_flight")
			glog.V(2).Infof(
				"repo skipped repo=%s reason=%s",
				repo.Key(),
				"emission_in_flight",
			)
			continue
		}
```

Semantics (all mandatory):
- The guard is NOT appended to the cycle filter chain and is NOT a `TaskCreationFilter`. It lives in `processRepos` because it needs the gate's result and the full `Candidate`.
- The skip is logged at `glog.V(2)` in the existing `"repo skipped repo=%s reason=%s"` shape, deliberately not at Warning — a skip is routine and fires once per claimed repo per cycle, whereas `dropRepo` logs at Warning because a drop is an anomaly. The observable is the metric, never the log line: the suites run at V(0) and cannot see it.
- `force=true` does NOT bypass the guard: the guard sits outside the chain, and a forced cycle is an operator "poll now", not an "emit regardless" (spec 003 precedent). Do not thread `force` into the guard.
- A rate limit **inside `processRepos`** returns before `SaveCursor`, so an in-memory clear from an earlier repo is discarded with the abort — no code is needed for this, just do not move the clear outside `processRepos`. (A rate limit in the later `completeMergedUpdates` pass does save the cursor, `pkg/watcher.go:125-134`; that path is out of scope here.)
- Do NOT touch `gatherCandidate`, `completeMergedUpdates`, `completeTask`, `markPublished`, or the `publish`/`markPublished` tail of the loop.

### 5. Skip-reason label — `pkg/metrics.go`

Append `"emission_in_flight"` to `FilterSkipReasons` after `"open_update_pr"`, and add it to the `IncFilterSkipped` doc comment's reason list. This pre-initialises `filter_skipped_total{reason="emission_in_flight"}` to 0 so the series exists before the first cycle and the AC's "stays 0" assertions are meaningful rather than trivially satisfied by a missing series.

### 6. Metric tests — `pkg/metrics_test.go`

Add one `It` mirroring the existing "pre-initialises auto_update_undecided to 0" test: `pkg.NewMetrics(reg)` then walk `reg.Gather()` for `github_update_go_watcher_filter_skipped_total` with `reason="emission_in_flight"` and assert the value is `0.0`; `Fail(...)` with a clear message if the series is absent. Also assert `pkg.FilterSkipReasons` contains `"emission_in_flight"` (the membership guard the watcher suite derives from).

### 7. Watcher tests — `pkg/watcher_test.go`

Add a new TOP-LEVEL `Describe("emission claim (spec 005)", ...)` as a sibling of `Describe("watcher")`. It must use the REAL metrics implementation so the counters can be read from a registry (the existing mock `*mocks.Metrics` cannot prove a series value):

```go
var _ = Describe("emission claim (spec 005)", func() {
```

Setup, in `BeforeEach`:

```go
		reg = prometheus.NewRegistry()
		metrics = pkg.NewMetrics(reg)
		currentDateTime = libtime.NewCurrentDateTime()
		currentDateTime.SetNow(libtimetest.ParseDateTime("2026-10-27T12:00:00Z"))
		cursorPath = GinkgoT().TempDir() + "/cursor.json"
		ghClient = &mocks.GitHubClient{}
		goDevClient = &mocks.GoDevClient{}
		publisher = &mocks.TaskPublisher{}
		completeSender = new(taskmocks.TaskCompleteCommandSender)

		ghClient.ListReposReturns([]pkg.Repo{
			{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
		}, nil)
		ghClient.GetHeadSHAReturns(newSHA, nil)
		ghClient.GetGoModReturns([]byte("go 1.27.1"), nil)
		ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
		ghClient.HasOpenUpdatePRReturns(false, nil)
		goDevClient.LatestStableReturns(pkg.Version{
			Major: 1, Minor: 27, Patch: 2, Raw: "1.27.2",
		}, nil)
		publisher.PublishCreateReturns(true)

		watcher = pkg.NewWatcher(
			ghClient,
			goDevClient,
			publisher,
			completeSender,
			metrics,
			cursorPath,
			"bborbe",
			filter.TaskCreationFilterList{
				filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/disk-status"}),
				filter.NewGoModPresentFilter(),
				filter.NewGoModParsableFilter(),
				filter.NewGoBehindFilter(),
				filter.NewAutoUpdateFilter(),
			},
			currentDateTime,
		)
```

Declare the Describe's own state — a new top-level `Describe` cannot see `Describe("watcher")`'s closures, so every variable and the `now` helper must be declared locally:

```go
	var (
		reg             *prometheus.Registry
		metrics         pkg.Metrics
		currentDateTime libtime.CurrentDateTime
		watcher         pkg.Watcher
		cursorPath      string
		ghClient        *mocks.GitHubClient
		goDevClient     *mocks.GoDevClient
		publisher       *mocks.TaskPublisher
		completeSender  *taskmocks.TaskCompleteCommandSender
	)
	const (
		repoKey = "github.com/bborbe/disk-status"
		oldSHA  = "aaaa000000000000000000000000000000000000"
		newSHA  = "bbbb000000000000000000000000000000000000"
	)
	// now reads the injected clock — never time.Now().
	now := func() libtime.DateTime { return currentDateTime.Now() }
```

Target `1.27.2`, behind go.mod `go 1.27.1`. Note `newSHA != oldSHA` in every claim fixture — that is what makes the suppression attributable to the claim rather than to `sha_unchanged`.

Helpers to define once inside the Describe:

```go
	writeCursorState := func(repoStateJSON string) {
		content := `{"repos":{"` + repoKey + `":` + repoStateJSON + `}}`
		Expect(os.WriteFile(cursorPath, []byte(content), 0600)).To(Succeed())
	}

	readRepoState := func() *pkg.RepoState {
		c, err := pkg.LoadCursor(context.Background(), cursorPath)
		Expect(err).NotTo(HaveOccurred())
		return c.Repos[repoKey]
	}

	// filterSkippedValue reads a counter through the registry — never through a
	// mock spy, and never from a log line (the skip line is V(2)).
	filterSkippedValue := func(reason string) float64 {
		families, err := reg.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, f := range families {
			if f.GetName() != "github_update_go_watcher_filter_skipped_total" {
				continue
			}
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "reason" && l.GetValue() == reason {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		Fail("filter_skipped_total series not found reason=" + reason)
		return 0
	}

	claimState := func(since libtime.DateTime) string {
		return `{"last_seen_head_sha":"` + oldSHA + `","last_seen_go_version":"1.27.2",` +
			`"in_flight_target":"1.27.2","in_flight_since":"` +
			since.Format(time.RFC3339) + `"}`
	}
```

`now` is `currentDateTime.Now()` — use it for the fixtures so every timestamp is derived from the injected clock, never from `time.Now()`.

Tests, one per acceptance criterion:

1. **"a live claim suppresses a second emission"** — `writeCursorState(claimState(now().Add(-libtime.Hour)))`, HEAD moved (`newSHA` vs `oldSHA`), go.mod behind, no open PR. `Poll(ctx, false)` → `publisher.PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 1`, `filterSkippedValue("sha_unchanged") == 0` (proves the claim, not the SHA gate, did the suppressing).
2. **"convergence clears the claim"** — same fixture, `ghClient.GetGoModReturns([]byte("go 1.27.2"), nil)`. After `Poll`: `readRepoState().InFlightTarget == ""`, `readRepoState().InFlightSince == ""`, `filterSkippedValue("go_current") == 1`, `filterSkippedValue("emission_in_flight") == 0`, `PublishCreateCallCount() == 0`.
3. **"ahead-of clears the claim"** — same fixture, go.mod `go 1.28`. After `Poll`: `readRepoState().InFlightTarget == ""`, `filterSkippedValue("emission_in_flight") == 0`, `PublishCreateCallCount() == 0`.
4. **"an unparsable go.mod clears the claim"** — same fixture, go.mod `go banana`. After `Poll`: `readRepoState().InFlightTarget == ""`, `filterSkippedValue("emission_in_flight") == 0`, `PublishCreateCallCount() == 0`.
5. **"an open update PR clears the claim"** — claim live for the current target, `ghClient.HasOpenUpdatePRReturns(true, nil)`, go.mod behind. After `Poll`: `filterSkippedValue("open_update_pr") == 1`, `filterSkippedValue("emission_in_flight") == 0`, `readRepoState().InFlightTarget == ""`, `PublishCreateCallCount() == 0`.
6. **"a GitHub error does not clear the claim"** — claim live, `ghClient.HasOpenUpdatePRReturns(false, errors.New("open pr check failed"))`, go.mod behind. After `Poll`: `PublishCreateCallCount() == 0`, `readRepoState().InFlightTarget == "1.27.2"` and `readRepoState().InFlightSince` unchanged, `filterSkippedValue("open_update_pr") == 0`, `filterSkippedValue("emission_in_flight") == 0` (the repo was dropped, not skipped).
7. **"the TTL re-arms the emission"** — claim with `in_flight_since = now().Add(-(pkg.InFlightTTL + libtime.Hour))`, no open PR, HEAD moved, go.mod behind. After `Poll`: `publisher.PublishCreateCallCount() == 1` (exactly one) and `readRepoState().InFlightSince == now().Format(time.RFC3339)` — the claim was re-armed at this cycle's clock, not left at the expired value.
8. **"a repo with no claim still emits, and does not emit twice"** — no cursor file at all. First `Poll` → `PublishCreateCallCount() == 1`. Second `Poll` at the same HEAD → still `1`. Do NOT assert the second cycle's skip reason is `sha_unchanged`: after the first publish the claim is live for the same target and the guard runs BEFORE the filter chain, so the reason is `emission_in_flight`. Assert `filterSkippedValue("emission_in_flight") == 1` if you want the reason pinned — that precedence is intended, not a bug.
9. **"a forced cycle does not bypass the claim"** — claim live, `Poll(ctx, true)` → `PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 1`.
10. **"a different target version is not suppressed"** — the claim targets an older release, so `claimState` is not reusable here: write the fixture inline with `"in_flight_target":"1.27.1"` and `"last_seen_go_version":"1.27.1"` (keep `"last_seen_head_sha":"` + oldSHA + `"` and `in_flight_since` from `now()`), while the cycle's target is `1.27.2` and go.mod is behind. After `Poll`: `PublishCreateCallCount() == 1` and `readRepoState().InFlightTarget == "1.27.2"` (the claim was re-keyed to the new target).
11. **"an unparsable in_flight_since fails open"** — write the fixture inline with `"in_flight_target":"1.27.2"` and `"in_flight_since":"not-a-timestamp"`, HEAD moved, go.mod behind. After `Poll`: `PublishCreateCallCount() == 1` and `readRepoState().InFlightSince == now().Format(time.RFC3339)` — `inFlightExpired` treats an unparsable value as expired, so the claim is cleared and re-armed instead of suppressing the repo forever (the error branch of requirement 3).
12. **"a rate-limited gate leaves the claim untouched"** — claim live for the current target, `ghClient.HasOpenUpdatePRReturns(false, pkg.ErrRateLimited)`. After `Poll`: `PublishCreateCallCount() == 0` and `readRepoState().InFlightTarget == "1.27.2"` (the cycle aborted before `SaveCursor`, so the file still holds the seeded claim). This is spec failure-modes row 3.

Imports this Describe adds to `pkg/watcher_test.go` (some are already present — do not duplicate): `"time"`, `"os"`, `"context"`, `"errors"`, `taskmocks "github.com/bborbe/agent/mocks"`, `. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`, `"github.com/prometheus/client_golang/prometheus"`, `libtime "github.com/bborbe/time"`, `libtimetest "github.com/bborbe/time/test"`, `"github.com/bborbe/github-update-go-watcher/mocks"`, `"github.com/bborbe/github-update-go-watcher/pkg"`, `"github.com/bborbe/github-update-go-watcher/pkg/filter"`.

Existing contexts must keep passing unchanged. The one intentional precedence change is that a repo with a live claim now reports `emission_in_flight` instead of `sha_unchanged` — no existing test asserts that combination (their cursor seeds carry no `in_flight_target`), so do not rewrite them.

</requirements>

<constraints>
- No vault, task-store or filesystem read outside the existing cursor file — `docs/dod.md:30` is a durable contract. Do not add a task-store lookup.
- The open-PR gate, the `sha_unchanged` filter and the task identity seed keep their current semantics; this adds a layer, it does not replace one. `openUpdatePRGate` keeps every existing behaviour and only gains the second return value.
- The guard adds NO GitHub API call: it reuses the `HasOpenUpdatePR` result the gate already fetched. Never call `HasOpenUpdatePR` twice per repo per cycle.
- `ErrRateLimited` keeps its existing behaviour (abort the cycle); the claim is neither written nor cleared on an aborted cycle.
- No config knob for the TTL, no opt-out flag, no per-repo lock, no "one open record per repo" rule (spec Non-goals). `InFlightTTL` is one package constant.
- The `- reemit` repair traffic is not suppressed: the claim is written only by `markPublished`, so hand-authored records with non-seed identifiers are never covered.
- The skip line stays at `glog.V(2)`; the observable is `IncFilterSkipped`. Never assert on log text in a test.
- Do NOT touch `pkg/cursor.go` (the fields exist), `pkg/candidate.go`, `pkg/filter/`, `pkg/taskbuilder.go`, `pkg/taskid.go`, `pkg/taskpublisher.go`, `pkg/githubclient.go`, `pkg/factory/`, `main.go`, `docs/`, `README.md`, or `CHANGELOG.md`.
- Never use `fmt.Errorf` — all errors go through `github.com/bborbe/errors` and carry ctx. Never call `time.Now()` in `pkg/` — read `w.currentDateTimeGetter.Now()`.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory. `Watcher`, `Metrics` and `GitHubClient` interfaces are unchanged here, so the regenerated mocks are identical.
- Tests use Ginkgo/Gomega; the suite's only entry point is `TestSuite` in `pkg/pkg_suite_test.go` (a `-run <Describe>` selector matches nothing and exits 0 — never use one).
- Keep every line under 100 characters and every function under 80 lines / 50 statements. `processRepos` is already near the limit — extract the helpers as specified rather than inlining them.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root:

```
make precommit
```

Must exit 0 (format + generate + test + lint/vet/vuln/osv/trivy + addlicense).

```
go test ./pkg/... -count=1
```

Must exit 0.

```
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='emission claim'
```

Must exit 0 and report the twelve specs of the new `Describe` as run.

```
grep -n "emission_in_flight" pkg/metrics.go pkg/watcher.go
```

Must match in both files (`FilterSkipReasons` entry + doc comment in `metrics.go`; `IncFilterSkipped` call and skip line in `watcher.go`).

```
grep -n -A3 "func (w \*watcher) openUpdatePRGate" pkg/watcher.go
```

Must show the two-value signature `(string, bool)` in the printed block — the return type sits on a later line than the `func` keyword, so a bare `grep -n` would print nothing useful and still exit 0.

```
grep -rn "time.Now()" pkg/ --include='*.go'
```

Must print nothing.

```
git diff -U0 origin/master...HEAD -- pkg/ | grep -c '^+.*\(obsidian\|task_store\|vault task\)'
```

Must print `0`. Read the printed count, not the exit code (`grep -c` exits 1 at zero). The base MUST stay pinned to `origin/master`: a bare `git diff` is empty once the work is committed and passes vacuously. If `origin/master` is not resolvable in this checkout, report that instead of assuming a pass — do not substitute an absolute grep over the tree, which matches two pre-existing comment lines (`pkg/watcher.go`, `pkg/githubclient.go`). This check needs `.git` present: `.dark-factory.yaml` sets `workflow: direct` with no `hideGit`, so it works today — if `hideGit: true` or `workflow: worktree` is ever enabled, move this check to the spec's verification ladder rather than letting it pass silently.
</verification>
