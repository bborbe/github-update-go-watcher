# Definition of Done

A prompt is complete when ALL of the following are true:

## Build

- [ ] `make precommit` passes (format + lint + test + security checks)
- [ ] No new linting warnings or errors

## Code Quality

- [ ] No `//nolint` without explanation
- [ ] Follows existing code patterns in the file being modified

## Tests

- [ ] New functions have tests
- [ ] Existing tests still pass
- [ ] Tests use Ginkgo/Gomega conventions
- [ ] Counterfeiter for mocks (`mocks/` dir)

## Style

- [ ] Functions over classes for stateless operations
- [ ] Error handling follows `github.com/bborbe/errors` patterns
- [ ] No absolute paths — all paths relative or using standard lib

## In-flight signal contract (durable — do not break)

The watcher runs in a k8s pod with **no vault filesystem access**; it cannot query the task store. A repo is in flight in one of two windows, and each window has its own signal.

**Before the update PR exists (the emission window).** The watcher records its own emission in the cursor: `in_flight_target` (the target Go version it filed for, the same string the emitted task carries as `latest_go`) and `in_flight_since` (RFC3339, when it filed). While a claim still targets the cycle's target version, the repo is skipped with reason `emission_in_flight` and no second task is filed. The claim is cleared by exactly three observable events: the repo converges (`!GoBehind()` — the same predicate the `go_current` filter uses, so equal-to, ahead-of and unparsable all clear), an open `fix/update-go-*` PR is observed, or the `InFlightTTL` backstop (336h / 14 days, twice the longest measured queue latency) expires. A GitHub error while checking for the PR never clears the claim — only a positive "a PR exists" does — and a rate-limited cycle neither writes nor clears it. The claim is written only by `markPublished`, so hand-authored `- reemit` repair records never carry one.

**Once the update PR exists.** The **open update PR on GitHub (`fix/update-go-*` head branch) IS the in-flight signal** for a repo: an open update PR means an update task is already underway, so the watcher must NOT emit another (spec 003, bug fix 2026-09-05 — task IDs derive from (owner, repo, target Go version, head_sha), so a new commit otherwise re-emits on undrained work). The branch prefix lives in one constant (`updateBranchPrefix` in `pkg/githubclient.go`) shared by the open-PR gate and the merged-PR completion pass. The gate is always-on (including forced cycles) and has no opt-out.

Both signals are readable without a vault read: the claim lives in the cursor file the watcher already persists, and the PR list is one the gate already fetches. The claim adds no store, no PVC, no new API call and no config knob.
