---
status: completed
spec: [001-go-update-task-watcher]
summary: Added cycle orchestrator (pkg/watcher.go), per-repo Candidate type, frozen emit contract (pkg/taskbuilder.go), TaskPublisher (pkg/taskpublisher.go), GitHubClient interface (pkg/githubclient.go), and comprehensive tests covering all AC5-AC12 requirements
execution_id: github-update-go-watcher-goupdate-exec-003-spec-001-cycle-and-emit
dark-factory-version: dev
created: "2026-08-16T12:53:45Z"
queued: "2026-08-16T13:15:33Z"
started: "2026-08-16T13:21:45Z"
completed: "2026-08-16T13:44:45Z"
---

<summary>
- Ties everything together into one scan cycle: look up the current stable Go once, list the owner's repos, examine each, and file a work item for each repo that qualifies.
- Freezes the work item's exact shape — twelve fields and a four-line body — byte-for-byte against what the consuming agent already accepts in production.
- Whole-cycle problems (repo listing failure, rate limiting, an unusable stable-Go lookup) abort the cycle and record no progress, so the next cycle retries from exactly the same state.
- A problem with one repo drops only that repo and names it in the log; the rest of the cycle keeps running and keeps publishing.
- A publish failure for one repo does not record that repo as reported, so the next cycle re-files it; downstream deduplication makes a duplicate harmless.
- After a successful cycle the watcher records each reported repo's commit, so an unchanged repo is silently skipped next time.
- A forced cycle re-examines repos whose commit has not moved, but every other gate still applies.
</summary>

<objective>
Add the cycle orchestrator (`pkg/watcher.go`), the per-repo observation type, and the emit path (`pkg/taskbuilder.go`, `pkg/taskpublisher.go`) that publishes exactly one `CreateTaskCommand` per qualifying repo with the frozen field-and-body contract. This is the only place the frozen contract is assembled.
</objective>

<context>
Read `docs/dod.md`.

Read these coding plugin docs before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-service-implementation-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`

Read these repo files before writing code (earlier prompts created them):
- `pkg/repo.go` — `Repo{Owner, Name, DefaultBranch}`, `Key()` → `"github.com/<owner>/<name>"`, `String()` → `"<owner>/<name>"`.
- `pkg/version.go` — `Version{Major, Minor, Patch, Raw}`, `ParseGoDirective`, `Compare`, `Less`, `String()`, `Number()` (three-part, no `go` prefix).
- `pkg/gomod.go` — `ParseGoModVersion(ctx, content []byte) (Version, error)`.
- `pkg/godevclient.go` — `GoDevClient.LatestStable(ctx) (Version, error)`.
- `pkg/githubclient.go` — `GitHubClient` with `ListRepos`, `GetHeadSHA`, `GetGoMod` (returns `(nil, nil)` when absent), `GetMaintainerConfig`; sentinel `ErrRateLimited`.
- `pkg/metrics.go` — `Metrics` with `IncPollCycle`, `IncPublished`, `IncReposScanned`, `IncFilterSkipped`; label slices `PollCycleResults`, `PublishStatuses`, `FilterSkipReasons`.
- `pkg/cursor.go` — `Cursor{Repos map[string]*RepoState}`, `RepoState{LastSeenHeadSHA string}`, `LoadCursor`, `SaveCursor`, `DefaultCursorPath`.
- `pkg/cursorreader.go` — `NewCursorReader(*Cursor) filter.CursorReader`.
- `pkg/filter/` — `Candidate{RepoKey, HeadSHA, GoModPresent, GoModParsable, GoBehind, AutoUpdate}`, `TaskCreationFilter`, `TaskCreationFilters`, `NewSHAUnchangedFilter(CursorReader)`.
- `pkg/pkg_suite_test.go`, `go.mod`, `Makefile.precommit`, `.golangci.yml`.

Library API facts (verified — do not re-derive from memory):
- `github.com/bborbe/agent` at v0.72.0 (import as `agentlib "github.com/bborbe/agent"`) declares:
  ```go
  type TaskFrontmatter map[string]interface{}
  type TaskIdentifier string
  ```
- `github.com/bborbe/agent/command/task` (import as `task "github.com/bborbe/agent/command/task"`) declares:
  ```go
  type CreateCommand struct {
      TaskIdentifier lib.TaskIdentifier  `json:"taskIdentifier"`
      Title          string              `json:"title"`
      Frontmatter    lib.TaskFrontmatter `json:"frontmatter"`
      Body           string              `json:"body,omitempty"`
      TargetVault    string              `json:"targetVault,omitempty"`
  }
  // Validate rejects: empty title or >200 runes; any of < > : " / \ | ? *
  // in the title; control characters; leading/trailing space or dot; ".."; a
  // Windows-reserved name; body over 500 KiB; a TargetVault that is non-empty
  // and does not match ^[a-z][a-z0-9-]*$.
  func (cmd CreateCommand) Validate(ctx context.Context) error

  type CreateCommandSender interface {
      SendCommand(ctx context.Context, cmd CreateCommand) error
  }
  ```
- `github.com/bborbe/maintainer/maintainerconfig` — `MaintainerConfig.GoUpdate.AutoUpdate bool`.
- `github.com/bborbe/errors` — `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, ...)`. Never `fmt.Errorf`.
- `stderrors "errors"` + `stderrors.Is(err, pkg.ErrRateLimited)` for the rate-limit sentinel check.
</context>

<requirements>

### 1. Add the agent dependency

```
go get github.com/bborbe/agent@v0.72.0
```

`github.com/bborbe/cqrs` arrives transitively; do not add it explicitly in this prompt (prompt 5 wires the Kafka sender and will promote it if `go mod tidy` requires).

### 2. `pkg/candidate.go` — the per-repo observation

Package `pkg`.

```go
// Candidate is the watcher's per-repo observation: everything needed to
// (a) decide whether to file a work item and (b) populate the emitted message.
//
// Built per cycle by the Watcher in this order, so partial failures degrade
// gracefully:
//  1. Repo         (from ListRepos)
//  2. HeadSHA      (from GetHeadSHA)
//  3. GoModPresent / GoModParsable / CurrentGo (from GetGoMod + ParseGoModVersion)
//  4. AutoUpdate   (from GetMaintainerConfig — false when .maintainer.yaml is absent)
//  5. LatestGo     (resolved once per cycle, identical across every Candidate)
type Candidate struct {
	Repo          Repo
	HeadSHA       string  // full SHA of the default branch HEAD
	GoModPresent  bool    // false when the repo has no go.mod
	GoModParsable bool    // false when go.mod exists but has no readable go directive
	CurrentGo     Version // zero value unless GoModParsable
	LatestGo      Version // current stable, resolved once per cycle
	AutoUpdate    bool    // .maintainer.yaml: goUpdate.autoUpdate — the consent gate
}

// ShortSHA returns the first 7 chars of HeadSHA, used in the title and body.
func (c Candidate) ShortSHA() string {
	if len(c.HeadSHA) < 7 {
		return c.HeadSHA
	}
	return c.HeadSHA[:7]
}

// GoBehind reports whether the declared Go version is strictly behind current
// stable. Equal-to and ahead-of both report false ("nothing to do").
func (c Candidate) GoBehind() bool {
	return c.GoModParsable && c.CurrentGo.Less(c.LatestGo)
}

// FilterCandidate projects this observation onto the filter package's input.
func (c Candidate) FilterCandidate() filter.Candidate {
	return filter.Candidate{
		RepoKey:       c.Repo.Key(),
		HeadSHA:       c.HeadSHA,
		GoModPresent:  c.GoModPresent,
		GoModParsable: c.GoModParsable,
		GoBehind:      c.GoBehind(),
		AutoUpdate:    c.AutoUpdate,
	}
}
```

### 3. `pkg/taskbuilder.go` — the FROZEN emit contract

Package `pkg`. **Every literal in this section is frozen against a shipped consumer. Changing any of it breaks the `github-update-go-agent`.**

```go
// TaskConfig groups per-task envelope settings.
type TaskConfig struct {
	Stage string // "dev" or "prod" — emitted as the `stage` field
}

// ComputeTaskTitle returns the frozen title form:
// "Update Go <owner>-<repo> <sha[:7]>".
//
// Dash, not slash-and-"at". CreateCommand.Validate rejects any '/' in a
// title, and SendCommand validates before publishing — a slash form would
// make every publish fail. The Stage-1 prototype's vault artifacts show a
// slash form in their frontmatter `title` field, but those artifacts were
// written directly by the prototype and never passed through this
// validator; the vault filenames it produced (which the production
// controller derives verbatim from `title`) already use the dash form,
// confirming the dash form is what the real contract requires.
func ComputeTaskTitle(c Candidate) string {
	return fmt.Sprintf("Update Go %s-%s %s", c.Repo.Owner, c.Repo.Name, c.ShortSHA())
}

// BuildCreateCommand assembles the CreateTaskCommand for a Candidate.
func BuildCreateCommand(c Candidate, cfg TaskConfig) task.CreateCommand {
	taskIDStr := DeriveTaskID(c.Repo.Owner, c.Repo.Name, c.HeadSHA).String()
	return task.CreateCommand{
		Title:          ComputeTaskTitle(c),
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildFrontmatter(c, taskIDStr, cfg),
		Body:           buildTaskBody(c),
	}
}
```

`buildFrontmatter` returns EXACTLY these twelve keys and no others:

```go
func buildFrontmatter(c Candidate, taskIDStr string, cfg TaskConfig) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "github-update-go",
		"assignee":        "github-update-go-agent",
		"phase":           "planning",
		"status":          "in_progress",
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           ComputeTaskTitle(c),
		"repo":            c.Repo.String(),
		"clone_url": fmt.Sprintf(
			"git@github.com:%s/%s.git",
			c.Repo.Owner,
			c.Repo.Name,
		),
		"ref":        c.HeadSHA,
		"current_go": c.CurrentGo.Number(),
		"latest_go":  c.LatestGo.Number(),
	}
}
```

The assignee is `github-update-go-agent`. It is NOT `go-update-agent` — that agent does not exist and the task controller silently drops unknown assignees.

`buildTaskBody` returns the frozen four-line block. The separator between the two Go versions is a middot `·` (U+00B7) with **two spaces on each side**:

```go
func buildTaskBody(c Candidate) string {
	owner := c.Repo.Owner
	name := c.Repo.Name
	return fmt.Sprintf(
		"# Update Go: %s/%s\n\n"+
			"**Current Go:** %s  ·  **Latest Go:** %s\n"+
			"**HEAD:** %s\n"+
			"**Repo:** [%s/%s](https://github.com/%s/%s)\n",
		owner, name,
		c.CurrentGo.Number(), c.LatestGo.Number(),
		c.ShortSHA(),
		owner, name, owner, name,
	)
}
```

Rendered for `bborbe/disk-status`, current `1.24.0`, latest `1.26.6`, HEAD `d630ef3…` (note: the title is `Update Go bborbe-disk-status d630ef3`, dash form; only the body's markdown heading and link text use the slash form `bborbe/disk-status`):

```markdown
# Update Go: bborbe/disk-status

**Current Go:** 1.24.0  ·  **Latest Go:** 1.26.6
**HEAD:** d630ef3
**Repo:** [bborbe/disk-status](https://github.com/bborbe/disk-status)
```

(The body ends with a single trailing newline after the `**Repo:**` line, matching the sibling watcher's convention.)

The body is an operator-readable header only. Never embed dependency data, vulnerability data, a diff, or any bytes read from the observed repo's files.

Do NOT set `CreateCommand.TargetVault` and do NOT add a `TARGET_VAULT` env knob — the spec's frozen contract table lists exactly twelve fields and does not include it. An empty `TargetVault` is valid (the validator explicitly permits `""`) and means the task controller uses its legacy default vault. Assert `cmd.TargetVault == ""` in the frozen-contract test in section 6. (If the deployment's target vault is not the controller's legacy default, that is a rollout decision for the spec/deployment step, not something this prompt should work around.)

### 4. `pkg/taskpublisher.go` — publish + counter

Package `pkg`.

```go
//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher builds the CreateTaskCommand for a Candidate and sends it via
// the supplied CreateCommandSender. Returns true only on a successful send —
// the caller records the repo's HEAD in the cursor only on true.
type TaskPublisher interface {
	PublishCreate(ctx context.Context, candidate Candidate) bool
}

// NewTaskPublisher returns a TaskPublisher wrapping the given sender + metrics.
func NewTaskPublisher(
	sender task.CreateCommandSender,
	metrics Metrics,
	cfg TaskConfig,
) TaskPublisher
```

`PublishCreate`:
- `cmd := BuildCreateCommand(candidate, p.cfg)`.
- On `p.sender.SendCommand(ctx, cmd)` error: `glog.Errorf("publish create-task failed repo=%s sha=%s taskID=%s err=%v", candidate.Repo.Key(), candidate.HeadSHA, string(cmd.TaskIdentifier), err)`, then `p.metrics.IncPublished("error")`, return `false`.
- On success: `glog.V(2).Infof("published CreateTaskCommand repo=%s sha=%s taskID=%s stage=%s", ...)`, then `p.metrics.IncPublished("create")`, return `true`.

### 5. `pkg/watcher.go` — cycle orchestration

Package `pkg`. Imports `stderrors "errors"` for the sentinel check.

```go
//counterfeiter:generate -o ../mocks/watcher.go --fake-name Watcher . Watcher

// Watcher scans one GitHub owner for repos whose declared Go version is behind
// current stable and publishes one CreateTaskCommand per qualifying repo.
type Watcher interface {
	// Poll runs one scan cycle. Safe to call repeatedly on an interval.
	//
	// force=true omits SHAUnchangedFilter from this cycle's chain, so repos
	// whose HEAD has not moved are re-evaluated. Every other gate (allowlist,
	// go.mod presence, parsability, version comparison, opt-in consent) still
	// applies. The interval loop always passes false.
	Poll(ctx context.Context, force bool) error
}

// NewWatcher wires the cycle's collaborators.
//
// owner is a single GitHub org per watcher instance (multi-org = multiple
// deployments). taskCreationFilter is the cycle-invariant chain built at
// wiring time; SHAUnchangedFilter is composed in per cycle because it needs a
// fresh CursorReader.
func NewWatcher(
	ghClient GitHubClient,
	goDevClient GoDevClient,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	owner string,
	taskCreationFilter filter.TaskCreationFilter,
) Watcher
```

**`Poll` — exact order and failure policy:**

1. `cursorState, err := LoadCursor(ctx, w.cursorPath)`. On error **return** `errors.Wrapf(ctx, err, "load cursor path=%s", w.cursorPath)` — a corrupt memory file makes the cycle refuse to run. Do NOT bump any poll-cycle counter for this case and publish nothing.
2. `latestGo, err := w.goDevClient.LatestStable(ctx)`. On error: `w.metrics.IncPollCycle("go_version_error")`, `glog.Errorf("poll cycle aborted: stable go lookup failed err=%v", err)`, `return nil`. This runs **before** `ListRepos`, so a bad stable-Go response aborts before any repo is evaluated and before any GitHub call is made.
3. `repos, err := w.ghClient.ListRepos(ctx, w.owner)`. On error: `stderrors.Is(err, ErrRateLimited)` → `IncPollCycle("rate_limited")` + `glog.Warningf("poll cycle aborted: rate limited during ListRepos owner=%s", w.owner)`; otherwise `IncPollCycle("github_error")` + `glog.Warningf("poll cycle aborted: ListRepos owner=%s err=%v", w.owner, err)`. Either way `return nil` without saving the cursor.
4. `w.metrics.IncReposScanned(len(repos))`.
5. Compose the cycle chain:
   ```go
   cycleFilter := filter.TaskCreationFilters{w.taskCreationFilter}
   if !force {
       cycleFilter = append(
           cycleFilter,
           filter.NewSHAUnchangedFilter(NewCursorReader(cursorState)),
       )
   }
   ```
6. `abortReason := w.processRepos(ctx, cursorState, repos, cycleFilter, latestGo)`. If non-empty: `w.metrics.IncPollCycle(abortReason)` and `return nil` **without saving the cursor** — the next cycle resumes from identical state.
7. `if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil { glog.Warningf("save cursor failed path=%s err=%v", w.cursorPath, err) }` — a save failure after publishing is best-effort: the publishes stand and the cycle still counts as success.
8. `w.metrics.IncPollCycle("success")`, `return nil`.

**`processRepos(ctx, cursorState *Cursor, repos []Repo, cycleFilter filter.TaskCreationFilter, latestGo Version) string`:**

- Iterate `repos` sequentially.
- At the top of each iteration:
  ```go
  select {
  case <-ctx.Done():
      glog.V(2).Infof("poll cancelled during processRepos at repo=%s", repo.Key())
      return ""
  default:
  }
  ```
- `candidate, abortReason, dropped := w.gatherCandidate(ctx, repo, latestGo)`. Non-empty `abortReason` → return it immediately. `dropped` → `continue`.
- `if reason := cycleFilter.Skip(candidate.FilterCandidate()); reason != "" { w.metrics.IncFilterSkipped(reason); glog.V(2).Infof("repo skipped repo=%s reason=%s", repo.Key(), reason); continue }`.
- `if w.publisher.PublishCreate(ctx, candidate) { if cursorState.Repos == nil { cursorState.Repos = make(map[string]*RepoState) }; cursorState.Repos[repo.Key()] = &RepoState{LastSeenHeadSHA: candidate.HeadSHA} }`. A failed publish leaves the cursor untouched for that repo, so the next cycle re-publishes.
- Return `""` when the loop completes.

**`gatherCandidate(ctx, repo Repo, latestGo Version) (Candidate, string, bool)`** — returns `(candidate, "", false)` on success, `(Candidate{}, "rate_limited", false)` when the whole cycle must abort, `(Candidate{}, "", true)` when only this repo is dropped.

Extract a small unexported helper so `gatherCandidate` stays under the 80-line `funlen` cap:

```go
// dropRepo logs the always-on per-repo drop line. The phrase
// "repo dropped from cycle" is the operator's grep handle — do not reword it.
// Warning level (not V(2)) so the line is visible at default verbosity.
func dropRepo(repo Repo, step string, err error) (Candidate, string, bool) {
	glog.Warningf(
		"repo dropped from cycle: owner=%s repo=%s step=%s err=%v",
		repo.Owner, repo.Name, step, err,
	)
	return Candidate{}, "", true
}
```

Body of `gatherCandidate`:
1. `headSHA, err := w.ghClient.GetHeadSHA(ctx, repo)` — rate limit → `return Candidate{}, "rate_limited", false`; other error → `return dropRepo(repo, "head_sha", err)`.
2. `goModContent, err := w.ghClient.GetGoMod(ctx, repo)` — rate limit → abort; other error → `return dropRepo(repo, "go_mod", err)`.
3. `cfg, err := w.ghClient.GetMaintainerConfig(ctx, repo)` — rate limit → abort; other error → `return dropRepo(repo, "maintainer_config", err)`. An unparsable `.maintainer.yaml` lands here: it is a DROP, never a consent verdict, so `filter_skipped_total{reason="auto_update_disabled"}` must not move for it.
4. Build the candidate:
   ```go
   candidate := Candidate{
       Repo:       repo,
       HeadSHA:    headSHA,
       LatestGo:   latestGo,
       AutoUpdate: cfg.GoUpdate.AutoUpdate,
   }
   if goModContent != nil {
       candidate.GoModPresent = true
       currentGo, perr := ParseGoModVersion(ctx, goModContent)
       if perr == nil {
           candidate.GoModParsable = true
           candidate.CurrentGo = currentGo
       } else {
           glog.V(2).Infof("go.mod unparsable repo=%s err=%v", repo.Key(), perr)
       }
   }
   return candidate, "", false
   ```
   A missing `go.mod` and an unparsable go directive are NOT drops — they flow into the chain and resolve to `no_gomod` / `gomod_unparsable`.

### 6. Tests

All in `package pkg_test`, Ginkgo/Gomega, using the counterfeiter fakes `mocks.GitHubClient`, `mocks.GoDevClient`, `mocks.Metrics`, `mocks.TaskPublisher`, `mocks.TaskCreationFilter`.

**`pkg/taskbuilder_test.go` — the frozen contract (spec AC 2, 3, 4):**
- Build a fixture `Candidate` for `bborbe/disk-status`, HEAD `d630ef3526cfc57fbdccd9ba53c5c3a02945e407`, current `1.24` (two-part on purpose), latest `1.26.6`, `TaskConfig{Stage: "dev"}`.
- Assert each of the twelve frontmatter keys individually with its exact value, including `"current_go" == "1.24.0"` (two-part normalised) and `"latest_go" == "1.26.6"`.
- Assert `len(cmd.Frontmatter) == 12` so an extra key fails the test.
- Assert `cmd.Title == "Update Go bborbe-disk-status d630ef3"` and `cmd.Frontmatter["title"] == cmd.Title`.
- Assert `cmd.Body` equals a golden string literal declared in the test file, written out byte-for-byte including the two spaces either side of `·`. Declare the golden as an interpreted string literal using the explicit escape `"·"` for the middot (not a raw literal with the glyph pasted in) so the assertion cannot pass on a visually similar character (e.g. U+2022 or U+22C5) accidentally typed into either the golden or the production code.
- Assert `string(cmd.TaskIdentifier) == pkg.DeriveTaskID("bborbe", "disk-status", "<sha>").String()`.
- Assert `cmd.Validate(ctx)` returns nil — this traverses the real library validator (title length, body, target vault) that the production publish path runs, so a contract change that the library rejects fails here rather than at runtime.
- Assert the body contains no substring from the observed repo's raw files (sanity: assert `cmd.Body` does not contain `"module "` or `"require"`).

**`pkg/taskpublisher_test.go`:**
- Declare a hand-written `fakeCreateCommandSender` in the test file implementing `task.CreateCommandSender` with a captured-commands slice and a configurable error (counterfeiter does not generate mocks for third-party interfaces here).
- Success → returns true, one captured command, `metrics.IncPublishedArgsForCall(0)` is `"create"`.
- Send error → returns false, `IncPublished("error")`.

**`pkg/watcher_test.go` — the cycle:**

Set up a helper that builds a `Watcher` over the fakes with a cursor path in `GinkgoT().TempDir()`.

- **AC 5 (consent-gate table)**: allowlisted repo, behind stable, over `{maintainer file absent, goUpdate section absent, autoUpdate key absent, autoUpdate false, autoUpdate true}`. Only the `true` row publishes (`publisher.PublishCreateCallCount() == 1`); the other four each yield `publisher.PublishCreateCallCount() == 0` and `metrics.IncFilterSkippedArgsForCall(0) == "auto_update_disabled"`. Model the first four rows by returning the matching zero-value / parsed `maintainerconfig.MaintainerConfig` from the fake `GetMaintainerConfig`.
- **AC 6 (version table)**: one row each for — two-part directive `go 1.24` behind stable → publishes and the built command carries `current_go == "1.24.0"`; directive equal to stable → `"go_current"`; directive ahead of stable → `"go_current"`; `GetGoMod` returns `(nil, nil)` → `"no_gomod"`; `go banana` → `"gomod_unparsable"`. Assert the returned skip reason via `metrics.IncFilterSkippedArgsForCall(0)` and the publish call count per row.
- **AC 7**: a cycle over three repos calls `goDevClient.LatestStableCallCount()` exactly 1.
- **AC 8**: `goDevClient.LatestStableReturns(pkg.Version{}, errors.New("banana"))` → `publisher.PublishCreateCallCount() == 0`, `ghClient.ListReposCallCount() == 0`, and `metrics.IncPollCycleArgsForCall(0) == "go_version_error"`.
- **AC 9**: seed a cursor file, record its bytes, make `GetHeadSHA` return `pkg.ErrRateLimited` for the first repo, run `Poll`, then assert the file bytes are byte-identical and `metrics.IncPollCycleArgsForCall(0) == "rate_limited"`. Repeat for a rate limit raised by `ListRepos` and by `GetMaintainerConfig`.
- **AC 10**: two repos where `GetHeadSHA` errors (non-rate-limit) for the first and succeeds for the second; assert the second repo is published and that captured log output contains `repo dropped from cycle` naming the first repo. Capture glog output with:
  ```go
  // glog resolves os.Stderr at write time, so swapping it captures the line.
  Expect(flag.Set("logtostderr", "true")).To(Succeed())
  old := os.Stderr
  r, w, err := os.Pipe()
  Expect(err).NotTo(HaveOccurred())
  os.Stderr = w
  // ... run Poll ...
  glog.Flush()
  os.Stderr = old
  Expect(w.Close()).To(Succeed())
  data, err := io.ReadAll(r)
  Expect(err).NotTo(HaveOccurred())
  Expect(string(data)).To(ContainSubstring("repo dropped from cycle"))
  ```
- **AC 11**: `GetMaintainerConfig` returns a parse error for the only repo → `publisher.PublishCreateCallCount() == 0` AND `metrics.IncFilterSkippedCallCount() == 0` (no `auto_update_disabled` verdict was recorded).
- **AC 12**: a qualifying repo publishes; the cursor file afterwards contains that repo key with the HEAD SHA; a second `Poll(ctx, false)` with the same HEAD publishes nothing and reports `sha_unchanged`; the persistence directory listing contains no `*.tmp` entry.
- **Forced cycle**: with the cursor already recording the HEAD, `Poll(ctx, true)` publishes; `Poll(ctx, false)` does not. Also assert a forced cycle still honours the consent gate: a not-opted-in repo publishes nothing on `Poll(ctx, true)` and reports `auto_update_disabled`.
- **Corrupt cursor**: write `not json` to the cursor path → `Poll` returns a non-nil error, `publisher.PublishCreateCallCount() == 0`, `metrics.IncPollCycleCallCount() == 0`.
- **Publish failure**: `publisher.PublishCreateReturns(false)` for a qualifying repo → the cursor file records nothing for that repo, and the cycle still ends with `IncPollCycle("success")`.
- **Cancellation**: run over at least three repos, cancel the context after the first repo has been fully processed (e.g. via a fake that cancels on its first call), and assert strictly fewer publishes than repos — not merely a nonzero gap on an already-trivial fixture. A cancelled cycle still falls through to `SaveCursor` and `IncPollCycle("success")` per step 6-8 above; do not invent a `"cancelled"` label — it is not a member of `PollCycleResults`.
- **Metric label containment**: collect every value passed to `metrics.IncPollCycle` and `metrics.IncFilterSkipped` across the whole test file's scenarios and assert membership in `pkg.PollCycleResults` / `pkg.FilterSkipReasons`.

### 7. Regenerate mocks and verify

Run `make precommit` from the repo root. Confirm `mocks/task_publisher.go` and `mocks/watcher.go` exist afterwards.

</requirements>

<constraints>
- **The emit contract is frozen.** The twelve field keys, their values, the title form `Update Go <owner>-<repo> <sha[:7]>` (dash, not slash — `CreateCommand.Validate` rejects `/`), and the four-line body (two spaces on each side of the U+00B7 middot, and the body itself still uses the slash form `<owner>/<repo>` in its heading and link text) match a shipped consumer. Do not add, remove, rename, or reorder a field. Do not reflow the body.
- `assignee` is `github-update-go-agent`. Never `go-update-agent`.
- The body is an operator-readable header only — never embed dependency, vulnerability, or diff data, and never raw bytes read from an observed repo.
- Do NOT add a per-cycle emit cap, throttle, or dry-run switch — spec Non-goal, invariant.
- Do NOT add an env flag or code path that disables the opt-in gate or the allowlist — spec Non-goal.
- Do NOT add a configurable source URL for the stable-Go lookup — spec Non-goal. The `GoDevClient` interface is injected; the URL stays a `const`.
- Do NOT add Prometheus metrics beyond the four already defined in `pkg/metrics.go`, and do not add label values outside `PollCycleResults` / `PublishStatuses` / `FilterSkipReasons`.
- This service only observes and reports: no clone, no checkout, no commit, no push, no tag, no PR against any observed repository. No `os/exec`, no `exec.Command`, no `go-git` anywhere.
- Whole-cycle abort paths (`go_version_error`, `rate_limited`, `github_error`) must NOT save the cursor. Per-repo failures must NOT abort the cycle.
- A corrupt cursor file makes `Poll` return an error and publish nothing — never a guessed-empty cursor.
- The log phrase `repo dropped from cycle` is the operator's grep handle. Emit it at `glog.Warningf` (always-on) and do not reword it.
- Never use `fmt.Errorf`. All errors go through `github.com/bborbe/errors` and carry `ctx`.
- Do NOT touch `main.go`, `main_test.go`, `pkg/factory/`, `pkg/handler/`, `pkg/mathutil/`, `k8s/`, or `README.md` in this prompt — prompt 5 owns the binary and docs.
- Never hand-edit anything under `mocks/`.
- Keep every line under 100 characters and every function under 80 lines / 50 statements (`funlen`); extract helpers rather than adding `//nolint`.
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
grep -rn "github-update-go-agent" --include=*.go .
```
Expect at least one match.

```
grep -rn "go-update-agent" --include=*.go . | grep -v "github-update-go-agent"
```
Expect zero matches.

```
grep -rn "os/exec\|go-git\|exec.Command" --include=*.go .
```
Expect zero matches.

```
grep -n "repo dropped from cycle" pkg/watcher.go
grep -n "task_type" pkg/taskbuilder.go
```
Each expects at least one line.

```
ls mocks/task_publisher.go mocks/watcher.go
```
Both must exist after `make precommit`.

```
go test -mod=mod ./pkg/...
```
Must exit 0.
</verification>
