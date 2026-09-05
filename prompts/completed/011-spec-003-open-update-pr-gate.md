---
status: completed
spec: [003-bug-update-go-watcher-reemits-open-repo]
summary: Added an always-on open-PR in-flight gate (HasOpenUpdatePR) to the watcher so repos with an open fix/update-go-* PR are never re-emitted, with the update branch prefix consolidated into a single updateBranchPrefix constant shared by the open-PR gate and merged-PR completion pass; added skip reason open_update_pr, client + watcher tests, README row, and CHANGELOG entry
execution_id: github-update-go-watcher-openpr-exec-011-spec-003-open-update-pr-gate
dark-factory-version: dev
created: "2026-09-05T12:00:28Z"
queued: "2026-09-05T12:03:39Z"
started: "2026-09-05T12:03:41Z"
completed: "2026-09-05T12:09:06Z"
---

<summary>
- A repo with an open `fix/update-go-*` PR is never emitted, on any cycle (including forced ones), regardless of head_sha — new commits to a repo holding an open update PR add no tasks.
- The `GitHubClient` interface gains a read-only `HasOpenUpdatePR` method that lists open PRs and matches any head branch carrying the update prefix client-side.
- The update branch prefix becomes a single-source constant shared by the new open-PR gate and the existing merged-PR completion pass; the literal appears in exactly one production line.
- The watcher runs the gate after candidate gathering and before the cycle-filter skip: open update PR → skip with reason `open_update_pr`; non-rate-limit error → repo dropped this cycle; rate limit → whole cycle aborts with result `rate_limited`.
- The counterfeiter mock is regenerated automatically (make precommit); existing tests keep passing because the mock's zero value (false, nil) skips nothing.
- Tests cover the client boundary (httptest against the real go-github list call, state=open asserted) and the watcher gate (publisher spy, forced cycles, drop, abort).
- No opt-out knob, no new dependencies, nothing writes to observed repos, and the merged-PR detection pass is untouched.
</summary>

<objective>
Stop re-emitting github-update-go tasks for repos that already have an update in flight. The GitHub open `fix/update-go-*` PR is the in-flight signal (the watcher pod has no vault access), so the watcher gains an always-on gate: a repo with an open update PR is skipped on every cycle regardless of head_sha. Land the interface method, the single-source prefix constant, the watcher gate, the regenerated mock, and the unit tests as one atomic change.
</objective>

<context>
Read `docs/dod.md` — it documents the in-flight signal contract and names the constant: `updateBranchPrefix` in `pkg/githubclient.go`.

Read these coding plugin docs before writing code (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read these repo files before writing code:
- `pkg/githubclient.go` — the `GitHubClient` interface, the `githubClient` implementation, `ErrRateLimited` (`var ErrRateLimited = stderrors.New("github rate limited")`), the `isRateLimitError` / `isNotFound` / `wrapRateLimitErr` helpers, and `GetMergedUpdatePR` (the error-contract template the new method must mirror exactly).
- `pkg/watcher.go` — `processRepos` (where the gate goes), `dropRepo(repo Repo, step string, err error) (Candidate, string, bool)` (the always-on "repo dropped from cycle" log; keep that phrase), `completeMergedUpdates` (the existing rate-limit abort pattern to mirror).
- `pkg/metrics.go` — `FilterSkipReasons` (closed label set, pre-initialised to 0 by `NewMetrics`) and `IncFilterSkipped(reason string)`.
- `pkg/githubclient_test.go` — the httptest + `pkg.SetBaseURL(client, server.URL+"/")` pattern and the `GetMergedUpdatePR` Describe (note it asserts `state=all` on the handler; the new test asserts `state=open`).
- `pkg/watcher_test.go` — the mock-stub patterns, the `metrics.IncFilterSkippedStub = func(string) {}` + `IncFilterSkippedArgsForCall(0)` idiom, and the "metric label containment" test that iterates `pkg.FilterSkipReasons`.
- `pkg/pkg_suite_test.go` — carries the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` line that regenerates mocks; `pkg/githubclient.go` carries `//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient`.
- `README.md` — the "Skip reasons" table (one row per `filter_skipped_total` label).
- `CHANGELOG.md` — add the change under `## Unreleased`.

Verified library API facts (do not re-derive from memory):
- `github.com/google/go-github/v84` (imported as `gogithub "github.com/google/go-github/v84/github"`): `client.PullRequests.List(ctx, owner, repo string, opts *gogithub.PullRequestListOptions) ([]*gogithub.PullRequest, *gogithub.Response, error)`. `PullRequestListOptions{State string, Head string, Base string, ListOptions gogithub.ListOptions}`. `pr.GetHead()` returns `*gogithub.PullRequestBranch` (nil-safe), `(*PullRequestBranch).GetRef() string` — verified in v84 accessors.
- `github.com/bborbe/errors` — `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never `fmt.Errorf`.
- stdlib `strings.HasPrefix` is the client-side prefix match.
</context>

<requirements>

### 1. Single-source prefix constant — `pkg/githubclient.go`

Add the constant next to the existing constants (`maxContentBytes`, `maxListPages`), with a doc comment explaining it is the single source of the update branch prefix shared by the open-PR in-flight gate and the merged-PR completion pass, kept in sync with the agent's `branchPrefix`:

```go
// updateBranchPrefix is the single source of the head-branch prefix the
// github-update-go-agent opens for update tasks. Both the open-PR in-flight
// gate (HasOpenUpdatePR) and the merged-PR completion pass (GetMergedUpdatePR)
// build branches from it. Kept in sync with the agent's branchPrefix.
const updateBranchPrefix = "fix/update-go-"
```

Refactor the existing `updateBranchName` (currently returns the string literal `"fix/update-go-" + short` at the bottom of `pkg/githubclient.go`) to build from the constant:

```go
func updateBranchName(headSHA string) string {
	short := headSHA
	if len(short) > 7 {
		short = short[:7]
	}
	return updateBranchPrefix + short
}
```

Reword `updateBranchName`'s doc comment (currently quotes the literal: `// "fix/update-go-<sha[:7]>". Kept in sync with the agent's branchPrefix ...`) to reference the constant instead — e.g. `// updateBranchPrefix + "<sha[:7]>". Kept in sync with the agent's branchPrefix ...` — otherwise the quoted literal in the comment is a second grep match.

After this step the string literal `"fix/update-go-` appears in exactly ONE non-test line of `pkg/` (the constant declaration). Do not introduce it anywhere else.

### 2. Interface method — `pkg/githubclient.go`

Add this method to the `GitHubClient` interface, directly after `GetMergedUpdatePR` (keep the existing methods unchanged):

```go
// HasOpenUpdatePR reports whether repo has an open pull request whose head
// branch carries the update prefix (fix/update-go-*). An open update PR is the
// in-flight signal for the update task: while one is open the watcher must not
// emit another create-task command for the repo, no matter how far head_sha has
// moved (spec 003). Open PRs are listed state=open and the prefix match is
// client-side.
//
//   - (true, nil) — at least one open PR with a fix/update-go-* head branch.
//   - (false, nil) — no such open PR.
//   - (false, ErrRateLimited) on primary or abuse rate limiting.
//   - (false, wrapped error) on every other failure.
HasOpenUpdatePR(ctx context.Context, repo Repo) (bool, error)
```

### 3. Implementation — `pkg/githubclient.go`

Add `"strings"` to the import block. Implement the method on `*githubClient`, mirroring `GetMergedUpdatePR` exactly (single page, PerPage 100 — do NOT add pagination; the sibling merged check has none and open PRs per repo are few):

```go
func (c *githubClient) HasOpenUpdatePR(ctx context.Context, repo Repo) (bool, error) {
	opts := &gogithub.PullRequestListOptions{
		State: "open",
		ListOptions: gogithub.ListOptions{
			PerPage: 100,
		},
	}
	prs, _, err := c.client.PullRequests.List(ctx, repo.Owner, repo.Name, opts)
	if err != nil {
		if isRateLimitError(err) {
			return false, ErrRateLimited
		}
		glog.Warningf(
			"list pull requests %s state=open failed: %v",
			repo.String(),
			err,
		)
		return false, errors.Wrapf(
			ctx, err, "list open pull requests %s", repo.String(),
		)
	}
	for _, pr := range prs {
		select {
		case <-ctx.Done():
			return false, errors.Wrap(
				ctx, ctx.Err(), "context cancelled during HasOpenUpdatePR",
			)
		default:
		}
		if strings.HasPrefix(pr.GetHead().GetRef(), updateBranchPrefix) {
			return true, nil
		}
	}
	return false, nil
}
```

Error contract MUST mirror `GetMergedUpdatePR` exactly: rate limit → `ErrRateLimited`; every other failure → `glog.Warningf` + `errors.Wrapf` carrying ctx. The client stays read-only (only `PullRequests.List`).

### 4. Watcher gate — `pkg/watcher.go`

In `processRepos`, insert the gate AFTER the `candidate, abortReason, dropped := w.gatherCandidate(ctx, repo, latestGo)` block's abort/drop handling and BEFORE the `if reason := cycleFilter.Skip(candidate.FilterCandidate()); reason != ""` block:

```go
		openUpdate, err := w.ghClient.HasOpenUpdatePR(ctx, repo)
		if err != nil {
			if stderrors.Is(err, ErrRateLimited) {
				glog.Warningf(
					"poll cycle aborted: rate limited during HasOpenUpdatePR owner=%s",
					w.owner,
				)
				return "rate_limited"
			}
			dropRepo(repo, "open_update_pr", err)
			continue
		}
		if openUpdate {
			w.metrics.IncFilterSkipped("open_update_pr")
			glog.V(2).Infof(
				"repo skipped repo=%s reason=%s",
				repo.Key(),
				"open_update_pr",
			)
			continue
		}
```

Semantics (all mandatory):
- The gate runs for EVERY candidate that survives candidate gathering, every cycle — including forced cycles (`force=true`); it is NOT the SHA cursor and `force` does not bypass it (only `sha_unchanged` is force-omitted, per spec Desired Behavior 4). `HasOpenUpdatePR` does not receive `headSHA` — the in-flight signal is repo-wide.
- Open update PR → skip the repo (do NOT publish), record `IncFilterSkipped("open_update_pr")`, log at V(2) with the same `"repo skipped repo=%s reason=%s"` shape the filter chain uses.
- Non-rate-limit error → drop the repo this cycle via `dropRepo(repo, "open_update_pr", err)` and `continue` (nothing emitted on uncertainty; the next poll re-checks). `dropRepo` returns values that are discarded here — call it as a statement.
- Rate limit → abort the whole cycle with `return "rate_limited"` (identical to the existing merge-detection abort in `completeMergedUpdates`; `Poll` turns the returned reason into `metrics.IncPollCycle("rate_limited")`).
- Do NOT touch `gatherCandidate`, `completeMergedUpdates`, `GetMergedUpdatePR`, or any other existing code path.

### 5. Metrics label set — `pkg/metrics.go`

Add `"open_update_pr"` to the `FilterSkipReasons` slice (after `"sha_unchanged"`) and to the `IncFilterSkipped` doc-comment reason list. This keeps the closed label set in lockstep with the new skip verdict, makes `NewMetrics` pre-initialise `filter_skipped_total{reason="open_update_pr"}=0`, and keeps the existing "metric label containment" test in `pkg/watcher_test.go` passing (it derives its valid-label set from `pkg.FilterSkipReasons`).

### 6. Client tests — `pkg/githubclient_test.go`

Add a new `Describe("HasOpenUpdatePR", ...)` following the exact `GetMergedUpdatePR` Describe shape (httptest handler routed by `r.URL.Path`, `pkg.NewGitHubClient(server.Client())`, `pkg.SetBaseURL(client, server.URL+"/")`). The handler for the positive case MUST assert `r.URL.Query().Get("state") == "open"` (reject otherwise with `http.Error(w, "expected state=open", http.StatusBadRequest)`) — regression guard: flipping the query to `all` would silently treat completed PRs as in-flight.

Contexts to cover:
- Open update PR exists — payload `[{"number": 1, "state": "open", "merged": null, "head": {"ref": "fix/update-go-d630ef3"}}]` → returns `(true, nil)`.
- Only non-update open PRs — payload `[{"number": 1, "state": "open", "merged": null, "head": {"ref": "feature/foo"}}]` → returns `(false, nil)`.
- Mixed open PRs where one carries the update prefix — payload with two entries, heads `feature/foo` and `fix/update-go-d630ef3` → returns `(true, nil)` (prefix match is client-side, any matching head wins).
- No open PRs — payload `[]` → returns `(false, nil)`.
- Rate limited — the standard 403 + `X-RateLimit-Remaining: 0` / `X-RateLimit-Reset: 0` payload → `errors.Is(err, pkg.ErrRateLimited)` is true.

### 7. Watcher tests — `pkg/watcher_test.go`

Add a new `Context("open update PR gate (spec 003)", ...)` under `Describe("Poll")`. The `BeforeEach` sets up the full happy path (one allowlisted repo `github.com/bborbe/disk-status`, `GetHeadSHA`, `GetGoMod` behind stable, `GetMaintainerConfig` → `filter.GrantedConsent`, `LatestStable` ahead, `publisher.PublishCreateReturns(true)`, `metrics.IncFilterSkippedStub = func(string) {}`, `buildWatcher()`), so the repo WOULD be published absent the gate. Then:

- "skips a repo with an open update PR and publishes nothing" — `ghClient.HasOpenUpdatePRReturns(true, nil)`; Poll(false) → `PublishCreateCallCount() == 0` and `IncFilterSkippedArgsForCall(0) == "open_update_pr"` (publisher spy).
- "emits a repo with no open update PR" — `ghClient.HasOpenUpdatePRReturns(false, nil)`; Poll(false) → `PublishCreateCallCount() == 1`.
- "aborts the cycle when HasOpenUpdatePR is rate limited" — `ghClient.HasOpenUpdatePRReturns(false, pkg.ErrRateLimited)`; Poll(false) → `PublishCreateCallCount() == 0`, no error returned, and the last `IncPollCycle` arg is `"rate_limited"` (mirror the existing `GetMergedUpdatePR` rate-limit test's `IncPollCycleArgsForCall(IncPollCycleCallCount() - 1)` assertion).
- "drops the repo on a non-rate-limit HasOpenUpdatePR error and continues" — `ghClient.HasOpenUpdatePRReturns(false, errors.New("open pr check failed"))`; Poll(false) → `PublishCreateCallCount() == 0`, `IncFilterSkippedCallCount() == 0` (dropped before the filter chain, not a skip verdict), and the last `IncPollCycle` arg is `"success"` (cycle continues).
- "applies on forced cycles" — `ghClient.HasOpenUpdatePRReturns(true, nil)`; Poll(true) → `PublishCreateCallCount() == 0` (without the gate, force=true would publish).

Existing tests must keep passing unchanged — the counterfeiter mock's default zero value for `HasOpenUpdatePR` is `(false, nil)`, which skips nothing. Do not modify existing contexts.

### 8. README and CHANGELOG

`README.md` — add one row to the "Skip reasons" table:

```
| `open_update_pr` | Repo has an open `fix/update-go-*` pull request — an update is already in flight, so no new task is emitted (always-on gate, spec 003) |
```

`CHANGELOG.md` — add one `- fix:` entry under `## Unreleased` (read the changelog guide first), following the existing entries' style, mentioning: the watcher no longer re-emits an update task for a repo with an open `fix/update-go-*` PR (open-PR in-flight gate via new `HasOpenUpdatePR`, skip reason `open_update_pr`, applies on every cycle including forced ones), and the branch prefix now lives in a single constant (`updateBranchPrefix`) shared by the open-PR gate and the merged-PR completion pass.

### 9. Regenerate mocks and verify

Run `make precommit` from the repo root. Its `generate` target (`rm -rf mocks && mkdir -p mocks && echo "package mocks" > mocks/mocks.go && go generate -mod=mod ./...`) regenerates `mocks/github_client.go` from the `//counterfeiter:generate` directive, so the new method appears in the mock automatically. Never hand-edit anything under `mocks/`.

</requirements>

<constraints>
- The gate gets NO opt-out knob — no config field, no flag, no threshold (spec Non-goal: a per-repo or global disable would re-introduce the bug). A repo with an open update PR is never emitted, period.
- `GitHubClient` stays read-only — nothing writes to observed repos; the only new upstream call is `PullRequests.List` with `state=open`.
- No new external dependencies — go-github v84 is already used.
- Errors follow `github.com/bborbe/errors` wrapping; info logs at glog V(2). The phrase "repo dropped from cycle" is the operator's grep handle — do not reword it.
- The existing merged-PR detection pass (`GetMergedUpdatePR` / `completeMergedUpdates`) is unchanged.
- The existing-duplicate backlog (216 open tasks) is NOT collapsed by this change — the live agent-task-controller drain handles that; this change only stops new growth.
- Do NOT touch `pkg/filter/`, `pkg/taskbuilder.go`, `pkg/taskid.go`, `pkg/taskpublisher.go`, `main.go`, `pkg/factory/`, or `pkg/auth/`.
- Never use `fmt.Errorf` — all errors go through `github.com/bborbe/errors` and carry ctx.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from scratch.
- Tests use Ginkgo/Gomega; counterfeiter fakes come from `//counterfeiter:generate` directives.
- Keep every line under 100 characters and every function under 80 lines / 50 statements.
- Every new `.go` file starts with the BSD license header block used by the existing files (no new `.go` files are expected for this change).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root:

```
make precommit
```
Must exit 0 (this regenerates mocks and runs format + lint + test + check + addlicense).

```
go test -mod=mod ./pkg/...
```
Must exit 0.

```
grep -n "HasOpenUpdatePR" pkg/githubclient.go pkg/watcher.go mocks/github_client.go
```
Each file must return at least one match.

```
grep -rEn '"fix/update-go-' pkg --include='*.go' | grep -v '_test.go'
```
Must return EXACTLY one line (the `updateBranchPrefix` constant).

```
grep -n "open_update_pr" pkg/watcher.go
```
Must return at least one match.

```
grep -n "updateBranchPrefix" pkg/githubclient.go
```
Must return at least one match.
</verification>
