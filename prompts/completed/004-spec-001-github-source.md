---
status: completed
spec: ["001"]
summary: Added GitHubClient interface in pkg/ with go-github/v84, pkg/auth for GitHub App credential resolution, and comprehensive tests; updated watcher.go and watcher_test.go to match value-type return signature.
execution_id: github-update-go-watcher-goupdate-exec-004-spec-001-github-source
dark-factory-version: dev
created: "2026-08-16T12:53:45Z"
queued: "2026-08-16T13:16:11Z"
started: "2026-08-16T13:44:47Z"
completed: "2026-08-16T13:59:25Z"
---

<summary>
- Adds the read-only GitHub surface the watcher uses: list an owner's repos, read a repo's current commit, read its `go.mod`, read its `.maintainer.yaml` consent file.
- Listing follows pagination to the end but refuses to loop forever, and drops archived repositories.
- Files larger than 1 MiB are rejected before they are decoded, so a hostile repo cannot make the watcher chew through a huge blob.
- A missing `go.mod` is a normal, non-error outcome ("this repo has none"); a malformed consent file is an error that drops that one repo — it is never read as consent.
- GitHub rate-limit responses are surfaced as one distinguishable error so the caller can abort the whole cycle instead of hammering the API.
- Adds GitHub App authentication resolution: all three credentials or none; partial configuration fails loudly, and the private key is never logged.
- Still nothing wired into the running binary; `main.go` is untouched.
</summary>

<objective>
Add the upstream GitHub source surface for the Go-update watcher: a `GitHubClient` interface in `pkg/` with a production implementation over `github.com/google/go-github/v84`, plus a `pkg/auth` package that resolves a GitHub App installation HTTP client. Everything here reads; nothing writes to any observed repository.
</objective>

<context>
Read `docs/dod.md`.

Read these coding plugin docs before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`

Read these repo files before writing code (prompt 1 created them):
- `pkg/repo.go` — `Repo{Owner, Name, DefaultBranch}` with `Key()` and `String()`.
- `pkg/version.go`, `pkg/gomod.go` — not called from this prompt, but confirm they exist.
- `pkg/pkg_suite_test.go` — the Ginkgo suite for `package pkg_test`; do not add a second `RunSpecs` to `pkg/`.
- `go.mod`, `Makefile.precommit`, `.golangci.yml`.

Library API facts (verified — do not re-derive from memory):
- `github.com/bborbe/maintainer` at **v0.48.1** exposes package `maintainerconfig` with:
  ```go
  type MaintainerConfig struct {
      Release    ReleaseConfig    `yaml:"release"`
      PrReviewer PrReviewerConfig `yaml:"prReviewer"`
      GoUpdate   GoUpdateConfig   `yaml:"goUpdate"`
  }
  type GoUpdateConfig struct {
      AutoUpdate bool `yaml:"autoUpdate"`
  }
  func Parse(ctx context.Context, content []byte) (MaintainerConfig, error)       // lenient: unknown fields ignored
  func ParseStrict(ctx context.Context, content []byte) (MaintainerConfig, error) // unknown fields rejected
  ```
  Empty input returns a zero-value config with a nil error. Use the lenient `Parse` — this is a fleet reader, and one repo's typo must not break the watcher for the other ~209 repos.
- The same module exposes package `githubapp` with:
  ```go
  type Config struct {
      AppID          int64
      InstallationID int64
      PEM            []byte // mutually exclusive with PEMPath
      PEMPath        string
      BaseURL        string
  }
  func NewClient(ctx context.Context, cfg Config) (*http.Client, error)
  ```
- `github.com/google/go-github/v84` (import as `gogithub "github.com/google/go-github/v84/github"`) provides:
  - `gogithub.NewClient(httpClient *http.Client) *Client`
  - `client.Apps.ListRepos(ctx, opts *gogithub.ListOptions) (*gogithub.ListRepositories, *gogithub.Response, error)` — `result.Repositories` is `[]*gogithub.Repository`; `resp.NextPage` is 0 on the last page.
  - `client.Repositories.GetBranch(ctx, owner, repo, branch string, maxRedirects int) (*gogithub.Branch, *gogithub.Response, error)` — `branch.GetCommit().GetSHA()` yields the full SHA.
  - `client.Repositories.GetContents(ctx, owner, repo, path string, opts *gogithub.RepositoryContentGetOptions) (*gogithub.RepositoryContent, []*gogithub.RepositoryContent, *gogithub.Response, error)` — `fileContent.GetSize() int`, `fileContent.GetContent() (string, error)` (base64-decodes).
  - Rate-limit detection: `gogithub.RateLimitError` and `gogithub.AbuseRateLimitError`, both matched with `stderrors.As`.
  - 404 detection: `gogithub.ErrorResponse` matched with `stderrors.As`, then `ghErr.Response.StatusCode == http.StatusNotFound`.
- `github.com/bborbe/errors` — `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never `fmt.Errorf`.
</context>

<requirements>

### 1. Add dependencies

```
go get github.com/google/go-github/v84@v84.0.0
go get github.com/bborbe/maintainer@v0.48.1
```

Do NOT add any other new module in this prompt.

### 2. `pkg/githubclient.go` — the upstream read surface

Package `pkg`. Import `stderrors "errors"` for `stderrors.As` / `stderrors.Is` and `gogithub "github.com/google/go-github/v84/github"`.

```go
// ErrRateLimited is returned when the GitHub API responds with a primary or
// abuse rate-limit error. Callers abort the whole cycle on this sentinel
// (poll_cycle_total{result="rate_limited"}) rather than retrying in a loop.
var ErrRateLimited = stderrors.New("github rate limited")

// maxContentBytes caps every file this client decodes. The contents of
// go.mod and .maintainer.yaml in any observed repo are attacker-controlled,
// so the API-reported Size is checked BEFORE decoding.
const maxContentBytes = 1024 * 1024

// maxListPages bounds repo-list pagination so a self-referential or
// misbehaving `next` link cannot loop the cycle forever.
const maxListPages = 100

//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient

// GitHubClient is the read-only upstream surface for the Go-update watcher.
// Nothing in this interface writes to an observed repository.
type GitHubClient interface {
	// ListRepos returns the non-archived repositories under owner that the
	// authenticated GitHub App installation can access — public AND private.
	// Forks are included: a fork carrying its own goUpdate.autoUpdate flag has
	// given its own consent (spec Non-goal: forks are not gated separately).
	// Enumeration goes through GET /installation/repositories (Apps.ListRepos),
	// NOT GET /users/{u}/repos, because the latter silently omits private repos
	// under an installation token. Pagination is internal and capped at
	// maxListPages; the returned slice is the full set.
	ListRepos(ctx context.Context, owner string) ([]Repo, error)

	// GetHeadSHA returns the full HEAD SHA of repo's default branch.
	GetHeadSHA(ctx context.Context, repo Repo) (string, error)

	// GetGoMod returns the raw bytes of go.mod at HEAD of repo's default
	// branch. Returns (nil, nil) when the file does not exist (HTTP 404) —
	// the caller maps a nil slice to skip reason "no_gomod". Returns
	// (nil, ErrRateLimited) on rate limiting. Every other failure (network,
	// 5xx, oversize, base64 decode) returns a wrapped error and drops the repo.
	GetGoMod(ctx context.Context, repo Repo) ([]byte, error)

	// GetMaintainerConfig returns the parsed `.maintainer.yaml` at HEAD of
	// repo's default branch. This file is the repo owner's consent record.
	//
	//   - (parsed config, nil) on a valid YAML document, including empty input
	//     and documents with the `goUpdate:` key absent (both zero-value).
	//   - (zero-value, nil) on HTTP 404 (file absent) — reads as NOT opted in.
	//   - (zero-value, ErrRateLimited) on primary or abuse rate limiting.
	//   - (zero-value, wrapped error) on every other failure including 5xx,
	//     oversize files, base64 decode failures, and YAML parse failures.
	//
	// Malformed YAML MUST NOT be silently treated as autoUpdate:false — it is
	// an error so the repo is dropped from the cycle rather than recorded as a
	// consent verdict.
	GetMaintainerConfig(ctx context.Context, repo Repo) (maintainerconfig.MaintainerConfig, error)
}

// NewGitHubClient returns the production GitHubClient backed by the given
// HTTP client (authenticated via GitHub App installation token).
func NewGitHubClient(httpClient *http.Client) GitHubClient {
	return &githubClient{client: gogithub.NewClient(httpClient)}
}

type githubClient struct {
	client *gogithub.Client
}
```

Helper functions in the same file:

```go
// isRateLimitError reports whether err is a GitHub rate-limit signal
// (primary or secondary/abuse).
func isRateLimitError(err error) bool {
	var rl *gogithub.RateLimitError
	var arl *gogithub.AbuseRateLimitError
	return stderrors.As(err, &rl) || stderrors.As(err, &arl)
}

// isNotFound reports whether err is a GitHub 404 response.
func isNotFound(err error) bool {
	var ghErr *gogithub.ErrorResponse
	return stderrors.As(err, &ghErr) && ghErr.Response != nil &&
		ghErr.Response.StatusCode == http.StatusNotFound
}

// wrapRateLimitErr maps rate-limit responses onto ErrRateLimited and wraps
// everything else with context.
func (c *githubClient) wrapRateLimitErr(
	ctx context.Context,
	err error,
	msg string,
	args ...interface{},
) error {
	if isRateLimitError(err) {
		return ErrRateLimited
	}
	return errors.Wrapf(ctx, err, msg, args...)
}
```

**`ListRepos` implementation:**
- `opts := &gogithub.ListOptions{PerPage: 100}`; loop with a `page` counter.
- At the top of each iteration, honour cancellation:
  ```go
  select {
  case <-ctx.Done():
      return nil, ctx.Err()
  default:
  }
  ```
- Call `c.client.Apps.ListRepos(ctx, opts)`. On error return `c.wrapRateLimitErr(ctx, err, "list installation repos for %s page=%d", owner, opts.Page)`.
- Map the page through an unexported `mapGitHubRepos(repos []*gogithub.Repository, owner string) []Repo` that drops entries where `repo.GetArchived()` is true, where `repo.GetOwner().GetLogin() != owner`, or where `repo.GetName() == ""`, and otherwise appends `Repo{Owner: repo.GetOwner().GetLogin(), Name: repo.GetName(), DefaultBranch: repo.GetDefaultBranch()}`.
- Increment the page counter each iteration BEFORE the `resp.NextPage == 0` break check. Break when `resp == nil || resp.NextPage == 0` — a legitimate installation whose last page is exactly page `maxListPages` must succeed. Only when the loop is still going (i.e. `resp.NextPage != 0`) AND the counter has reached `maxListPages`, return `errors.Errorf(ctx, "list installation repos for %s exceeded %d pages", owner, maxListPages)`. This is a whole-cycle error (spec Security: pagination must not loop forever) and only fires when a `next` link is still present after the cap.
- Otherwise `opts.Page = resp.NextPage` and continue.
- Emit one always-on summary line per cycle so a silent shrink in installation scope is visible in logs:
  ```go
  glog.Infof(
      "github-update-go-watcher listed installation repos owner=%s total=%d private=%d in_scope=%d",
      owner, total, private, len(repos),
  )
  ```
  where `total` counts every returned API entry and `private` counts `repo.GetPrivate()`.

**`GetHeadSHA` implementation:**
- Guard the empty default branch: `if repo.DefaultBranch == "" { return "", errors.Errorf(ctx, "repo %s has empty DefaultBranch — cannot fetch HEAD SHA", repo.String()) }`.
- `branch, _, err := c.client.Repositories.GetBranch(ctx, repo.Owner, repo.Name, repo.DefaultBranch, 1)` — the `1` follows one redirect, which GitHub returns for renamed default branches.
- On error return `c.wrapRateLimitErr(ctx, err, "get branch %s@%s", repo.String(), repo.DefaultBranch)`.
- Return `branch.GetCommit().GetSHA()`.

**`GetGoMod` implementation:**
- `opts := &gogithub.RepositoryContentGetOptions{Ref: repo.DefaultBranch}`; `fileContent, _, _, err := c.client.Repositories.GetContents(ctx, repo.Owner, repo.Name, "go.mod", opts)`.
- On error: `isNotFound(err)` → `return nil, nil`; `isRateLimitError(err)` → `return nil, ErrRateLimited`; otherwise wrapped error naming the repo and ref.
- `fileContent == nil` → `return nil, nil` (directory response or empty result; treated as "no go.mod").
- `fileContent.GetSize() > maxContentBytes` → `errors.Errorf(ctx, "go.mod %s too large: %d bytes (max %d)", repo.String(), fileContent.GetSize(), maxContentBytes)`.
- `decoded, err := fileContent.GetContent()`; wrap decode failures.
- `len(decoded) > maxContentBytes` → oversize error naming the decoded length. Keep this second check even though base64 cannot deflate: `GetSize()` is upstream metadata and is not trusted.
- Return `[]byte(decoded), nil`.

**`GetMaintainerConfig` implementation:**
- Same fetch shape against path `.maintainer.yaml`.
- 404 → `(maintainerconfig.MaintainerConfig{}, nil)`. Rate limit → `(maintainerconfig.MaintainerConfig{}, ErrRateLimited)`. `fileContent == nil` → `(maintainerconfig.MaintainerConfig{}, nil)`.
- Size cap identical to `GetGoMod`, applied before AND after decode.
- `cfg, err := maintainerconfig.Parse(ctx, []byte(decoded))`; on error return `(maintainerconfig.MaintainerConfig{}, errors.Wrapf(ctx, err, "parse .maintainer.yaml %s", repo.String()))`.

### 3. `pkg/auth/auth.go` — GitHub App credential resolution

New package `auth` at `pkg/auth/auth.go` with a package doc comment explaining that I/O (JWT exchange + installation-token fetch) happens here, which is why it lives outside `pkg/factory` (the factory package is pure composition).

```go
// Credentials carries the inputs needed for GitHub App auth. Read from the
// binary's argument struct by the caller. PEMKey is the long-lived secret:
// it arrives by environment only and is never logged.
type Credentials struct {
	AppID          int64
	InstallationID int64
	PEMKey         []byte
}

// ResolveGitHubClient returns an *http.Client authenticated as the GitHub App
// installation.
//
// Rules:
//   - All three fields set -> App auth.
//   - Any subset set without the other two -> error naming the MISSING env var
//     names only (never the value of PEM_KEY).
//   - Nothing set -> error.
func ResolveGitHubClient(ctx context.Context, creds Credentials) (*http.Client, error)
```

Implementation:
- `appPartial := creds.AppID != 0 || creds.InstallationID != 0 || len(creds.PEMKey) != 0`
- `appComplete := creds.AppID != 0 && creds.InstallationID != 0 && len(creds.PEMKey) != 0`
- Partial-but-not-complete → collect the missing names into `[]string{"APP_ID", "INSTALLATION_ID", "PEM_KEY"}` as appropriate and return `errors.Errorf(ctx, "watcher auth: partial GitHub App config — missing %v; set all three or none", missing)`.
- Complete → log `glog.Infof("watcher auth mode=github-app app_id=%d installation_id=%d", creds.AppID, creds.InstallationID)` (App ID and installation ID are public; the PEM is not logged at any level), then `githubapp.NewClient(ctx, githubapp.Config{AppID: creds.AppID, InstallationID: creds.InstallationID, PEM: creds.PEMKey})`, wrapping any error with `errors.Wrap(ctx, err, "create github app client")`.
- Neither → `errors.Errorf(ctx, "watcher auth: GitHub App credentials not configured — set APP_ID, INSTALLATION_ID, and PEM_KEY")`.

Add `pkg/auth/auth_suite_test.go` with a Ginkgo `RunSpecs` for `package auth_test`, mirroring the shape of `pkg/pkg_suite_test.go` (`time.Local = time.UTC`, `format.TruncatedDiff = false`, 60s suite timeout). Counterfeiter's generate mode is per-directory and non-recursive, so every package with mocks needs its own bootstrap line — mirror the existing convention (`pkg/pkg_suite_test.go`, `pkg/factory/factory_suite_test.go`, `pkg/handler/handler_suite_test.go` each carry one) by adding, immediately above `func TestAuth(...)`:

```go
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate
```

`pkg/auth` declares no interfaces in this prompt, so this generates nothing today, but omitting it means the first future interface added to this package silently never gets a mock.

### 4. Tests

**`pkg/githubclient_test.go`** (`package pkg_test`) — drive `NewGitHubClient` against an `httptest.NewServer` whose handler routes by `r.URL.Path`, and point the client at it. Because `gogithub.NewClient(httpClient)` targets api.github.com, add an unexported export-test helper (mirroring the sibling `github-release-watcher`'s proven `pkg/githubclient_export_test.go` pattern) that reaches into the concrete `*githubClient` and overrides its `gogithub.Client.BaseURL`:

```go
// pkg/githubclient_export_test.go
package pkg

import "net/url"

// SetBaseURL points c at a test server. Test-only.
func SetBaseURL(c GitHubClient, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	c.(*githubClient).client.BaseURL = u
	return nil
}
```

Then in `pkg/githubclient_test.go`: `client := pkg.NewGitHubClient(server.Client()); Expect(pkg.SetBaseURL(client, server.URL+"/")).To(Succeed())`. This uses the client's real transport rather than a hand-rolled `RoundTripper`.

Cover:
- `ListRepos` over two pages (first response sets a `Link: <...>; rel="next"` header so `resp.NextPage` is non-zero) returns the union.
- `ListRepos` drops archived repos and repos owned by another login.
- `ListRepos` with a 403 rate-limit response body (`{"message":"API rate limit exceeded", "documentation_url":"..."}` plus header `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset`) returns an error satisfying `errors.Is(err, pkg.ErrRateLimited)`.
- `ListRepos` with a 500 returns a non-`ErrRateLimited` error.
- `ListRepos` with a cancelled context returns an error.
- `ListRepos` against a handler that always responds with a `Link: <...?page=N>; rel="next"` header (i.e. an unbounded/self-referential next link) returns an error mentioning `exceeded 100 pages`, and the test asserts the handler received at most `maxListPages` requests (bound the test server's own request counter, do not let the test hang).
- `GetHeadSHA` returns the full SHA from the branch payload; empty `DefaultBranch` returns an error without issuing a request.
- `GetGoMod` on a 404 returns `(nil, nil)`.
- `GetGoMod` returns the decoded bytes for a base64 `content` payload with `encoding: "base64"`.
- `GetGoMod` with an API-reported `size` above 1 MiB returns an error mentioning the size, and the test asserts the body was NOT decoded (assert on the error message).
- `GetMaintainerConfig` on 404 returns a zero-value config and a nil error.
- `GetMaintainerConfig` on `goUpdate:\n  autoUpdate: true` returns `cfg.GoUpdate.AutoUpdate == true`.
- `GetMaintainerConfig` on a document with no `goUpdate:` block returns `cfg.GoUpdate.AutoUpdate == false` and a nil error.
- `GetMaintainerConfig` on malformed YAML (e.g. `goUpdate:\n\tautoUpdate: [`) returns an error — assert explicitly that the returned config is the zero value AND the error is non-nil, i.e. malformed never reads as opted-in.
- `GetMaintainerConfig` on a rate-limited response returns `ErrRateLimited`.

**`pkg/auth/auth_test.go`** (`package auth_test`):
- Nothing set → error mentioning `APP_ID`.
- Only `AppID` set → error naming `INSTALLATION_ID` and `PEM_KEY` and NOT containing the PEM bytes.
- All three set with an invalid PEM → error (the `githubapp.NewClient` path); assert the error message does not contain the PEM content.

### 5. Regenerate mocks and verify

Run `make precommit` from the repo root. Confirm `mocks/github_client.go` exists afterwards.

</requirements>

<constraints>
- This service only observes and reports. Do NOT clone, check out, commit, push, tag, open PRs against, or otherwise write to any observed repository. Nothing in this prompt may call a mutating GitHub endpoint.
- The module must never shell out or clone: no `os/exec`, no `exec.Command`, no `go-git` anywhere.
- Do NOT seed, write, or migrate the opt-in flag into any other repository's `.maintainer.yaml`.
- The opt-in schema comes from the released `github.com/bborbe/maintainer` at `v0.48.1` (`maintainerconfig`). Do NOT hand-roll the struct or a local copy of it.
- Nothing is ever defaulted to opted-in: absent file, absent section, absent key, false key all read as false; an unparsable file is an ERROR, not a false verdict.
- `.maintainer.yaml` and `go.mod` contents are attacker-controlled. Size-cap at 1 MiB before decoding; never let raw file bytes escape into a returned error message beyond the offending token.
- Never use `fmt.Errorf`. All errors go through `github.com/bborbe/errors` and carry `ctx`.
- The GitHub App private key arrives by environment only and is never logged at any verbosity.
- Do NOT touch `main.go`, `main_test.go`, `pkg/factory/`, `pkg/handler/`, `pkg/mathutil/`, `k8s/`, or `README.md` in this prompt.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from scratch.
- Tests use Ginkgo/Gomega; counterfeiter fakes come from `//counterfeiter:generate` directives.
- Keep every line under 100 characters and every function under 80 lines / 50 statements.
- Every new `.go` file starts with the BSD license header block used by the existing files.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root:

```
make precommit
```

Must exit 0.

```
grep -rn "os/exec\|go-git\|exec.Command" --include=*.go .
```
Expect zero matches.

```
grep -n "maintainer v0.48.1" go.mod
grep -n "maxContentBytes\|maxListPages" pkg/githubclient.go
grep -n "ErrRateLimited" pkg/githubclient.go
```
Each expects at least one line.

```
ls mocks/github_client.go
```
Must exist after `make precommit`.

```
go test -mod=mod ./pkg/...
```
Must exit 0.
</verification>
