---
status: completed
spec: [001-go-update-task-watcher]
summary: Implemented pkg/filter decision chain (6 filters + interface/composite), pkg/cursor persistence with atomic writes, and pkg/cursorreader adapter
execution_id: github-update-go-watcher-goupdate-exec-002-spec-001-filters-and-cursor
dark-factory-version: dev
created: "2026-08-16T12:53:45Z"
queued: "2026-08-16T13:14:22Z"
started: "2026-08-16T13:15:58Z"
completed: "2026-08-16T13:21:44Z"
---

<summary>
- Adds the decision chain that turns one observed repo into either "file a work item" or exactly one named reason not to.
- The six reasons are a closed set: out of operator scope, no `go.mod`, unreadable `go.mod`, already on current Go, owner has not opted in, and nothing changed since last time.
- The opt-in check is positive-consent only: every ambiguous state resolves to skipped, and there is no code path or setting that flips it to on-by-default.
- Adds the watcher's memory: a small JSON file recording the last commit reported per repo, so a restart does not re-file everything.
- Memory is written atomically, so a crash halfway through a write can never leave a half-written file, and no stray temp file is left behind.
- A corrupt memory file makes the service refuse to run rather than guess an empty state and re-file the whole fleet.
- Still nothing wired into the running binary.
</summary>

<objective>
Add the `pkg/filter` decision chain covering all six skip reasons, and the persisted-memory (cursor) load/save surface in `pkg/`, including the read-only adapter that lets the SHA-unchanged filter consult the cursor without an import cycle.
</objective>

<context>
Read `CLAUDE.md` (repo root) and `docs/dod.md`.

Read these coding plugin docs before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-filter-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`

Read these repo files before writing code (earlier prompts created them):
- `pkg/repo.go` — `Repo.Key()` returns the host-qualified `"github.com/<owner>/<name>"` form the allowlist filter matches on.
- `pkg/metrics.go` — `FilterSkipReasons` is the closed label slice: `scope`, `no_gomod`, `gomod_unparsable`, `go_current`, `auto_update_disabled`, `sha_unchanged`. The strings returned by the filters in this prompt MUST be exactly these six.
- `pkg/githubclient.go` — for context on where the filter inputs come from.
- `pkg/pkg_suite_test.go`, `go.mod`, `Makefile.precommit`, `.golangci.yml`.

Library API facts (verified — do not re-derive from memory):
- `github.com/bborbe/maintainer/repoallowlist` (added as a dependency at v0.48.1 by the prior prompt; if `go.mod` does not yet require it, run `go get github.com/bborbe/maintainer@v0.48.1`) exposes:
  ```go
  func IsAllowed(allowlist []string, target string) bool
  func Validate(ctx context.Context, allowlist []string) error
  ```
  `IsAllowed` treats an empty/nil allowlist as allow-all, expects `target` in `host/owner/repo` form, supports `host/owner/*` wildcards and `!`-prefixed exclusions. The live import path is `github.com/bborbe/maintainer/repoallowlist` (NOT `lib/repoallowlist`).
- `github.com/bborbe/errors` — `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never `fmt.Errorf`.
</context>

<requirements>

### 1. `pkg/filter/filter.go` — chain contract and input type

New package `filter` at `pkg/filter/filter.go`. Start with a package doc comment listing the chain in evaluation order.

```go
// Package filter implements the TaskCreationFilter chain — the predicates that
// decide whether a Go-update work item should be filed for one observed repo.
//
// Chain order (frozen; the first non-empty reason wins):
//
//  1. RepoAllowlistFilter  -> "scope"                — operator-configured scope
//  2. GoModPresentFilter   -> "no_gomod"             — repo has no go.mod
//  3. GoModParsableFilter  -> "gomod_unparsable"     — go directive unreadable
//  4. GoBehindFilter       -> "go_current"           — at or ahead of stable
//  5. AutoUpdateFilter     -> "auto_update_disabled" — owner has not opted in
//  6. SHAUnchangedFilter   -> "sha_unchanged"        — HEAD already reported
//
// Filters 1-5 are cycle-invariant and built once at wiring time.
// SHAUnchangedFilter is composed in per cycle because it needs a fresh
// CursorReader, and it is omitted entirely on a forced cycle.
package filter
```

```go
//counterfeiter:generate -o ../../mocks/task_creation_filter.go --fake-name TaskCreationFilter . TaskCreationFilter

// Candidate is the filter-evaluation input. It mirrors the watcher's per-repo
// observation as a local type so this package never imports pkg (pkg imports
// filter; the reverse would be an import cycle).
type Candidate struct {
	// RepoKey is the host-qualified key "github.com/<owner>/<name>".
	RepoKey string
	// HeadSHA is the full HEAD SHA of the default branch.
	HeadSHA string
	// GoModPresent is false when the repo has no go.mod at all.
	GoModPresent bool
	// GoModParsable is false when go.mod exists but carries no readable
	// <major>.<minor>[.<patch>] go directive.
	GoModParsable bool
	// GoBehind is true when the declared Go version is strictly behind the
	// current stable release. The comparison itself lives in pkg.Version;
	// only the verdict crosses into this package.
	GoBehind bool
	// AutoUpdate is `.maintainer.yaml: goUpdate.autoUpdate`. True is the ONLY
	// value that passes the consent gate.
	AutoUpdate bool
}

// TaskCreationFilter decides whether a single Candidate should be skipped.
// Implementations return the metric-label reason for the skip, or "" to pass
// through. Returning the reason (rather than a bool) means the caller never
// re-evaluates the predicates to work out which counter to bump.
type TaskCreationFilter interface {
	// Skip returns the skip reason (metric label) or "" to pass through.
	Skip(candidate Candidate) string
}

// TaskCreationFilterFunc adapts a function to the TaskCreationFilter interface.
type TaskCreationFilterFunc func(candidate Candidate) string

// Skip implements TaskCreationFilter for the function adapter.
func (f TaskCreationFilterFunc) Skip(candidate Candidate) string {
	return f(candidate)
}

// TaskCreationFilters is a slice composite returning the first non-empty
// reason from its members. An empty slice never skips.
type TaskCreationFilters []TaskCreationFilter

// Skip returns the first non-empty reason from any contained filter,
// short-circuiting on the first hit.
func (fs TaskCreationFilters) Skip(candidate Candidate) string {
	for _, f := range fs {
		if reason := f.Skip(candidate); reason != "" {
			return reason
		}
	}
	return ""
}
```

### 2. The five cycle-invariant filters

Each in its own file, each with a doc comment stating what it gates and why.

**`pkg/filter/repo_allowlist_filter.go`**

```go
// ParseRepoAllowlist parses a comma-separated allowlist string into
// host-qualified repo keys (e.g. "github.com/bborbe/disk-status").
// Whitespace is trimmed and empty entries dropped. nil on empty input, which
// repoallowlist.IsAllowed treats as allow-all within the configured owner.
func ParseRepoAllowlist(raw string) []string

// NewRepoAllowlistFilter returns the operator-scope gate: "scope" for any
// Candidate whose RepoKey is not permitted by the allowlist.
func NewRepoAllowlistFilter(allowlist []string) TaskCreationFilter
```

`Skip` returns `"scope"` when `!repoallowlist.IsAllowed(f.allowlist, candidate.RepoKey)`, else `""`.

**`pkg/filter/gomod_present_filter.go`**

```go
// NewGoModPresentFilter returns "no_gomod" for a repo with no go.mod at HEAD.
// Not a failure: most repos in a mixed-language org simply are not Go repos.
func NewGoModPresentFilter() TaskCreationFilter
```

Implement with `TaskCreationFilterFunc`: return `"no_gomod"` when `!candidate.GoModPresent`.

**`pkg/filter/gomod_parsable_filter.go`**

```go
// NewGoModParsableFilter returns "gomod_unparsable" when go.mod exists but
// carries no readable <major>.<minor>[.<patch>] go directive. This filter
// assumes GoModPresentFilter ran first, so it only fires for present-but-
// unreadable files.
func NewGoModParsableFilter() TaskCreationFilter
```

Return `"gomod_unparsable"` when `candidate.GoModPresent && !candidate.GoModParsable`.

**`pkg/filter/go_behind_filter.go`**

```go
// NewGoBehindFilter returns "go_current" when the repo's declared Go version
// is NOT strictly behind current stable — i.e. equal to stable and ahead of
// stable both read as "nothing to do here".
func NewGoBehindFilter() TaskCreationFilter
```

Return `"go_current"` when `!candidate.GoBehind`.

**`pkg/filter/auto_update_filter.go`**

```go
// NewAutoUpdateFilter is the per-repo trust gate sourced from
// `.maintainer.yaml: goUpdate.autoUpdate`. It is POSITIVE OPT-IN: only
// autoUpdate: true passes. Absent file, absent goUpdate section, absent key
// and explicit false all yield "auto_update_disabled".
//
// This gate is the only thing that turns this service's attention into agent
// action on somebody else's repository. There is deliberately no flag,
// env var, or code path that disables it or defaults it to true.
//
// An UNPARSABLE `.maintainer.yaml` never reaches this filter: the gatherer
// drops that repo from the cycle before evaluation, so a malformed file is a
// drop, not a consent verdict.
func NewAutoUpdateFilter() TaskCreationFilter
```

Return `""` when `candidate.AutoUpdate`, else `"auto_update_disabled"`.

**`pkg/filter/sha_unchanged_filter.go`**

```go
//counterfeiter:generate -o ../../mocks/cursor_reader.go --fake-name CursorReader . CursorReader

// CursorReader is the minimal read surface SHAUnchangedFilter needs. Declared
// locally (Hollywood principle) so this package never imports pkg.Cursor.
type CursorReader interface {
	// LastSeenSHA returns the recorded HEAD for repoKey, or "" if unseen.
	LastSeenSHA(repoKey string) string
}

// NewSHAUnchangedFilter returns "sha_unchanged" when Candidate.HeadSHA equals
// the recorded HEAD for the same repo. A cold cursor always passes; later
// cycles only pass once HEAD advances.
//
// A forced cycle omits this filter from the chain entirely — every other gate
// still applies (spec Desired Behavior 8).
func NewSHAUnchangedFilter(cursor CursorReader) TaskCreationFilter
```

### 3. `pkg/filter/filter_suite_test.go`

Add a Ginkgo suite for `package filter_test`, mirroring `pkg/pkg_suite_test.go` (`time.Local = time.UTC`, `format.TruncatedDiff = false`, 60s suite timeout). Counterfeiter's generate mode is per-directory and non-recursive, so `pkg/filter` needs its own bootstrap directive — immediately above `func TestFilter(...)`, add:

```go
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate
```

Without this line, `mocks/task_creation_filter.go` and `mocks/cursor_reader.go` are never written by `go generate ./...`, and `make precommit` fails. Do NOT add a second package doc comment to this file — the package doc from Requirement 1 (`pkg/filter/filter.go`) already covers it.

### 4. `pkg/cursor.go` — persisted memory

Package `pkg`.

```go
// DefaultCursorPath is the default persisted-memory location. The quant Helm
// chart mounts the PVC at exactly this path; the binary must start with
// CURSOR_PATH unset.
const DefaultCursorPath = "/data/cursor.json"

// Cursor is the per-repo head-SHA dedup state.
//
// Concurrency: not safe for concurrent use. Exactly one cycle runs at a time,
// so the file has a single writer — the cycle loads at start and saves at end.
type Cursor struct {
	Repos map[string]*RepoState `json:"repos"` // key: Repo.Key(), "github.com/owner/name"
}

// RepoState is the cursor entry per repo.
type RepoState struct {
	LastSeenHeadSHA string `json:"last_seen_head_sha"`
}
```

```go
// LoadCursor reads cursor state from path.
//
//   - Missing file -> fresh empty cursor, nil error (cold start is valid and
//     re-publishes; downstream dedup by deterministic identifier absorbs it).
//   - Unreadable or corrupt JSON -> error. The caller MUST refuse to run the
//     cycle rather than proceed on a guessed-empty state, which would re-file
//     the entire fleet. Recovery is the operator deleting the file to accept
//     a cold start.
func LoadCursor(ctx context.Context, path string) (*Cursor, error)

// SaveCursor persists cursor state atomically via temp file + rename, so a
// crash mid-write can never leave a half-written file and no .tmp file
// survives a successful save.
func SaveCursor(ctx context.Context, path string, c *Cursor) error
```

`LoadCursor` implementation:
- `data, err := os.ReadFile(path)` with a `// #nosec G304 -- path is config-controlled` comment on that line.
- `os.IsNotExist(err)` → `glog.V(2).Infof("cursor file not found, cold-start path=%s", path)` then return `&Cursor{Repos: make(map[string]*RepoState)}, nil`.
- Any other read error → `errors.Wrapf(ctx, err, "read cursor file path=%s", path)`.
- `json.Unmarshal` failure → `errors.Wrapf(ctx, err, "unmarshal cursor file path=%s", path)`.
- Nil `c.Repos` after a successful unmarshal → initialise to an empty map.

`SaveCursor` implementation:
- `json.Marshal(c)` failure → `errors.Wrapf(ctx, err, "marshal cursor state path=%s", path)`.
- `tmp := path + ".tmp"`; `os.WriteFile(tmp, data, 0600)` with a `// #nosec G306 -- intentional 0600` comment; failure → `errors.Wrapf(ctx, err, "write cursor tmp path=%s", path)`.
- `os.Rename(tmp, path)`; on failure remove the temp file with `_ = os.Remove(tmp)` before returning `errors.Wrapf(ctx, err, "rename cursor tmp path=%s", path)`, so a failed save leaves no stray `.tmp` behind.

### 5. `pkg/cursorreader.go` — filter adapter

Package `pkg`. Bridges `*Cursor` to `filter.CursorReader` without an import cycle:

```go
// NewCursorReader exposes a filter-compatible read view over a Cursor.
func NewCursorReader(c *Cursor) filter.CursorReader

type cursorReader struct{ c *Cursor }

func (r *cursorReader) LastSeenSHA(repoKey string) string
```

`LastSeenSHA` returns `""` when the cursor or its map is nil, or the entry is missing/nil; otherwise the recorded `LastSeenHeadSHA`.

### 6. Tests

**`pkg/filter/filter_test.go`** (`package filter_test`):
- `TaskCreationFilters{}` (empty) never skips.
- Composite returns the FIRST non-empty reason and does not consult later filters — assert with two `TaskCreationFilterFunc` members where the second records that it ran.
- `TaskCreationFilterFunc` satisfies `TaskCreationFilter`.

**One test file per filter** (or one combined `filters_test.go`) covering:
- `ParseRepoAllowlist`: `""` → nil; `"a/b/c, d/e/f "` → two trimmed entries; `"a/b/c,,"` → one entry.
- `RepoAllowlistFilter`: nil allowlist passes anything; literal match passes; non-match returns `"scope"`; wildcard `github.com/bborbe/*` passes a bborbe repo and returns `"scope"` for another owner; `!github.com/bborbe/skeleton` exclusion returns `"scope"` for that repo while a sibling passes.
- `GoModPresentFilter`: `GoModPresent:false` → `"no_gomod"`; true → `""`.
- `GoModParsableFilter`: present+unparsable → `"gomod_unparsable"`; present+parsable → `""`; absent → `""` (the present filter owns that case).
- `GoBehindFilter`: `GoBehind:true` → `""`; false → `"go_current"`.
- `AutoUpdateFilter`: a table over the consent matrix with `AutoUpdate` false → `"auto_update_disabled"` and true → `""`.
- `SHAUnchangedFilter`: driven with the counterfeiter `mocks.CursorReader` — matching SHA → `"sha_unchanged"`; different SHA → `""`; empty recorded SHA (cold) → `""`.
- **Closed-set assertion**: a table test that runs every constructor in this package against a Candidate that trips it, collects the returned reasons, and asserts each one is a member of `pkg.FilterSkipReasons`. This catches a typo'd label that would otherwise emit an unregistered Prometheus series at runtime. Import `pkg` from `filter_test` (external test package — no cycle).
- **Full-chain ordering assertion**: assemble `TaskCreationFilters{allowlist, present, parsable, behind, autoUpdate, shaUnchanged}` and assert that a Candidate failing several gates at once returns the earliest reason in chain order (e.g. out-of-scope AND no go.mod AND not opted in → `"scope"`).

**`pkg/cursor_test.go`** (`package pkg_test`) — use Ginkgo `BeforeEach` with `GinkgoT().TempDir()` or `os.MkdirTemp`:
- `LoadCursor` on a missing path returns a non-nil cursor with a non-nil empty `Repos` map and a nil error.
- `LoadCursor` on `{"repos":{"github.com/bborbe/a":{"last_seen_head_sha":"abc"}}}` round-trips the value.
- `LoadCursor` on `{"repos":null}` returns a non-nil `Repos` map.
- `LoadCursor` on `not json` returns an error.
- `SaveCursor` then `LoadCursor` round-trips.
- After a successful `SaveCursor`, the directory listing contains the cursor file and NO `*.tmp` entry.
- `SaveCursor` to a path in a non-existent directory returns an error and leaves no `.tmp` file in the parent directory that does exist.
- `SaveCursor` where the rename fails (e.g. the destination path is itself an existing non-empty directory, so `os.Rename` errors) returns an error and leaves no `*.tmp` file in the target directory.
- The written file has mode `0600`.

**`pkg/cursorreader_test.go`** (`package pkg_test`):
- Nil cursor → `""`.
- Cursor with nil `Repos` → `""`.
- Missing key → `""`.
- Nil `*RepoState` value stored under a key → `""` (no panic).
- Present key → the recorded SHA.

### 7. Regenerate mocks and verify

Run `make precommit` from the repo root. Confirm `mocks/task_creation_filter.go` and `mocks/cursor_reader.go` exist afterwards. New files in `pkg/filter` and the new `pkg/cursor.go` / `pkg/cursorreader.go` should reach at least 80% statement coverage.

</requirements>

<constraints>
- Skip-reason strings are a closed set of exactly six: `scope`, `no_gomod`, `gomod_unparsable`, `go_current`, `auto_update_disabled`, `sha_unchanged`. Do not invent a seventh, and do not rename any of them.
- Nothing is ever defaulted to opted-in. Do NOT add an env flag, constructor option, or code path that disables the opt-in gate or the allowlist — spec Non-goal: "an escape hatch on a trust gate is the regression this spec exists to prevent."
- Do NOT gate forks separately — `Candidate` has no fork field and no fork filter exists.
- Do NOT add cursor-editing admin endpoints (reset/set per repo) — spec Non-goal. This prompt adds `LoadCursor`/`SaveCursor` only, no HTTP handlers.
- The repo allowlist comes from `github.com/bborbe/maintainer/repoallowlist` — do NOT hand-roll matching, wildcards, or exclusions.
- `pkg/filter` must NOT import `github.com/bborbe/github-update-go-watcher/pkg` in non-test code (import cycle). External test files in `package filter_test` may import it.
- Cursor writes are atomic (temp file + rename) and leave no `.tmp` behind on either the success or the failure path.
- A corrupt cursor file is an error, never a silently-empty cursor.
- Never use `fmt.Errorf`. All errors go through `github.com/bborbe/errors` and carry `ctx`.
- The module must never shell out or clone: no `os/exec`, no `exec.Command`, no `go-git` anywhere.
- Do NOT touch `main.go`, `main_test.go`, `pkg/factory/`, `pkg/handler/`, `pkg/mathutil/`, `k8s/`, or `README.md` in this prompt.
- Never hand-edit anything under `mocks/`.
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
grep -rn "github-update-go-watcher/pkg\"" pkg/filter/*.go | grep -v _test.go
```
Expect zero matches (no import cycle).

```
grep -rho 'scope\|no_gomod\|gomod_unparsable\|go_current\|auto_update_disabled\|sha_unchanged' pkg/filter/*.go | sort -u | wc -l
```
Expect `6`.

```
grep -n "DefaultCursorPath = \"/data/cursor.json\"" pkg/cursor.go
grep -n "counterfeiter/v6@v6.12.2 -generate" pkg/filter/*_test.go
```
Both expect at least one match.

```
ls mocks/task_creation_filter.go mocks/cursor_reader.go
```
Both must exist after `make precommit`.

```
go test -mod=mod ./pkg/...
```
Must exit 0.
</verification>
