---
status: completed
spec: [008-stale-ignore-signal]
created: "2026-11-24T16:05:00Z"
queued: "2026-11-24T14:37:42Z"
completed: "2026-11-24T14:54:00Z"
pr-url: https://github.com/bborbe/github-update-go-watcher/pull/45
---

# The stale-ignore pass: eligibility, emission and wiring

<summary>
- Every poll cycle now also scans a bounded slice of repos for ignores OSV has withdrawn, inside the same cycle and the same cycle lock — no new cron, ticker or goroutine.
- Only allowlisted repos are considered, and only repos whose last ignore scan is absent or older than a day; at most ten per cycle, oldest first, so a fleet of ~210 repos is covered about once a day.
- A repo with no `go.mod`, or without explicit opt-in consent, is skipped but still marked scanned, so a permanently ineligible repo cannot hold a queue slot forever.
- A repo is scanned even when it is Go-current and even when its HEAD was already reported — the two gates that hide this population today play no part here.
- Any upstream read failure drops only that repo for that cycle and leaves its scan timestamp unadvanced, so the next eligible cycle retries it.
- A repo with at least one withdrawn advisory emits exactly one task; a repo whose ignores are all legitimate or unresolved emits nothing.
- Re-emission is deduplicated by repo plus a monotonic generation over the sorted stale set, so an unchanged set re-emits at most once a week with a genuinely new task identifier, and a task that dies is not frozen forever.
- The emitted task reuses the frozen envelope — same task type, assignee, phase, status, stage, repo, clone URL, ref, current and latest Go — with only the identifier, the title and the body changed, and it still reaches the board through the same typed sender that stamps the target vault.
- One new counter reports the pass's outcome per repo, pre-initialised to zero before the first cycle; emissions need no new counter because the existing published counter already counts them.
- A small exercise program drives the real pass against the real GitHub and OSV APIs and writes its own transcript, so the alias case can be proven live rather than argued from unit tests.
</summary>

<objective>
Land the pass that turns "this repo suppresses a withdrawn advisory" into one filed work item, on the existing schedule and with no new deployment surface. It must reach repos the Go-version chain structurally cannot — Go-current repos whose HEAD has not moved — and it must never emit on an upstream failure, only on a positive withdrawn verdict.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:

- `pkg/watcher.go` — `Poll` (`:89`), `processRepos` (`:165`), `completeMergedUpdates` (`:677`), `gatherCandidate` (`:615`), `dropRepo` (`:795`, the `"repo dropped from cycle"` grep handle — reuse the phrase), `NewWatcher` (`:46`) and the `watcher` struct (`:72`). `Poll`'s shape is: load cursor → resolve latest Go → list repos → `processRepos` → `completeMergedUpdates` → `SaveCursor` + `consumePending` → `IncPollCycle("success")`; every abort branch counts a result, saves the cursor and returns nil.
- `pkg/cursor.go` — `Cursor` and `RepoState` (`:30`); every new field is `omitempty`. `Cursor.CachedVerdict` / `Cursor.SetVerdict` were added by the previous prompt — read them, do not re-declare them. Declare `const osvVerdictTTL = 24 * libtime.Hour` in `pkg/cursor.go` yourself, next to `maxIgnoreScansPerCycle` / `staleReemitInterval` — this prompt is its only consumer.
- `pkg/taskbuilder.go` — `TaskConfig` (`:15`), `ComputeTaskTitle` (`:34`), `BuildCreateCommand` (`:44`), `buildFrontmatter` (`:54`) and `buildTaskBody` (`:86`) — the frozen emit contract the new builder must reproduce exactly.
- `pkg/taskid.go` — `taskIDNamespace` (`:16`), `DeriveTaskID` (`:26`), `decisionTaskIDNamespace` (`:34`), `DeriveDecisionTaskID` (`:44`) — the two existing namespaces the new one must stay distinct from.
- `pkg/taskpublisher.go` — the `TaskPublisher` interface (`:19`), `NewTaskPublisher` (`:25`) and the `PublishCreate` implementation (`:43`) whose logging and metric shape the new method mirrors.
- `pkg/metrics.go` — the `Metrics` interface (`:14`), the closed label sets (`:39`), `NewMetrics` (`:68`) and the pre-initialisation loop (`:119`) — the new counter follows that exact shape.
- `pkg/githubclient.go` — `GetFile` was added by the first prompt; `filter.GrantedConsent` is the consent value the gate requires.
- `pkg/filter/repo_allowlist_filter.go` — `NewRepoAllowlistFilter(allowlist []string) TaskCreationFilter`; its `Skip` reads only `Candidate.RepoKey`, so the pass can call it with `filter.Candidate{RepoKey: repo.Key()}`.
- `pkg/factory/factory.go` — `CreateGoDevHTTPClient` (`:47`, the plain-client precedent), `CreateWatcher` (`:119`, the single composition point) and `CreateCreateCommandSender` (`:73`).
- `main.go` — `application.Run` (`:65`): `allowlist` is computed at `:73` and `factory.CreateWatcher(...)` is called at `:120`; `main.go` is the only production caller.
- `pkg/watcher_test.go` — the three `pkg.NewWatcher(` call sites (`:66`, `:1093`, `:1230`) and the `mocks` package usage pattern.
- `pkg/factory/factory_test.go` — `capturingCommandObjectSender` (`:146`) is the shape for asserting the stamped `target_vault`; it lives in `package factory_test` so copy the pattern into your own test package rather than importing it.
- `docs/dod.md` — `make precommit` green, new functions tested, Ginkgo/Gomega, `mocks/` regenerated, `github.com/bborbe/errors`, no absolute paths. Its "In-flight signal contract" section is a durable contract you must not break.
- `libtime` (`github.com/bborbe/time v1.27.25`): `libtime.Hour` / `libtime.Day` are `libtime.Duration` constants; `libtime.DateTime` has `Sub(HasTime) Duration`, `Format(layout string) string` and `IsZero() bool`; `libtime.ParseDateTime(ctx, value) (*DateTime, error)` returns a POINTER. Never call `time.Now()` — use the injected `libtime.CurrentDateTimeGetter`.

Guides to read before writing code (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the per-repo `select { case <-ctx.Done(): … default: }` shape `processRepos` uses.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`

`.golangci.yml` is strict: funlen 80 lines / 50 statements, gocognit 20, nestif 4, gocyclo, dupl, prealloc. Split the pass into the small helpers named below rather than writing one long loop.
</context>

<requirements>

### 1. Cursor fields — `pkg/cursor.go`, on `RepoState`

Add four fields after `InFlightSince`, each `omitempty` so a cursor written by the previous binary loads and cold-starts:

```go
	// IgnoreScannedAt is when the stale-ignore pass last read this repo's
	// ignore surfaces (RFC3339, UTC). Absent means "never scanned", which sorts
	// first in the throttle. It is advanced on every completed scan and on every
	// eligibility skip, but NOT when the repo was dropped mid-scan, so a failed
	// read is retried on the next eligible cycle.
	IgnoreScannedAt string `json:"ignore_scanned_at,omitempty"`
	// IgnoreStaleIDs is the sorted stale advisory set recorded at the last
	// emission, joined by ",". Empty means "no stale set recorded" — a repo
	// whose set became empty has this cleared so a later identical set emits
	// again.
	IgnoreStaleIDs string `json:"ignore_stale_ids,omitempty"`
	// IgnoreStaleGeneration is monotonic per repo and never resets. It is part
	// of the task identifier, so every emission mints a NEW record instead of an
	// identity-identical create the controller would dedup into a no-op.
	IgnoreStaleGeneration int `json:"ignore_stale_generation,omitempty"`
	// IgnoreStaleEmittedAt is when the recorded set was last emitted (RFC3339,
	// UTC). Together with IgnoreStaleIDs it bounds the re-emit to one task per
	// staleReemitInterval while the set is unchanged.
	IgnoreStaleEmittedAt string `json:"ignore_stale_emitted_at,omitempty"`
```

### 2. Task identity — `pkg/taskid.go`

```go
// staleIgnoreTaskIDNamespace is the UUID5 namespace for stale-ignore tasks.
// Frozen and deliberately distinct from taskIDNamespace and
// decisionTaskIDNamespace: a stale-ignore task must never collide with an
// update task or a decision task.
var staleIgnoreTaskIDNamespace = uuid.MustParse("4f2a7c1e-9b3d-4c58-8e21-6d0b7a5c9f13")

// DeriveStaleIgnoreTaskID returns a UUID5 over the seed
// "stale-ignore-<owner>-<repo>-g<generation>-<ids>", where ids is the sorted
// stale set joined by ",".
//
// The generation is part of the identity and only ever increases, so a re-emit
// past staleReemitInterval mints a record that does not yet exist and therefore
// actually runs. Resetting the generation to 1 on a set change would be wrong:
// an oscillating set ({A} -> {A,B} -> {A}) would re-derive the first emission's
// identifier, the controller would dedup it, and the still-stale A would never
// be re-filed.
func DeriveStaleIgnoreTaskID(owner, repo string, generation int, ids []string) uuid.UUID
```

Use `uuid.NewSHA1(staleIgnoreTaskIDNamespace, []byte(seed))`, exactly as the two existing derivations do. Copy the incoming `ids` before sorting inside the function (never mutate the caller's slice); join with `","`.

### 3. Builder — `pkg/taskbuilder.go`

Add the finding type and two functions, and refactor the shared frontmatter so the frozen contract lives in ONE place:

```go
// StaleIgnoreFinding is one repo's stale-ignore verdict: the advisory IDs OSV
// reports as withdrawn, and per ID the ignore surface files that declare it.
type StaleIgnoreFinding struct {
	IDs      []string            // sorted, de-duplicated
	Surfaces map[string][]string // advisory ID -> surface paths, in IgnoreSurfaceFiles order
}

// ComputeStaleIgnoreTaskTitle returns the frozen title form
// "Remove Stale Ignores <owner>-<repo>" — dash form, no HEAD SHA: the identity
// is the stale set, not the commit. CreateCommand.Validate rejects any '/' in a
// title, so the dash form is mandatory.
func ComputeStaleIgnoreTaskTitle(c Candidate) string

// BuildStaleIgnoreCommand assembles the CreateTaskCommand for one stale-ignore
// finding. The envelope is exactly BuildCreateCommand's — same task_type,
// assignee, phase, status, stage, repo, clone_url, ref, current_go, latest_go
// and optional update_scope — with only the identifier, the title and the body
// different. target_vault is NOT set here: it is stamped at send time by the
// typed sender.
func BuildStaleIgnoreCommand(
	c Candidate,
	finding StaleIgnoreFinding,
	generation int,
	cfg TaskConfig,
) task.CreateCommand
```

Refactor `buildFrontmatter(c Candidate, taskIDStr string, cfg TaskConfig)` into a shared `baseFrontmatter(c Candidate, title string, taskIDStr string, cfg TaskConfig) agentlib.TaskFrontmatter` that builds the whole map (including `"title": title` and the conditional `update_scope`), and make the existing `buildFrontmatter` a two-line delegate passing `ComputeTaskTitle(c)`. `BuildStaleIgnoreCommand` calls `baseFrontmatter(c, ComputeStaleIgnoreTaskTitle(c), DeriveStaleIgnoreTaskID(...).String(), cfg)`. The existing update-task frontmatter must come out byte-identical — that is a regression test, not a comment.

The body must name every stale ID, the surface(s) each appears on, and that OSV reports the advisory as withdrawn. Build it in a `buildStaleIgnoreBody(c Candidate, finding StaleIgnoreFinding) string` helper in the same shape as `buildTaskBody`:

```
# Remove Stale Ignores: <owner>/<name>

OSV reports the following advisories as withdrawn, but this repo still
suppresses them:

- `<id>` — surfaces: `<file>`, `<file>`
- `<id>` — surfaces: `<file>`

**Repo:** [<owner>/<name>](https://github.com/<owner>/<name>)
**HEAD:** <c.ShortSHA()>
```

Iterate `finding.IDs` in order and look up `finding.Surfaces[id]`; a missing entry renders as the empty list (do not panic).

### 4. Publisher — `pkg/taskpublisher.go`

Extend the `TaskPublisher` interface with:

```go
	// PublishStaleIgnore builds and sends the stale-ignore CreateTaskCommand.
	// Returns true only on a successful send — the caller records the emission
	// in the cursor only on true.
	PublishStaleIgnore(
		ctx context.Context,
		candidate Candidate,
		finding StaleIgnoreFinding,
		generation int,
	) bool
```

Implement it exactly like `PublishCreate`: build the command, `sender.SendCommand`; on error `glog.Errorf("publish stale-ignore task failed repo=%s generation=%d taskID=%s err=%v", …)` + `metrics.IncPublished("error")` + `false`; on success one `glog.V(2).Infof("published StaleIgnoreCommand repo=%s stale_ids=%s generation=%d taskID=%s stage=%s", …)` + `metrics.IncPublished("create")` + `true`. The emission log line must name the repo, the stale IDs, the generation and the task identifier — that is the spec's observability requirement, and it needs no second counter.

`mocks/task_publisher.go` is regenerated by `make precommit`; never hand-edit it.

### 5. Metrics — `pkg/metrics.go`

Add to the `Metrics` interface:

```go
	// IncIgnoreScan — result: "scanned" | "clean" | "stale" | "unresolved" |
	// "error" | "rate_limited". One increment per repo that reached the surface
	// reads ("scanned" as the denominator) plus exactly one outcome increment
	// ("clean" | "stale" | "unresolved" | "error"), or one "rate_limited" per
	// aborted pass.
	IncIgnoreScan(result string)
```

Add the closed label set beside `FilterSkipReasons`:

```go
	IgnoreScanResults = []string{
		"scanned",
		"clean",
		"stale",
		"unresolved",
		"error",
		"rate_limited",
	}
```

Register a `prometheus.CounterVec` named `ignore_scan_total` with the `result` label and Help text "Total number of stale-ignore scans by result" under the existing `metricNamespace`, add it to `metricsImpl`, add `IncIgnoreScan` to the implementation, and extend the pre-initialisation loop so all six series exist at 0 before the first cycle. Do not add any other metric — emission is already counted by `published_total`.

### 6. The pass — create `pkg/staleignore.go` (package `pkg`)

Constants (package-level, no config):

```go
// maxIgnoreScansPerCycle bounds how many repos the pass reads surfaces for in
// one cycle: 10 repos x <=7 GitHub calls = <=70 calls per cycle, which covers a
// ~210-repo fleet about once a day at a 10-minute poll interval.
const maxIgnoreScansPerCycle = 10

// ignoreRescanInterval is how long a repo's ignore surfaces are trusted before
// being read again.
const ignoreRescanInterval = 24 * libtime.Hour

// staleReemitInterval bounds the re-emit of an unchanged stale set to one task
// per repo per interval, so a task that dies without removing the ignore is
// re-filed, while a healthy repo is not spammed.
const staleReemitInterval = 7 * libtime.Day
```

The interface and constructor:

```go
// StaleIgnorePass is the second signal: it reads a repo's ignore surfaces, asks
// OSV whether any suppressed advisory has been withdrawn, and publishes one
// task per repo with a stale set. It runs inside the poll cycle, after the
// merge-detection pass, under the same CycleGate.
type StaleIgnorePass interface {
	// Scan runs one pass and returns an abort reason ("rate_limited" |
	// "github_error" | "") exactly as the watcher's other passes do. Every
	// per-repo failure is counted and logged; it never aborts the cycle.
	Scan(ctx context.Context, cursorState *Cursor, repos []Repo, latestGo Version) string
}

// NewStaleIgnorePass wires the pass's collaborators. allowlist is the raw
// operator allowlist (the same slice the filter chain was built from); the pass
// applies it itself because it must not inherit the Go-version gates.
func NewStaleIgnorePass(
	ghClient GitHubClient,
	osvClient OSVClient,
	publisher TaskPublisher,
	metrics Metrics,
	allowlist []string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) StaleIgnorePass
```

The implementation is a private struct holding those six values.

**`Scan` — the exact order.** `now := p.currentDateTimeGetter.Now()` once per pass, then for each repo returned by `selectRepos`:

1. `select { case <-ctx.Done(): glog.V(2).Infof("poll cancelled during staleIgnores at repo=%s", repo.Key()); return "" default: }` — the same shape `processRepos` uses.
2. Gather with the shared candidate function: `candidate, abortReason, dropped := gatherCandidate(ctx, p.ghClient, repo, latestGo)`. If `abortReason != ""` → `p.metrics.IncIgnoreScan("rate_limited")` and return it. If `dropped` → `p.metrics.IncIgnoreScan("error")` and continue (the timestamp stays unadvanced).
3. `state := repoState(cursorState, repo)` — create the `*RepoState` and insert it into `cursorState.Repos[repo.Key()]` when absent.
4. Eligibility gate: when `!candidate.GoModPresent` or `candidate.Consent != filter.GrantedConsent`, log one `glog.V(2).Infof("stale-ignore: repo skipped repo=%s reason=%s", repo.Key(), "no_gomod"|"consent_not_granted")`, set `state.IgnoreScannedAt = now.Format(time.RFC3339)` and continue. Do not increment an outcome counter here — the skip is visible in the log.
5. `surfaces, ids, abortReason, dropped := p.readSurfaces(ctx, repo)`. Abort → `IncIgnoreScan("rate_limited")` + return; dropped → `IncIgnoreScan("error")` + continue (timestamp NOT advanced).
6. `p.metrics.IncIgnoreScan("scanned")`.
7. No IDs (`len(ids) == 0`) → `IncIgnoreScan("clean")`, clear the recorded stale set (`clearStaleSet(state)`), advance the timestamp, continue.
8. `staleIDs, unresolved := p.resolveVerdicts(ctx, cursorState, ids, now)`.
9. No stale IDs → `IncIgnoreScan("unresolved")` when `unresolved` else `IncIgnoreScan("clean")`, `clearStaleSet(state)`, advance the timestamp, continue.
10. `p.metrics.IncIgnoreScan("stale")`; `finding := StaleIgnoreFinding{IDs: staleIDs, Surfaces: surfaces}`; when `p.emit(ctx, state, candidate, finding, now)` returns true, advance `state.IgnoreScannedAt`.

Return `""` at the end.

**`selectRepos(repos []Repo, cursorState *Cursor, now libtime.DateTime) []Repo`**
- Keep a repo only when `filter.NewRepoAllowlistFilter(p.allowlist).Skip(filter.Candidate{RepoKey: repo.Key()}) == ""`.
- Keep a repo only when its scan is due: state absent, `IgnoreScannedAt == ""`, an unparsable value (log `V(2)`), or `now.Sub(parsed) >= ignoreRescanInterval`.
- Sort ascending by that scan time, absent/unparsable first; break ties by `repo.Key()` so the selection is deterministic.
- Truncate to `maxIgnoreScansPerCycle`.

**`repoState(cursorState *Cursor, repo Repo) *RepoState`** — returns the existing entry for `repo.Key()`, allocating and inserting one when absent. It never mutates the other fields.

**`readSurfaces(ctx, repo) (map[string][]string, []string, string, bool)`**
- Iterate `IgnoreSurfaceFiles` in order; `content, err := p.ghClient.GetFile(ctx, repo, file)`.
- `stderrors.Is(err, ErrRateLimited)` → return `(nil, nil, "rate_limited", false)`.
- Any other error → `glog.Warningf("repo dropped from cycle: owner=%s repo=%s step=%s err=%v", repo.Owner, repo.Name, "stale_ignore:"+file, err)` and return `(nil, nil, "", true)`. Keep the `"repo dropped from cycle"` phrase verbatim — it is the operator's grep handle — and the error text already names the size for the oversize case.
- `content == nil` → that surface is empty, continue.
- For each ID from `ParseIgnoreSurface(file, content)`: append `file` to `surfaces[id]` and append `id` to the id list on first sight.
- Return the id list sorted (`sort.Strings`).

**`resolveVerdicts(ctx, cursorState, ids []string, now libtime.DateTime) ([]string, bool)`**
- For each ID in order: `verdict, ok := cursorState.CachedVerdict(ctx, id, now, osvVerdictTTL)`. When `!ok`, call `p.osvClient.CheckWithdrawn(ctx, id)`; on a non-nil error log one `glog.Warningf("stale-ignore: osv lookup failed id=%s err=%v", id, err)` and continue with the returned verdict. When the verdict is neither `OSVVerdictUnresolved` nor the empty string, record it with `cursorState.SetVerdict(id, verdict, now)`.
- `OSVVerdictWithdrawn` → collect the ID. `OSVVerdictLegitimate` → nothing. Anything else (including the empty string a mock returns by default) → mark `unresolved = true`.
- Return the sorted stale IDs and the unresolved flag.

**`emit(ctx, state, candidate, finding, now) bool`**
- `joined := strings.Join(finding.IDs, ",")`.
- When `joined == state.IgnoreStaleIDs` and the re-emit is not due, log one `glog.V(2).Infof("stale-ignore: no emission repo=%s reason=unchanged generation=%d", …)` and return true (nothing pending, so the scan timestamp advances).
- Otherwise `generation := state.IgnoreStaleGeneration + 1`; call `p.publisher.PublishStaleIgnore(ctx, candidate, finding, generation)`. On false, return false (leave every cursor field alone, including the scan timestamp, so the next cycle retries the whole repo). On true, set `state.IgnoreStaleIDs = joined`, `state.IgnoreStaleGeneration = generation`, `state.IgnoreStaleEmittedAt = now.Format(time.RFC3339)` and return true.
- `staleReemitDue(ctx, state, now)`: parse `IgnoreStaleEmittedAt` with `libtime.ParseDateTime`; empty or unparsable → false (an unparsable value must never cause a weekly emission every cycle); else `now.Sub(parsed) >= staleReemitInterval`.
- `clearStaleSet(state)`: set `IgnoreStaleIDs = ""` when it is non-empty. Never touch the generation or the emitted-at value — the generation is monotonic.

### 7. Share the candidate gather — `pkg/watcher.go`

Convert the `(*watcher).gatherCandidate` method into a package-level function so both passes use one implementation of the 3-call gather and its drop/abort semantics:

```go
func gatherCandidate(
	ctx context.Context,
	ghClient GitHubClient,
	repo Repo,
	latestGo Version,
) (Candidate, string, bool)
```

Move the existing body unchanged (`GetHeadSHA` → `GetGoMod` → `GetMaintainerConfig`, the `ErrRateLimited` → `"rate_limited"` mapping, and `dropRepo(repo, "head_sha"|"go_mod"|"maintainer_config", err)` for everything else) and update `processRepos`'s single call site to `gatherCandidate(ctx, w.ghClient, repo, latestGo)`. Do not change any behaviour, log line or metric.

### 8. Wire the pass into the cycle — `pkg/watcher.go`

- `NewWatcher` gains two trailing parameters, after `terminalClaims TerminalClaims`: `osvClient OSVClient` and `allowlist []string`.
- The `watcher` struct gains `staleIgnorePass StaleIgnorePass`.
- `NewWatcher` sets `staleIgnorePass: NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, allowlist, currentDateTimeGetter)` — the pass is built inside the watcher, so there is no second wiring site and no mock to regenerate for it.
- Add the pass's entry point as a method on `*watcher`, so `Poll` reads like the other passes and the exercise program can still drive exactly this pass:

```go
// staleIgnores runs the stale-ignore pass and returns an abort reason
// ("rate_limited" | "github_error" | ""). It is a thin delegate: the pass is a
// separate type so the live exercise program can drive it without running the
// Go-bump cycle.
func (w *watcher) staleIgnores(
	ctx context.Context,
	cursorState *Cursor,
	repos []Repo,
	latestGo Version,
) string {
	return w.staleIgnorePass.Scan(ctx, cursorState, repos, latestGo)
}
```

- In `Poll`, immediately AFTER the `completeMergedUpdates` block and BEFORE the final `SaveCursor` call, insert:

```go
	if reason := w.staleIgnores(ctx, cursorState, repos, latestGo); reason != "" {
		w.metrics.IncPollCycle(reason)
		glog.Warningf(
			"poll cycle: stale-ignore pass aborted reason=%s owner=%s",
			reason,
			w.owner,
		)
		if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil {
			glog.Warningf("save cursor failed path=%s err=%v", w.cursorPath, err)
		} else {
			w.consumePending(ctx)
		}
		return nil
	}
```

The cursor is saved on abort exactly as the other passes do, so work already learned this cycle is not lost. Do not change `Poll`'s signature, add a goroutine, a ticker, a `run.Func`, or a second `CycleGate`.

### 9. Factory and binary wiring — `pkg/factory/factory.go` and `main.go`

In `pkg/factory/factory.go`:

```go
// osvTimeout bounds each OSV request. The pass is best-effort: a slow upstream
// must not stall a cycle.
const osvTimeout = 10 * time.Second

// CreateOSVHTTPClient returns the plain HTTP client used for OSV lookups.
//
// It is deliberately NOT the GitHub App-authenticated client: reusing that
// client would send an installation token to a third-party host.
func CreateOSVHTTPClient() *http.Client {
	return &http.Client{Timeout: osvTimeout}
}
```

`CreateWatcher` gains `osvHTTPClient *http.Client` immediately after `goDevHTTPClient`, and `allowlist []string` immediately after `updateScope`. Inside it build `osvClient := pkg.NewOSVClient(osvHTTPClient, pkg.DefaultOSVVulnBaseURL, pkg.DefaultOSVIndexURL, currentDateTimeGetter)` and pass `osvClient, allowlist` as the two trailing arguments to `pkg.NewWatcher`.

In `main.go`, add `factory.CreateOSVHTTPClient(),` as the third argument of `factory.CreateWatcher(...)` (right after `factory.CreateGoDevHTTPClient(),`) and `allowlist,` after `a.UpdateScope,`. No new flag, env var, chart value or struct field on `application`.

### 10. Create `hack/staleignore-exercise/main.go` — the live exercise program

A `package main` program that drives the real pass against the real APIs and writes its own transcript, so the alias case can be proven live rather than argued. It must build and lint clean (`go list ./...` includes it, so `vet` and `golangci-lint` run on it).

Flags:
- `-repo string` (required) — host-qualified, `github.com/<owner>/<name>`.
- `-transcript string` — path to write the transcript; empty means stdout.

Behaviour:
1. Read the token from `GITHUB_TOKEN`; when empty, write one transcript line `error: GITHUB_TOKEN is not set` and exit 2. Never print the token, never log a header.
2. Build the GitHub client: `pkg.NewGitHubClient(&http.Client{Transport: &bearerTransport{token: token}})`, where `bearerTransport` is a local `http.RoundTripper` that sets `Authorization: Bearer <token>` and delegates to `http.DefaultTransport`.
3. Resolve latest Go with `pkg.NewGoDevClient(factory.CreateGoDevHTTPClient(), pkg.DefaultGoDevURL)`.
4. Resolve the repo WITHOUT `ListRepos`: that method calls `GET /installation/repositories`, which only an installation access token may call, so a `GITHUB_TOKEN` user token would 403 and the program would misreport "repo not found". Issue `GET <apiBase>/repos/<owner>/<name>` through the same bearer `*http.Client` and build `pkg.Repo{Owner: owner, Name: name, DefaultBranch: resp.GetDefaultBranch()}`; a 404 writes a transcript line and exits 1. This supplies the `DefaultBranch` that `GetFile` uses as its ref.
5. Build the OSV client on `factory.CreateOSVHTTPClient()` whose `Transport` is wrapped in a local `recordingTransport{inner: <previous transport or http.DefaultTransport>, out: transcript}`, then `pkg.NewOSVClient(osvHTTP, pkg.DefaultOSVVulnBaseURL, pkg.DefaultOSVIndexURL, libtime.NewCurrentDateTime())`.
6. `metrics := pkg.NewMetrics(prometheus.NewRegistry())`; `publisher := &printingPublisher{out: transcript}` implementing all three `pkg.TaskPublisher` methods (it prints the command as JSON for `PublishStaleIgnore` and prints nothing for the other two).
7. `pass := pkg.NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, nil, libtime.NewCurrentDateTime())` — a nil allowlist means allow-all within the owner, the same semantics `repoallowlist.IsAllowed` gives.
8. `cursor := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}`; `reason := pass.Scan(ctx, cursor, []pkg.Repo{repo}, latestGo)`.
9. Write the transcript: a header naming the repo; one line per OSV request from `recordingTransport` (`url`, `status`, and `withdrawn` parsed out of the response body); the decoded `CreateTaskCommand` JSON for each `PublishStaleIgnore`; every `ignore_scan_total` series read back from the registry via `Gather()`; the abort reason; and a final `exit_code=<n>`.
10. Exit 0 when `reason == ""`, 1 otherwise.

`recordingTransport.RoundTrip` must peek at most 64 KiB of the response body, RESTORE the body for the caller (the same `io.MultiReader(prefix, rest)` shape `pkg/non2xx_log.go`'s `logNon2xxResponse` uses — read it, do not invent a second pattern), and record `status`. When the peeked body is JSON carrying a `withdrawn` key, record its value; otherwise record `absent`.

The live run itself needs a GitHub token and unrestricted network, so it is performed outside the prompt's verification; this prompt delivers the program and its compile check.

### 11. Tests — create `pkg/staleignore_test.go`, `package pkg_test`

Build the pass directly: `pkg.NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, allowlist, currentDateTime)` with `mocks.GitHubClient`, `mocks.OSVClient`, `mocks.TaskPublisher`, `mocks.Metrics` and a settable `libtime.CurrentDateTime`. Drive cycles by calling `Scan` with a `*pkg.Cursor` you own (a `&pkg.Cursor{Repos: map[string]*pkg.RepoState{}}`), which keeps each scenario independent of the file round-trip. Cover:

- **AC7** — the `bborbe/run` shape: `GetFile` returns the committed `pkg/testdata/ignore/*` contents, `CheckWithdrawn` returns `OSVVerdictLegitimate` for every ID → `publisher.PublishStaleIgnoreCallCount()` is 0, `metrics` sees `clean`, and `state.IgnoreStaleIDs` stays empty.
- **AC8** — one stale ID and no other finding → exactly one `PublishStaleIgnore` call. Assert field by field on the command produced by `pkg.BuildStaleIgnoreCommand` (task_type `github-update-go`, assignee `github-update-go-agent`, phase `planning`, status `in_progress`, stage from `TaskConfig`, `repo`, `clone_url`, `ref`, `current_go`, `latest_go`, title `Remove Stale Ignores <owner>-<repo>`, body containing the stale ID and its surface). Then send that command through `factory.CreateCreateCommandSender(capture, "openclaw")` using a `capturingCommandObjectSender` you define in the test file (copy the shape from `pkg/factory/factory_test.go:146` — it is in another package and cannot be imported) and assert the decoded `TargetVault` equals `openclaw`. Asserting `TargetVault` on the builder's output would read an empty value and fail; the stamp happens at send time.
- **AC8, title** — `cmd.Validate(ctx)` succeeds on the built command (this is the boundary that rejects `/` in a title), asserted explicitly.
- **AC9** — with an injected clock: cycle 1 emits with generation 1; cycle 2 (clock +1h, same set) publishes nothing and the cursor still records generation 1; cycle 3 (clock +8 days) publishes again with a DIFFERENT `task_identifier` and generation 2. Assert the decoded cursor fields `IgnoreStaleIDs`, `IgnoreStaleGeneration`, `IgnoreStaleEmittedAt` at each step.
- **AC9, oscillating set** — `{A}` → `{A,B}` → `{A}` produces three distinct identifiers (the third differs from the first), proving the generation never resets.
- **AC9, failed publish** — `publisher.PublishStaleIgnoreReturns(false)`: the three cursor fields stay empty AND `IgnoreScannedAt` is not advanced, so the repo is selected again next cycle.
- **AC9, lost cursor write** — a pass that published but whose cursor was never persisted (drive a fresh `&pkg.Cursor{Repos: …}` with the same finding) re-derives the SAME `task_identifier`, so the controller dedups the repeat instead of filing a second record. Assert the two identifiers are equal and the generations are both 1.
- **Cursor round-trip** — `SaveCursor` → `LoadCursor` on a `*pkg.Cursor` whose `RepoState` carries all four new fields returns them intact; and a cursor JSON written WITHOUT those keys (the previous binary's shape) loads and `selectRepos` selects that repo (absent `IgnoreScannedAt` sorts first). This is the boundary the in-memory scenarios above deliberately skip.
- **AC10** — `maxIgnoreScansPerCycle` binds: 12 eligible repos, assert exactly 10 `GetFile`-bearing repos were gathered (count `ghClient.GetHeadSHACallCount()`), and that the two unselected ones keep an empty `IgnoreScannedAt`. `ignoreRescanInterval` binds: a repo scanned at `now` is not selected at `now+1h` and is selected at `now+25h`.
- **AC10, ineligible still advances** — a repo with no `go.mod` (or `filter.RefusedConsent`) is skipped and its `IgnoreScannedAt` is set; on the next cycle it is not selected again.
- **Allowlist** — a repo outside the allowlist is never gathered (no `GetHeadSHA` call) and its cursor state is untouched.
- **Drop leaves state alone** — `GetFile` returns an error for `.trivyignore`: the repo is dropped, `IncIgnoreScan("error")` is recorded, `IgnoreScannedAt` stays empty, and a later cycle with a working client scans it.
- **Unresolved never emits** — `CheckWithdrawn` returns `OSVVerdictUnresolved` for the only ID → no publish, `IncIgnoreScan("unresolved")`, and the verdict is NOT in `cursor.OSVVerdicts`.
- **Cached verdict is reused** — a cursor already holding a fresh `OSVVerdictLegitimate` for the ID produces zero `CheckWithdrawn` calls.
- **Context cancellation** — a cancelled context returns `""` from `Scan` without panicking.
- **Metrics** — in `pkg/metrics_test.go` (append a `Describe`, do not rewrite the file) assert `github_update_go_watcher_ignore_scan_total` is registered and that all six `IgnoreScanResults` series exist at 0 before any increment.
- **Wiring** — a spec asserting `pkg.NewWatcher(...)` accepts the two new arguments and that a full `Poll` runs the pass: with a stale finding set up on the mocks, `Poll` publishes exactly one stale-ignore task and increments `poll_cycle_total{result="success"}`.

Also update the three existing `pkg.NewWatcher(` call sites in `pkg/watcher_test.go` to pass `&mocks.OSVClient{}` and the allowlist slice already in scope. The pass now runs inside every existing `Poll` test: `mocks.GitHubClient.GetFile` returns `(nil, nil)` by default and `mocks.OSVClient.CheckWithdrawn` returns `("", nil)`, which the pass treats as unresolved, so no existing test emits anything new. If an existing spec fails, fix its fixture (for example point its allowlist away from the repo, or set the scan timestamp), never weaken its assertion.

### 12. Self-check before finishing

Re-run `<verification>` and confirm every command passes. Then walk the acceptance criteria: no emission for legitimate-only ignores, exactly one emission for one stale ID with the frozen envelope and a stamped target vault, no emission on an unchanged set before the interval and a NEW identifier after it, both throttles binding, an ineligible repo still advancing its timestamp, a dropped repo not advancing it, and unresolved never emitting.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add any dependency. The pass uses `sort`, `strings`, `time`, `github.com/bborbe/errors`, `github.com/golang/glog`, `github.com/bborbe/time`, `github.com/google/uuid` and `github.com/prometheus/client_golang/prometheus` — all already direct deps.
- Do NOT change the Go-version-behind path, its filter chain, its cursor semantics, or `Poll`'s schedule. The pass is an ADDITION: `processRepos`, `completeMergedUpdates`, the emission claim and the open-PR gate are untouched.
- Do NOT add a new `task_type`, assignee, topic, Kafka command or controller lane. The emitted task reuses `task_type: github-update-go` and `assignee: github-update-go-agent`.
- Do NOT close or supersede an earlier stale-ignore task. A changed stale set files a new task; reconciling duplicates on the board is out of scope.
- Do NOT scan a fourth ignore surface (`TRIVY_IGNORE` in the `bborbe/coding` templates) — it lives in another repository and no repo-level scan can see it.
- Do NOT triage or fix firing (non-ignored) vulnerabilities; that is `/github-update-go-vuln-watcher`'s role.
- Do NOT add an env knob, a dry-run switch or a per-cycle EMIT cap. The two throttles here bound upstream READS only; emission stays uncapped and dedup-absorbed.
- Do NOT emit for a repo whose consent is not `filter.GrantedConsent`.
- No new env var, chart value, PVC, topic or Config lane — all four durations/caps are package constants.
- The pass must never return an error that aborts the cycle's earlier work: its failures are counted and logged, and the cycle completes (a GitHub rate limit aborts the pass and the cycle counts `rate_limited`, exactly as the other passes do).
- The OSV client keeps its own plain `http.Client` with a 10s timeout, never the GitHub App-authenticated client.
- Cursor compatibility: new `RepoState` fields are `omitempty`; a cursor written by the previous binary loads and cold-starts. A rollback erases them — that is accepted and costs one rescan, not correctness.
- No vault, task-store or filesystem read in the pass: it reads GitHub and OSV only. No `exec`, no shell, no `git` binary.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates it from the `//counterfeiter:generate` directives.
- Every new `.go` file starts with the BSD license header used by the existing files.
- Error handling follows `github.com/bborbe/errors`; time comes from the injected `libtime.CurrentDateTimeGetter`, never `time.Now()`.
- `docs/dod.md` is a durable contract: its "In-flight signal contract" section describes the Go-bump claim and must not be reworded here (the next prompt adds one sentence about the stale-ignore emission).
- Existing tests must still pass unchanged.
- All paths in this prompt are repo-relative.
</constraints>

<verification>
Run from the repo root. Every `make` call MUST pass `ROOTDIR=/workspace` — `.dark-factory.yaml` sets `workflow: worktree`, the container masks `.git`, and `Makefile.variables` derives `ROOTDIR` from `git rev-parse --show-toplevel`, so an unqualified `make` dies before running any target.

```
ROOTDIR=/workspace make precommit
```

Must exit 0 (ensure, format, generate, test, check, addlicense).

```
ROOTDIR=/workspace go test -mod=mod ./pkg/ -run TestSuite -count=1
```

Must exit 0.

```
ROOTDIR=/workspace go build -mod=mod ./hack/staleignore-exercise && echo EXERCISE_BUILDS
```

Must print `EXERCISE_BUILDS`.

```
grep -c 'maxIgnoreScansPerCycle = 10' pkg/staleignore.go
grep -c 'ignoreRescanInterval = 24 \* libtime.Hour' pkg/staleignore.go
grep -c 'staleReemitInterval = 7 \* libtime.Day' pkg/staleignore.go
```

Each must print exactly `1`.

```
grep -c 'staleIgnoreTaskIDNamespace' pkg/taskid.go
```

Must print at least `2` — the declaration and its use.

```
grep -cE 'ignore_scanned_at,omitempty|ignore_stale_ids,omitempty|ignore_stale_generation,omitempty|ignore_stale_emitted_at,omitempty' pkg/cursor.go
```

Must print exactly `4`.

```
grep -c 'ignore_scan_total' pkg/metrics.go
grep -c 'IncIgnoreScan' pkg/metrics.go
```

The first must print at least `1`; the second must print at least `3` (interface, struct method, call site in the pre-init or implementation).

```
grep -c 'func (w \*watcher) staleIgnores' pkg/watcher.go
grep -c 'w.staleIgnores(ctx, cursorState, repos, latestGo)' pkg/watcher.go
```

The first must print exactly `1` (the entry point exists); the second must print exactly `1` (Poll calls it once).

```
grep -c 'staleIgnorePass.Scan' pkg/watcher.go
```

Must print exactly `1` — the entry point delegates to the pass once.

```
grep -c 'CreateOSVHTTPClient' pkg/factory/factory.go main.go
```

Must print at least `2` — the definition and its one call site.

```
test "$(grep -c 'TargetVault' pkg/taskbuilder.go)" = "0" && echo NO_BUILDER_VAULT
```

Must print `NO_BUILDER_VAULT` — no builder sets `target_vault`; the typed sender stamps it at send time.

```
! grep -q 'time.Now()' pkg/staleignore.go pkg/cursor.go && echo NO_SYSTEM_CLOCK
! grep -q 'fmt.Errorf' pkg/staleignore.go pkg/taskbuilder.go pkg/taskid.go && echo NO_FMT_ERRORF
! grep -q '/Users/' pkg/staleignore_test.go hack/staleignore-exercise/main.go && echo NO_ABSOLUTE_PATHS
```

Each line must print its marker.

```
test -f hack/staleignore-exercise/main.go && echo EXERCISE_PRESENT
```

Must print `EXERCISE_PRESENT`.

```
ROOTDIR=/workspace go vet -mod=mod ./... && echo VET_OK
```

Must print `VET_OK`.
</verification>
