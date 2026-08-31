---
status: completed
spec: [002-consent-tristate-undecided]
summary: 'Added decision-task pathway: repo-keyed DeriveDecisionTaskID, BuildDecisionCommand/10-key frontmatter, TaskPublisher.PublishDecision, and watcher skip-with-emit on auto_update_undecided, with full test coverage and CHANGELOG entry'
execution_id: github-update-go-watcher-tristate-exec-009-spec-002-decision-task-emit
dark-factory-version: dev
created: "2026-08-31T10:33:16Z"
queued: "2026-08-31T11:22:11Z"
started: "2026-08-31T11:39:54Z"
completed: "2026-08-31T11:47:02Z"
---

<summary>

- When a repo's consent is undecided, the watcher now files a human-facing decision task instead of just silently skipping the repo every cycle.
- The decision task tells the repo owner exactly what to add to `.maintainer.yaml` to answer either way, and is addressed to a fixed human reviewer (`bborbe`), never to the automated update agent.
- The decision task's identity is keyed to the repo itself, not to any particular commit — a repo that keeps getting new commits while still undecided does not spawn a new decision task every cycle; the same one is simply re-emitted, which the downstream task system already treats as a no-op.
- Filing the decision task never blocks or replaces the existing skip: the repo is still correctly counted and logged as skipped for the `auto_update_undecided` reason, and no update task is ever filed for it.
- If publishing the decision task itself fails (e.g. a downstream outage), the repo simply stays undecided and the same decision task is attempted again next cycle — no crash, no partial state, no new suppression bookkeeping.
- New tests prove the decision task's title, identity, frontmatter (task type, assignee, no HEAD-SHA dependency), and body content end to end, plus the full watcher-level pathway: an undecided repo produces exactly one decision-task publish and zero update-task publishes.

</summary>

<objective>

Introduce a decision-task shape (title, frontmatter, body, deterministic repo-keyed UUID identity) parallel to the existing update-task shape, add `TaskPublisher.PublishDecision`, and wire the watcher's per-repo skip branch so that a `"auto_update_undecided"` skip additionally publishes exactly one decision task (skip-with-emit), while every other skip reason continues to only skip. This satisfies spec 002 Desired Behaviors 5, 6, 7, 8, 10.

</objective>

<context>

Read before editing:
- `pkg/taskid.go` — full current file (29 lines): `taskIDNamespace`, `DeriveTaskID(owner, repo, headSHA string) uuid.UUID` using `uuid.NewSHA1`/`uuid.MustParse` from `github.com/google/uuid`.
- `pkg/taskid_test.go` — full current file (32 lines), the `Describe("DeriveTaskID", ...)` pattern to mirror.
- `pkg/taskbuilder.go` — full current file (100 lines): `TaskConfig`, `ComputeTaskTitle`, `BuildCreateCommand`, `buildFrontmatter` (12 keys, 13th `update_scope` conditional), `buildTaskBody`. Uses `agentlib "github.com/bborbe/agent"` and `"github.com/bborbe/agent/command/task"`.
- `pkg/taskbuilder_test.go` — the candidate fixture and `goldenBody` at the top of the file, and the existing `Describe("ComputeTaskTitle", ...)` / `Describe("BuildCreateCommand", ...)` blocks to mirror the structure of.
- `pkg/taskpublisher.go` — full current file (68 lines): `TaskPublisher` interface (currently one method, `PublishCreate`), `NewTaskPublisher`, the `taskPublisher` struct (`sender`, `metrics`, `cfg`), and `PublishCreate`'s exact implementation (build via `BuildCreateCommand`, `p.sender.SendCommand`, `glog.Errorf`/`IncPublished("error")` on failure, `glog.V(2).Infof`/`IncPublished("create")` on success).
- `pkg/taskpublisher_test.go` — the `fakeTaskPublisherSender` test double at the bottom of the file and the existing `Describe("PublishCreate", ...)` structure to mirror.
- `pkg/watcher.go` — `processRepos`'s skip block (inside the `for _, repo := range repos` loop, the `if reason := cycleFilter.Skip(...); reason != ""` branch).
- `pkg/watcher_test.go` — the `buildWatcher := func() {...}` helper near the top of the file, and any existing `Context` for its structural pattern (`ghClient.ListReposReturns`, `ghClient.GetHeadSHAReturns`, `ghClient.GetGoModReturns`, `ghClient.GetMaintainerConfigReturns`, `goDevClient.LatestStableReturns`, `metrics.IncFilterSkippedStub`).
- `pkg/candidate.go` — `Candidate` struct fields (`Repo`, `HeadSHA`, `CurrentGo`, `LatestGo`, `Consent`) used to build the decision command.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md` — counterfeiter mock regeneration conventions (`<Method>Stub`, `<Method>CallCount()`, `<Method>Returns(...)`, `<method>ArgsForCall`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega conventions in use throughout this repo.

This prompt assumes the two prior prompts in this sequence have already landed: `pkg.Candidate.Consent filter.Consent` exists, and `filter.NewAutoUpdateFilter()` returns `"auto_update_undecided"` for an undecided repo. If either is missing, stop and report — do not re-derive them here.

</context>

<requirements>

## 1. `pkg/taskid.go` — deterministic repo-keyed decision-task identity

Append, after `DeriveTaskID`:
```go
// decisionTaskIDNamespace is the UUID5 namespace for decision tasks.
// Frozen and deliberately distinct from taskIDNamespace — decision tasks
// and update tasks must never collide.
var decisionTaskIDNamespace = uuid.MustParse("8a96832c-8007-4d9a-922c-d9cdbdfeca89")

// DeriveDecisionTaskID returns a UUID5 derived deterministically from
// (owner, repo) only — deliberately excluding any HEAD SHA (spec 002
// Desired Behavior 7). A repo that receives new commits while its consent
// remains undecided must not produce a second decision task; the same
// identity is re-emitted every cycle until the repo owner records an
// answer in .maintainer.yaml, and that re-emit is a downstream no-op (spec
// 002 Desired Behavior 8), exactly mirroring the dedup contract
// DeriveTaskID documents for the SHA-keyed update-task identity above.
func DeriveDecisionTaskID(owner, repo string) uuid.UUID {
	seed := fmt.Sprintf("update-go-decision-%s-%s", owner, repo)
	return uuid.NewSHA1(decisionTaskIDNamespace, []byte(seed))
}
```

## 2. `pkg/taskid_test.go` — mirror the existing `DeriveTaskID` test structure

Append a new `Describe`, mirroring the structure of the existing `Describe("DeriveTaskID", ...)`:
```go
var _ = Describe("DeriveDecisionTaskID", func() {
	It("returns identical UUID for same owner/repo", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id1).To(Equal(id2))
	})

	It("returns different UUID for different repo", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "other-repo")
		Expect(id1).NotTo(Equal(id2))
	})

	It("returns different UUID for different owner", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("other-owner", "disk-status")
		Expect(id1).NotTo(Equal(id2))
	})

	It("returns VERSION_5 UUID", func() {
		id := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id.Version().String()).To(Equal("VERSION_5"))
	})

	It("is independent of HeadSHA (spec 002 Desired Behavior 7)", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id1).To(Equal(id2), "identity must not depend on any commit SHA")
	})
})
```

## 3. `pkg/taskbuilder.go` — decision-task builder functions

Append, after `buildTaskBody`, using exactly 10 frontmatter keys — deliberately fewer than the 12/13-key update-task shape: `clone_url`, `ref`, and `update_scope` are agent-consumption fields (cloning, pinning a commit, fleet scope) with no consumer for a human-facing decision task read via the vault by a fixed human assignee.
```go
// ComputeDecisionTaskTitle returns the frozen decision-task title form:
// "Go Update Decision <owner>-<repo>" — no HEAD SHA, matching the
// repo-keyed identity (spec 002 Desired Behavior 7).
func ComputeDecisionTaskTitle(c Candidate) string {
	return fmt.Sprintf("Go Update Decision %s-%s", c.Repo.Owner, c.Repo.Name)
}

// BuildDecisionCommand assembles the decision-task CreateTaskCommand for a
// Candidate whose consent is undecided (spec 002 Desired Behaviors 5-8, 10).
func BuildDecisionCommand(c Candidate, cfg TaskConfig) task.CreateCommand {
	taskIDStr := DeriveDecisionTaskID(c.Repo.Owner, c.Repo.Name).String()
	return task.CreateCommand{
		Title:          ComputeDecisionTaskTitle(c),
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildDecisionFrontmatter(c, taskIDStr, cfg),
		Body:           buildDecisionTaskBody(c),
	}
}

func buildDecisionFrontmatter(
	c Candidate,
	taskIDStr string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "github-update-go-decision",
		"assignee":        "bborbe",
		"phase":           "planning",
		"status":          "in_progress",
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           ComputeDecisionTaskTitle(c),
		"repo":            c.Repo.String(),
		"current_go":      c.CurrentGo.Number(),
		"latest_go":       c.LatestGo.Number(),
	}
}

func buildDecisionTaskBody(c Candidate) string {
	owner := c.Repo.Owner
	name := c.Repo.Name
	return fmt.Sprintf(
		"# Go Update Decision Needed: %s/%s\n\n"+
			"This repo's declared Go version is behind the latest stable release, "+
			"and nobody has recorded whether it should be updated automatically.\n\n"+
			"**Current Go:** %s  ·  **Latest Go:** %s\n"+
			"**Repo:** [%s/%s](https://github.com/%s/%s)\n\n"+
			"Add one of the following to `.maintainer.yaml` to answer:\n\n"+
			"```yaml\n"+
			"goUpdate:\n"+
			"  autoUpdate: true   # opt in — this repo starts receiving automatic Go bump PRs\n"+
			"```\n\n"+
			"or\n\n"+
			"```yaml\n"+
			"goUpdate:\n"+
			"  autoUpdate: false  # opt out — this repo stays silent going forward\n"+
			"```\n\n"+
			"Either answer makes this repo silent again; only an unanswered decision "+
			"re-files this task on the next scan.\n",
		owner, name,
		c.CurrentGo.Number(), c.LatestGo.Number(),
		owner, name, owner, name,
	)
}
```
No new imports are needed — `fmt`, `agentlib`, and `task` are already imported by this file.

## 4. `pkg/taskbuilder_test.go` — mirror the existing `ComputeTaskTitle`/`BuildCreateCommand` test structure

This file is a single outer `Describe("taskbuilder", func() { ... })` block with `var (ctx, candidate, cfg, goldenBody)` declared once and populated in one shared `BeforeEach`, and the existing `Describe("ComputeTaskTitle", ...)` / `Describe("BuildCreateCommand", ...)` blocks nested INSIDE it as siblings. Add the two new blocks below as ADDITIONAL nested `Describe` calls inside that same outer `Describe("taskbuilder", func() { ... })` block, immediately after the existing `Describe("BuildCreateCommand", ...)` block, so they reuse the outer `candidate`/`cfg`/`ctx` variables already in scope. Do NOT add them as new top-level `var _ = Describe(...)` statements — those would not have `candidate`/`cfg`/`ctx` in scope and would fail to compile.

```go
	Describe("ComputeDecisionTaskTitle", func() {
		It("returns the frozen decision-task title form", func() {
			title := pkg.ComputeDecisionTaskTitle(candidate)
			Expect(title).To(Equal("Go Update Decision bborbe-disk-status"))
		})
	})

	Describe("BuildDecisionCommand", func() {
		It("returns nil from Validate", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("sets task_type to github-update-go-decision", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Frontmatter["task_type"]).To(Equal("github-update-go-decision"))
		})

		It("sets assignee to bborbe, never the update agent", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Frontmatter["assignee"]).To(Equal("bborbe"))
			Expect(cmd.Frontmatter["assignee"]).NotTo(Equal("github-update-go-agent"))
		})

		It("has exactly 10 frontmatter keys", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(len(cmd.Frontmatter)).To(Equal(10))
		})

		It("derives task_identifier from owner/repo only, independent of HeadSHA", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			other := candidate
			other.HeadSHA = "0000000000000000000000000000000000000000"
			otherCmd := pkg.BuildDecisionCommand(other, cfg)
			Expect(otherCmd.TaskIdentifier).To(Equal(cmd.TaskIdentifier))
		})

		It("task_identifier does not contain the HeadSHA", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(string(cmd.TaskIdentifier)).NotTo(ContainSubstring(candidate.HeadSHA))
		})

		It("different repos yield different task_identifier", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			other := candidate
			other.Repo.Name = "other-repo"
			otherCmd := pkg.BuildDecisionCommand(other, cfg)
			Expect(otherCmd.TaskIdentifier).NotTo(Equal(cmd.TaskIdentifier))
		})

		It("body names the current and latest Go versions", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Body).To(ContainSubstring("1.24.0"))
			Expect(cmd.Body).To(ContainSubstring("1.26.6"))
		})

		It("body shows both opt-in and opt-out .maintainer.yaml answers", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Body).To(ContainSubstring("autoUpdate: true"))
			Expect(cmd.Body).To(ContainSubstring("autoUpdate: false"))
		})
	})
```
`task.CreateCommand.Validate` has a value receiver (`func (cmd CreateCommand) Validate(ctx context.Context) error`), so calling `.Validate(ctx)` directly on the value returned by `pkg.BuildDecisionCommand` needs no pointer and no extra import — `cmd := pkg.BuildDecisionCommand(...)` infers its type via `:=` exactly as the existing `BuildCreateCommand` tests already do inline for most of their assertions.

## 5. `pkg/taskpublisher.go` — `PublishDecision`

Change the interface from:
```go
type TaskPublisher interface {
	PublishCreate(ctx context.Context, candidate Candidate) bool
}
```
to:
```go
type TaskPublisher interface {
	PublishCreate(ctx context.Context, candidate Candidate) bool
	PublishDecision(ctx context.Context, candidate Candidate) bool
}
```
Append, after `PublishCreate`'s implementation, reusing the same `IncPublished("create"/"error")` metric and labels as `PublishCreate` — no new metric or label is introduced:
```go
func (p *taskPublisher) PublishDecision(
	ctx context.Context,
	candidate Candidate,
) bool {
	cmd := BuildDecisionCommand(candidate, p.cfg)
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish decision-task failed repo=%s taskID=%s err=%v",
			candidate.Repo.Key(),
			string(cmd.TaskIdentifier),
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published DecisionTaskCommand repo=%s taskID=%s stage=%s",
		candidate.Repo.Key(),
		string(cmd.TaskIdentifier),
		p.cfg.Stage,
	)
	p.metrics.IncPublished("create")
	return true
}
```
The mock `mocks/task_publisher.go` is fully regenerated by `make precommit`'s `generate` step from the `//counterfeiter:generate` directive already present above the `TaskPublisher` interface — do not hand-edit any file under `mocks/`. The regenerated mock will expose `PublishDecisionStub`, `PublishDecisionCallCount()`, `PublishDecisionReturns(bool)`, matching the existing `PublishCreate*` naming pattern.

## 6. `pkg/taskpublisher_test.go` — mirror the existing `PublishCreate` test structure

This file is a single outer `Describe("taskpublisher", func() { ... })` block with `var (ctx, sender, metrics, publisher, candidate)` declared once, populated in a shared `BeforeEach`/`JustBeforeEach`, and the existing `Describe("PublishCreate", ...)` block nested INSIDE it. Add the new block below as an ADDITIONAL nested `Describe` inside that same outer `Describe("taskpublisher", func() { ... })` block, as a sibling immediately after the existing `Describe("PublishCreate", ...)` block, reusing the outer `sender`/`metrics`/`publisher`/`candidate`/`ctx` variables and the `fakeTaskPublisherSender` test double already declared at the bottom of the file. Do NOT add it as a new top-level `var _ = Describe(...)` statement — mirror the existing `Context("on send success", ...)` / `Context("on send error", ...)` naming exactly:

```go
	Describe("PublishDecision", func() {
		Context("on send success", func() {
			It("returns true", func() {
				Expect(publisher.PublishDecision(ctx, candidate)).To(BeTrue())
			})

			It("captures one command", func() {
				publisher.PublishDecision(ctx, candidate)
				Expect(len(sender.SentCommands)).To(Equal(1))
			})

			It("IncPublished called with create", func() {
				publisher.PublishDecision(ctx, candidate)
				Expect(metrics.IncPublishedCallCount()).To(Equal(1))
				label := metrics.IncPublishedArgsForCall(0)
				Expect(label).To(Equal("create"))
			})

			It("sends a command with task_type github-update-go-decision", func() {
				publisher.PublishDecision(ctx, candidate)
				cmd := sender.SentCommands[0]
				Expect(cmd.Frontmatter["task_type"]).To(Equal("github-update-go-decision"))
			})

			It("sends a command assigned to bborbe, not the update agent", func() {
				publisher.PublishDecision(ctx, candidate)
				cmd := sender.SentCommands[0]
				Expect(cmd.Frontmatter["assignee"]).To(Equal("bborbe"))
				Expect(cmd.Frontmatter["assignee"]).NotTo(Equal("github-update-go-agent"))
			})
		})

		Context("on send error", func() {
			BeforeEach(func() {
				sender.SendError = errors.New("send failed")
			})

			It("returns false", func() {
				Expect(publisher.PublishDecision(ctx, candidate)).To(BeFalse())
			})

			It("IncPublished called with error", func() {
				publisher.PublishDecision(ctx, candidate)
				Expect(metrics.IncPublishedCallCount()).To(Equal(1))
				label := metrics.IncPublishedArgsForCall(0)
				Expect(label).To(Equal("error"))
			})
		})
	})
```
`sender.SentCommands` is `[]task.CreateCommand` (the `task` package is already imported by this file for the `fakeTaskPublisherSender` type declaration), `sender.SendError` is the field the existing `Context("on send error", ...)` for `PublishCreate` already sets, and `metrics` is `*mocks.Metrics` with `IncPublishedCallCount()` / `IncPublishedArgsForCall(int)` already used by the existing `PublishCreate` tests — reuse these exact names verbatim.

## 7. `pkg/watcher.go` — skip-with-emit branch

Replace the `processRepos` skip block:
```go
		if reason := cycleFilter.Skip(candidate.FilterCandidate()); reason != "" {
			w.metrics.IncFilterSkipped(reason)
			glog.V(2).Infof(
				"repo skipped repo=%s reason=%s",
				repo.Key(),
				reason,
			)
			continue
		}
```
with:
```go
		if reason := cycleFilter.Skip(candidate.FilterCandidate()); reason != "" {
			w.metrics.IncFilterSkipped(reason)
			glog.V(2).Infof(
				"repo skipped repo=%s reason=%s",
				repo.Key(),
				reason,
			)
			if reason == "auto_update_undecided" {
				// Skip-with-emit (spec 002 Desired Behavior 5): the repo is
				// still correctly counted and logged as skipped above; this
				// additionally files a human-facing decision task. The
				// return value is deliberately ignored — a failed publish
				// here is absorbed exactly like a failed PublishCreate
				// would be: the repo stays undecided and the same decision
				// task is attempted again next cycle (spec 002 Failure
				// Modes). No cursor/suppression state is written for this
				// branch (spec 002 Desired Behavior 8) — DeriveDecisionTaskID
				// is repo-keyed, so re-emitting it every cycle is already a
				// downstream no-op.
				w.publisher.PublishDecision(ctx, candidate)
			}
			continue
		}
```

## 8. `pkg/watcher_test.go` — new end-to-end Context

This file is a single outer `Describe("watcher", func() { ... })` block; `ghClient`, `goDevClient`, `publisher`, `completeSender`, `metrics`, `watcher`, `cursorPath`, `allowlist` are declared once in that outer block's `var (...)` and populated in its `BeforeEach`, and `buildWatcher := func() { ... }` is a closure declared in that same outer block. `Describe("Poll", func() { ... })` is nested inside it, and every existing `Context("AC5 consent gate", ...)`, `Context("AC6 version table", ...)`, etc. are nested INSIDE `Describe("Poll", ...)` as siblings. Add the new block below as an ADDITIONAL nested `Context` inside `Describe("Poll", func() { ... })`, sibling to `Context("AC5 consent gate", ...)`, reusing the outer `ghClient`/`goDevClient`/`publisher`/`metrics`/`watcher`/`buildWatcher` from scope. Do NOT add it as a new top-level `var _ = Describe(...)` statement — it would not have those variables in scope and would fail to compile:
```go
			Context("undecided repo skip-with-emit (spec 002 Desired Behaviors 5, 6)", func() {
				BeforeEach(func() {
					ghClient.ListReposReturns([]pkg.Repo{
						{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
					}, nil)
					ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
					ghClient.GetGoModReturns([]byte("go 1.24"), nil)
					ghClient.GetMaintainerConfigReturns(filter.UndecidedConsent, nil)
					goDevClient.LatestStableReturns(pkg.Version{
						Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
					}, nil)
					metrics.IncFilterSkippedStub = func(string) {}
					buildWatcher()
				})

				It("publishes exactly one decision task and zero update tasks", func() {
					publisher.PublishDecisionReturns(true)
					err := watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())
					Expect(publisher.PublishDecisionCallCount()).To(Equal(1))
					Expect(publisher.PublishCreateCallCount()).To(Equal(0))
				})

				It("records reason auto_update_undecided", func() {
					publisher.PublishDecisionReturns(true)
					_ = watcher.Poll(context.Background(), false)
					Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
					arg := metrics.IncFilterSkippedArgsForCall(0)
					Expect(arg).To(Equal("auto_update_undecided"))
				})

				It("a failed decision publish does not fail the poll cycle", func() {
					publisher.PublishDecisionReturns(false)
					err := watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())
				})
			})
```
Note for the executing agent: spec 002 AC3's "three input-class distinction" (absent file / absent section / absent key all reading as undecided) is already proven at the parse layer (`pkg/filter/consent_test.go`, prior prompt) and the filter layer (`pkg/filter/filter_test.go`'s consent matrix, prior prompt). This watcher-level test uses `filter.UndecidedConsent` directly — the single value all three input classes collapse to by the time they reach the watcher — and is not required to re-enumerate the three raw-YAML shapes again. Spec 002 AC9's "zero published commands carry the update task type" evidence is satisfied by two complementary checks: at this watcher level, `PublishCreateCallCount()` being 0 proves no update-type command reached the sender (since `PublishCreate` is the only path that could ever emit one); at `pkg/taskpublisher_test.go`'s level (this prompt, item 6 above), the new `PublishDecision` tests positively confirm the decision command's own `task_type`/`assignee` fields are correct.

Verify the exact `ghClient`/`goDevClient`/`publisher`/`metrics`/`watcher` variable names and the `buildWatcher` helper's exact signature against this file's existing top-of-file `var (...)` block and `BeforeEach`/`JustBeforeEach` before writing this Context — match whatever names the file actually uses verbatim.

</requirements>

<constraints>

- Weakening the consent gate is out of scope. Absent consent must still never file an update task and never cause any write to the observed repo.
- Defaulting absent consent to true, under any flag, env var, or code path, is forbidden.
- Decision-task identity is repo-keyed (spec 002 Desired Behavior 7) — never SHA-keyed. A repo receiving new commits while undecided must not spawn a second decision task.
- A decision task carries type `github-update-go-decision` and assignee `bborbe` (spec 002 Desired Behavior 6) — deliberately NOT the update agent's assignee (`github-update-go-agent`).
- No new configuration list, no flag, no env var. The set of repos evaluated is unchanged.
- Filter chain order stays frozen: 1 scope, 2 no_gomod, 3 gomod_unparsable, 4 go_current, 5 auto_update_disabled/auto_update_undecided, 6 sha_unchanged. This prompt does not touch the chain — it only adds a side effect in the watcher's already-existing skip branch.
- No new suppression/dedup store, no new cursor field, no new persisted state for the decision-task pathway (spec 002 Desired Behavior 8) — the repo-keyed identity alone makes re-emission a downstream no-op.
- A failed decision-task publish must not fail the poll cycle and must not be treated any differently from a failed update-task publish — the repo is simply retried next cycle.
- Do not hand-edit any file under `mocks/` — it is fully regenerated by `make precommit`'s `generate` step.
- The metrics/skip-reason wiring for `auto_update_undecided` (Prometheus label pre-initialisation, README) is explicitly out of scope for this prompt — it lands in a later prompt.

</constraints>

<verification>

```
make precommit
```
must exit 0.

Confirm the new decision-task pathway is reachable and distinct from the update-task pathway:
```
grep -n "PublishDecision" pkg/taskpublisher.go pkg/watcher.go
```
must show `PublishDecision` defined in `pkg/taskpublisher.go` and called (guarded by `reason == "auto_update_undecided"`) in `pkg/watcher.go`.

</verification>
