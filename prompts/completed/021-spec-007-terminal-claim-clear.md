---
status: completed
spec: [007-bug-emission-claim-survives-a-failed-emission]
summary: Reworked spec 007 so the watcher ends an emission claim on a task-lifecycle event (terminal, hold, or human_review) through an in-process repo-keyed store, re-emitting at a moved HEAD and skipping sha_unchanged at an unchanged HEAD, with once-per-generation bounding.
execution_id: 6dca66e6-b096-52d0-a675-8d5737bdb772-exec-021-spec-007-terminal-claim-clear
dark-factory-version: dev
created: "2026-11-15T16:52:57Z"
queued: "2026-11-15T17:14:28Z"
started: "2026-11-15T17:14:32Z"
completed: "2026-11-15T17:27:43Z"
branch: dark-factory/spec-007
---

<summary>
- A repo whose emitted task can no longer progress becomes re-emittable on the next watcher cycle instead of being frozen for up to 14 days.
- The watcher now listens to the task lifecycle event stream (read-only, in-process) and learns when an emitted task reaches a terminal status, is parked on `hold`, or sits at `human_review`.
- A genuinely live emission still suppresses duplicates; the new clear fires only on an observed non-progressing event.
- A failed emission at a moved HEAD is re-emitted; at an unchanged HEAD the repo is skipped as `sha_unchanged`, so no identity-identical duplicate is filed.
- The clear is bounded to once per claim generation, and a forced cycle cannot re-arm an identity-identical publish.
- A store read error leaves the claim intact and keeps suppressing; a GitHub error still never clears; a forced cycle still does not bypass.
- A restart replays the event topic from its oldest offset, rebuilding the non-progressing set, so pre-existing frozen claims end without an operator sweep.
- The DoD contract, README skip-reason table, and spec 005 are updated in the same change to describe the fourth clearing event.
</summary>

<objective>
End the emission claim when the task it covers can no longer progress — a terminal status (`completed`, `aborted`), `status: hold`, or `phase: human_review` — instead of waiting out `InFlightTTL` (14 days). A new task-lifecycle event consumer feeds an in-process, repo-keyed store; `emissionInFlight` gains a fifth clearing condition; a moved-HEAD failed emission drops the dedup keys and re-emits, while an unchanged-HEAD one keeps them and is skipped `sha_unchanged`. All parts must land together: a consumer with no clearing path observes and does nothing, a clearing path with no consumer ends claims on nothing, and a store whose entitlement is never consumed re-emits every cycle.
</objective>

<context>
Read these repo files before writing code — every anchor below was verified against them:

- `pkg/watcher.go` — `NewWatcher` (9 params), the `watcher` struct, `Poll` (two `SaveCursor` sites: the normal tail and the `completeMergedUpdates` abort branch), `processRepos` (calls `w.gateRepo`, `w.suppressInFlight`, `cycleFilter.Skip`, then `publisher.PublishCreate` + `markPublished`), `emissionInFlight` (target guard → `!candidate.GoBehind()` convergence → `inFlightExpired` TTL), `clearInFlight`, `inFlightExpired`, `markPublished` (writes `LastSeenHeadSHA`, `LastSeenGoVersion`, `CompletedHeadSHA`, `InFlightTarget`, `InFlightSince`), `InFlightTTL = 14 * libtime.Day`.
- `pkg/cursor.go` — `Cursor{Repos map[string]*RepoState}`, `RepoState` JSON tags (`last_seen_head_sha`, `last_seen_go_version,omitempty`, `completed_head_sha,omitempty`, `in_flight_target,omitempty`, `in_flight_since,omitempty`), `LoadCursor`, `SaveCursor`.
- `pkg/repo.go` — `Repo.Key()` returns `github.com/<owner>/<name>`; `Repo.String()` returns `<owner>/<name>`.
- `pkg/taskid.go` — `DeriveTaskID(owner, repo, goVersion, headSHA string) uuid.UUID`. It survives only as the record-minting seed; do NOT use it as a claim key.
- `pkg/taskbuilder.go` — the emitted frontmatter sets `"repo": c.Repo.String()`; that is the key the consumer writes and the watcher reads.
- `pkg/metrics.go` — `FilterSkipReasons`, `IncFilterSkipped`.
- `pkg/factory/factory.go` — `CreateWatcher` (11 params) and `CreateKafkaSender` / `CreateKafkaCompleteSender`.
- `main.go` — the `application.Run` body: `syncProducer`, `metrics`, `factory.CreateWatcher`, and the two-argument `service.Run(ctx, a.pollLoop(...), a.createHTTPServer(...))` at the end.
- `main_test.go` — `package main_test`; the "does not declare the removed scaffold flags" test reads `main.go` via `os.ReadFile`.
- `pkg/watcher_test.go` — `Describe("watcher")`'s `buildWatcher` closure (calls `pkg.NewWatcher`), and the top-level `Describe("emission claim (spec 005)")` (also calls `pkg.NewWatcher`; helpers `writeCursorState`, `readRepoState`, `filterSkippedValue`, `claimState`; constants `repoKey`, `oldSHA`, `newSHA`).
- `pkg/pkg_suite_test.go` — `TestSuite` is the only entry point.
- `docs/dod.md` — the "In-flight signal contract (durable — do not break)" section.
- `README.md` — the skip-reasons table row for `emission_in_flight` (line ~82).
- `specs/in-progress/005-bug-watcher-reemits-while-task-still-queued.md` — Desired Behavior 3 and the Failure Modes row for "Task dies before opening a PR".
- `go.mod` — `github.com/bborbe/kv v1.21.28` is already indirect; `github.com/IBM/sarama v1.61.1` is already indirect.

Read these coding plugin docs (in-container paths, not host paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cqrs.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`

Verified library API facts (do not re-derive from memory):

- `agentlib "github.com/bborbe/agent"` (package name `lib`): `agentlib.Task` has `Frontmatter agentlib.TaskFrontmatter`; `TaskFrontmatter.Status() domain.TaskStatus`; `TaskFrontmatter.Phase() *domain.TaskPhase`; `TaskFrontmatter.String(key string) (string, bool)`. `TaskFrontmatter.Status().String()` yields `"completed"`, `"aborted"`, `"hold"`, `"in_progress"`, `"next"`, `"backlog"`; `Phase().String()` yields `"human_review"`, `"execution"`, `"planning"`, etc. **Do not import `github.com/bborbe/vault-cli/pkg/domain` directly** — compare `.String()` so vault-cli stays an indirect dependency.
- `agentlib.TaskV1SchemaID` is a `cdb.SchemaID`. `cdb.SchemaID.EventTopic(prefix base.TopicPrefix) kafka.Topic` builds `<prefix>-agent-task-v1-event` (or `agent-task-v1-event` when the prefix is empty). It must never be a bare literal.
- `kafka "github.com/bborbe/kafka"`: `kafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(provider SaramaClientProvider, topic Topic, offsetManager OffsetManager, messageHandlerBatch MessageHandlerBatch, batchSize BatchSize, trigger run.Fire, logSamplerFactory log.SamplerFactory) Consumer`; `Consumer.Consume(ctx) error`; `kafka.NewStoreOffsetManager(store OffsetStore, initialOffset, fallbackOffset Offset) OffsetManager`; `kafka.NewOffsetStore(db libkv.DB) OffsetStore`; `kafka.NewMessageHandlerBatch(handler MessageHandler) MessageHandlerBatch`; `kafka.NewMessageHandlerSkipErrors(handler MessageHandler, logSamplerFactory log.SamplerFactory) MessageHandler`; `kafka.MessageHandlerFunc`; `kafka.BatchSize(1)`; `kafka.OffsetOldest`, `kafka.OffsetNewest`; `kafka.NewSaramaClientProviderNew(brokers Brokers, opts ...SaramaConfigOptions) SaramaClientProvider`; `kafka.ParseBrokersFromString(value string) Brokers`.
- `libkv "github.com/bborbe/kv"`: `DB` (`Update`, `View`, `Sync`, `Close`, `Remove`, `Stats`, `StatsDetailed`), `Tx` (`Bucket`, `CreateBucket`, `CreateBucketIfNotExists`, `DeleteBucket`, `ListBucketNames`), `Bucket` (`Put`, `Get`, `Delete`, `Iterator`, `IteratorReverse`), `Item` (`Exists`, `Key`, `Value`), `Iterator` (`Close`, `Item`, `Next`, `Valid`, `Rewind`, `Seek`); `libkv.NewByteItem(key, value []byte) Item`; `libkv.NewBucketName(name string) BucketName`; `libkv.ErrBucketNotFound`, `libkv.ErrBucketAlreadyExists`; `libkv.Stats{Backend string, Detailed bool, Buckets []BucketStats}`, `libkv.BucketStats{Name BucketName, KeyCount int64}`.
- `run "github.com/bborbe/run"`: `type Func func(ctx context.Context) error`; `run.NewTrigger() Trigger` (satisfies `run.Fire`).
- `log "github.com/bborbe/log"`: `log.DefaultSamplerFactory`.
- `github.com/bborbe/errors`: `errors.Wrap`, `errors.Wrapf`, `errors.Errorf`; never `fmt.Errorf`.
- `github.com/IBM/sarama`: `*sarama.ConsumerMessage` has `.Key []byte` and `.Value []byte`.

The event payload is the `Task` JSON itself (the CDB event sender marshals the event value directly) and the message key is the task identifier. Decode `msg.Value` into `agentlib.Task`; an empty `msg.Value` is a delete event.
</context>

<requirements>

### Reconciliation decisions (the spec has four internal inconsistencies — resolve them exactly this way)

1. **Claim key is REPO, not a derived task identifier.** Desired Behavior 2/3 say "identifier", but Desired Behavior 4 and 4b override that: key the store by the task's frontmatter `repo` (`<owner>/<name>`) and have the watcher pass `repo.String()`. Do not derive or compare a task identifier. `DeriveTaskID` stays only as the publish seed.
2. **The non-progressing set is exactly three populations: `status` in {`completed`, `aborted`}, `status: hold`, or `phase: human_review`.** Desired Behavior 2's literal `{completed, aborted}` is superseded by the Non-goals ("`status: hold` ends a claim") and the Constraints ("exactly three populations"). Add a dedicated `hold` test in addition to the six-event test.
3. **Do not assert an empty `InFlightTarget` after a successful publish.** Acceptance Criterion 4 says `InFlightTarget` empty after a publish, but AC 1's warning explains a successful publish re-arms a fresh claim via `markPublished`. For the `human_review` AC assert the publish count (1) and the `emission_in_flight` counter (0); do not assert an empty claim.
4. **The Failure Modes row "A `- reemit` hand-authored task ... can never match a claim" is stale under repo-keyed matching.** Follow Desired Behavior 4 (repo key). A hand-authored task that carries `repo: <owner>/<name>` and ends non-progressing will observe that repo; this is accepted and out of scope to filter.

### 1. In-process kv DB — new file `pkg/memorydb.go`

Implement the minimal `libkv.DB` the Kafka offset store needs. The upstream `github.com/bborbe/kv` ships no public memory constructor, so carry this implementation in the repo (do not add `github.com/bborbe/memorykv` — it is not in the module graph and the spec permits only `github.com/bborbe/kv`).

```go
package pkg

import (
	"context"
	"sort"
	"sync"

	libkv "github.com/bborbe/kv"
)

// NewMemoryDB returns an in-process libkv.DB. Offsets live in the process, so a
// restart replays the task event topic from its oldest offset — by design.
func NewMemoryDB() libkv.DB {
	return &memoryDB{buckets: make(map[string]map[string][]byte)}
}

type memoryDB struct {
	mu      sync.RWMutex
	buckets map[string]map[string][]byte
}

func (d *memoryDB) Update(ctx context.Context, fn func(ctx context.Context, tx libkv.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(ctx, &memoryTx{db: d})
}

func (d *memoryDB) View(ctx context.Context, fn func(ctx context.Context, tx libkv.Tx) error) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return fn(ctx, &memoryTx{db: d})
}

func (d *memoryDB) Sync() error   { return nil }
func (d *memoryDB) Close() error  { return nil }
func (d *memoryDB) Remove() error { return nil }
```

`Stats` and `StatsDetailed` return `&libkv.Stats{Backend: "memory", Detailed: ...}` with one `libkv.BucketStats` per bucket (populate `KeyCount` only when detailed).

`memoryTx` implements `libkv.Tx`:
- `Bucket` returns `libkv.ErrBucketNotFound` when the bucket is absent; otherwise a `*memoryBucket` over the stored map.
- `CreateBucket` returns `libkv.ErrBucketAlreadyExists` when present.
- `CreateBucketIfNotExists` creates the map lazily.
- `DeleteBucket` returns `libkv.ErrBucketNotFound` when absent.
- `ListBucketNames` returns sorted `libkv.BucketNames`.

`memoryBucket` implements `libkv.Bucket`:
- `Put` copies the value (`append([]byte(nil), value...)`) and stores it under `string(key)`.
- `Get` returns `libkv.NewByteItem(key, value)`; a missing key yields `NewByteItem(key, nil)` so `Item.Exists()` is false and `Value(fn)` calls `fn(nil)`.
- `Delete` removes the key.
- `Iterator` / `IteratorReverse` return a sorted key iterator (`sort.Strings`; reverse flips the slice).

The iterator implements `libkv.Iterator`: `Close` no-op; `Item` returns `libkv.NewByteItem(nil, nil)` when invalid; `Next` advances; `Valid` is `0 <= index < len(keys)`; `Rewind` resets to 0; `Seek` uses `sort.SearchStrings` (or `sort.Search` for the reversed slice). Use `_ context.Context` on methods that do not read the context to satisfy `unparam`.

This boundary must be tested: `libkv.NewOffsetStore(pkg.NewMemoryDB())` must `Set` then `Get` a topic/partition offset (a level-1 test in `pkg/memorydb_test.go`), proving the offset store's `CreateBucketIfNotExists` + `Bucket` + `Put`/`Get` path works against the memory DB.

### 2. Terminal claims store — new file `pkg/terminal_claims_store.go`

```go
//counterfeiter:generate -o ../mocks/terminal_claims_store.go --fake-name TerminalClaimsStore . TerminalClaimsStore

// TerminalClaimsStore answers, from in-process state only, whether a repo's
// emitted task can no longer progress. It holds no clock, no GitHub call and no
// cursor access: the task lifecycle consumer feeds it, the poll loop reads it.
type TerminalClaimsStore interface {
	// Observe records that a non-progressing task event was seen for repo. It
	// deliberately leaves a consumed entitlement consumed: a repeat sighting of
	// the same claim generation does not re-arm it.
	Observe(ctx context.Context, repo string) error

	// CanConsume reports whether the entitlement to end a claim for repo is
	// available: observed and not yet consumed. It does not consume it, so a
	// cycle that aborts before SaveCursor does not burn the entitlement.
	CanConsume(ctx context.Context, repo string) (bool, error)

	// Consume marks the entitlement for repo consumed.
	Consume(ctx context.Context, repo string) error

	// Rearm resets repo after a publish wrote a fresh claim, so that claim's
	// eventual non-progressing event can end it. It clears BOTH flags: the prior
	// observation belonged to the claim that just ended. If observed were left
	// set, the next cycle would immediately clear the fresh claim.
	Rearm(ctx context.Context, repo string) error
}

// NewInMemoryTerminalClaimsStore returns a TerminalClaimsStore backed by a
// mutex-guarded map. The map grows with the topic's non-progressing repo
// cardinality and is rebuilt from the topic on restart.
func NewInMemoryTerminalClaimsStore() TerminalClaimsStore {
	return &terminalClaimsStore{states: make(map[string]*terminalClaimState)}
}
```

Implementation semantics (exact):
- `terminalClaimState{observed bool; consumed bool}`; `terminalClaimsStore{mu sync.Mutex; states map[string]*terminalClaimState}`.
- `Observe`: reject an empty repo with `errors.Errorf(ctx, "repo must not be empty")`; otherwise set `observed = true` (do not touch `consumed`).
- `CanConsume`: return `(false, nil)` for an absent entry; otherwise `state.observed && !state.consumed`.
- `Consume`: set `consumed = true` for the repo (creating the entry if needed).
- `Rearm`: set `observed = false` and `consumed = false`.
- Every method locks the mutex; the consumer goroutine and the poll goroutine share this store.

### 3. Task-lifecycle consumer — new file `pkg/terminal_claims_consumer.go`

Add the topic helper, the handler, and the run loop.

```go
// TerminalClaimsRetryDelay is the delay before retrying after a recoverable
// error. The poll loop keeps running meanwhile.
const TerminalClaimsRetryDelay = 5 * time.Second

// TerminalClaimsEventTopic is the task lifecycle event topic. Derived from the
// schema id and the configured prefix — a bare literal subscribes to the wrong
// topic under a prefix.
func TerminalClaimsEventTopic(prefix base.TopicPrefix) kafka.Topic {
	return agentlib.TaskV1SchemaID.EventTopic(prefix)
}

// NewTerminalClaimsHandler records repos whose emitted task can no longer
// progress. The payload decodes to agentlib.Task; the store key is the
// frontmatter `repo` field (owner/name), the same key the watcher passes as
// Repo.String(). An empty payload is a delete event and is ignored.
func NewTerminalClaimsHandler(store TerminalClaimsStore) kafka.MessageHandler {
	return kafka.MessageHandlerFunc(func(ctx context.Context, msg *sarama.ConsumerMessage) error {
		if len(msg.Value) == 0 {
			return nil
		}
		var task agentlib.Task
		if err := json.Unmarshal(msg.Value, &task); err != nil {
			return errors.Wrapf(ctx, err, "decode task event key=%s", string(msg.Key))
		}
		if !taskCanNoLongerProgress(task) {
			return nil
		}
		repo, ok := task.Frontmatter.String("repo")
		if !ok || repo == "" {
			glog.V(2).Infof("task event has no repo frontmatter, ignoring key=%s", string(msg.Key))
			return nil
		}
		return store.Observe(ctx, repo)
	})
}

// taskCanNoLongerProgress is the dispatchability predicate: a terminal status,
// status: hold (a block with no scheduled resume date), or phase: human_review
// (no Config dispatches it and a record cannot leave it on its own). Every other
// status is a queued or running wait the claim exists to cover.
func taskCanNoLongerProgress(task agentlib.Task) bool {
	switch task.Frontmatter.Status().String() {
	case "completed", "aborted", "hold":
		return true
	}
	phase := task.Frontmatter.Phase()
	return phase != nil && phase.String() == "human_review"
}

// RunTerminalClaimsConsumer runs consume until ctx is cancelled. A recoverable
// error is logged and retried; the function never returns on a recoverable
// error, because service.Run tears the whole service down when any run.Func
// returns. On ctx cancellation it returns nil.
func RunTerminalClaimsConsumer(
	consume func(context.Context) error,
	retryDelay time.Duration,
) run.Func {
	return func(ctx context.Context) error {
		for {
			err := consume(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				glog.Warningf("task-event consumer stopped, retrying err=%v", err)
			}
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}
```

Notes:
- Do NOT import `github.com/bborbe/vault-cli/pkg/domain`; compare `.String()`.
- The `*sarama.ConsumerMessage` parameter makes `github.com/IBM/sarama` a direct dependency (it is already in the module graph; `go mod tidy` will move it).
- The handler is wrapped in `NewMessageHandlerSkipErrors` at composition time (requirement 5), so a malformed payload is logged and skipped.

### 4. Watcher — `pkg/watcher.go`

#### 4a. Constructor and field

Add `claims TerminalClaimsStore` to the `watcher` struct and to `NewWatcher` as the parameter immediately before `currentDateTimeGetter`:

```go
func NewWatcher(
	ghClient GitHubClient,
	goDevClient GoDevClient,
	publisher TaskPublisher,
	completeSender task.CompleteCommandSender,
	metrics Metrics,
	cursorPath string,
	owner string,
	taskCreationFilter filter.TaskCreationFilter,
	claims TerminalClaimsStore,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) Watcher
```

Add `claims: claims` to the returned struct literal.

#### 4b. Pending-consume set

```go
// pendingConsume is the set of repos whose terminal-claims entitlement this
// cycle ended a claim for. It is consumed only after SaveCursor succeeds, so an
// aborted cycle (a rate limit returns before the save) does not burn an
// entitlement whose clear was discarded.
type pendingConsume map[string]struct{}
```

#### 4c. `Poll` — create and drain the set

- `pending := pendingConsume{}` immediately before `w.processRepos(...)`; pass it as the last argument.
- In the `completeMergedUpdates` abort branch, after the existing `SaveCursor`: on success call `w.consumePending(ctx, pending)` before `return nil`; on save error log and `return nil` (do not consume).
- In the normal tail, replace the unconditional save/log with: on save success call `w.consumePending(ctx, pending)`; on save error log and skip consumption. Keep `w.metrics.IncPollCycle("success")` after it (preserve the existing metric behaviour).

```go
func (w *watcher) consumePending(ctx context.Context, pending pendingConsume) {
	for repo := range pending {
		if err := w.claims.Consume(ctx, repo); err != nil {
			glog.Warningf("terminal-claims consume failed repo=%s err=%v", repo, err)
		}
	}
}
```

#### 4d. `processRepos`, `suppressInFlight`, `emissionInFlight` signatures

Thread `pending pendingConsume` through `processRepos` (last param), `suppressInFlight` (last param), and `emissionInFlight` (last param). `markPublished` gains `ctx context.Context` and `pending pendingConsume`.

#### 4e. The fifth condition in `emissionInFlight`

Insert it **after the target-match guard and after the convergence branch, before the TTL branch**:

```go
	// Fifth condition: the emitted task can no longer progress (terminal, hold,
	// or human_review), as observed by the task lifecycle consumer. A store read
	// failure leaves the claim intact and keeps suppressing.
	canConsume, err := w.claims.CanConsume(ctx, repo.String())
	if err != nil {
		glog.Warningf("terminal-claims read failed repo=%s err=%v", repo.Key(), err)
		return true
	}
	if canConsume {
		if candidate.HeadSHA != state.LastSeenHeadSHA {
			clearFailedEmission(cursorState, repo.Key(), target)
		} else {
			clearInFlight(cursorState, repo.Key(), target)
		}
		pending[repo.String()] = struct{}{}
		return false
	}
```

Then the existing `inFlightExpired` branch and `return true` stay unchanged.

#### 4f. `clearFailedEmission`

```go
// clearFailedEmission ends a claim whose emitted task can no longer progress and
// drops the dedup keys so the moved-HEAD re-emit passes the sha_unchanged gate.
// The CompletedHeadSHA merge marker always survives. Call only when HEAD moved:
// at an unchanged HEAD the re-emit would be identity-identical, so the caller
// uses clearInFlight instead.
func clearFailedEmission(cursorState *Cursor, repoKey, target string) {
	state := cursorState.Repos[repoKey]
	if state == nil || state.InFlightTarget != target {
		return
	}
	state.InFlightTarget = ""
	state.InFlightSince = ""
	state.LastSeenHeadSHA = ""
	state.LastSeenGoVersion = ""
}
```

#### 4g. `markPublished` — identity-identical branch and entitlement re-arm

Replace the body with:

```go
func (w *watcher) markPublished(
	ctx context.Context,
	cursorState *Cursor,
	repo Repo,
	candidate Candidate,
	pending pendingConsume,
) {
	if cursorState.Repos == nil {
		cursorState.Repos = make(map[string]*RepoState)
	}
	previous := cursorState.Repos[repo.Key()]
	var completedHeadSHA string
	if previous != nil {
		completedHeadSHA = previous.CompletedHeadSHA
	}
	// An identity-identical publish (same HEAD and target as the cursor already
	// records) files nothing new: write the dedup keys and no claim, so a forced
	// cycle cannot re-create a claim the ordinary cycle would never write.
	identityIdentical := previous != nil &&
		previous.LastSeenHeadSHA == candidate.HeadSHA &&
		previous.LastSeenGoVersion == candidate.LatestGo.Number()

	next := &RepoState{
		LastSeenHeadSHA:   candidate.HeadSHA,
		LastSeenGoVersion: candidate.LatestGo.Number(),
		CompletedHeadSHA:  completedHeadSHA,
	}
	if !identityIdentical {
		next.InFlightTarget = candidate.LatestGo.Number()
		next.InFlightSince = w.currentDateTimeGetter.Now().Format(time.RFC3339)
	}
	cursorState.Repos[repo.Key()] = next

	if identityIdentical {
		return
	}
	if err := w.claims.Rearm(ctx, repo.String()); err != nil {
		glog.Warningf("terminal-claims rearm failed repo=%s err=%v", repo.Key(), err)
	}
	delete(pending, repo.String())
}
```

The `delete(pending, ...)` is load-bearing: without it, `consumePending` (which runs after `SaveCursor`) would consume the entitlement for the claim this publish just wrote. Update `markPublished`'s doc comment to describe the identity-identical branch and the re-arm.

### 5. Factory and `main.go` composition

#### 5a. `pkg/factory/factory.go`

Add `claims pkg.TerminalClaimsStore` to `CreateWatcher` immediately before `currentDateTimeGetter`, and pass it to `pkg.NewWatcher`. Add:

```go
// NewTerminalClaimsConsumer builds the task lifecycle event consumer as a
// run.Func. It subscribes to the task event topic derived from the schema id and
// the configured prefix, records non-progressing repos in the in-process store,
// and never returns on a recoverable error.
func NewTerminalClaimsConsumer(
	saramaClientProvider kafka.SaramaClientProvider,
	topicPrefix base.TopicPrefix,
	claims pkg.TerminalClaimsStore,
) run.Func {
	consumer := kafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(
		saramaClientProvider,
		pkg.TerminalClaimsEventTopic(topicPrefix),
		kafka.NewStoreOffsetManager(
			kafka.NewOffsetStore(pkg.NewMemoryDB()),
			kafka.OffsetOldest,
			kafka.OffsetNewest,
		),
		kafka.NewMessageHandlerBatch(
			kafka.NewMessageHandlerSkipErrors(
				pkg.NewTerminalClaimsHandler(claims),
				log.DefaultSamplerFactory,
			),
		),
		kafka.BatchSize(1),
		run.NewTrigger(),
		log.DefaultSamplerFactory,
	)
	return pkg.RunTerminalClaimsConsumer(consumer.Consume, pkg.TerminalClaimsRetryDelay)
}
```

Add imports `agentlib "github.com/bborbe/agent"` and `"github.com/bborbe/run"`; `kafka`, `base`, `log`, `pkg` are already imported.

#### 5b. `main.go`

Inside `application.Run`, after `metrics := pkg.NewMetrics(nil)`:

```go
claims := pkg.NewInMemoryTerminalClaimsStore()
saramaClientProvider := kafka.NewSaramaClientProviderNew(
	kafka.ParseBrokersFromString(a.KafkaBrokers),
)
defer func() {
	if cerr := saramaClientProvider.Close(); cerr != nil {
		glog.Warningf("close sarama client provider: %v", cerr)
	}
}()
```

Pass `claims` to `factory.CreateWatcher` (immediately before `currentDateTime`), and pass the third `run.Func` to `service.Run`:

```go
return service.Run(
	ctx,
	a.pollLoop(w, gate, pollInterval),
	a.createHTTPServer(sentryClient),
	factory.NewTerminalClaimsConsumer(saramaClientProvider, a.TopicPrefix, claims),
)
```

The literal `NewTerminalClaimsConsumer` must appear inside the `service.Run(...)` argument list (the composition test greps for it there).

### 6. Tests

All Go tests use Ginkgo/Gomega; the only suite entry point is `TestSuite`.

#### 6a. `pkg/watcher_test.go` — update both existing `pkg.NewWatcher` call sites

- The `Describe("watcher")` `buildWatcher` closure: add `pkg.NewInMemoryTerminalClaimsStore()` immediately before `currentDateTime`.
- The `Describe("emission claim (spec 005)")` `pkg.NewWatcher` call: add `pkg.NewInMemoryTerminalClaimsStore()` immediately before `currentDateTime`.

Existing spec 005 specs must keep passing unchanged: with an empty store, `CanConsume` is false, so the fifth condition is inert and the pre-existing convergence/PR/TTL/target assertions still hold.

#### 6b. `pkg/watcher_test.go` — new top-level `Describe("terminal claims (spec 007)")`

Mirror the spec 005 Describe's setup (real `prometheus.Registry`, injected clock at `2026-10-27T12:00:00Z`, temp cursor path, `mocks.GitHubClient`, `mocks.GoDevClient`, `mocks.TaskPublisher`, `taskmocks.TaskCompleteCommandSender`, target `1.27.4`, go.mod `1.27.1`, `HasOpenUpdatePRReturns(false, nil)`). Add a `claims *mocks.TerminalClaimsStore` created in `BeforeEach`. Reuse the `writeCursorState`, `readRepoState`, `filterSkippedValue` helper shapes.

Constants: `repoKey = "github.com/bborbe/disk-status"`, `oldSHA = "aaaa..."`, `newSHA = "bbbb..."`, `repoString = "bborbe/disk-status"`.

Claim fixture: `{"last_seen_head_sha":"<oldSHA>","last_seen_go_version":"1.27.4","in_flight_target":"1.27.4","in_flight_since":"<now-1h>"}`.

Specs:

1. **"a non-progressing task ends the claim and re-emits at a moved HEAD"** — `GetHeadSHAReturns(newSHA, nil)`; `claims.CanConsumeReturns(true, nil)`. After `Poll(false)`: `PublishCreateCallCount() == 1`, `filterSkippedValue("emission_in_flight") == 0`, `claims.RearmCallCount() == 1`, `claims.ConsumeCallCount() == 0`, `readRepoState().InFlightSince == now().Format(time.RFC3339)` (the current cycle, not the stale hour), `InFlightTarget == "1.27.4"`, `LastSeenHeadSHA == newSHA`.
2. **"a non-progressing task at an unchanged HEAD clears but publishes nothing"** — `GetHeadSHAReturns(oldSHA, nil)`; `claims.CanConsumeReturns(true, nil)`. After `Poll(false)`: `PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 0`, `filterSkippedValue("sha_unchanged") == 1`, `claims.RearmCallCount() == 0`, `claims.ConsumeCallCount() == 1`, `InFlightTarget == ""`, `InFlightSince == ""`, `LastSeenHeadSHA == oldSHA`. Run `Poll(false)` a second time and assert `sha_unchanged == 2`, `PublishCreateCallCount() == 0`, `claims.ConsumeCallCount() == 1` (steady state; one cycle cannot show it).
3. **"a forced cycle cannot re-arm an identity-identical publish"** — cursor `{"last_seen_head_sha":"<oldSHA>","last_seen_go_version":"1.27.4","completed_head_sha":"<oldSHA>"}` (no claim); `GetHeadSHAReturns(oldSHA, nil)`. After `Poll(true)`: `PublishCreateCallCount() == 1`, `claims.RearmCallCount() == 0`, `InFlightTarget == ""`, `CompletedHeadSHA == oldSHA`, `completeSender.SendCommandCallCount() == 0`.
4. **"a human_review-parked task ends the claim"** — `GetHeadSHAReturns(newSHA, nil)`; `claims.CanConsumeReturns(true, nil)`. After `Poll(false)`: `PublishCreateCallCount() == 1`, `filterSkippedValue("emission_in_flight") == 0`. (The handler test proves `human_review` is what produced the observation; do NOT assert an empty `InFlightTarget` — a successful publish re-arms a fresh claim.)
5. **"a queued or running task does not end the claim"** — `GetHeadSHAReturns(newSHA, nil)`; `claims.CanConsumeReturns(false, nil)`. After `Poll(false)`: `PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 1`, `InFlightTarget == "1.27.4"`.
6. **"a second sighting does not end the claim"** — use the REAL store: build a watcher with `store := pkg.NewInMemoryTerminalClaimsStore()`, call `store.Observe(ctx, repoString)` and `store.Consume(ctx, repoString)`, seed the claim, `GetHeadSHAReturns(newSHA, nil)`. After `Poll(false)`: `PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 1`, `InFlightTarget == "1.27.4"`.
7. **"a live claim with no observation still suppresses"** — REAL empty store, claim live, `newSHA` (moved). After `Poll(false)`: `PublishCreateCallCount() == 0`, `filterSkippedValue("emission_in_flight") == 1`.
8. **"a store read error does not clear"** — `claims.CanConsumeReturns(false, errors.New("store unavailable"))`, claim live, `newSHA`. After `Poll(false)`: `PublishCreateCallCount() == 0`, `InFlightTarget == "1.27.4"`, `InFlightSince` unchanged.
9. **"the four existing conditions still work"** — keep the spec 005 specs; add a decoded-struct assertion per condition if one is missing (convergence empties the claim; open PR empties it; TTL re-stamps `InFlightSince`; a different target re-keys `InFlightTarget`). The new fifth condition sits before the TTL branch, so with an empty/false store the TTL test still reaches the TTL branch.

#### 6c. `pkg/terminal_claims_consumer_test.go` — new file

Use `pkg.NewInMemoryTerminalClaimsStore()` and build messages with `&sarama.ConsumerMessage{Key: []byte(id), Value: []byte(payload)}`.

1. **"records only non-progressing tasks"** — six distinct repos, six payloads: `completed`, `aborted`, `in_progress`+`human_review`, `in_progress`+`execution`, `next`+`planning`, and an empty-payload delete. After feeding all six through `NewTerminalClaimsHandler(store).ConsumeMessage(ctx, msg)`: `CanConsume` is true for the first three and false for the last three.
2. **"hold is non-progressing"** — a seventh repo with `status: hold` (any phase) → `CanConsume` true.
3. **"the entitlement is available exactly once"** — `Observe` one repo → `CanConsume` true; `Consume` it → `CanConsume` false.
4. **"the consumer never returns on a recoverable error"** — call `pkg.RunTerminalClaimsConsumer(consume, time.Millisecond)` with a fake `consume` that increments a counter, returns an error on the first call, and then blocks on `<-ctx.Done()` returning `ctx.Err()`. Run it in a goroutine, `Eventually` the counter is `>= 2`, cancel the context, and `Eventually` the goroutine's error channel receives `nil`. This proves the loop continued after the error and returned nil on cancellation.
5. **"the event topic is derived from the prefix"** — `pkg.TerminalClaimsEventTopic(base.TopicPrefix("develop")).String() == "develop-agent-task-v1-event"` and `pkg.TerminalClaimsEventTopic("").String() == "agent-task-v1-event"`. This is the level-1 boundary test for the "never a bare literal" constraint.

Payload helper shape: `{"frontmatter":{"repo":"<owner>/<name>","status":"<status>","phase":"<phase>"}}`. Omit `created`/`modified`/`identifier`; `libtime.DateTime` fields left absent decode to the zero value without error.

#### 6d. `pkg/memorydb_test.go` — new file

A level-1 test that `libkv.NewOffsetStore(pkg.NewMemoryDB())` can `Set(ctx, topic, partition, offset)` then `Get(ctx, topic, partition)` the same offset, and that `Get` before `Set` returns an error wrapping `libkv.BucketNotFoundError` (the offset manager then falls back to `OffsetOldest`). Use `kafka.NewTopic`-free construction: the `kafka.Topic` is a string type, so `kafka.Topic("agent-task-v1-event")` and `kafka.Partition(0)` are sufficient.

#### 6e. `main_test.go` — composition assertion

Add `"strings"` to the imports and this spec:

```go
It("passes the terminal-claims consumer to service.Run", func() {
	data, err := os.ReadFile("main.go")
	Expect(err).NotTo(HaveOccurred())
	block := serviceRunCallBlock(string(data))
	Expect(block).NotTo(BeEmpty())
	Expect(block).To(ContainSubstring("NewTerminalClaimsConsumer"),
		"the terminal-claims consumer must be a run.Func passed to service.Run")
})
```

with a package-level helper that extracts the call block by balancing parentheses:

```go
func serviceRunCallBlock(source string) string {
	start := strings.Index(source, "service.Run(")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(source); i++ {
		switch source[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return source[start : i+1]
			}
		}
	}
	return source[start:]
}
```

This is scoped to the call block, not a file-wide count, so a constructor that is declared and tested but never passed cannot pass. It must fail (RED) if the argument is replaced by a no-op `run.Func`.

#### 6f. Failing-first evidence

Implement the store, consumer, handler, memory DB, the `NewWatcher` signature change, and specs 6a/6b/6c/6d/6e **before** wiring the fifth condition into `emissionInFlight`. Run the new moved-HEAD spec (6b.1) against that state and record the RED output in the execution log: `PublishCreateCallCount()` is `0` and `filter_skipped_total{reason="emission_in_flight"}` is `1` (the claim still suppresses). Then add requirement 4e and re-run to GREEN. The `.git` directory is masked in the build container, so a `git log` proof is not runnable; the recorded RED assertion output is the evidence.

### 7. Documentation (same change)

#### 7a. `docs/dod.md`

In the "Before the update PR exists (the emission window)" paragraph, replace "cleared by exactly three observable events" with four, and add the new event. Include the literal phrase `once-per-task` (the verification greps for it) and the word `terminal`. Suggested text:

> The claim is cleared by exactly four observable events: the repo converges (`!GoBehind()` — the same predicate the `go_current` filter uses, so equal-to, ahead-of and unparsable all clear), an open `fix/update-go-*` PR is observed, the task lifecycle consumer observes that the emitted task can no longer progress, or the `InFlightTTL` backstop (336h / 14 days) expires. A task can no longer progress when it reaches a terminal status (`completed`, `aborted`), `status: hold` (a block with no scheduled resume date), or `phase: human_review` — a **dispatchability** test, not a status string: no Config dispatches any of them. The bound is **once-per-task** per claim generation: the entitlement is consumed by the first cycle that uses it, and `markPublished` re-arms it only for a publish that carries a new identifier. The non-progressing set is rebuilt from the task event topic on restart (the consumer replays from its oldest offset), so pre-existing frozen claims end on the first deploy without an operator sweep. The claim is written only by `markPublished`, so hand-authored `- reemit` repair records never carry one.

#### 7b. `README.md` (skip-reasons row for `emission_in_flight`, ~line 82)

Change "none of the three clearing events has occurred (convergence, an open `fix/update-go-*` PR, or the 14-day backstop)" to "none of the four clearing events has occurred (convergence, an open `fix/update-go-*` PR, a task lifecycle event showing the emitted task can no longer progress, or the 14-day backstop)". Keep the rest of the row.

#### 7c. `specs/in-progress/005-bug-watcher-reemits-while-task-still-queued.md`

- Desired Behavior 3 ("Clearing — evaluated before the skip decision"): add a bullet after the TTL bullet:

  > - **The emitted task can no longer progress (spec 007):** the task lifecycle consumer observed the repo's task reach a terminal status (`completed`, `aborted`), `status: hold`, or `phase: human_review` — the dispatchability test. The claim ends once per claim generation; the dedup keys drop only when HEAD moved, so an unchanged-HEAD re-emit is skipped `sha_unchanged`. Pointer: spec 007.

- The Failure Modes row "Task dies before opening a PR; claim holds the repo": change its Recovery cell from "TTL expires after `336h` and the next cycle emits; the cursor's `InFlightSince` is refreshed on that emit" to "the non-progressing event ends the claim on the next cycle; the TTL remains the backstop".

</requirements>

<constraints>
- No vault, task-store or filesystem read outside the existing cursor file — `docs/dod.md`'s durable contract. The non-progressing set is in-process; the event stream is subscribed, not queried.
- No new cursor field, no new chart value, no new PVC, no new config knob, no new GitHub API call. `InFlightTTL` is unchanged (do not shorten it).
- Do not change `DeriveTaskID`, `openUpdatePRGate`'s semantics, or `SHAUnchangedFilter`'s rule. The dedup-key drop happens in the cursor, not in the filter.
- The cursor file must stay readable by an older binary — this change adds no field.
- The consumer must never return on a recoverable error. `service.Run` wraps every func in `run.CancelOnFirstFinishWait`, so a return tears down the poll loop and the HTTP server.
- The non-progressing set is exactly three populations: a terminal status (`completed`, `aborted`), `status: hold`, and `phase: human_review`. Do not widen it to `next` or `backlog`.
- Ending a failed emission (`clearFailedEmission`) and clearing on convergence (`clearInFlight`) stay separate code paths: they differ in whether the dedup keys survive.
- The claim is matched by repo (`repo.String()`), never by a derived task identifier. `DeriveTaskID` remains only the publish seed.
- Never use `fmt.Errorf` — all errors go through `github.com/bborbe/errors` and carry `ctx`. Never call `time.Now()` in `pkg/` — read `w.currentDateTimeGetter.Now()`; the consumer run loop may use `time.NewTimer` for its retry delay.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from the counterfeiter annotations.
- Keep every line under 100 characters and every function under 80 lines / 50 statements. `emissionInFlight` and `processRepos` are near the limit — extract helpers rather than inlining.
- Existing tests must still pass. The only intended precedence change is that a repo with a live claim and a non-progressing observation now clears instead of suppressing; no existing spec seeds an observation, so none should change.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run from the repo root (the container repo is `/workspace`; `ROOTDIR` is required because `.git` is masked and `Makefile.variables` derives `ROOTDIR` from `git rev-parse`):

```
ROOTDIR=/workspace make precommit
```

Must exit 0 (format + generate + test + lint/vet/vuln/osv/trivy + addlicense). This regenerates `mocks/terminal_claims_store.go`.

```
go test ./pkg/... -count=1
```

Must exit 0.

```
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='terminal claims'
```

Must exit 0 and report the new `Describe` specs as run.

```
go test ./... -count=1 -run TestSuite -ginkgo.focus='Main'
```

Must exit 0, including the composition spec.

```
grep -n "terminal\|once-per-task" docs/dod.md
```

Must print at least one line containing `terminal` and one containing `once-per-task`.

```
grep -n "four clearing events" README.md
```

Must print the `emission_in_flight` row.

```
grep -n "terminal status" specs/in-progress/005-*.md
```

Must print at least one line.

```
awk '/service.Run\(/,/\)/' main.go
```

Must show `NewTerminalClaimsConsumer` inside the printed block.

Do NOT run `docker`, `kubectl`, `make buca`, `dark-factory`, or any `git` command — this container has no Docker socket, no cluster credentials, and a masked `.git`. Deployment observation (the post-pin `kubectl` checks in the spec) is operator-side and lives on the spec's verification ladder, not here.
</verification>
