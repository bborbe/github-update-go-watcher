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

The watcher runs in a k8s pod with **no vault filesystem access**; it cannot query the task store. The **open update PR on GitHub (`fix/update-go-*` head branch) IS the in-flight signal** for a repo: an open update PR means an update task is already underway, so the watcher must NOT emit another (spec 003, bug fix 2026-09-05 — task IDs derive from (owner, repo, head_sha), so a new commit otherwise re-emits on undrained work). The branch prefix lives in one constant (`updateBranchPrefix` in `pkg/githubclient.go`) shared by the open-PR gate and the merged-PR completion pass. The gate is always-on (including forced cycles) and has no opt-out.
