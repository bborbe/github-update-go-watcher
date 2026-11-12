---
status: approved
spec: [007-bug-emission-claim-survives-a-failed-emission]
summary: Clears the emission claim when the emitted task can no longer progress — terminal, or parked at human_review — via a task-lifecycle consumer, so a failed emission no longer suppresses a re-emit for 14 days
created: "2026-11-12T18:48:17Z"
queued: "2026-11-12T18:48:17Z"
---

<summary>
- A repo whose update task can no longer progress is re-emitted on the next watcher cycle instead of after the fourteen-day backstop.
- "Can no longer progress" is a dispatchability question, not a status string: a terminal status, or `phase: human_review`, which no Config dispatches and which a record cannot leave on its own.
- The watcher gains a task-lifecycle consumer on the same event topic the pull-request watcher already consumes; identifiers whose task can no longer progress are recorded in-process.
- The claim's task identifier is derived from cursor fields that are already written together, so no cursor field is added and no live claim needs a migration.
- Clearing a failed emission also drops the `sha_unchanged` dedup keys, because without that the re-emit is suppressed by the unchanged-HEAD gate and the whole path is inert.
- The clear fires once per emitted task: a repo whose re-emitted task is the same identifier and stops progressing again keeps its claim and waits for the backstop, so the fix cannot turn a fourteen-day period into a ten-minute one.
- Duplicate suppression for a genuinely live emission is unchanged, including the two negative controls — a GitHub error never clears, and a forced cycle never bypasses.
- A task that is queued or running at a dispatchable phase (`planning`, `execution`, `ai_review`) does not clear, and a `human_review` row whose update PR is open is already handled by the pull-request gate above it.

</summary>

<objective>
Add the fifth condition to the emission claim: "the emission's task can no longer progress". Three existing paths end the claim (convergence, an observed open update pull request, and the `InFlightTTL` backstop) and a fourth — a changed target Go version — stops the claim matching without clearing it; none of them fires when a task dies or is parked. Land the consumer, the store, the clearing branch, the dedup-key drop, the once-per-task bound, and the tests that prove the existing paths still clear.
</objective>

<context>
Read `docs/dod.md` — its "In-flight signal contract" section (`:32`) is the durable contract this prompt extends (the same change updates its wording). Read `README.md`'s skip-reasons table (`:70-82`) and `pkg/watcher.go`'s `emissionInFlight`.

Read these coding plugin docs before writing code (in-container paths, not host paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-state-machine-pattern.md` — § status-phase-separation: `status` and `phase` are orthogonal. This prompt uses both: a terminal **status** is one non-progressing population, and `phase: human_review` is the other. `human_review` is not "terminal" — it is *undispatchable*, which is the property this prompt acts on.

Read these repo files before writing code — every anchor below was verified against them:

- `pkg/watcher.go` — `emissionInFlight` (`:390-414`) has the target-match guard (`:398`), the convergence clear (`:405`) and the TTL clear (`:409`). `clearInFlight` (`:307-314`) mutates the existing `RepoState` in place and refuses when the target does not match. `gateRepo` (`:339-356`) clears on an observed open pull request (`:351`). `markPublished` (`:232-248`) writes `LastSeenHeadSHA`, `LastSeenGoVersion`, `CompletedHeadSHA`, `InFlightTarget` and `InFlightSince` in one assignment from the same candidate. `NewWatcher` is at `:44-66` and its struct at `:68-80`.
- `pkg/cursor.go` — `RepoState` (`:30-62`). Do **not** add a field; the task identifier is derived.
- `pkg/taskid.go` — `DeriveTaskID(owner, repo, goVersion, headSHA string) uuid.UUID` (`:26-29`).
- `pkg/filter/sha_unchanged_filter.go` — `:28-36`; it returns `"sha_unchanged"` when `HeadSHA == cursor.LastSeenSHA(repoKey)` **and** `LatestGoVersion == cursor.LastSeenGoVersion(repoKey)`.
- `pkg/factory/factory.go` — `CreateWatcher` (`:86-116`) is the only construction site.
- `main.go` — `Run` (`:64-136`) composes `service.Run(ctx, pollLoop, httpServer)` (`:131-135`); it already builds `syncProducer` (`:92-104`) and `libtime.NewCurrentDateTime()` (`:109`). `service.Run` is variadic and already wraps its funcs in `run.CancelOnFirstFinishWait`, so adding a third func needs no new import.
- `pkg/watcher_test.go` — the `buildWatcher` closure and the second `pkg.NewWatcher(` call at `:1076` in the `Describe("emission claim (spec 005)")` suite; that suite is the regression floor for the existing clearing conditions.
- `docs/dod.md` — the "In-flight signal contract" paragraph (`:32`).
- `README.md` — the `emission_in_flight` row (`:82`).
- `specs/in-progress/005-bug-watcher-reemits-while-task-still-queued.md` — Desired Behavior 3 (`:62-65`) and the Failure Modes row at `:84`.

Reference implementation, in the sibling repo `bborbe/github-pr-watcher` — **not mounted in this container, so the description below is authoritative; do not search for it**:
- `pkg/retry_guard_consumer.go` — `NewRetryGuardOutcomeMessageHandler` decodes `agentlib.Task` from `msg.Value`, treats the message key as the task identifier, ignores empty values and non-terminal statuses, and clears its record on a terminal one. `NewRetryGuardOutcomeConsumer` wires `libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider` over `agentlib.TaskV1SchemaID.EventTopic(prefix)` with `NewStoreOffsetManager(NewOffsetStore(db), OffsetOldest, OffsetNewest)`, `BatchSize(1)`, `NewMessageHandlerBatchTxUpdate` + `NewMessageHandlerBatchTx` + `NewMessageHandlerTxSkipErrors`, and a 5s reconnect loop that returns only on `ctx` cancellation.
- `pkg/memdb.go` — `NewMemDB() libkv.DB`, an in-process `libkv.DB` (the upstream package ships no public memory constructor). Copy it into this repo.
- `main.go:426-479` — the four-`run.Func` composition under `run.CancelOnFirstFinish`.
</context>

<requirements>

1. **Add the in-memory kv DB.** New `pkg/memdb.go` exporting `NewMemDB() libkv.DB`, an in-process `libkv.DB` — the upstream `github.com/bborbe/kv` package ships no public memory constructor (only `NewDBWithMetrics`). Implement exactly the surface the offset store and the terminal store touch:

   - `Update(ctx, fn func(ctx, libkv.Tx) error) error` and `View(ctx, fn …) error`, both holding a `sync.RWMutex` and passing a tx backed by `map[string]map[string][]byte`;
   - `Sync() error`, `Close() error`, `Remove() error`, `Stats(ctx) (*libkv.Stats, error)`, `StatsDetailed(ctx) (*libkv.Stats, error)`;
   - tx methods `Bucket`, `CreateBucket`, `CreateBucketIfNotExists`, `DeleteBucket`, `ListBucketNames` — `Bucket` returns `libkv.ErrBucketNotFound` when absent;
   - bucket methods `Put`, `Get` (a missing key returns `libkv.NewByteItem(key, nil)`, not an error), `Delete`, `Iterator`, `IteratorReverse`;
   - an iterator implementing `Close`, `Item`, `Next`, `Valid`, `Rewind`, `Seek` over a sorted key slice, with `Seek` using `sort.Search` and reversing the comparison for the reverse iterator.

   Document why it exists and why replay is safe: a session-scoped offset store means a restart replays the event topic from the oldest offset, and the downstream effect is idempotent (the emitted task identifier is a deterministic UUID5).

   Add `github.com/bborbe/kv` as a direct dependency — write the files that import it first, then run `go mod tidy` last, which moves it out of the indirect block; it is already in the module graph, as is `github.com/IBM/sarama`.

2. **Add `TerminalClaims`.** New `pkg/terminal_claims.go`:

   ```go
   //counterfeiter:generate -o ../mocks/terminal_claims.go --fake-name TerminalClaims . TerminalClaims

   type TerminalClaims interface {
       // Observe records taskID as having reached a non-progressing state.
       // Idempotent: observing the same identifier twice changes nothing.
       Observe(ctx context.Context, taskID string) error
       // ClaimClearAllowed reports whether taskID has been observed
       // non-progressing and the entitlement to end a claim for it has not yet
       // been consumed. It consumes the entitlement so a second cycle does not
       // end the claim again.
       ClaimClearAllowed(ctx context.Context, taskID string) (bool, error)
   }
   ```

   `NewTerminalClaims(db libkv.DB) TerminalClaims` stores two keys per identifier in bucket `libkv.NewBucketName("terminal_claims")`, creating the bucket with `CreateBucketIfNotExists` on first write: the bare `taskID` (observed) and `taskID + ":cleared"` (entitlement consumed). **Both values must be non-empty** — write `[]byte{1}`, never `nil`: `libkv`'s `byteItem.Exists()` is `len(value) > 0`, so an empty value reads back as absent and `Observe` would be silently inert. `ClaimClearAllowed` reads both under one `Update` transaction and writes the `:cleared` marker when it returns true; it treats `libkv.ErrBucketNotFound` as "not observed" and returns `(false, nil)`, so a fresh process does not log a warning per claimed repo per cycle before any event has been consumed. No counter, no clock, no TTL, no pruning — the store holds one short UUID per non-progressing task the topic has ever carried, which is the accepted bound the spec's Failure Modes table names.

3. **Add the consumer.** New `pkg/terminal_claims_consumer.go`, shaped exactly after the sibling's retry-guard consumer:

   - `terminalClaimsTopic(prefix base.TopicPrefix) libkafka.Topic` returns `agentlib.TaskV1SchemaID.EventTopic(prefix)`. Never a bare literal — a literal subscribes to the wrong topic under a prefix.
   - `isNonProgressingTask(task agentlib.Task) bool` reports whether no Config will dispatch this task again. True when `task.Frontmatter.Status()` is `domain.TaskStatusCompleted` or `domain.TaskStatusAborted`, **or** when `task.Frontmatter.Phase()` is non-nil and equals `domain.TaskPhaseHumanReview`. Comment the two populations and why the second one counts: a record at `human_review` is not running, not queued, and cannot leave that phase on its own — 36 such records exist and every one of them freezes its repo. Explicitly comment that `planning`, `execution` and `ai_review` are dispatchable and must NOT clear, and that `Frontmatter.Phase()` returns a pointer so an absent phase is neither.
   - `NewTerminalClaimsMessageHandler(claims TerminalClaims) libkafka.MessageHandlerTx` decodes `agentlib.Task` from `msg.Value` (`return nil` on `len(msg.Value) == 0` — a delete event), returns `nil` when `isNonProgressingTask` is false, and otherwise calls `claims.Observe(ctx, string(msg.Key))`, logging at `glog.V(2)` (`non-progressing task observed taskID=%s`).
   - `NewTerminalClaimsConsumer(saramaClientProvider libkafka.SaramaClientProvider, db libkv.DB, prefix base.TopicPrefix, claims TerminalClaims) run.Func` wires the offset consumer with `OffsetOldest`, `BatchSize(1)`, the tx-update/tx/skip-errors handler stack, and `log.DefaultSamplerFactory`; it loops with a 5s reconnect delay and returns `nil` only when `ctx` is done. Document why it must never return on a recoverable error: `service.Run` wraps every func in `run.CancelOnFirstFinishWait`, so a return would tear down the poll loop and the HTTP server.

   `domain` is `github.com/bborbe/vault-cli/pkg/domain`, which `agentlib.TaskFrontmatter.Status()` already returns — it is in the module graph through `github.com/bborbe/agent`.

4. **Thread the store into the watcher.** Add a `terminalClaims TerminalClaims` parameter to `NewWatcher` (place it after `currentDateTimeGetter`) and a field of the same name on the `watcher` struct; add the same parameter to `factory.CreateWatcher` and pass it through. Update every call site — `main.go`, the `buildWatcher` closure in `pkg/watcher_test.go`, and the second `pkg.NewWatcher(` call in the `Describe("emission claim (spec 005)")` suite at `pkg/watcher_test.go:1076`. Do not make the field nil-tolerant: every call site passes a real store, so a nil one is a wiring bug rather than a supported state.

5. **Add the fifth clearing path in `emissionInFlight`.** After the target-match guard and after the convergence branch, before the TTL branch:

   ```go
   // The emission's task terminated without converging the repo. The claim's
   // identifier is derived, not stored: markPublished writes LastSeenHeadSHA,
   // LastSeenGoVersion and InFlightTarget together, and a live claim suppresses
   // every later publish, so no later write can move LastSeenHeadSHA out from
   // under a live claim.
   if w.terminalEmission(ctx, repo, state) {
       clearFailedEmission(cursorState, repo.Key(), target)
       return false
   }
   ```

   `terminalEmission` derives `DeriveTaskID(repo.Owner, repo.Name, state.InFlightTarget, state.LastSeenHeadSHA)`, asks the store `ClaimClearAllowed`, and returns false when `state.LastSeenHeadSHA` is empty (no identifier to derive) or the store errors — a store fault must not clear a claim. Log the store error at `glog.Warningf` and count nothing new.

6. **Add `clearFailedEmission`.** Alongside `clearInFlight` in `pkg/watcher.go`:

   ```go
   // clearFailedEmission drops the emission claim for repoKey when it targets
   // target, and additionally drops the sha_unchanged dedup keys. Without the
   // dedup-key drop the re-emit is suppressed by SHAUnchangedFilter — the failed
   // emission did not move HEAD — and this path would be inert. CompletedHeadSHA
   // survives: it is the merge-detection marker, not a dedup key.
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

7. **Bound the end to one per emitted task per process lifetime.** `Observe` is called only by the consumer; the poll cycle calls only `ClaimClearAllowed`, which consumes the entitlement on first use. `Observe` must never clear the `:cleared` marker, so a second terminal event for the same identifier does not re-arm it. A repo whose re-emitted task is the same identifier (HEAD unchanged) and goes terminal again therefore keeps its claim and waits for `InFlightTTL`.

   Rationale to carry in a comment: without the bound, a repo whose task deterministically completes without converging re-emits every poll interval (10m) instead of every 14 days. Note also, in the same comment, that the bound is process-scoped — a restart replays the topic and rebuilds the entitlement, so such a repo re-emits once per restart.

   The consumer goroutine and the poll cycle run concurrently and both touch the store, so the store's own `sync.RWMutex` (requirement 1) is the concurrency guard between them; a terminal event arriving mid-cycle simply lands in the next cycle's read, and needs no separate test beyond the store's own.

8. **Wire the consumer in `main.go`.** Build `saramaClientProvider := kafka.NewSaramaClientProviderNew(kafka.ParseBrokersFromString(a.KafkaBrokers))`, `db := pkg.NewMemDB()`, `claims := pkg.NewTerminalClaims(db)`, pass `claims` to `factory.CreateWatcher`, and pass the consumer as a **third** `run.Func` to the existing `service.Run` call:

   ```go
   return service.Run(
       ctx,
       a.pollLoop(w, gate, pollInterval),
       a.createHTTPServer(sentryClient),
       pkg.NewTerminalClaimsConsumer(saramaClientProvider, db, a.TopicPrefix, claims),
   )
   ```

   `service.Run` already wraps every func in `run.CancelOnFirstFinishWait` (`github.com/bborbe/service` `service_run.go:29`), so this needs no new `run` import and no composition change beyond the extra argument. That is exactly why the consumer must never return on a recoverable error.

   The handler stack is the sibling watcher's, reproduced here so this prompt is self-contained: `libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(provider, topic, libkafka.NewStoreOffsetManager(libkafka.NewOffsetStore(db), libkafka.OffsetOldest, libkafka.OffsetNewest), libkafka.NewMessageHandlerBatchTxUpdate(db, libkafka.NewMessageHandlerBatchTx(libkafka.NewMessageHandlerTxSkipErrors(handler, log.DefaultSamplerFactory))), libkafka.BatchSize(1), run.NewTrigger(), log.DefaultSamplerFactory)` — wrapped in a `for` loop that reconnects after a 5s delay on error and returns `nil` only when `ctx` is done.

9. **Update the docs in the same change.** Use these exact phrasings, because the acceptance criteria grep for them:
   - `docs/dod.md` § In-flight signal contract: the paragraph must contain the word `terminal` and the phrase `once-per-task`. It names four clearing events plus the non-clearing target-change condition (five conditions in total), defines the fifth as *dispatchability* — a terminal status **or** `phase: human_review` — states that a task queued or running at a dispatchable phase does not clear, states the once-per-task bound, and states that the set is rebuilt from the event topic on restart.
   - `README.md`'s `emission_in_flight` row (`:82`): replace "the three clearing events" with the phrase `four clearing events` and name the new one (the emitted task can no longer progress — terminal or parked at `human_review`).
   - `specs/in-progress/005-…md`: Desired Behavior 3 (`:62-65`) gains the phrase `terminal status` as the fifth condition with a pointer to spec 007; the Failure Modes row at `:84` changes its recovery from "TTL expires after 336h" to "the non-progressing event ends the claim on the next cycle; the TTL remains the backstop".

10. **Tests.** Follow the repo's Ginkgo/Gomega conventions (`DescribeTable`/`Entry`, no `for range` + `t.Run`, the injected clock, Counterfeiter for the new interface). Cover, in the existing emission-claim suite:

    - **Failing-first:** a fixture with a live claim, a still-behind `go.mod`, no open PR and a non-progressing observation for the derived identifier produces exactly 1 `PublishCreate`, `filter_skipped_total{reason="emission_in_flight"}` stays 0, and the decoded cursor has an empty `InFlightTarget` **and** an empty `LastSeenHeadSHA`. Confirm this case fails before the fix (0 publishes, counter 1).
    - **A `human_review`-parked task ends the claim too:** the observed task is `status: in_progress` + `phase: human_review`, no open PR → 1 publish, counter 0, `InFlightTarget` empty. This is the 36-record population a terminal-only predicate would miss.
    - **A queued or running task does not:** the observed task is `status: in_progress` + `phase: execution` (and a second case at `status: next` + `phase: planning`) → 0 publishes, counter 1, `InFlightTarget` intact. Without this the widening turns the claim off.
    - **Second sighting does not end the claim:** the entitlement already consumed → 0 publishes, counter 1, `InFlightTarget` intact.
    - **Live claim, no non-progressing observation:** still suppressed.
    - **The four existing conditions still work** — convergence, an observed open update PR, TTL expiry, and a different target version — with their existing assertions untouched.
    - **Negative controls:** a `HasOpenUpdatePR` error does not clear; a forced cycle with a live claim and no non-progressing observation does not emit.
    - **Store errors do not clear:** `ClaimClearAllowed` returning an error leaves the claim intact and the repo suppressed.
    - **Consumer handler:** six events — `completed`, `aborted`, `in_progress`+`human_review`, `in_progress`+`execution`, `next`+`planning`, and an empty-value delete — leave the store reporting observed for the first three and not for the last three, and `ClaimClearAllowed` returns true exactly once for a single-observation identifier and false on a second call. Build each payload by marshalling a real `agentlib.Task` (round-trip through the wire type) rather than hand-writing JSON, so a field or tag mismatch cannot pass. Also assert `terminalClaimsTopic(prefix)` equals `agentlib.TaskV1SchemaID.EventTopic(prefix)` for a non-empty prefix.
    - **Consumer lifecycle:** a handler error is skipped and the loop continues until `ctx` is cancelled, returning `nil`.
    - **Composition:** `main_test.go` is `package main_test` and cannot import `main`, so assert over the source: parse `main.go` with `go/parser`, locate the `service.Run(...)` call, and require its third argument to be a `pkg.NewTerminalClaimsConsumer(...)` call — identity, not a count, so a declared-but-uncalled constructor cannot pass.
    - **The in-process DB satisfies the upstream contract:** `pkg/memdb_test.go` drives `NewMemDB` through the real consumer path — `libkafka.NewOffsetStore(memdb)` puts a key, reads it back in a second `Update` transaction, and confirms it survives — plus a table over the `libkv.DB` surface: `CreateBucketIfNotExists` then `Bucket`, a missing bucket returning `libkv.ErrBucketNotFound`, a missing key returning an empty `Item` and no error, forward and reverse iteration over three keys in key order, and `Seek` landing on the first key ≥ the sought key. This file is a new ~150-line implementation of an upstream interface; the repo DoD requires new functions to have tests, and every consumer behaviour above runs on it.
    - **Self-check.** Before finishing, re-run `<verification>` and confirm every command passes; then walk each acceptance criterion from spec 007 against the change and say in the prompt log which one each piece of code satisfies.

</requirements>

<constraints>
- No vault, task-store or filesystem read. The non-progressing set lives in the process; the event stream is subscribed, not queried. `docs/dod.md:30` is a durable contract.
- No new cursor field, no new chart value, no new PVC, no new config knob, no new GitHub API call.
- Do not change `InFlightTTL`, `DeriveTaskID`, `openUpdatePRGate`'s semantics, or `SHAUnchangedFilter`'s rule. The dedup-key drop happens in the cursor, not in the filter.
- The cursor file must stay readable by an older binary — this change adds no field.
- The consumer must never return on a recoverable error.
- The non-progressing set is exactly two populations: a terminal status, and `phase: human_review`. Do not widen it to `hold`, `next` or `backlog` — those are waits the claim exists to cover. A `human_review` row whose update PR is open never reaches this path, because the pull-request gate runs first.
- Keep `clearInFlight` and `clearFailedEmission` separate functions: they differ in whether the dedup keys survive, and collapsing them would either lose the re-emit or clear dedup state on convergence.
- Every new exported identifier needs a GoDoc comment; no `//nolint` without an explanation.
</constraints>

<verification>
```bash
ROOTDIR=/workspace make precommit    # format + lint + test + security; must exit 0
go test ./pkg/... -count=1           # the Ginkgo suites behind the acceptance criteria
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='emission'   # the claim suite alone
grep -n "terminal\|once-per-task\|four clearing events" docs/dod.md README.md
awk '/service.Run\(/,/\)/' main.go
```

`ROOTDIR=/workspace` is required, not cosmetic: this repo's `Makefile.variables` derives `ROOTDIR` from `git rev-parse --show-toplevel`, and the container masks `.git`, so an unqualified `make` dies before running any target. Never `cd` to a host path — the repo is `/workspace` inside the container.

The failing-first evidence is the RED run's own output: the non-progressing-observation fixture asserts 1 publish and reads 0, with `filter_skipped_total{reason="emission_in_flight"}` at 1. The RED output is recorded in the prompt log, not committed — dark-factory owns the commit and `.git` is masked in this container.
</verification>
