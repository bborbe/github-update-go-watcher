---
status: completed
spec: [004-bug-sha-unchanged-skips-new-go-release]
summary: Made the watcher's dedup two-key (HEAD SHA + target Go version) and folded the Go version into the update-task identifier, with regenerated mock, tests and doc updates.
execution_id: github-update-go-watcher-exec-012-spec-004-go-version-aware-dedup
dark-factory-version: v0.196.0
created: "2026-10-09T14:52:51Z"
queued: "2026-10-09T15:04:51Z"
started: "2026-10-09T15:12:23Z"
completed: "2026-10-09T15:20:41Z"
---

# Make the watcher's dedup two-key: HEAD SHA plus target Go version

<summary>
- A repo is re-checked for a new Go release even when its last commit has not moved.
- The watcher now remembers which Go release it last acted on for each repo, beside the commit it last acted on.
- A repo is left alone only when BOTH the commit and the Go release are unchanged since the last successful emit.
- The identity of a filed work item now includes the target Go release, so a new release produces a genuinely new work item rather than one the pipeline already absorbed.
- When an update PR merges, the watcher still closes exactly the work item it filed — matched by the same commit and the same Go release.
- A repo whose stored state predates this change is re-checked once instead of being skipped forever; that one-off burst is the recovery path for the repos currently stalled.
- A repo whose commit and Go release are both unchanged is still skipped with the same skip reason as before, and publishes nothing.
- The open-update-PR in-flight gate, the allowlist, the consent gate, the go.mod parsing and the version comparison are untouched; no new config knob.
- Two documented contracts (the work-item identifier and the skip-reason table) are updated in the same change so the docs do not drift from the code.
</summary>

<objective>
Stop the watcher from permanently skipping a repo that is genuinely behind on Go just because its HEAD has not moved since the last emit. A new stable Go release moves no repo's HEAD, so the HEAD-only dedup key is unrelated to the unit of work (the release): the repo passes the "behind" gate, is killed by the skip, and never re-enters the pipeline. Make the skip two-key (HEAD SHA **and** target Go version) and fold the target Go version into the work-item identity, so a new release re-drives a stalled repo while a re-emit at an unchanged (HEAD, version) pair stays a downstream no-op. Land all four code surfaces plus the regenerated mock, the tests and the two doc contracts as one atomic change — a partial landing is worse than the bug.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` — it carries the "In-flight signal contract" section this change must update.

Read these coding plugin docs before writing code (in-container paths, not host paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-filter-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read these repo files before writing code — every anchor below was verified against them:
- `pkg/cursor.go` — `Cursor`, `RepoState` (`LastSeenHeadSHA`, `CompletedHeadSHA` with `json:"completed_head_sha,omitempty"`), `LoadCursor`, `SaveCursor`.
- `pkg/cursorreader.go` — `NewCursorReader(c *Cursor) filter.CursorReader` and the `cursorReader` struct with the nil-safe `LastSeenSHA(repoKey string) string`.
- `pkg/filter/filter.go` — the `Candidate` struct, the frozen chain-order package doc (position 6 is `SHAUnchangedFilter`), and the import-cycle constraint: this package must NEVER import `pkg`.
- `pkg/filter/sha_unchanged_filter.go` — the local `CursorReader` interface and `NewSHAUnchangedFilter(cursor CursorReader) TaskCreationFilter`.
- `pkg/candidate.go` — `Candidate` (`Repo`, `HeadSHA`, `CurrentGo`, `LatestGo`, `Consent`), `GoBehind()`, `FilterCandidate() filter.Candidate`.
- `pkg/version.go` — `Version` and `Number() string` (three-part form without the `go` prefix, e.g. `1.27.2`; this is the exact string the emitted task carries as `latest_go`).
- `pkg/taskid.go` — `taskIDNamespace`, `DeriveTaskID(owner, repo, headSHA string) uuid.UUID` (seed `update-go-<owner>-<repo>-<headSHA>`), `DeriveDecisionTaskID`.
- `pkg/taskbuilder.go` — `BuildCreateCommand(c Candidate, cfg TaskConfig) task.CreateCommand`, which seeds the identifier and sets `"latest_go": c.LatestGo.Number()`.
- `pkg/watcher.go` — `Poll` (builds `cycleFilter`; appends `filter.NewSHAUnchangedFilter(NewCursorReader(cursorState))` only when `!force`), `processRepos` (the cursor write on a successful `PublishCreate`), `openUpdatePRGate`, `gatherCandidate`, `completeMergedUpdates` (its suppression guard is `state.LastSeenHeadSHA == state.CompletedHeadSHA`), `completeTask`.
- `pkg/githubclient.go` — `GetMergedUpdatePR(ctx, repo, headSHA)` matches a merged PR by the SHA-derived head branch (`updateBranchName(headSHA)` = `fix/update-go-<sha[:7]>`) with `state=all`; `HasOpenUpdatePR` matches the branch prefix with `state=open`.
- `pkg/metrics.go` — `FilterSkipReasons` (the closed label set; `"sha_unchanged"` stays in it) and `IncFilterSkipped(reason string)`.
- `pkg/pkg_suite_test.go` — the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` line; `pkg/filter/sha_unchanged_filter.go` carries the `//counterfeiter:generate -o ../../mocks/cursor_reader.go --fake-name CursorReader . CursorReader` directive.
- `Makefile.precommit` — the `generate` target (`rm -rf mocks && mkdir -p mocks && echo "package mocks" > mocks/mocks.go && go generate -mod=mod ./...`) that regenerates `mocks/` from the directives.
- `README.md` — the "Skip reasons" table and the "Emitted task contract" table (`task_identifier` row).
- `CHANGELOG.md` — it already has a `## Unreleased` section with two `- chore:` bullets.

Verified library API facts (do not re-derive from memory):
- `github.com/bborbe/errors` — `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Never `fmt.Errorf`.
- `github.com/google/uuid` — `uuid.NewSHA1(namespace uuid.UUID, data []byte) uuid.UUID`; `taskIDNamespace` is already `uuid.MustParse`-ed in `pkg/taskid.go`.
- `encoding/json` honours `,omitempty` on a struct field, so an absent `last_seen_go_version` key unmarshals to `""` with no code change to `LoadCursor`.
</context>

<requirements>

### 1. Cursor schema — `pkg/cursor.go`

Add a third field to `RepoState`, directly after `LastSeenHeadSHA` and before `CompletedHeadSHA`:

```go
	// LastSeenGoVersion is the cycle's resolved stable Go version at the moment
	// LastSeenHeadSHA was recorded, in the three-part form the emitted task
	// carries as latest_go (e.g. "1.27.2"). Together with LastSeenHeadSHA it is
	// the two-key dedup input: a new Go release moves no repo's HEAD, so a
	// HEAD-only key would skip a repo that is genuinely behind. omitempty so a
	// pre-fix cursor file (no such key) still loads — an absent value
	// deserialises to "" and therefore never equals a real version, so the repo
	// is re-evaluated once.
	LastSeenGoVersion string `json:"last_seen_go_version,omitempty"`
```

The JSON tag must be exactly `last_seen_go_version,omitempty`. Do not change `LoadCursor`, `SaveCursor`, the atomic temp-file + rename behaviour, the `.corrupt` cold-start path, or the `0600` mode.

### 2. CursorReader implementation — `pkg/cursorreader.go`

Add `LastSeenGoVersion(repoKey string) string` to `cursorReader`, mirroring `LastSeenSHA`'s nil-safety exactly (nil cursor, nil `Repos`, missing key, nil `RepoState` under the key all return `""`):

```go
func (r *cursorReader) LastSeenGoVersion(repoKey string) string {
	if r.c == nil || r.c.Repos == nil {
		return ""
	}
	entry := r.c.Repos[repoKey]
	if entry == nil {
		return ""
	}
	return entry.LastSeenGoVersion
}
```

`NewCursorReader`'s signature and return type are unchanged — it still returns `filter.CursorReader`.

### 3. Filter input — `pkg/filter/filter.go`

Add a field to `Candidate` (place it directly after `HeadSHA`):

```go
	// LatestGoVersion is the cycle's resolved stable Go version in three-part
	// form (e.g. "1.27.2") — the same value the emitted task carries as
	// latest_go. A plain string, not pkg.Version, so this package still never
	// imports pkg.
	LatestGoVersion string
```

Also update the frozen chain-order package doc: the position-6 line currently reads `//  6. SHAUnchangedFilter   -> "sha_unchanged"        — HEAD already reported`. Change the trailing prose to name both inputs (`— HEAD and target Go version already reported`). Keep the line's leading `//  6. SHAUnchangedFilter   -> "sha_unchanged"` and the `"sha_unchanged"` label string exactly as they are.

Do NOT rename `TaskCreationFilter`, `TaskCreationFilterFunc`, `TaskCreationFilterList`, or `Skip`. The chain order is frozen.

### 4. Two-key skip — `pkg/filter/sha_unchanged_filter.go`

Extend the local `CursorReader` interface with a second method, and keep `LastSeenSHA` unchanged:

```go
type CursorReader interface {
	// LastSeenSHA returns the recorded HEAD for repoKey, or "" if unseen.
	LastSeenSHA(repoKey string) string
	// LastSeenGoVersion returns the recorded target Go version for repoKey, or
	// "" if unseen — or if the entry predates the field.
	LastSeenGoVersion(repoKey string) string
}
```

Make the predicate two-key. The constructor name `NewSHAUnchangedFilter` and the returned reason string `"sha_unchanged"` are retained; only the doc comments change to name both inputs:

```go
func NewSHAUnchangedFilter(cursor CursorReader) TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if candidate.HeadSHA != "" &&
			candidate.HeadSHA == cursor.LastSeenSHA(candidate.RepoKey) &&
			candidate.LatestGoVersion == cursor.LastSeenGoVersion(candidate.RepoKey) {
			return "sha_unchanged"
		}
		return ""
	})
}
```

Semantics (all mandatory):
- The reason is returned only when the candidate's HEAD equals the recorded HEAD **and** the candidate's target Go version equals the recorded Go version.
- A repo whose HEAD is unchanged but whose stable Go advanced is NOT skipped — it proceeds to emit.
- A legacy cursor entry (recorded HEAD, no recorded Go version → `""`) never equals a real target version, so it is NOT skipped. Do not add a `!= ""` guard on `candidate.LatestGoVersion`: plain equality on both keys is the contract, and a `""`-vs-`""` match only arises when both sides are empty, which cannot happen on the production path (`Candidate.LatestGo` is always resolved once per cycle and `Number()` always returns a three-part string).
- A forced cycle still omits this filter from the chain entirely (`pkg/watcher.go` `Poll`); every other gate still applies.

`gofmt`/`golines` (max-len 100) will wrap the boolean chain across lines — that is expected and correct.

### 5. Projection onto the filter input — `pkg/candidate.go`

In `FilterCandidate()`, add the new key, populated from the cycle's resolved version (never a literal):

```go
	return filter.Candidate{
		RepoKey:         c.Repo.Key(),
		HeadSHA:         c.HeadSHA,
		LatestGoVersion: c.LatestGo.Number(),
		GoModPresent:    c.GoModPresent,
		GoModParsable:   c.GoModParsable,
		GoBehind:        c.GoBehind(),
		Consent:         c.Consent,
	}
```

Leave `Candidate`, `ShortSHA`, `GoBehind` and `FilterCandidate`'s other keys unchanged.

### 6. Work-item identity — `pkg/taskid.go`

Change `DeriveTaskID` to take the target Go version as well, and fold it into the seed. The frozen parameter list and seed format are:

```go
// DeriveTaskID returns a UUID5 derived deterministically from
// (owner, repo, goVersion, headSHA) via the seed
// "update-go-<owner>-<repo>-<goVersion>-<headSHA>".
//
// Same repo at the same HEAD and the same target Go version always yields the
// same identifier, so a re-emit is a downstream no-op; a new HEAD OR a new
// target Go version yields a new identifier, so a new commit and a new stable
// Go release each correctly produce a fresh work item.
func DeriveTaskID(owner, repo, goVersion, headSHA string) uuid.UUID {
	seed := fmt.Sprintf("update-go-%s-%s-%s-%s", owner, repo, goVersion, headSHA)
	return uuid.NewSHA1(taskIDNamespace, []byte(seed))
}
```

Parameter order is `(owner, repo, goVersion, headSHA)`; the seed order matches. `taskIDNamespace` and `DeriveDecisionTaskID` are untouched — the decision-task identity stays `(owner, repo)` only.

### 7. Create pass — `pkg/taskbuilder.go`

In `BuildCreateCommand`, seed the identifier with the same target Go version the command carries as `latest_go`:

```go
	taskIDStr := DeriveTaskID(c.Repo.Owner, c.Repo.Name, c.LatestGo.Number(), c.HeadSHA).String()
```

The emitted command shape is otherwise unchanged: the same twelve frontmatter keys with the same values, the same title form (`ComputeTaskTitle`), the same body bytes, and the conditional `update_scope` key. Do not change `buildFrontmatter`, `buildTaskBody`, `ComputeTaskTitle`, `BuildDecisionCommand` or `ComputeDecisionTaskTitle`.

### 8. Watcher wiring — `pkg/watcher.go`

**8a. The cursor write in `processRepos`.** It must record the cycle's resolved Go version and must NOT wipe the completion marker. Replace the current `cursorState.Repos[repo.Key()] = &RepoState{LastSeenHeadSHA: candidate.HeadSHA}` block with:

```go
		if w.publisher.PublishCreate(ctx, candidate) {
			if cursorState.Repos == nil {
				cursorState.Repos = make(map[string]*RepoState)
			}
			// Carry the prior completion marker forward. This cycle's
			// merge-detection pass runs right after processRepos and is
			// suppressed only by LastSeenHeadSHA == CompletedHeadSHA; a fresh
			// RepoState would wipe that marker and the pass would publish a
			// CompleteCommand for the task this cycle just filed (see the
			// reviewer note below).
			previous := cursorState.Repos[repo.Key()]
			var completedHeadSHA string
			if previous != nil {
				completedHeadSHA = previous.CompletedHeadSHA
			}
			cursorState.Repos[repo.Key()] = &RepoState{
				LastSeenHeadSHA:   candidate.HeadSHA,
				LastSeenGoVersion: candidate.LatestGo.Number(),
				CompletedHeadSHA:  completedHeadSHA,
			}
		}
```

The value MUST come from the cycle's resolved version via `candidate.LatestGo.Number()` (not a literal, and not a second lookup) — `Candidate.LatestGo` is set once per cycle in `gatherCandidate` from `goDevClient.LatestStable`, and it is the exact value `BuildCreateCommand` folds into the identifier, so the recorded key and the filed identifier agree by construction. It is written only on a successful `PublishCreate` — a failed publish leaves the repo re-evaluable next cycle.

<!--
REVIEWER NOTE — open question to confirm before approving.

The spec's Desired Behavior 1 says the new field is "written in the same place
LastSeenHeadSHA is written today". Read literally as "replace the whole
RepoState literal", that drops CompletedHeadSHA — which is what the code does
today. Requirement 8a deliberately does NOT do that; it carries the prior
CompletedHeadSHA forward. Reason, verified against the source at prompt-authoring
time:

  * pkg/watcher.go's Poll runs completeMergedUpdates immediately after
    processRepos, in the same cycle, over the same cursor object.
  * Its only suppression guard is `state.LastSeenHeadSHA == state.CompletedHeadSHA`.
  * pkg/githubclient.go's GetMergedUpdatePR matches a merged PR by the
    SHA-derived head branch `fix/update-go-<sha[:7]>` alone (state=all), so for a
    repo whose HEAD has not moved, the OLD merged PR still matches.

So with a fresh RepoState the guard is defeated and the same cycle publishes a
CompleteCommand for the task it just filed: the re-emission appears in the logs
and the vault while producing no work — the exact failure class this spec exists
to remove ("looks like progress while producing no work").

Accepted residual limitation, also for the reviewer: with the marker carried
forward, a re-filed release at an unchanged HEAD will not auto-complete, because
the guard stays HEAD-only and the merge-detection pass is frozen by the spec's
Constraints. The task then sits in human_review for the existing close-sweep —
the documented fallback the merge-detection pass was built to shorten. Making the
completion marker version-aware means changing the merge-detection pass, which
the spec's Constraints freeze; that is a follow-up spec, not this change. Do not
widen scope here — if the reviewer prefers the literal reading, the alternative
is to accept same-cycle premature completion.

Second, smaller window, NOT closed by carrying the marker forward: on a cold
start — a missing cursor file, the `.corrupt` cold-start path, or a lost cursor
write — the marker is "" rather than a stale HEAD, so a (HEAD unchanged, Go
advanced) publish still writes CompletedHeadSHA "" and the same-cycle premature
completion still fires. Carrying the marker forward only helps when it was
already the recorded HEAD. Closing this needs the frozen merge-detection pass
changed, so it is a follow-up spec too; it is called out here so the reviewer
does not read requirement 8a as a complete fix for the same-cycle completion
hazard. Requirement 10f.3's carried-forward-marker test covers the case this
change does close.
-->

**8b. `completeTask`.** Derive the identifier with the version recorded at create time, so a merged update PR closes the exact task the watcher filed:

```go
	taskID := DeriveTaskID(repo.Owner, repo.Name, state.LastSeenGoVersion, headSHA)
```

Keep `completeTask`'s signature, its `CompleteCommand` shape, its metrics, its log lines, and its in-place `state.CompletedHeadSHA = headSHA` marker update unchanged.

**8c. Everything else in `pkg/watcher.go` is untouched.** Specifically: `Poll`'s structure (including the `force` handling that omits the skip filter), `openUpdatePRGate` and its three verdicts, `gatherCandidate`, `dropRepo` (keep the phrase "repo dropped from cycle" verbatim), `completeMergedUpdates`' structure and its `state.LastSeenHeadSHA == state.CompletedHeadSHA` guard, and `GetMergedUpdatePR`'s call site. No new opt-out knob, no new config field, no new metric label, no rename of the `sha_unchanged` label.

### 9. Regenerate the mock

`mocks/cursor_reader.go` is generated. Run `ROOTDIR=/workspace make precommit` from the repo root; its `generate` target (`rm -rf mocks avro && mkdir -p mocks && echo "package mocks" > mocks/mocks.go && go generate -mod=mod ./...`) regenerates the fake from the `//counterfeiter:generate` directive in `pkg/filter/sha_unchanged_filter.go`, so `LastSeenGoVersion` appears on `mocks.CursorReader` automatically. NEVER hand-edit anything under `mocks/`.

### 10. Tests

All tests use Ginkgo v2 / Gomega. Update the existing tests that the signature and behaviour changes break, and add the new coverage below.

**10a. `pkg/taskid_test.go`** — update every existing `pkg.DeriveTaskID(...)` call to the four-argument form, and add:
- identical identifier when all four inputs are equal;
- different identifier when ONLY the Go version differs (`1.26.6` vs `1.27.2`, same owner/repo/HEAD) — this is the regression guard for the whole fix;
- different identifier when only the SHA differs (already present, keep it);
- `VERSION_5` (already present, keep it).

**10b. `pkg/filter/filter_test.go`** — the local `fakeCursor` must satisfy the extended `filter.CursorReader`. Add a `goVersions map[string]string` field and the matching method (a read from a nil map returns `""` in Go, so the existing `&fakeCursor{shas: ...}` and `&fakeCursor{}` literals keep compiling):

```go
func (f *fakeCursor) LastSeenGoVersion(repoKey string) string {
	return f.goVersions[repoKey]
}
```

Keep the existing `SHAUnchangedFilter` specs passing (the "matching SHA returns sha_unchanged" spec passes `filter.Candidate{RepoKey: ..., HeadSHA: "abc123"}` with no version, and the fake records no version, so both sides are `""` and the two-key predicate still skips — do not weaken the predicate to make this pass). Add:
- same HEAD + same Go version → `"sha_unchanged"`;
- same HEAD + advanced Go version → `""` (NOT skipped);
- same HEAD + recorded Go version empty (legacy entry) + non-empty candidate version → `""` (NOT skipped).

**10c. `pkg/cursorreader_test.go`** — add `LastSeenGoVersion` specs mirroring the existing `LastSeenSHA` matrix: nil cursor → `""`; cursor with nil `Repos` → `""`; missing key → `""`; nil `RepoState` under the key → `""`; present key with a recorded version → that version.

**10d. `pkg/cursor_test.go`** — add:
- a `SaveCursor` → `LoadCursor` round-trip that preserves `LastSeenGoVersion`;
- a legacy-shape load: write `{"repos":{"github.com/bborbe/a":{"last_seen_head_sha":"abc"}}}` (no `last_seen_go_version` key), load it, and assert the entry's `LastSeenGoVersion` is `""` and its `LastSeenHeadSHA` is `"abc"` — the pre-fix on-disk shape must load unchanged.

**10e. `pkg/taskbuilder_test.go`** — update the `task_identifier is derived from owner/repo/HEAD` spec's expected value to the four-argument call, using the candidate's `LatestGo.Number()` (`"1.26.6"` in that fixture). No other assertion in that file changes: the emitted command shape is unchanged.

**10f. `pkg/watcher_test.go`** — this is where the acceptance criteria are proven. Required changes and additions:

1. **Fix the `forced cycle bypasses SHAUnchangedFilter` context's cursor fixture.** It currently writes `{"repos":{"github.com/bborbe/disk-status":{"last_seen_head_sha":"d630ef3526cfc57fbdccd9ba53c5c3a02945e407"}}}`. That is now a legacy entry and would be re-emitted, so the spec `force=false does not publish` would fail. Add `"last_seen_go_version":"1.26.6"` to the fixture (the cycle's resolved stable version in that context) so the spec keeps its meaning: `force=false` publishes nothing when both keys match, `force=true` still publishes. Leave the third spec (`force=true still respects consent gate`) as is.

2. **Update the merge-detection context's expected identifier** from `pkg.DeriveTaskID("bborbe", "disk-status", headSHA)` to `pkg.DeriveTaskID("bborbe", "disk-status", "1.26.6", headSHA)` — the cursor entry was written this cycle with `candidate.LatestGo.Number()` = `"1.26.6"`.

3. **Add a `Context("two-key dedup (spec 004)", ...)`** with a `BeforeEach` that sets up the full happy path: one allowlisted repo `github.com/bborbe/disk-status`, `GetHeadSHA` → a fixed SHA, `GetGoMod` → a directive behind stable, `GetMaintainerConfig` → `filter.GrantedConsent`, `publisher.PublishCreateReturns(true)`, `metrics.IncFilterSkippedStub = func(string) {}`, `buildWatcher()`. Cover:
   - **same HEAD + advanced Go version → NOT skipped, publishes a NEW identifier.** Pre-seed `cursorPath` with `{"repos":{"github.com/bborbe/disk-status":{"last_seen_head_sha":"<H>","last_seen_go_version":"1.26.6"}}}` and set `LatestStable` to `1.27.2` (go.mod `go 1.26.6`). `Poll(ctx, false)` → `PublishCreateCallCount() == 1`, and the identifier actually emitted differs from the pre-advance one: take `publisher.PublishCreateArgsForCall(0)`'s `pkg.Candidate`, build `pkg.BuildCreateCommand(candidate, pkg.TaskConfig{Stage: "prod"})`, and assert its `TaskIdentifier` is NOT equal to `pkg.DeriveTaskID("bborbe", "disk-status", "1.26.6", "<H>").String()`. Also assert the captured candidate's `LatestGo.Number()` is `"1.27.2"`.
   - **a (HEAD unchanged, Go advanced) publish does NOT complete the task it just filed — the regression guard for requirement 8a's carried-forward marker.** Pre-seed `cursorPath` with `{"repos":{"github.com/bborbe/disk-status":{"last_seen_head_sha":"<H>","last_seen_go_version":"1.26.6","completed_head_sha":"<H>"}}}`, set `LatestStable` to `1.27.2` (go.mod `go 1.26.6`), and stub `ghClient.GetMergedUpdatePRReturns(true, nil)`. `Poll(ctx, false)` → `PublishCreateCallCount() == 1` **and** `completeSender.SendCommandCallCount() == 0`. Without the carried-forward marker the merge-detection pass in the same cycle sees `CompletedHeadSHA == ""`, the `LastSeenHeadSHA == CompletedHeadSHA` guard is defeated, `GetMergedUpdatePR(repo, H)` matches the repo's old merged PR, and the cycle publishes a `CompleteCommand` for the identifier it just created — a re-emission that shows up in the logs and the vault while producing no work.
   - **same HEAD + same Go version → skipped, zero publishes.** Pre-seed the cursor with `<H>` and `"1.27.2"`, set `LatestStable` to `1.27.2` and go.mod to `go 1.27.1` (still behind, so the only reason not to emit is the skip). `Poll(ctx, false)` → `PublishCreateCallCount() == 0` and `metrics.IncFilterSkippedArgsForCall(0) == "sha_unchanged"`.
   - **legacy cursor entry (recorded HEAD, no recorded Go version) → NOT skipped, publishes.** Pre-seed `{"repos":{"github.com/bborbe/disk-status":{"last_seen_head_sha":"<H>"}}}`, same HEAD, `LatestStable` ahead. `Poll(ctx, false)` → `PublishCreateCallCount() == 1`. This is the one-off recovery path for the stalled repos.
   - **the cursor entry written on a successful publish records the cycle's resolved version.** Set `LatestStable` to `1.27.2`, no pre-seeded cursor, `Poll(ctx, false)`, then read `cursorPath` and assert the raw JSON contains `"last_seen_go_version":"1.27.2"` (not `""`).
   - **the filter boundary is traversed through the real cursor reader, not a fake.** In the `pkg` test package, build `&pkg.Cursor{Repos: map[string]*pkg.RepoState{"github.com/bborbe/disk-status": {LastSeenHeadSHA: "<H>"}}}` (no Go version), wrap it with `pkg.NewCursorReader`, run `filter.NewSHAUnchangedFilter(reader).Skip(filter.Candidate{RepoKey: "github.com/bborbe/disk-status", HeadSHA: "<H>", LatestGoVersion: "1.27.2"})`, and assert the reason is `""`. This exercises the extended `CursorReader` implementation rather than a hand-written double.

4. **Completion still matches creation.** In the merge-detection context, strengthen the existing "publishes a CompleteCommand for a merged update PR" spec: capture `publisher.PublishCreateArgsForCall(0)`'s candidate, build `pkg.BuildCreateCommand(candidate, pkg.TaskConfig{Stage: "prod"})`, and assert `string(completeSender.SendCommandArgsForCall(0))`'s `TaskIdentifier` equals `string(createCmd.TaskIdentifier)`. The create pass and the completion pass must derive the same identifier in the same cycle.
   Also add: **a legacy cursor entry derives a completion identifier that matches no filed task, and is absorbed downstream.** Pre-seed `cursorPath` with `{"repos":{"github.com/bborbe/disk-status":{"last_seen_head_sha":"<H>"}}}` (no `last_seen_go_version`), stub `GetMergedUpdatePRReturns(true, nil)`, `Poll(ctx, false)`, and assert the emitted `CompleteCommand`'s `TaskIdentifier` equals `pkg.DeriveTaskID("bborbe", "disk-status", "", "<H>").String()`. The empty version is the documented pre-fix shape: the completion is a no-op against a task that was never filed under that identity, which is why the next successful emit recording the version is what makes completions match again.

Existing contexts that must keep passing unchanged: the consent matrix, `AC6 version table`, `AC7 LatestStable called exactly once per cycle`, `AC8 goDevClient error aborts before ListRepos`, `AC9 rate limit preserves cursor`, `AC10 per-repo drop logs and continues`, `AC11 unparsable maintainer config`, `AC12 cursor records HEAD and skips on re-run`, `corrupt cursor cold-starts`, `publish failure still ends with success`, `cancellation mid-cycle`, `metric label containment`, and the `open update PR gate (spec 003)` context.

### 11. Documentation — update the two contracts in the same change

**`README.md`** — two rows:
- In the "Skip reasons" table, the `sha_unchanged` row currently reads `Repo HEAD SHA has not changed since last successful cycle (not evaluated on forced cycles)`. Replace with: `Repo HEAD SHA and target Go version are both unchanged since the last successful cycle (not evaluated on forced cycles)`.
- In the "Emitted task contract" table, the `task_identifier` row currently reads `deterministic UUID5 derived from `(owner, repo, HEAD SHA)``. Replace with: `deterministic UUID5 derived from `(owner, repo, target Go version, HEAD SHA)``.
- Leave the `open_update_pr` row, the decision-task `task_identifier` row (`(owner, repo)` only), the twelve-key list, the body, and the metrics table unchanged.

**`docs/dod.md`** — in the "In-flight signal contract" section, the parenthetical currently reads `task IDs derive from (owner, repo, head_sha), so a new commit otherwise re-emits on undrained work`. Change the tuple to `(owner, repo, target Go version, head_sha)`. Keep the rest of the sentence and the section's rationale intact — the SHA remains part of the seed, so a new commit still produces a new identifier and the open-PR gate rationale still holds. Note that the section also describes the gate as the in-flight signal; that contract is unchanged.

**`CHANGELOG.md`** — append ONE `- fix:` bullet to the existing `## Unreleased` section (do NOT create a second `## Unreleased` heading; read the changelog guide first). It must state: the cursor now records the cycle's target Go version beside the HEAD, the `sha_unchanged` skip requires both to be unchanged, and the update-task identifier folds the target Go version into its seed (`update-go-<owner>-<repo>-<goVersion>-<headSHA>`), so a new stable Go release produces a fresh work item instead of one downstream dedup already absorbed; a pre-fix cursor entry (no recorded version) is re-evaluated once, which is how the currently stalled repos re-enter the pipeline.

### 12. Self-check before finishing

Re-run the `<verification>` block below and confirm every command passes, then walk each requirement above against the change. Confirm in particular: (a) the recorded Go version comes from the cycle's resolved version, not a literal; (b) the skip needs BOTH keys; (c) the identity seed is `update-go-<owner>-<repo>-<goVersion>-<headSHA>`; (d) `completeTask` derives with `state.LastSeenGoVersion`; (e) the open-PR gate and the merge-detection guard are structurally untouched; (f) no second `## Unreleased` section exists.

</requirements>

<constraints>
- Errors follow `github.com/bborbe/errors` wrapping (`errors.Wrap` / `errors.Wrapf` / `errors.Errorf`); info logs at glog V(2). Never `fmt.Errorf`. The phrase "repo dropped from cycle" is the operator's grep handle — do not reword it.
- Tests use Ginkgo v2 / Gomega. Mocks are Counterfeiter fakes in `mocks/` — never hand-edited, always regenerated by `make precommit`'s `generate` target.
- The `sha_unchanged` metric label string and the `NewSHAUnchangedFilter` constructor name are RETAINED; only their doc comments change to name both inputs. Renaming either is out of scope.
- The open `fix/update-go-*` PR gate (`HasOpenUpdatePR` / `openUpdatePRGate`), the merge-detection pass's structure, the allowlist, `go.mod` parsing, the version comparison, and the consent gate are unchanged. The open-PR gate gets NO new opt-out knob.
- The decision-task identity (`DeriveDecisionTaskID`, seeded on `(owner, repo)` only) is NOT changed — it is deliberately SHA-free and re-emits every cycle as a documented no-op.
- The watcher stays read-only against observed repos and gains no new external dependency. The change reads one additional value from the same `go.dev` response already fetched and writes it to the existing PVC-mounted cursor file.
- The value placed in the cursor and in the identifier seed is the parsed three-part form produced by `Version.Number()`, never raw upstream text. No credentials, no new network calls, no new files.
- The emitted `CreateTaskCommand` shape is unchanged apart from the identifier's derivation: the same twelve frontmatter keys with the same values, the same title form, the same body bytes.
- Forced cycles are unchanged: a forced cycle still omits the skip filter entirely and every other gate still applies.
- `LatestStable` resolves to the same version for every candidate within one cycle, so a single recorded value per repo is sufficient.
- `main.go`, `pkg/factory/`, `pkg/handler/`, `pkg/metrics.go`, `pkg/githubclient.go`, `pkg/gomod.go`, `pkg/version.go`, `pkg/taskpublisher.go` and `pkg/auth/` need NO change. `NewWatcher`, `CreateWatcher` and `CreateStaticFilters` signatures are unchanged, so `main.go` and `pkg/factory/` keep compiling as they are.
- Every `.go` file keeps the BSD license header block. No new `.go` files are expected (only `mocks/cursor_reader.go` is regenerated).
- Keep every line under 100 characters and every function under 80 lines / 50 statements.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, apart from the deliberate expectation updates listed in requirement 10 (the `DeriveTaskID` call sites, the forced-cycle cursor fixture, the merge-detection identifier, and the taskbuilder identifier).
</constraints>

<verification>
Run from the repo root. Every `make` invocation MUST pass `ROOTDIR=/workspace` explicitly — the daemon may run with `hideGit=true`, which masks `.git`; `Makefile.variables:3` derives `ROOTDIR` from the repository root, so with `.git` masked it resolves to empty and every target dies before it runs.

```
ROOTDIR=/workspace make precommit
```
Must exit 0. This runs `ensure format generate test check addlicense` — it regenerates `mocks/` and is the authoritative gate.

```
ROOTDIR=/workspace go test -mod=mod ./pkg/filter/... ./pkg/...
```
Must exit 0, and the run must include the extended predicate's specs: skip on same HEAD + same Go version, pass through on an advanced Go version, and pass through on a legacy entry with no recorded version. This is the behavioural counterweight to the grep checks below, which are shape-only — an implementation that adds the struct field, the interface method and the extra `DeriveTaskID` parameter but leaves the predicate HEAD-only would satisfy every grep and fail here.

```
grep -c LastSeenGoVersion pkg/cursor.go pkg/watcher.go pkg/filter/sha_unchanged_filter.go pkg/cursorreader.go
```
Must print four `file:N` lines, each with N ≥ 1.

```
grep -n "last_seen_go_version" pkg/cursor.go
```
Must print exactly 1 line (the JSON tag on `RepoState.LastSeenGoVersion`).

```
grep -nE "LastSeenGoVersion:.*LatestGo" pkg/watcher.go
```
Must print ≥ 1 line — the cursor write takes its value from the cycle's resolved version, not a literal.

```
grep -n "func DeriveTaskID" pkg/taskid.go
```
Must show a signature carrying the Go-version argument (`func DeriveTaskID(owner, repo, goVersion, headSHA string) uuid.UUID`).

```
grep -rn "DeriveTaskID(" pkg --include='*.go' | grep -v '_test.go' | grep -v 'func DeriveTaskID'
```
Must list exactly `pkg/taskbuilder.go` and `pkg/watcher.go` — both call sites updated, none missed. (The `grep -v 'func DeriveTaskID'` is required: without it the definition in `pkg/taskid.go` matches its own call pattern and the command prints three files.)

```
grep -n "DeriveTaskID" pkg/watcher.go
```
Must print ≥ 1 line inside `completeTask`, passing `state.LastSeenGoVersion`.

```
grep -n "LastSeenGoVersion" mocks/cursor_reader.go
```
Must print ≥ 1 line — the Counterfeiter fake was regenerated for the extended interface.

```
grep -c "open_update_pr" pkg/watcher.go
```
Must print `3` — the same count as before this change, so the open-PR gate is untouched.

```
grep -c '^## Unreleased' CHANGELOG.md
```
Must print exactly `1` — the fix bullet was appended to the existing section, not added under a second one.

```
awk '/^## /{sec=$0} /^- fix:/{if (sec=="## Unreleased") print}' CHANGELOG.md
```
Must print ≥ 1 line — the `fix:` bullet sits under `## Unreleased`.
</verification>
