---
status: completed
spec: [003-bug-update-go-watcher-reemits-open-repo]
summary: Verified the existing spec 003 open-update-PR gate end-to-end and added the missing `## Unreleased` CHANGELOG fix entry; make precommit passes.
execution_id: 512c61be-732d-552a-8e13-af7c168c3fec-exec-017-spec-003-open-update-pr-gate
dark-factory-version: dev
created: "2026-11-09T09:05:00Z"
queued: "2026-11-09T09:14:23Z"
started: "2026-11-09T09:14:24Z"
completed: "2026-11-09T09:23:43Z"
branch: dark-factory/bug-update-go-watcher-reemits-open-repo
---

<!--
OPEN QUESTION FOR THE HUMAN REVIEWER
The implementation this spec asks for already appears on the base branch at commit
66f5e37 ("011-spec-003-open-update-pr-gate"), which is an ancestor of HEAD 9dd35bf.
`pkg/githubclient.go` already declares `updateBranchPrefix` and `HasOpenUpdatePR`,
`pkg/watcher.go` already gates on it (helpers `openUpdatePRGate` / `gateRepo`),
`mocks/github_client.go` is regenerated, the client + watcher tests exist, and
README/CHANGELOG carry the `open_update_pr` row/entry. This prompt is therefore
written as a conformance / regression guard: verify each requirement against the
current code and change only what diverges; do NOT duplicate the method, constant,
label, or tests. If this spec was re-queued by mistake, discard this prompt.
-->

<summary>
- A repo that already has an open `fix/update-go-*` pull request is never emitted, on any cycle including forced ones, no matter how far its HEAD SHA has moved — new commits to a repo holding an open update PR add no tasks.
- The read-only GitHub client gains a `HasOpenUpdatePR` method that lists open pull requests and matches the update branch prefix client-side, with the same error contract as the existing merged-PR check (rate limit sentinel, otherwise a wrapped error).
- The update branch prefix lives in exactly one production constant, shared by the new open-PR gate and the existing merged-PR completion pass.
- The watcher runs the gate after candidate gathering and before the cycle-filter skip: an open update PR skips the repo with reason `open_update_pr`; any other check error drops the repo for this cycle; a rate limit aborts the whole cycle as `rate_limited`.
- The `filter_skipped_total` label set, the counterfeiter mock, the skip-reasons table, and the changelog all reflect the new reason.
- Tests cover both the client boundary (state=open asserted against a real HTTP handler) and the watcher gate (publisher spy: skip, emit, abort, drop, forced cycle).
- No opt-out knob, no new dependency, no writes to observed repos, and the merged-PR completion pass is untouched.
</summary>

<objective>
Guarantee at most one in-flight github-update-go task per repo: the presence of an open `fix/update-go-*` pull request is the in-flight signal (the watcher pod has no vault access), so the watcher must skip such a repo on every cycle regardless of head_sha. Ensure the interface method, single-source prefix constant, watcher gate, metrics label, regenerated mock, tests, and docs are all present and semantically correct; where prior work already satisfies a requirement, verify it and leave it in place.
</objective>

<context>
Read `docs/dod.md` — it documents the durable in-flight signal contract and names `updateBranchPrefix` in `pkg/githubclient.go` as the single source of the branch prefix.

Read these coding plugin docs before changing code (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read these repo files before changing code:
- `pkg/githubclient.go` — the `GitHubClient` interface, `githubClient`, `ErrRateLimited` (`var ErrRateLimited = stderrors.New("github rate limited")`), `isRateLimitError`, `GetMergedUpdatePR` (the error-contract template), `updateBranchPrefix`, `updateBranchName`.
- `pkg/watcher.go` — `processRepos`, the `gatherCandidate` abort/drop handling, the `openUpdatePRGate` / `gateRepo` helpers, `dropRepo(repo Repo, step string, err error) (Candidate, string, bool)`, `completeMergedUpdates` (the existing rate-limit abort pattern).
- `pkg/metrics.go` — `FilterSkipReasons` (closed label set, pre-initialised to 0 by `NewMetrics`) and `IncFilterSkipped(reason string)`.
- `pkg/githubclient_test.go` — the httptest + `pkg.SetBaseURL(client, server.URL+"/")` pattern and the `HasOpenUpdatePR` Describe (asserts `state=open` on the handler).
- `pkg/watcher_test.go` — the counterfeiter stub patterns, the `metrics.IncFilterSkippedStub = func(string) {}` + `IncFilterSkippedArgsForCall(0)` idiom, and the `Context("open update PR gate (spec 003)", ...)` block.
- `pkg/pkg_suite_test.go` — carries `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate`; `pkg/githubclient.go` carries `//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient`.
- `README.md` — the "Skip reasons" table.
- `CHANGELOG.md` — the entry under the current Unreleased/release heading.

Verified library API facts (do not re-derive from memory):
- `gogithub "github.com/google/go-github/v84/github"`: `client.PullRequests.List(ctx context.Context, owner, repo string, opts *gogithub.PullRequestListOptions) ([]*gogithub.PullRequest, *gogithub.Response, error)`. `PullRequestListOptions{State string, Head string, Base string, ListOptions gogithub.ListOptions}`. `pr.GetHead()` returns `*gogithub.PullRequestBranch` (nil-safe); `(*PullRequestBranch).GetRef() string`.
- `github.com/bborbe/errors`: `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never `fmt.Errorf`.
- stdlib `strings.HasPrefix` is the client-side prefix match.
</context>

<requirements>

0. **Inspect first.** Read the files listed in `<context>`. The gate may already be present from prior work. For each requirement below: if it is already satisfied, verify the semantics match exactly and leave it in place; only add or change what is missing or divergent. Never create a second `HasOpenUpdatePR`, a second prefix constant, a duplicate skip-reason label, or duplicate tests.

1. **Single-source prefix constant — `pkg/githubclient.go`.**
   The string literal `"fix/update-go-` must appear in exactly ONE non-test `.go` file under `pkg/`. It must be this constant (next to `maxContentBytes` / `maxListPages`):
   ```go
   // updateBranchPrefix is the single source of the head-branch prefix the
   // github-update-go-agent opens for update tasks. Both the open-PR in-flight
   // gate (HasOpenUpdatePR) and the merged-PR completion pass (GetMergedUpdatePR)
   // build branches from it. Kept in sync with the agent's branchPrefix.
   const updateBranchPrefix = "fix/update-go-"
   ```
   `updateBranchName(headSHA string) string` must build from it:
   ```go
   func updateBranchName(headSHA string) string {
       short := headSHA
       if len(short) > 7 {
           short = short[:7]
       }
       return updateBranchPrefix + short
   }
   ```
   Its doc comment must reference the constant (e.g. `updateBranchPrefix + "<sha[:7]>"`) and must not quote the literal `"fix/update-go-`, or the grep count becomes 2.

2. **Interface method — `pkg/githubclient.go`.**
   The `GitHubClient` interface must declare, after `GetMergedUpdatePR`, exactly:
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

3. **Implementation — `pkg/githubclient.go`.**
   Implement on `*githubClient`; add `"strings"` to the import block if absent. Mirror `GetMergedUpdatePR` exactly (single page, `PerPage: 100`; do NOT add pagination — the sibling merged check has none):
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
   The error contract MUST mirror `GetMergedUpdatePR`: rate limit → `ErrRateLimited`; every other failure → `glog.Warningf` + `errors.Wrapf` carrying ctx. The client stays read-only (only `PullRequests.List`).

4. **Watcher gate — `pkg/watcher.go`.**
   In `processRepos`, the gate must run for every repo that survives `gatherCandidate` (after its abort/drop handling) and before the `cycleFilter.Skip(candidate.FilterCandidate())` block, and before any existing emission-claim suppression step. It must run on every cycle including forced ones; it is repo-wide and does NOT receive `headSHA`; `force` must not bypass it (only `sha_unchanged` is force-omitted). Semantics:
   - open update PR → `w.metrics.IncFilterSkipped("open_update_pr")`, `glog.V(2).Infof("repo skipped repo=%s reason=%s", repo.Key(), "open_update_pr")`, skip the repo (publish nothing);
   - non-rate-limit error → `dropRepo(repo, "open_update_pr", err)` and `continue` (nothing emitted on uncertainty; retried next poll);
   - rate limit → abort the whole cycle with `return "rate_limited"` (mirroring `completeMergedUpdates`; `Poll` turns the reason into `metrics.IncPollCycle("rate_limited")`).

   If the codebase already has the helpers `openUpdatePRGate` / `gateRepo`, verify them against these semantics and preserve them. If it does not, this reference shape is acceptable — the gate helper returns `(action string, openUpdate bool)` and the call site translates it:
   ```go
   func (w *watcher) openUpdatePRGate(ctx context.Context, repo Repo) (string, bool) {
       openUpdate, err := w.ghClient.HasOpenUpdatePR(ctx, repo)
       if err != nil {
           if stderrors.Is(err, ErrRateLimited) {
               glog.Warningf(
                   "poll cycle aborted: rate limited during HasOpenUpdatePR owner=%s",
                   w.owner,
               )
               return "rate_limited", false
           }
           dropRepo(repo, "open_update_pr", err)
           return "skip", false
       }
       if openUpdate {
           w.metrics.IncFilterSkipped("open_update_pr")
           glog.V(2).Infof(
               "repo skipped repo=%s reason=%s",
               repo.Key(),
               "open_update_pr",
           )
           return "skip", true
       }
       return "", false
   }
   ```
   Call site, after the `gatherCandidate` abort/drop block:
   ```go
   abortReason, skip := w.gateRepo(ctx, cursorState, repo, candidate)
   if abortReason != "" {
       return abortReason
   }
   if skip {
       continue
   }
   ```
   If no `gateRepo` wrapper exists, inline the `openUpdatePRGate` call and handle `"rate_limited"` / `"skip"` directly. Do NOT touch `gatherCandidate`, `completeMergedUpdates`, `GetMergedUpdatePR`, or any other existing code path. If a spec-005 emission-claim suppression step already exists, leave it untouched and keep the open-PR gate ahead of it.

5. **Metrics label set — `pkg/metrics.go`.**
   `FilterSkipReasons` must contain `"open_update_pr"` (currently after `"sha_unchanged"`), and the `IncFilterSkipped` doc-comment reason list must mention it. This keeps the closed label set in lockstep with the new verdict, makes `NewMetrics` pre-initialise `filter_skipped_total{reason="open_update_pr"}=0`, and keeps the existing label-containment test passing.

6. **Client tests — `pkg/githubclient_test.go`.**
   Ensure a `Describe("HasOpenUpdatePR", ...)` exists, following the `GetMergedUpdatePR` Describe shape (httptest handler routed by `r.URL.Path`, `pkg.NewGitHubClient(server.Client())`, `pkg.SetBaseURL(client, server.URL+"/")`). The positive-case handler MUST assert `r.URL.Query().Get("state") == "open"` (reject with `http.Error(w, "expected state=open", http.StatusBadRequest)` otherwise). Contexts required:
   - open update PR (`head.ref = fix/update-go-d630ef3`) → `(true, nil)`;
   - only non-update open PRs (`head.ref = feature/foo`) → `(false, nil)`;
   - mixed open PRs, one with the update prefix → `(true, nil)` (client-side match, any head wins);
   - no open PRs (`[]`) → `(false, nil)`;
   - rate limited (403 + `X-RateLimit-Remaining: 0` + `X-RateLimit-Reset: 0`) → `errors.Is(err, pkg.ErrRateLimited)`.

7. **Watcher tests — `pkg/watcher_test.go`.**
   Ensure a `Context("open update PR gate (spec 003)", ...)` under `Describe("Poll")` exists. Its `BeforeEach` sets up the full happy path (allowlisted repo, `GetHeadSHA`, `GetGoMod` behind stable, `GetMaintainerConfig` → `filter.GrantedConsent`, `LatestStable` ahead, `publisher.PublishCreateReturns(true)`, `metrics.IncFilterSkippedStub = func(string) {}`, `buildWatcher()`), so the repo WOULD publish absent the gate. Cases required:
   - `HasOpenUpdatePRReturns(true, nil)`; `Poll(ctx, false)` → `publisher.PublishCreateCallCount() == 0` and `metrics.IncFilterSkippedArgsForCall(0) == "open_update_pr"`;
   - `HasOpenUpdatePRReturns(false, nil)`; `Poll(ctx, false)` → `publisher.PublishCreateCallCount() == 1`;
   - `HasOpenUpdatePRReturns(false, pkg.ErrRateLimited)`; `Poll(ctx, false)` → `PublishCreateCallCount() == 0`, no error returned, last `metrics.IncPollCycleArgsForCall(IncPollCycleCallCount()-1) == "rate_limited"`;
   - `HasOpenUpdatePRReturns(false, errors.New("open pr check failed"))`; `Poll(ctx, false)` → `PublishCreateCallCount() == 0`, `metrics.IncFilterSkippedCallCount() == 0` (dropped, not a skip verdict), last `IncPollCycle` arg `"success"`;
   - `HasOpenUpdatePRReturns(true, nil)`; `Poll(ctx, true)` → `PublishCreateCallCount() == 0` (forced cycles do not bypass the gate).

   Existing tests must keep passing unchanged — the counterfeiter fake's default zero value for `HasOpenUpdatePR` is `(false, nil)`, which skips nothing. Do not modify existing contexts.

8. **Docs — `README.md` and `CHANGELOG.md`.**
   Ensure `README.md` has the skip-reason row:
   ```
   | `open_update_pr` | Repo has an open `fix/update-go-*` pull request — an update is already in flight, so no new task is emitted (always-on gate, spec 003) |
   ```
   Ensure `CHANGELOG.md` has one `- fix:` entry under the current release/Unreleased heading following the existing style, mentioning: no re-emit for a repo with an open `fix/update-go-*` PR (open-PR in-flight gate via `HasOpenUpdatePR`, skip reason `open_update_pr`, applies on every cycle including forced ones), and the branch prefix consolidated into the single `updateBranchPrefix` constant shared with the merged-PR completion pass.

9. **Regenerate mocks and verify.**
   Run `make precommit` from the repo root. Its `generate` target (`rm -rf mocks && mkdir -p mocks && echo "package mocks" > mocks/mocks.go && go generate -mod=mod ./...`) regenerates `mocks/github_client.go` from the `//counterfeiter:generate` directive. Never hand-edit anything under `mocks/`.

</requirements>

<constraints>
- The gate gets NO opt-out knob — no config field, no flag, no threshold. A repo with an open update PR is never emitted, period (spec Non-goal: a per-repo or global disable would re-introduce the bug).
- `GitHubClient` stays read-only — nothing writes to observed repos; the only new upstream call is `PullRequests.List` with `state=open`.
- No new external dependencies — go-github v84 is already used.
- Errors follow `github.com/bborbe/errors` wrapping and carry ctx; info logs at glog V(2). The phrase "repo dropped from cycle" is the operator's grep handle — do not reword it.
- The existing merged-PR detection pass (`GetMergedUpdatePR` / `completeMergedUpdates`) is unchanged.
- The existing-duplicate backlog (216 open tasks) is NOT collapsed by this change — the live agent-task-controller drain handles that; this change only stops new growth.
- The branch prefix stays in sync with the agent's `branchPrefix`; the single-source constant is the only place the literal appears in production code.
- Do NOT touch `pkg/filter/`, `pkg/taskbuilder.go`, `pkg/taskid.go`, `pkg/taskpublisher.go`, `main.go`, `pkg/factory/`, or `pkg/auth/`.
- Never use `fmt.Errorf` — all errors go through `github.com/bborbe/errors`.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from scratch.
- Tests use Ginkgo/Gomega; counterfeiter fakes come from `//counterfeiter:generate` directives.
- Keep every line under 100 characters and every function under 80 lines / 50 statements.
- The spec's operator-only acceptance check (post-deploy `sweep.sh` observation) is NOT runnable in the container — do not attempt it here; it belongs to the spec's Verification ladder.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root.

```
make precommit
```
Must exit 0 (regenerates mocks, then format + lint + test + check + addlicense).

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

```
grep -n "open_update_pr" pkg/metrics.go README.md
```
Each file must return at least one match.
</verification>
