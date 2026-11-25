---
status: completed
summary: Added header-aware retry of GitHub rate-limit responses, bounded concurrent requests at 4, threaded an API base URL through the argument struct, and covered the wiring with Ginkgo tests; make precommit passes.
execution_id: github-update-go-watcher-exec-017-retry-backoff-roundtripper
dark-factory-version: v0.198.17
created: "2026-11-25T02:35:05Z"
queued: "2026-11-25T02:35:05Z"
started: "2026-11-25T02:35:59Z"
completed: "2026-11-25T02:38:52Z"
---

# Retry GitHub rate-limit responses and bound concurrent requests

<summary>
- A GitHub rate-limit response is retried with a bounded, header-aware wait instead of failing the request
- A wait longer than five minutes is deferred to the next poll cycle rather than slept
- Concurrent GitHub requests are capped at a named bound
- The API base URL becomes an argument-struct field, so a stub probe can point the watcher at a test server
- The poll cycle logs one line at start, so a cycle rate can be counted before and after the change
- Every other behaviour is unchanged
</summary>

<objective>
Stop a rate limit from ending the watcher's work. Today a 429 or a primary-quota 403 surfaces as `ErrRateLimited` and aborts the cycle; the next tick retries from scratch, so a long window silently does nothing. Retrying inside the client, with the server's own delay, keeps the cycle alive — and bounding concurrency stops a retry from making a secondary limit worse.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done.

Read these files before writing anything:
- `pkg/githubclient.go` — `NewGitHubClient` and the transport wrapper it installs; `ErrRateLimited` and `wrapRateLimitErr` are the current fail-fast path.
- `pkg/auth/auth.go` — `Credentials` and the `githubapp.Config` it builds.
- `main.go` — the argument struct, the `factory.CreateWatcher` call (~line 110), and the poll loop where `poll cycle skipped` is logged.
- `pkg/factory/factory.go` — `CreateWatcher` is the **only** production caller of `pkg.NewGitHubClient` (~line 99); `main.go` is the only caller of `CreateWatcher`. There is no `NewGitHubClient` call in `main.go`.
- `pkg/githubclient_test.go` and `pkg/pkg_suite_test.go` — the Ginkgo/Gomega style.
- `pkg/non2xx_wiring_test.go` — the existing transport-wiring assertion, which requirement 3 breaks (see 9a).
- `pkg/githubclient_export_test.go` — the `SetBaseURL` test export that already points a client at a test server; reuse its URL handling rather than diverging.

The library this uses is already released: `github.com/bborbe/http v1.28.0`, which ships `NewRoundTripperRetryWithBackoff` (honours `Retry-After` then `X-RateLimit-Reset`, defers a wait over five minutes as `retry deferred wait=<d>`, emits `retry attempt=<n> status=<403|429> class=<…> wait=<…>` and `retry exhausted status=<code> retries=<n>`) and `NewRoundTripperLimitConcurrency` (logs `inflight_bound=<n>` once at construction). Read their source under `$(go env GOMODCACHE)/github.com/bborbe/http@v1.28.0/` before wiring.
</context>

<requirements>
1. Bump `github.com/bborbe/http` to `v1.28.0` (`go get github.com/bborbe/http@v1.28.0 && go mod tidy`). That release's `go.mod` declares `go 1.27.4`, one patch above this repo's `go 1.27.3`, so the `go get` raises this repo's `go` directive. In the same change update the Dockerfile build stage from `FROM golang:1.27.3 AS build` to `FROM golang:1.27.4 AS build` — the builder tag must match the `go` directive, or the image build silently depends on a GOTOOLCHAIN auto-download inside the builder.

2. In `pkg/githubclient.go`, add a named constant `githubAPIInflightBound = 4` with a doc comment explaining that GitHub's secondary limit is a concurrency heuristic.

3. In `NewGitHubClient`, wrap the transport as **retry → capture → limit → network** (outermost first): the existing non-2xx logging / rate-capturing wrapper stays in the middle, `NewRoundTripperLimitConcurrency(inner, githubAPIInflightBound, nil)` innermost, and `NewRoundTripperRetryWithBackoff(<capture>, RetryBackoffOptions{RetryHosts: retryHosts})` outermost. A retry must re-acquire a slot, not hold one across the wait.

   **`RetryHosts` is not optional here.** The library gates retries on `req.URL.Hostname()` and defaults `RetryHosts` to `[]string{"api.github.com"}`. Set `retryHosts = nil` when `apiBaseURL` is empty (the default applies), and `[]string{<parsed base URL>.Hostname()}` when it is set — otherwise a client pointed at a test server or a GitHub Enterprise host silently never retries, and the retry test below cannot pass.

4. Give `NewGitHubClient` an `apiBaseURL string` parameter and, after `gogithub.NewClient(httpClient)`, apply it to the go-github client's `BaseURL` when non-empty — parse it, ensure a trailing slash on the path, and log a warning and keep the default when it does not parse. Extract that into a small `applyAPIBaseURL` helper so the constructor stays under the linter's complexity limits.

5. In `pkg/auth/auth.go`, add `BaseURL string` to `Credentials` and pass it as `githubapp.Config.BaseURL`.

6. In `main.go`, add `APIBaseURL string` to the argument struct with `arg:"api-base-url" env:"GITHUB_API_BASE_URL"`, pass it into `auth.Credentials`, and pass it to `factory.CreateWatcher` as a new `apiBaseURL string` parameter. Do not read it with `os.Getenv` — this repo threads configuration through the argument struct.

6a. In `pkg/factory/factory.go`, add `apiBaseURL string` to `CreateWatcher` and change its `pkg.NewGitHubClient(...)` call (~line 99) to pass it. Update `main.go`'s `CreateWatcher` call (~line 110) to match. **Do not add a `NewGitHubClient` call to `main.go`** — there is none today.

7. In `pollLoop`'s `poll` closure, add one line immediately after the cycle gate is acquired (a skipped cycle keeps logging only `poll cycle skipped`):
   ```go
   glog.V(2).Infof("poll cycle start stage=%s", a.Stage)
   ```
   It must be visible at `-v=2`, which is what the deployed StatefulSets run.

8. Add tests in `pkg/githubclient_chain_test.go` (`package pkg_test`, Ginkgo/Gomega):
   - a 429 with `Retry-After: 0` followed by a 200 drives exactly **two** calls through the transport — build the client with `pkg.NewGitHubClient(server.Client(), server.URL+"/")` and issue a real API call, so the request host is the test server and `RetryHosts` includes it. Do **not** construct the round-tripper directly in the test: that would leave the production wiring untested
   - an API call issued through a client built with `apiBaseURL` set to `http://stub.invalid/` reaches host `stub.invalid` — pass an `&http.Client{Transport: <recorder>}` whose transport records `req.URL.Host` and returns a canned 200 (`stub.invalid` does not resolve, so a real transport would fail on DNS)
   - an unparsable `apiBaseURL` (e.g. `"://bad"`) logs the warning and keeps the default base URL — same recording transport, assert the recorded host is `api.github.com`

9. Update every existing test call site for the new `NewGitHubClient` signature — `pkg/githubclient_test.go` and `pkg/non2xx_wiring_test.go`. Pass `""` as `apiBaseURL` everywhere, including the existing 429 specs: they hit a 127.0.0.1 test server, so an empty base URL keeps `RetryHosts` at the `api.github.com` default and each spec stays a single attempt. Passing the server URL there would make every 429 spec retry five times with a ~1s backoff.

9a. `pkg/non2xx_wiring_test.go` asserts the **outermost** transport is the non-2xx logging transport. Requirement 3 puts the retry round-tripper outermost, so that assertion fails. Rewrite it to assert **behaviour** instead of the outer type: serve a 500 from an `httptest` server, drive one client call, and assert a `github non-2xx` line is captured (add a sink-injecting export in `pkg` if the constructor gives no hook). Do **not** delete this coverage — it is the only guard that the non-2xx logging wrapper survives a re-wrap.

10. Add a `## Unreleased` entry to `CHANGELOG.md` (prefix `fix:`), and add a `GITHUB_API_BASE_URL` row to the env-var table in `README.md`, matching the existing rows.

11. Run `make precommit` and confirm it passes; walk each numbered requirement against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- All paths are repo-relative; no absolute or home-relative paths.
- Use Ginkgo/Gomega for tests.
- Wrap errors with `github.com/bborbe/errors`.
- Do not add a `//nolint` without an explanation on the same line.
- Do not run `go mod vendor`.
</constraints>

<verification>
Run `make precommit` — must pass.
</verification>
