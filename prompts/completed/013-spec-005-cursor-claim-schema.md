---
status: completed
spec: [005-bug-watcher-reemits-while-task-still-queued]
summary: Added an omitempty emission claim (in_flight_target/in_flight_since) to cursor RepoState, written only by markPublished from an injected libtime clock, wired through NewWatcher/CreateWatcher/main.go and covered by cursor and watcher tests.
execution_id: github-update-go-watcher-emission-claim-exec-013-spec-005-cursor-claim-schema
dark-factory-version: v0.198.7
created: "2026-10-27T22:25:43Z"
queued: "2026-10-27T22:35:02Z"
started: "2026-10-27T22:40:23Z"
completed: "2026-10-27T22:44:38Z"
---

<summary>
- The watcher now records, per repo, which Go release it last filed an update task for and when it did so.
- The record lives in the existing persisted state file, so it survives pod restarts with no new store, no new volume and no vault access.
- An entry carrying no record serialises to nothing, so existing state files load unchanged and need no migration.
- A state file written by this version still loads in an older build: the new keys are ignored, not rejected, so a rollback is safe.
- A successful emit writes the record; a failed emit writes nothing, exactly as the commit marker already behaves.
- Nothing reads the record yet — this change is behaviourally inert and every existing test keeps passing.
- The watcher now receives its clock from the process entry point instead of reading the system clock, so the recorded time is testable.
</summary>

<objective>
Persist an emission claim — the target Go version the watcher last filed a task for, plus when — in the per-repo cursor entry, written only by a successful publish. This is the storage half of the fix: it lands first, on its own, because the guard that reads the claim (next prompt) must be able to assume the field exists, is tagged `omitempty`, and is written atomically with the rest of the cursor. After this prompt the watcher behaves exactly as before — the claim is written and never read.
</objective>

<context>
Read `CLAUDE.md` (if present) and `docs/dod.md` — it carries the durable "In-flight signal contract" section this change must not contradict (the docs prompt in this batch updates it; do not edit `docs/` here).

Read these coding plugin docs before writing code (in-container paths, not host paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — the MUST rules this prompt obeys: never `time.Now()` in production code, inject a `libtime.CurrentDateTimeGetter`, never create it inside a factory.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`

Read these repo files before writing code — every anchor below was verified against them:
- `pkg/cursor.go` — `Cursor` (`Repos map[string]*RepoState`), `RepoState` (`LastSeenHeadSHA`, `LastSeenGoVersion` with `json:"last_seen_go_version,omitempty"`, `CompletedHeadSHA` with `json:"completed_head_sha,omitempty"`), `LoadCursor`, `SaveCursor` (atomic temp file + rename, mode 0600).
- `pkg/watcher.go` — `NewWatcher(ghClient GitHubClient, goDevClient GoDevClient, publisher TaskPublisher, completeSender task.CompleteCommandSender, metrics Metrics, cursorPath string, owner string, taskCreationFilter filter.TaskCreationFilter) Watcher`, the `watcher` struct, and `markPublished(cursorState *Cursor, repo Repo, candidate Candidate)` (it carries `CompletedHeadSHA` forward from the previous entry and replaces the whole `RepoState`).
- `pkg/factory/factory.go` — `CreateWatcher(githubHTTPClient *http.Client, goDevHTTPClient *http.Client, sender task.CreateCommandSender, completeSender task.CompleteCommandSender, metrics pkg.Metrics, cursorPath string, owner string, stage string, updateScope string, taskCreationFilter filter.TaskCreationFilter) pkg.Watcher`, which ends in the single `pkg.NewWatcher(...)` call at its bottom.
- `main.go` — `application.Run`, which builds `metrics := pkg.NewMetrics(nil)` and then calls `factory.CreateWatcher(...)`; `main.go` already imports `libtime "github.com/bborbe/time"` for the `BuildDate` field.
- `pkg/watcher_test.go` — the `Describe("watcher")` block: the `var (...)` declaration list, the `BeforeEach` that sets `cursorPath = tmpDir + "/cursor.json"`, and the `buildWatcher` closure that calls `pkg.NewWatcher(...)` with the eight positional arguments.
- `pkg/cursor_test.go` — the `Describe("LoadCursor")` / `Describe("SaveCursor")` shapes, including the "loads a pre-fix cursor entry with no `last_seen_go_version` key" test.
- `pkg/pkg_suite_test.go` — `TestSuite` is the only entry point; it sets `time.Local = time.UTC`.
- `Makefile.precommit` — `precommit: ensure format generate test check addlicense`.

Verified library API facts (do not re-derive from memory):
- `github.com/bborbe/time` (imported as `libtime "github.com/bborbe/time"`): `type CurrentDateTimeGetter interface { Now() DateTime }`; `libtime.NewCurrentDateTime() CurrentDateTime` (which embeds both `CurrentDateTimeGetter` and `CurrentDateTimeSetter{ SetNow(now DateTime) }`); `type DateTime` is a `stdtime.Time` wrapper with `Time() stdtime.Time`, `Format(layout string) string`, `String() string` (RFC3339Nano), `Add(duration HasDuration) DateTime`, `Sub(time HasTime) Duration`.
- `github.com/bborbe/time/test` (imported as `libtimetest "github.com/bborbe/time/test"`): `libtimetest.ParseDateTime(value interface{}) libtime.DateTime` — panics/`Expect`s on a bad value, test-only.
- `libtime.Duration` constants: `libtime.Second`, `libtime.Minute`, `libtime.Hour`, `libtime.Day`, `libtime.Week` (all typed `Duration`); `func (d Duration) Duration() stdtime.Duration`.
- `encoding/json` honours `,omitempty` on a `string` field: an empty string omits the key entirely. (It does NOT omit struct-typed fields — which is why this prompt uses a plain `string`, not `libtime.DateTime`, for the persisted timestamp.)
</context>

<requirements>

### 1. Cursor schema — `pkg/cursor.go`

Add two fields to `RepoState`, directly after `CompletedHeadSHA`:

```go
	// InFlightTarget is the target Go version — three-part form, e.g. "1.27.2",
	// the same string the emitted task carries as latest_go — that this watcher
	// last filed a create-task for on this repo. InFlightSince is when it filed
	// it (RFC3339, UTC). Together they are the emission claim: while a claim
	// still targets the cycle's target version, the repo is not emitted again,
	// which covers the window between emission and the agent opening its update
	// PR — the window the open-PR gate cannot see. Both are omitempty so an
	// entry with no claim serialises to nothing, and a pre-fix cursor file (no
	// such keys) loads with an empty claim, so no migration is needed.
	//
	// A claim is cleared by convergence, by the appearance of an open update PR,
	// or by the InFlightTTL backstop; it is written only by markPublished, so
	// hand-authored "- reemit" repair tasks never carry one.
	InFlightTarget string `json:"in_flight_target,omitempty"`
	InFlightSince  string `json:"in_flight_since,omitempty"`
```

Tags must be exactly `in_flight_target,omitempty` and `in_flight_since,omitempty`. Do not change `LoadCursor`, `SaveCursor`, the atomic temp-file + rename behaviour, the `.corrupt` cold-start path, the `0600` mode, or the existing three fields.

`pkg/cursor.go` gains NO new import — the timestamp is a plain string on the wire.

### 2. Clock injection — `pkg/watcher.go`

`pkg/watcher.go` currently has no time source, and `time.Now()` is forbidden in this package by the project's `go-time/no-time-now-direct` rule. Inject the clock instead of reading it.

Add the import (grouped with the existing third-party imports):

```go
	libtime "github.com/bborbe/time"
```

plus the stdlib `"time"` import (needed for the `time.RFC3339` layout in step 3).

Add the field to the `watcher` struct as the last field:

```go
	currentDateTimeGetter libtime.CurrentDateTimeGetter
```

Append the parameter to `NewWatcher` as the LAST parameter (keep the existing eight in their current order) and assign it in the constructor body:

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
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) Watcher {
```

Extend `NewWatcher`'s doc comment with one sentence: the getter is the cycle's clock, used to stamp the emission claim's `InFlightSince`; it is injected rather than read from the system clock so the recorded time is deterministic under test.

### 3. Write the claim on a successful publish — `pkg/watcher.go` `markPublished`

`markPublished` keeps its signature `func (w *watcher) markPublished(cursorState *Cursor, repo Repo, candidate Candidate)` — it is already a method on `*watcher`, so it reads the clock from `w`. Set both new fields in the `RepoState` it builds:

```go
func (w *watcher) markPublished(cursorState *Cursor, repo Repo, candidate Candidate) {
	if cursorState.Repos == nil {
		cursorState.Repos = make(map[string]*RepoState)
	}
	previous := cursorState.Repos[repo.Key()]
	var completedHeadSHA string
	if previous != nil {
		completedHeadSHA = previous.CompletedHeadSHA
	}
	cursorState.Repos[repo.Key()] = &RepoState{
		LastSeenHeadSHA:   candidate.HeadSHA,
		LastSeenGoVersion: candidate.LatestGo.Number(),
		CompletedHeadSHA:  completedHeadSHA,
		InFlightTarget:    candidate.LatestGo.Number(),
		InFlightSince:     w.currentDateTimeGetter.Now().Format(time.RFC3339),
	}
}
```

Semantics (all mandatory):
- `InFlightTarget` is `candidate.LatestGo.Number()` — the same string written to `LastSeenGoVersion`, so the claim's key and the dedup key agree by construction. Never a literal, never `candidate.LatestGo.Raw`.
- `InFlightSince` is the injected clock formatted with the stdlib `time.RFC3339` layout (NOT `DateTime.String()`, which is RFC3339Nano — the wire format must stay second-resolution so a fixture and a written value compare equal).
- The `CompletedHeadSHA` carry-forward is unchanged. The claim is written only from this function, which is only reached when `publisher.PublishCreate` returned true — a failed publish writes no claim, exactly as it writes no `LastSeenHeadSHA` today.
- Extend `markPublished`'s doc comment with one sentence naming the claim: it also re-arms the emission claim for the cycle's target version, so a repo that just got a task cannot get a second one while that task is still queued.

### 4. Nothing reads the claim yet

Do NOT add any guard, skip reason, metric label, or clearing logic in this prompt. `InFlightTarget` / `InFlightSince` are written and never read; the guard lands in the next prompt. Do not touch `processRepos`, `openUpdatePRGate`, `completeMergedUpdates`, `completeTask`, `gatherCandidate`, `pkg/metrics.go`, `pkg/candidate.go`, or `pkg/filter/`.

### 5. Wiring — `pkg/factory/factory.go`

`CreateWatcher` gains `currentDateTimeGetter libtime.CurrentDateTimeGetter` as its LAST parameter and forwards it as the last argument to `pkg.NewWatcher`. Add `libtime "github.com/bborbe/time"` to the factory's import block. `CreateWatcher` stays pure composition — do NOT call `libtime.NewCurrentDateTime()` inside it (forbidden by `go-time/inject-getter-not-create`).

### 6. Wiring — `main.go`

In `application.Run`, before the `factory.CreateWatcher(...)` call, create the clock once and pass it as the last argument:

```go
	currentDateTime := libtime.NewCurrentDateTime()
	w := factory.CreateWatcher(
		httpClient,
		factory.CreateGoDevHTTPClient(),
		sender,
		completeSender,
		metrics,
		a.CursorPath,
		a.Owner,
		a.Stage,
		a.UpdateScope,
		factory.CreateStaticFilters(allowlist),
		currentDateTime,
	)
```

`main.go` already imports `libtime`; no new import there. This is the only `CreateWatcher` call site — verify with `grep -rn "CreateWatcher" main.go pkg/` and confirm nothing else needs updating.

### 7. Test wiring — `pkg/watcher_test.go`

The `buildWatcher` closure calls `pkg.NewWatcher(...)` with eight arguments; add the ninth.

Declare the clock in the `Describe("watcher")` `var (...)` block:

```go
		currentDateTime libtime.CurrentDateTime
```

and create it in the existing `BeforeEach` (alongside `metrics = &mocks.Metrics{}`):

```go
		currentDateTime = libtime.NewCurrentDateTime()
```

Pass `currentDateTime` as the last argument in `buildWatcher`. Imports to add to the test file: `libtime "github.com/bborbe/time"` and `libtimetest "github.com/bborbe/time/test"`. Do NOT add stdlib `"time"` — the assertions compare the literal `"2026-10-27T12:00:00Z"`, so an unused import would fail the build. Every existing test must keep passing unchanged — the claim is written but never read, so no existing expectation moves.

### 8. Cursor tests — `pkg/cursor_test.go`

Add these to the existing `Describe` blocks (Ginkgo/Gomega, same style as the neighbouring specs):

1. **Round-trip both claim fields.** Save a `Cursor` whose single `RepoState` carries `LastSeenHeadSHA: "abc"`, `InFlightTarget: "1.27.2"`, `InFlightSince: "2026-10-27T12:00:00Z"`; load it; assert both claim fields decode back exactly. Then read the raw file bytes and assert they contain the exact wire keys `"in_flight_target":"1.27.2"` and `"in_flight_since":"2026-10-27T12:00:00Z"` — the field-name contract, not just the round-trip. A misspelled `json` tag round-trips fine but silently breaks the fixtures in the next prompt and the spec's AC1.
2. **An empty claim serialises to nothing.** Save a `RepoState` with `LastSeenHeadSHA: "abc"` and both claim fields empty; read the raw file bytes and assert the JSON contains neither `in_flight_target` nor `in_flight_since`. This is the AC's "the serialised JSON has no `in_flight_target` on its object" check, done at the file boundary rather than on a hand-built string.
3. **A pre-fix cursor loads with an empty claim.** Write `{"repos":{"github.com/bborbe/a":{"last_seen_head_sha":"abc","last_seen_go_version":"1.27.2"}}}`; `LoadCursor` succeeds and both claim fields are `""`.
4. **Forward compatibility — an older binary reading a newer cursor.** Add `"encoding/json"` to `pkg/cursor_test.go`'s import block (it is not imported there today). Declare a local struct in the test that carries ONLY the pre-existing keys, e.g.

```go
		type legacyRepoState struct {
			LastSeenHeadSHA   string `json:"last_seen_head_sha"`
			LastSeenGoVersion string `json:"last_seen_go_version,omitempty"`
			CompletedHeadSHA  string `json:"completed_head_sha,omitempty"`
		}
		type legacyCursor struct {
			Repos map[string]*legacyRepoState `json:"repos"`
		}
```

   then `json.Unmarshal` a cursor JSON that DOES carry `in_flight_target` / `in_flight_since` into `legacyCursor` and assert: no error, the pre-existing fields (`LastSeenHeadSHA`, `LastSeenGoVersion`) survive, and re-marshalling `legacyCursor` back to JSON still carries `last_seen_head_sha` / `last_seen_go_version` (the spec AC9 "re-encoding preserves the pre-existing fields" half). This proves the additive-change constraint — an older pod ignores the new keys instead of failing.

### 9. Watcher tests — `pkg/watcher_test.go`

Add one `Context("emission claim write (spec 005)", ...)` under `Describe("Poll")` with the standard happy-path `BeforeEach` used by the neighbouring contexts (one allowlisted repo `github.com/bborbe/disk-status`, `GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)`, `GetGoModReturns([]byte("go 1.24"), nil)`, `GetMaintainerConfigReturns(filter.GrantedConsent, nil)`, `LatestStable` at `1.26.6`, `metrics.IncFilterSkippedStub = func(string) {}`), plus a fixed clock:

```go
		currentDateTime.SetNow(libtimetest.ParseDateTime("2026-10-27T12:00:00Z"))
```

Set inside that context's `BeforeEach`, before `Poll` runs.

- **"a successful publish records the claim"** — `publisher.PublishCreateReturns(true)`; `Poll(ctx, false)`; `LoadCursor` the file and assert the entry for `github.com/bborbe/disk-status` has `InFlightTarget == "1.26.6"` (the cycle's resolved target, matching `LastSeenGoVersion`) and `InFlightSince == "2026-10-27T12:00:00Z"`. Assert on the decoded struct, not a file-wide `ContainSubstring`.
- **"a failed publish records no claim"** — `publisher.PublishCreateReturns(false)`; `Poll(ctx, false)`; the decoded entry (or the raw file, if no entry exists at all) carries an empty `InFlightTarget` and no `in_flight_target` key.

</requirements>

<constraints>
- The task identity seed is NOT changed (`pkg/taskid.go` untouched).
- The open-PR gate, the `sha_unchanged` filter and every existing skip reason keep their current semantics; this prompt adds storage only.
- No vault, task-store or filesystem read outside the existing cursor file — `docs/dod.md:30` is a durable contract.
- The cursor file stays readable by an older binary: additive change only, both new keys `omitempty`.
- No new external dependency — `github.com/bborbe/time` is already a direct dependency (`go.mod:18`) and already used by `main.go`.
- Never call `time.Now()` in `pkg/` — inject and read `libtime.CurrentDateTimeGetter` (`go-time/no-time-now-direct`, `go-time/inject-getter-not-create`). Never create the getter inside `pkg/factory/`.
- Do NOT add a guard, a skip reason, a metric label, or any clearing logic in this prompt. The claim is written and never read here.
- Do NOT touch `pkg/metrics.go`, `pkg/candidate.go`, `pkg/filter/`, `pkg/taskbuilder.go`, `pkg/taskid.go`, `pkg/taskpublisher.go`, `pkg/githubclient.go`, `docs/`, `README.md`, or `CHANGELOG.md`.
- Never use `fmt.Errorf` — all errors go through `github.com/bborbe/errors` and carry ctx. (No new error path is expected in this prompt.)
- Never hand-edit anything under `mocks/` — `make precommit` regenerates that directory from the `//counterfeiter:generate` directives. `Watcher` and `Metrics` interfaces are unchanged here, so the regenerated mocks are identical.
- Tests use Ginkgo/Gomega; the suite's only entry point is `TestSuite` in `pkg/pkg_suite_test.go` (a `-run <Describe>` selector matches nothing and exits 0 — never use one).
- Keep every line under 100 characters and every function under 80 lines / 50 statements.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root:

```
make precommit
```

Must exit 0 (format + generate + test + lint/vet/vuln/osv/trivy + addlicense).

```
go test ./pkg/... -count=1
```

Must exit 0.

```
grep -n "in_flight_target\|in_flight_since" pkg/cursor.go
```

Must show both JSON tags.

```
grep -n "currentDateTimeGetter" pkg/watcher.go pkg/factory/factory.go
```

Must match in both files (struct field, constructor parameter and assignment in `pkg/watcher.go`; parameter and forwarded argument in `pkg/factory/factory.go`).

```
grep -n "NewCurrentDateTime" main.go
```

Must print exactly one match — the clock is created once in `application.Run`, never inside the factory.

```
grep -rn "time.Now()" pkg/ --include='*.go'
```

Must print nothing (the injected getter is the only clock; `main.go` creates it via `libtime.NewCurrentDateTime()`).

```
go test ./pkg/... -count=1 -run TestSuite -ginkgo.focus='cursor'
```

Must exit 0 and report a non-zero number of specs run (the AC's cursor-compatibility evidence; the focus string is the only one that works, `-run Cursor` matches nothing).
</verification>
