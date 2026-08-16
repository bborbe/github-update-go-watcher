---
status: completed
spec: ["001"]
summary: Created pkg/ leaf domain package with Go-version parsing/normalisation/comparison, stable-Go lookup client, deterministic task-identifier derivation, Prometheus metrics surface, and Repo value type
execution_id: github-update-go-watcher-goupdate-exec-001-spec-001-domain-core
dark-factory-version: dev
created: "2026-08-16T12:53:45Z"
queued: "2026-08-16T13:10:55Z"
started: "2026-08-16T13:10:57Z"
completed: "2026-08-16T13:15:57Z"
---

<summary>
- Adds the pure-domain building blocks the rest of the watcher composes: nothing here talks to GitHub or Kafka.
- The service can now parse a Go release version string and a `go.mod` go-directive into a comparable, normalised three-part version (a two-part `1.26` becomes `1.26.0`).
- The service can now look up the current stable Go release from go.dev, rejecting any response that is not a recognisable Go version.
- Every work item gets a stable identity derived from owner + repo + commit, so re-reporting the same repo at the same commit is a downstream no-op.
- Prometheus counters for the four required watcher signals exist; registration is wired in by the caller rather than happening implicitly at package load, and every documented label is exposed at zero before the first cycle runs.
- Adds a repository value type shared by later prompts.
- Nothing is wired into the running binary yet.
</summary>

<objective>
Create the leaf domain package for the Go-update watcher inside `pkg/`: Go-version parsing/normalisation/comparison, the stable-Go lookup client, deterministic task-identifier derivation, the Prometheus metrics surface, and the `Repo` value type. All are pure or single-purpose collaborators with no dependency on GitHub, Kafka, filters, or the binary.
</objective>

<context>
Read `docs/dod.md` and `README.md` (repo root) for project conventions and the Definition of Done.

Read these coding plugin docs before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-parse-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`

Read these repo files before writing code:
- `go.mod` — module path is `github.com/bborbe/github-update-go-watcher`, go 1.26.6. `github.com/prometheus/client_golang` is already a direct dependency.
- `pkg/pkg_suite_test.go` — the Ginkgo suite for `package pkg_test` already exists and carries the
  `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` directive. Do NOT add a second suite file to `pkg/`.
- `mocks/mocks.go` — generated stub; `make precommit` deletes and regenerates the whole `mocks/` dir from `//counterfeiter:generate` directives. Never hand-edit files in `mocks/`.
- `Makefile.precommit` — `precommit: ensure format generate test check addlicense`. `format` runs `golines --max-len=100`, so keep lines under 100 chars.
- `.golangci.yml` — `funlen` caps functions at 80 lines / 50 statements; `gocyclo`, `gocognit`, `nestif`, `dupl` are enabled.

Library API facts (verified — do not re-derive from memory):
- `github.com/bborbe/errors` provides `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never use `fmt.Errorf`.
- `github.com/google/uuid` provides `uuid.MustParse(string) uuid.UUID` and `uuid.NewSHA1(space uuid.UUID, data []byte) uuid.UUID` (UUID version 5). `uuid.UUID` has `.String()` and `.Version()` (returns `uuid.Version`; `.String()` on it yields `"VERSION_5"`).
- `github.com/prometheus/client_golang/prometheus` provides `prometheus.NewCounterVec(prometheus.CounterOpts{...}, []string{...})`, `prometheus.NewCounter(prometheus.CounterOpts{...})`, `prometheus.Registerer.MustRegister(...)`, `prometheus.DefaultRegisterer`, and `prometheus.NewRegistry()` for tests.
- `github.com/prometheus/client_golang/prometheus/testutil` provides `testutil.ToFloat64(prometheus.Collector) float64` and `testutil.CollectAndCount(...)` for asserting counter values in tests.
</context>

<requirements>

### 1. Add the `github.com/google/uuid` dependency

Run `go get github.com/google/uuid@v1.6.0`. Do NOT add any other new module in this prompt.

### 2. `pkg/repo.go` — repository value type

Package `pkg`. Create:

```go
// Repo identifies a GitHub repository within the watcher's scope.
type Repo struct {
	Owner         string
	Name          string
	DefaultBranch string // typically "master" or "main"; cached to avoid a per-cycle lookup
}

// Key returns the host-qualified repo key consumed by the repo allowlist
// (e.g. "github.com/bborbe/disk-status").
func (r Repo) Key() string {
	return fmt.Sprintf("github.com/%s/%s", r.Owner, r.Name)
}

// String returns the short "owner/name" form used in the emitted task fields
// and in log lines.
func (r Repo) String() string {
	return fmt.Sprintf("%s/%s", r.Owner, r.Name)
}
```

There is deliberately NO `Fork` field: spec Non-goal — "Do NOT gate forks separately. A fork carrying its own opt-in flag has given its own consent."

### 3. `pkg/version.go` — Go version parsing, normalisation, comparison

Package `pkg`. Two distinct regexes and two distinct parse entry points — one for go.dev release strings (`go1.26.6`), one for `go.mod` go-directive values (`1.26.6` / `1.26`):

```go
// goReleasePattern matches a go.dev release version string:
// "go" followed by major.minor with an optional patch (e.g. go1.27, go1.26.5).
var goReleasePattern = regexp.MustCompile(`^go(\d+)\.(\d+)(?:\.(\d+))?$`)

// goDirectivePattern matches a go.mod `go` directive value: major.minor with an
// optional patch (e.g. 1.26, 1.26.6). A two-part value normalises to patch 0.
var goDirectivePattern = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?$`)

// Version is a parsed Go version. Patch defaults to 0 when the source string
// omits it (e.g. "1.26" -> patch 0). Raw preserves the original string.
type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string
}
```

Exported functions (all return `(Version, error)`; errors wrapped with `github.com/bborbe/errors`):

- `func ParseGoRelease(ctx context.Context, s string) (Version, error)` — uses `goReleasePattern`. Non-matching input returns an error whose message includes the rejected payload, e.g. `errors.Errorf(ctx, "invalid go release version %q", s)`.
- `func ParseGoDirective(ctx context.Context, s string) (Version, error)` — uses `goDirectivePattern`. Non-matching input returns `errors.Errorf(ctx, "invalid go directive version %q", s)`.

Both entry points delegate to a shared unexported `parseVersion(ctx context.Context, re *regexp.Regexp, s, kind string) (Version, error)`; `ParseGoRelease` and `ParseGoDirective` are thin wrappers passing their own pattern and error label ("go release version" / "go directive version"). This keeps the two functions from reading as duplicated blocks to `dupl`.

These are string-domain parsers over a controlled input type, not `any`-input parsers. The `ParseXDefault` pairing rule in `go-parse-pattern.md` does NOT apply here — do NOT add `ParseGoReleaseDefault`, `ParseGoDirectiveDefault`, or `ParseGoModVersionDefault`.

Methods on `Version`:

- `func (v Version) Compare(other Version) int` — orders by (major, minor, patch); negative when `v < other`, zero when equal, positive when `v > other`.
- `func (v Version) Less(other Version) bool` — `v.Compare(other) < 0`.
- `func (v Version) String() string` — canonical `"go%d.%d.%d"`, derived from the parsed components (always three-part), never from `Raw`.
- `func (v Version) Number() string` — `"%d.%d.%d"` without the `go` prefix. **This is the exact form emitted as the `current_go` and `latest_go` message fields**, so a two-part `go.mod` directive `1.26` must render `"1.26.0"`.

Use `strconv.Atoi` on each captured group and wrap conversion failures with `errors.Wrapf`.

### 4. `pkg/gomod.go` — go-directive extraction from `go.mod` bytes

Package `pkg`. One exported function:

```go
// ParseGoModVersion extracts the `go` directive from raw go.mod bytes and
// returns it as a normalised Version. It is deliberately narrow: it scans for
// the first line whose first field is exactly "go" and parses that line's
// second field via ParseGoDirective. A file with no `go` directive, a
// directive with no value, or a value that is not <major>.<minor>[.<patch>]
// all return an error — the caller maps that to skip reason "gomod_unparsable".
//
// Security: content is attacker-controlled (any observed repo can write any
// go.mod). Only the extracted numeric triple ever leaves this function; raw
// bytes are never propagated into an emitted message or an error message
// longer than the offending token.
func ParseGoModVersion(ctx context.Context, content []byte) (Version, error)
```

Implementation rules:
- Scan line by line with `bufio.Scanner` over `bytes.NewReader(content)`. Set a scanner buffer large enough for a 1 MiB file (`scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)`) and treat `scanner.Err() != nil` as a parse error.
- For each line: `strings.TrimSpace`, skip empty lines and lines beginning with `//`. Split on whitespace with `strings.Fields`. If `len(fields) >= 2 && fields[0] == "go"`, call `ParseGoDirective(ctx, fields[1])` and return its result immediately (success or error).
- If the scan finishes with no `go` directive found, return `errors.Errorf(ctx, "no go directive found in go.mod")`.
- Do NOT use `golang.org/x/mod/modfile`, do NOT shell out, do NOT import `os/exec`.

### 5. `pkg/godevclient.go` — stable-Go lookup

Package `pkg`. Mirror the shape below exactly:

```go
// DefaultGoDevURL is the go.dev release-list endpoint returning the JSON array
// of releases (including unstable ones — the client filters to stable).
//
// Spec Non-goal: this URL is NOT configurable. Tests inject the GoDevClient
// interface, never a URL.
const DefaultGoDevURL = "https://go.dev/dl/?mode=json"

//counterfeiter:generate -o ../mocks/go_dev_client.go --fake-name GoDevClient . GoDevClient

// GoDevClient resolves the current stable Go release.
type GoDevClient interface {
	// LatestStable returns the maximum stable Go version reported by go.dev.
	// It returns an error when the request fails, the response is non-200,
	// the body is not the expected JSON array, or no entry carries a version
	// string matching go<major>.<minor>[.<patch>]. The caller aborts the whole
	// cycle on any error (poll_cycle_total{result="go_version_error"}).
	LatestStable(ctx context.Context) (Version, error)
}

// goDevRelease is the subset of the go.dev release JSON the watcher consumes.
type goDevRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// NewGoDevClient returns the production GoDevClient backed by the given HTTP
// client and URL (always DefaultGoDevURL in production wiring).
func NewGoDevClient(httpClient *http.Client, url string) GoDevClient
```

Behaviour of the unexported implementation:
- Build the request with `http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)` so the cycle context cancels the call. Wrap creation failures with `errors.Wrapf(ctx, err, "create request %s", c.url)`.
- Execute with `c.httpClient.Do(req)`; wrap transport failures with `errors.Wrapf(ctx, err, "get %s", c.url)`.
- `defer` a body close whose error is logged via `glog.Warningf("close go.dev response body: %v", cerr)` (not returned).
- Non-200 → `errors.Errorf(ctx, "go.dev returned status %d", resp.StatusCode)`.
- `io.ReadAll` failure → wrapped error. `json.Unmarshal` into `[]goDevRelease` failure → wrapped error.
- Select the maximum among entries with `Stable == true` whose `Version` parses via `ParseGoRelease`. Skip unparseable entries with `glog.V(2).Infof("skip unparseable go.dev version %q: %v", r.Version, err)`.
- If no stable, parseable entry exists → `errors.Errorf(ctx, "no stable go version found in go.dev response")`.

### 6. `pkg/taskid.go` — deterministic identifier

Package `pkg`. The namespace UUID is frozen in code and must never change:

```go
// taskIDNamespace is the UUID5 namespace for github-update-go tasks.
// Frozen: changing it would break the task controller's dedup and re-file
// every open work item.
var taskIDNamespace = uuid.MustParse("8a9e45ee-da1f-4939-a3b5-11201f600a1a")

// DeriveTaskID returns a UUID5 derived deterministically from
// (owner, repo, headSHA) via the seed "update-go-<owner>-<repo>-<headSHA>"
// (spec Desired Behavior 6).
//
// Same repo at the same HEAD always yields the same identifier, so a re-emit
// is a downstream no-op; a new HEAD yields a new identifier, so a new commit
// correctly produces a fresh work item.
func DeriveTaskID(owner, repo, headSHA string) uuid.UUID {
	seed := fmt.Sprintf("update-go-%s-%s-%s", owner, repo, headSHA)
	return uuid.NewSHA1(taskIDNamespace, []byte(seed))
}
```

`uuid.NewSHA1` produces a version-5 UUID — do NOT substitute `uuid.NewMD5` (version 3) or `uuid.New` (version 4).

### 7. `pkg/metrics.go` — four counters, injected registerer

Package `pkg`. Metric namespace is `github_update_go_watcher` (matches the binary name).

```go
//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics is the four observable counters required of every watcher.
type Metrics interface {
	// IncPollCycle — result: "success" | "rate_limited" | "github_error" | "go_version_error"
	IncPollCycle(result string)

	// IncPublished — status: "create" | "error"
	IncPublished(status string)

	// IncReposScanned adds n repos scanned in one cycle (no labels).
	IncReposScanned(n int)

	// IncFilterSkipped — reason: "scope" | "no_gomod" | "gomod_unparsable" |
	// "go_current" | "auto_update_disabled" | "sha_unchanged"
	IncFilterSkipped(reason string)
}

const metricNamespace = "github_update_go_watcher"

// PollCycleResults, PublishStatuses and FilterSkipReasons are the closed label
// sets. They are exported so tests and the README stay in lockstep with the
// pre-initialisation loop below.
var (
	PollCycleResults = []string{"success", "rate_limited", "github_error", "go_version_error"}
	PublishStatuses  = []string{"create", "error"}
	FilterSkipReasons = []string{
		"scope",
		"no_gomod",
		"gomod_unparsable",
		"go_current",
		"auto_update_disabled",
		"sha_unchanged",
	}
)

// NewMetrics returns the Prometheus-backed Metrics registered against the
// supplied Registerer. Pass nil for prometheus.DefaultRegisterer. Every label
// value is pre-initialised to 0 so /metrics exposes the full series set before
// the first cycle runs.
//
// Registration goes through the injected Registerer — never a package-level
// init() and never prometheus.MustRegister directly.
func NewMetrics(registerer prometheus.Registerer) Metrics
```

Metric names (after namespace prefixing): `github_update_go_watcher_poll_cycle_total{result}`, `github_update_go_watcher_published_total{status}`, `github_update_go_watcher_repos_scanned_total`, `github_update_go_watcher_filter_skipped_total{reason}`.

Use `prometheus.NewCounterVec` for the three labelled counters and `prometheus.NewCounter` for `repos_scanned_total`. Register all four with `registerer.MustRegister(...)`, then loop the three exported label slices calling `.WithLabelValues(v).Add(0)`.

### 8. Tests — Ginkgo/Gomega, `package pkg_test`

Add test files alongside the sources. Use the existing suite in `pkg/pkg_suite_test.go`; do not add another `RunSpecs`.

- `pkg/version_test.go` — table-driven:
  - `ParseGoRelease`: `"go1.26.6"` → 1/26/6; `"go1.27"` → 1/27/0; `"1.26.6"` (no prefix) → error; `"go1"` → error; `""` → error; `"gox.y.z"` → error.
  - `ParseGoDirective`: `"1.26.6"` → 1/26/6; `"1.26"` → 1/26/0 and `.Number() == "1.26.0"`; `"go1.26"` → error; `"1"` → error; `"abc"` → error.
  - `Compare`/`Less` across major, minor and patch differences plus equality.
  - `String()` on a two-part-sourced version returns the three-part `"go1.26.0"`.
- `pkg/gomod_test.go` — table-driven over raw go.mod bodies:
  - Canonical file with `module ...`, blank line, `go 1.26.6` → 1/26/6.
  - Two-part `go 1.26` → `.Number() == "1.26.0"`.
  - Leading whitespace / tab before `go` → parsed.
  - `go` directive appearing after `require (` block start → still parsed (the first line whose first field is exactly `go` wins).
  - Commented-out `// go 1.26` only → error.
  - `go banana` → error.
  - `go` with no value → error.
  - Empty content → error.
  - A file whose only `go`-prefixed token is `gopkg.in/yaml.v3 v3.0.1` inside a require block → error (field[0] is not exactly `"go"`).
- `pkg/godevclient_test.go` — drive `NewGoDevClient` against an `httptest.NewServer`:
  - 200 with `[{"version":"go1.26.6","stable":true},{"version":"go1.27rc1","stable":false}]` → returns 1/26/6.
  - 200 with a mix where a later entry is a higher stable version → returns the maximum.
  - 200 with `[{"version":"banana","stable":true}]` → error (this is the "response not matching `go<major>.<minor>[.<patch>]`" case).
  - 500 → error mentioning the status.
  - Non-JSON body → error.
  - A cancelled context → error (call `cancel()` before `LatestStable`).
- `pkg/taskid_test.go`:
  - Same `(owner, repo, sha)` twice → identical UUID.
  - Changed sha → different UUID.
  - `DeriveTaskID(...).Version().String()` equals `"VERSION_5"`.
- `pkg/metrics_test.go`:
  - `NewMetrics(prometheus.NewRegistry())` then `registry.Gather()` yields all four metric families.
  - Every label value in `PollCycleResults`, `PublishStatuses` and `FilterSkipReasons` is present at value 0 before any `Inc*` call (use `testutil.ToFloat64` on the child counters, or assert on the gathered families).
  - `IncFilterSkipped("scope")` moves that series to 1 and leaves the other five at 0.
  - Two `NewMetrics` calls against two distinct `prometheus.NewRegistry()` instances both succeed (proves no package-level registration).

### 9. Regenerate mocks and verify

Run `make precommit` from the repo root. It deletes `mocks/`, regenerates from the `//counterfeiter:generate` directives, formats, lints, and tests. Confirm `mocks/go_dev_client.go` and `mocks/metrics.go` exist afterwards.

</requirements>

<constraints>
- Module path is `github.com/bborbe/github-update-go-watcher`. The scaffold's Makefile targets stay as they are.
- Do NOT touch `main.go`, `main_test.go`, `pkg/factory/`, `pkg/handler/`, `pkg/mathutil/`, `k8s/`, or `README.md` in this prompt.
- Do NOT add a configurable source URL for the stable-Go lookup — spec Non-goal, invariant. No env var, flag, or config field ever binds the go.dev URL; `DefaultGoDevURL` is the only production value. The constructor's `url` parameter exists solely so `godevclient_test.go` can point at an `httptest` server. Consumers of `GoDevClient` inject the interface, not a URL.
- The module must never shell out or clone: no `os/exec`, no `exec.Command`, no `go-git` anywhere.
- Never use `fmt.Errorf`. All errors go through `github.com/bborbe/errors` (`errors.Wrap`, `errors.Wrapf`, `errors.Errorf`) and carry `ctx`.
- Metrics register through the injected `prometheus.Registerer` only — never a package-level `init()`.
- The UUID5 namespace constant `8a9e45ee-da1f-4939-a3b5-11201f600a1a` is frozen. Do not regenerate it.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from scratch.
- Tests use Ginkgo/Gomega and live in `package pkg_test`; counterfeiter fakes come from `//counterfeiter:generate` directives.
- Keep every line under 100 characters (`golines --max-len=100` runs in `make precommit`) and every function under 80 lines / 50 statements (`funlen`).
- Every new `.go` file starts with the BSD license header block used by the existing files (`make precommit` runs `addlicense`, but write it yourself so the diff is stable).
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
grep -n "8a9e45ee-da1f-4939-a3b5-11201f600a1a" pkg/taskid.go
grep -n "github_update_go_watcher" pkg/metrics.go
grep -n "https://go.dev/dl/?mode=json" pkg/godevclient.go
```
Each expects at least one line.

```
ls mocks/go_dev_client.go mocks/metrics.go
```
Both must exist after `make precommit`.

```
go test -mod=mod ./pkg/...
```
Must exit 0.
</verification>
