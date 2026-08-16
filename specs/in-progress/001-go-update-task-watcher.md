---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-08-16T12:48:53Z"
generating: "2026-08-16T13:05:07Z"
prompted: "2026-08-16T13:05:07Z"
verifying: "2026-08-16T14:39:31Z"
branch: dark-factory/go-update-task-watcher
---

## Summary

- Turn this scaffolded repo into a running watcher: it periodically asks GitHub which of an owner's repos declare an old Go version in their `go.mod`, and files one work item per affected repo for a separate agent to act on.
- Two independent permissions must both be present before any work item is filed: the operator's allowlist, and the repo owner's own opt-in flag inside that repo. Missing either means silently skipped, never assumed-yes.
- The work item's exact shape (its fields, its wording, its identity) is already proven in production by a shell prototype that filed ~80 real items on 2026-08-16 — this spec freezes that shape byte-for-byte so the consuming agent needs no change.
- Identity of each work item is derived from (owner, repo, current HEAD commit), so refiling the same item is a no-op downstream, while a new commit correctly produces a fresh one.
- This service only observes and reports. It never clones, edits, commits, or pushes to any repo it looks at.

## Problem

A fleet of ~210 Go repositories drifts behind the current stable Go release. Today the only way to find and act on that drift is an operator running a shell slash-command by hand, which means the drift is only ever noticed when somebody remembers to look. The prototype proved the detection signal and the hand-off contract work end to end, but it is operator-triggered, keeps no memory between runs, and re-derives everything from scratch each time. What is missing is an always-on service that watches for the drift continuously, remembers what it already reported, refuses to act on repos whose owners have not opted in, and hands each finding to the existing update agent through the normal message pipeline instead of writing files on somebody's laptop.

## Goal

A deployable service exists in this repository that, on its own interval and without human involvement, discovers repos under one configured GitHub owner whose declared Go version is behind current stable, applies two consent gates, and publishes exactly one create-task message per qualifying repo — with the frozen field contract the update agent already consumes. It remembers what it published across restarts, degrades safely (skip, never guess) on every upstream failure, reports its behaviour through Prometheus counters and log lines, and can be forced to run a cycle on demand through an admin endpoint. It reads from GitHub only; it modifies nothing anywhere except its own persisted memory.

## Non-goals

- Do NOT clone, check out, commit, push, tag, open PRs against, or otherwise write to any observed repository — determination of what actually needs changing and the PR itself belong to the sibling update agent after it clones.
- Do NOT determine dependency staleness or vulnerabilities. The declared Go version is the only signal this service computes.
- Do NOT ship Kubernetes/Helm deployment wiring. This repo builds and pushes an image; deployment is Helm-driven from the separate `quant` repo.
- Do NOT seed, write, or migrate the opt-in flag into any other repository's `.maintainer.yaml`.
- Do NOT add a configurable source URL for the "latest stable Go" lookup — invariant; tests inject the client interface, not a URL. If a second source is ever demanded by a named consumer, that's a separate spec.
- Do NOT add a per-cycle emit cap, throttle, or dry-run switch — invariant; downstream dedup absorbs re-emits and the prototype already drained ~80 items in one pass. If a cap is ever needed, that's a separate spec.
- Do NOT add an env flag that disables the opt-in gate or the allowlist — an escape hatch on a trust gate is the regression this spec exists to prevent.
- Do NOT add cursor-editing admin endpoints (reset/set per repo). The forced-cycle endpoint covers the documented end-to-end verification need; per-repo cursor surgery has no named consumer.
- Do NOT gate forks separately. A fork carrying its own opt-in flag has given its own consent; that is the whole point of the per-repo gate.

## Desired Behavior

1. **Continuous scan.** The service runs as a long-lived process with an in-process poll loop on a configurable interval (default 10 minutes), firing one cycle immediately on start and one per tick thereafter. Its memory of what it already reported survives process restarts.

2. **Cheap Go-version signal, no clone.** Once per cycle it resolves the current stable Go version from the public Go release endpoint. Per repo it reads only the repo's `go.mod` through the GitHub contents API and extracts the declared Go version. A two-part declaration (`X.Y`) is treated as `X.Y.0`. A repo with no `go.mod`, an unreadable/unparsable declaration, or a declared version at or ahead of current stable is skipped.

3. **Two consent gates, both mandatory.** A repo qualifies only when (a) it passes the operator-configured repo allowlist, and (b) its own `.maintainer.yaml` declares the Go-update opt-in as true. Absent file, absent section, absent key, false key, or an unparsable file all mean skipped. Nothing is ever defaulted to opted-in.

4. **Named skip reasons.** Every non-emitting outcome resolves to exactly one reason label drawn from a closed set: `scope`, `no_gomod`, `gomod_unparsable`, `go_current`, `auto_update_disabled`, `sha_unchanged`. Each skip increments a counter carrying that label and is attributable in logs to a specific repo.

5. **Frozen emit contract.** For each qualifying repo the service publishes exactly one create-task message to Kafka whose fields and body match the production-proven contract byte-for-byte (see Constraints). The body is an operator-readable header only; no dependency, vulnerability, or diff data is ever embedded.

6. **Deterministic identity.** The task identifier is a version-5 UUID derived from the seed `update-go-<owner>-<repo>-<head_sha>` against a namespace constant frozen in code. Same repo at the same HEAD always yields the same identifier; a new HEAD yields a new one.

7. **Fail-closed cycle semantics.** Whole-cycle failures (repo listing failure, rate limiting, unusable stable-Go lookup) abort the cycle without recording any progress, so the next cycle retries from the same state. Per-repo failures drop only that repo from the cycle and leave the rest of the cycle running.

8. **Operable surface.** The service exposes liveness, readiness, Prometheus metrics, log-level control, and a forced-cycle endpoint. The forced cycle re-evaluates repos whose HEAD has not moved, but is subject to every other gate. The four required watcher counters exist with all label values pre-initialised to zero before the first cycle. Key-value-store endpoints inherited from the scaffold that this service does not use are gone.

## Constraints

**Frozen emit contract — matching a shipped consumer, changing any of this breaks it.**

Message fields, exactly these keys:

| Key | Value |
|---|---|
| `task_type` | `github-update-go` |
| `assignee` | `github-update-go-agent` (never `go-update-agent` — that agent does not exist; the controller drops unknown assignees) |
| `phase` | `planning` |
| `status` | `in_progress` |
| `stage` | from the stage env var (`dev` / `prod`) |
| `task_identifier` | derived UUID5 (Desired Behavior 6) |
| `title` | `Update Go <owner>-<repo> <sha[:7]>` (dash, not slash-and-"at" — `CreateCommand.Validate` rejects `/` in titles; verified against the production controller's validator, not the Stage-1 prototype's vault artifacts, which bypassed that validator) |
| `repo` | `<owner>/<repo>` |
| `clone_url` | `git@github.com:<owner>/<repo>.git` |
| `ref` | full HEAD SHA |
| `current_go` | normalised three-part version from `go.mod` |
| `latest_go` | current stable three-part version |

Body, byte-for-byte (note the two spaces either side of the middot):

```markdown
# Update Go: <owner>/<repo>

**Current Go:** <X.Y.Z>  ·  **Latest Go:** <X.Y.Z>
**HEAD:** <sha[:7]>
**Repo:** [<owner>/<repo>](https://github.com/<owner>/<repo>)
```

The vault filename is derived downstream from `title` verbatim by the task controller; this service emits `title` and does not construct filenames. Because `title` already uses the dash form, the filename matches it exactly: `Update Go <owner>-<repo> <sha[:7]>.md`.

**Structural constraints:**

- Mirror the sibling repo `github-release-watcher` for package layout, Kafka publishing via `github.com/bborbe/cqrs`, cursor handling, filter-chain style (each filter exposes a skip-reason-string method, not a boolean), identifier derivation, and metrics registration through an injected Prometheus registerer (never package-level registration in `init`).
- The opt-in schema comes from the already-released `github.com/bborbe/maintainer` at `v0.48.1` (`maintainerconfig`, `goUpdate.autoUpdate`). Do not hand-roll the struct. The repo allowlist comes from the same module's `repoallowlist` package, used exactly as `github-release-watcher` uses it. (Decision: the guide's prose says `lib/repoallowlist`; the live import path in the template repo is `github.com/bborbe/maintainer/repoallowlist` — follow the template.)
- Metric namespace is `github_update_go_watcher`, matching the binary name. The four required counters are poll-cycle-by-result, published-by-status, repos-scanned, filter-skipped-by-reason.
- Module path `github.com/bborbe/github-update-go-watcher` and the scaffold's Makefile targets stay as they are.
- The Go-version comparison and the emit path must not shell out. No process execution, no git library, anywhere in the module.
- Governing documents: `50 Knowledge Base/Watcher Writing Guide.md` in the Personal vault (six required components + required observability + verification checklist) and this repo's `docs/dod.md`.
- Persisted memory lives at the path bound from the `CURSOR_PATH` environment variable, defaulting to `/data/cursor.json` — matching `github-release-watcher`'s `DefaultCursorPath` constant. The binary must start with `CURSOR_PATH` unset (the default applies); the `quant` Helm chart mounts the PVC at that default path.

**Deliberate deviation from the template, decided at spec time:** `github-release-watcher` implements its forced-cycle endpoint by defining its own local Kafka command operation (`TriggerReleaseCheckCommandOperation`, declared in its own `pkg/command` package — not sourced from `github.com/bborbe/maintainer`) and consuming it in-pod. That path's executor imports `cqrs/cdb`, which pulls in a key-value store, an in-pod Kafka consumer, and Helm topic wiring — all to serve a forced cycle whose only caller is an operator's curl. For this service, that surface is not worth its cost: it implements the forced cycle in-process (the endpoint triggers the cycle directly) and ships no command topic, no command consumer, and no key-value store. If a Go-update command topic is ever needed for another reason, that's a separate spec.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| GitHub 5xx / network error while listing repos | Cycle aborts before any evaluation; no progress recorded | Next scheduled cycle retries from identical state | `poll_cycle_total{result="github_error"}` increments; warning log line | Fully reversible (nothing emitted) | Cycle lock held for the aborted cycle only |
| GitHub rate limit at any call | Cycle aborts immediately; no progress recorded | Next cycle after quota reset resumes | `poll_cycle_total{result="rate_limited"}` | Fully reversible | As above |
| Stable-Go lookup unreachable, non-200, or response not matching `go<major>.<minor>[.<patch>]` | Cycle aborts before evaluating any repo; nothing published | Next cycle retries | `poll_cycle_total{result="go_version_error"}`; error log naming the rejected payload | Fully reversible | As above |
| Per-repo fetch failure (HEAD SHA, `go.mod`, or `.maintainer.yaml`) that is not a rate limit | That repo only is dropped from the cycle; remaining repos still evaluated and published | Next cycle re-fetches that repo | Log line `repo dropped from cycle` naming owner/repo/error | Reversible | Independent of other repos |
| `.maintainer.yaml` present but unparsable YAML | Repo dropped from the cycle. Never read as opted-in | Repo owner fixes the file; next cycle picks it up | `repo dropped from cycle` log naming the parse error | Reversible | Independent |
| `go.mod` or `.maintainer.yaml` larger than 1 MiB | Repo dropped before decoding | Repo owner shrinks the file | `repo dropped from cycle` log naming the size | Reversible | Independent |
| Kafka publish fails for one repo | That repo is not recorded as reported; the cycle continues with the next repo | Next cycle re-publishes; downstream dedup by identifier makes a duplicate a no-op | `published_total{status="error"}`; error log with repo, SHA, identifier | Reversible | Independent |
| Process crashes mid-cycle after some publishes | Progress for the cycle is unrecorded; the next start re-publishes those repos | Downstream dedup by deterministic identifier absorbs the re-publish | Missing `poll_cycle_total{result="success"}` for that cycle | Reversible; duplicate emission is a downstream no-op | Partial-progress crash is the designed-for case |
| Persisted memory file corrupt / unreadable JSON | Cycle refuses to run and returns an error; nothing is published on a guessed-empty state | Operator deletes the file to accept a cold start (cold start re-publishes; dedup absorbs) | Error log naming the path; no success counter | Reversible by deleting the file | Single writer only |
| Write of persisted memory fails after publishing | Publishes stand; the failure is logged and the cycle still counts as success | Next cycle re-publishes; dedup absorbs | Warning log naming the path | Reversible | Write is atomic (temp file + rename); a crash mid-write never leaves a half-written file |
| Forced-cycle request arrives while a cycle is already running | Request is refused; no second concurrent cycle starts | Caller retries after the running cycle finishes | HTTP 409 response | No state change | Exactly one cycle at a time, enforced by a non-blocking lock |
| A prototype-era task for the same repo and HEAD is still open at cutover | One duplicate work item may appear for that repo, because the shell prototype's pseudo-identifier and this service's UUID5 differ by construction | Operator closes the stale duplicate once | Two open items for one repo/SHA | Reversible by closing one | One-time cutover effect only |
| Clock skew on the host | No effect — no time windows, no `since` queries, no expiry logic anywhere; all dedup is commit-SHA-based | n/a | n/a | n/a | n/a |

## Security / Abuse Cases

- **Attacker-controlled input:** the entire contents of `go.mod` and `.maintainer.yaml` in any observed repo. Both are size-capped at 1 MiB before decoding, parsed defensively, and a parse failure drops the repo. Only the extracted numeric version triple ever reaches the emitted message; raw file bytes never do.
- **Trust boundary:** `.maintainer.yaml` is the repo owner's consent record and is the only thing that can turn this service's attention into agent action on their repo. It is positive-opt-in: every ambiguity (missing, empty, wrong type, malformed) resolves to skipped. There is no code path and no configuration that turns consent on by default.
- **Admin endpoint exposure:** the forced-cycle endpoint takes no owner, repo, or scope parameter — a forced cycle can only re-examine repos that already pass the allowlist and the opt-in gate, so reaching the endpoint grants no ability to target a repo the operator did not configure. Unknown query parameters are ignored.
- **Hang / unbounded work:** every upstream call runs under the cycle context and is cancelled with it; list endpoints follow pagination to exhaustion with a page cap so a self-referential `next` link cannot loop forever; a rate-limit response aborts the cycle rather than retrying in a tight loop.
- **Race:** exactly one cycle runs at a time, so the persisted-memory file has a single writer, and its write is atomic (temp file + rename).
- **Secrets:** GitHub App private key arrives by environment only, is never logged, and is length-displayed rather than value-displayed in any config dump.

## Acceptance Criteria

- [ ] `make precommit` exits 0 at the repo root — evidence: exit code
- [ ] The built message for a fixture repo carries exactly the twelve frozen keys with the frozen values, asserted field by field — evidence: unit-test assertion; plus `grep -rn "github-update-go-agent" --include=*.go .` returns ≥1 match and `grep -rn "go-update-agent" --include=*.go .` returns 0 matches
- [ ] The built body for a fixture repo equals the frozen four-line block byte-for-byte, including the double spaces around the middot — evidence: golden-string equality assertion in a unit test
- [ ] Identifier derivation: identical (owner, repo, HEAD SHA) yields an identical UUID; a changed SHA yields a different UUID; the produced value reports UUID version 5 — evidence: three unit-test assertions
- [ ] Consent-gate table test covers allowlisted × {file absent, section absent, key absent, key false, key true}: only the `true` case publishes; the other four each produce skip reason `auto_update_disabled` and a publish call count of 0 — evidence: fake-publisher call count plus `filter_skipped_total{reason="auto_update_disabled"}` value
- [ ] Version-comparison table test covers: two-part directive normalised to `X.Y.0`, behind stable → publishes, equal to stable → `go_current`, ahead of stable → `go_current`, `go.mod` absent → `no_gomod`, non-numeric/garbage directive → `gomod_unparsable` — evidence: returned skip-reason strings and publish counts per table row
- [ ] A cycle over three repos performs exactly one stable-Go lookup — evidence: fake lookup-client call count equals 1
- [ ] A stable-Go response that does not match `go<major>.<minor>[.<patch>]` aborts the cycle before any repo is evaluated: publish call count 0 and `poll_cycle_total{result="go_version_error"}` equals 1 — evidence: counter value plus fake-publisher call count
- [ ] A rate-limit error at any GitHub call aborts the cycle and leaves the persisted-memory file byte-identical to its pre-cycle contents — evidence: file byte comparison before/after plus `poll_cycle_total{result="rate_limited"}`
- [ ] With two repos where the first returns a transient per-repo error, the second is still evaluated and published, and a log line containing `repo dropped from cycle` names the first — evidence: fake-publisher received message for repo two; captured log output contains the phrase
- [ ] An unparsable `.maintainer.yaml` produces zero publishes for that repo and does not increment `filter_skipped_total{reason="auto_update_disabled"}` (it is a drop, not a consent verdict) — evidence: publish count and counter values
- [ ] After a successful publish the persisted memory records that repo's HEAD SHA; a second cycle with an unchanged HEAD publishes nothing and reports `sha_unchanged`; no `.tmp` file remains in the persistence directory after a save — evidence: file contents, publish call counts, directory listing
- [ ] The persisted-memory path is read from `CURSOR_PATH` and defaults to `/data/cursor.json` when unset; the binary starts successfully with `CURSOR_PATH` unset — evidence: unit assertion on the default constant/value, plus `grep -n "CURSOR_PATH" README.md` returns ≥1 line
- [ ] `GET /metrics` before any cycle has run returns a body containing all four `github_update_go_watcher_*` metric names with every documented label value present at 0 — evidence: HTTP 200 plus response-body substring assertions for each metric+label pair
- [ ] Endpoint contract: `GET /healthz` → 200, `GET /readiness` → 200, `GET /metrics` → 200, `POST /trigger` → 202 with body `{"status":"accepted"}`, `POST /trigger` while a cycle is running → 409, `GET /setloglevel/2` → 200, `GET /resetdb` → 404 — evidence: HTTP status codes and response body
- [ ] `POST /trigger?force=true&repo=attacker/repo` runs a forced cycle that publishes only for the allowlisted, opted-in fixture repos and never for the repo named in the query string — evidence: fake-publisher received repo list
- [ ] The module never shells out or clones: `grep -rn "os/exec\|go-git\|exec.Command" --include=*.go .` returns 0 matches — evidence: grep exit code 1 / zero matches
- [ ] `README.md` documents the environment-variable table, the four metrics, the six skip-reason labels, the frozen emit contract (frontmatter keys and body shape), and the forced-emit end-to-end verification procedure — evidence: `grep -n "REPO_ALLOWLIST" README.md`, `grep -n "auto_update_disabled" README.md`, `grep -n "/trigger" README.md`, `grep -n "task_type" README.md`, `grep -n "Latest Go:" README.md` each return ≥1 line
- [ ] The compile test builds the binary and the binary starts with only the required environment set (no data-directory variable required) — evidence: `go test ./...` exit code 0 covering the existing build test

**Scenario coverage — no new scenario.** The behaviours above are all reachable with unit tests plus fakes for GitHub, the stable-Go lookup, and the Kafka sender. The only behaviour genuinely requiring real infrastructure is detect→Kafka→controller→vault-file, which needs the Helm deployment that this spec explicitly excludes; it is documented in the README as an operator procedure instead.

## Verification

```
make precommit
```

Expected: exit code 0, no lint findings, all Ginkgo suites green.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Domain core: stable-Go lookup client + response validation, go-directive parsing/normalisation/comparison, deterministic identifier derivation, metrics with injected registerer | 2, 6, part of 4, part of 8 | 4, 6, 7, 8 (counter existence), 13 | — |
| 2 | GitHub source surface: repo listing with pagination and archived-repo exclusion, HEAD SHA, `go.mod` contents, `.maintainer.yaml` contents with size cap and rate-limit mapping | 2, 3 (fetch half) | 9 (error mapping), 11 (parse drop), 16 | 1 |
| 3 | Filter chain (all six skip reasons) + persisted-memory load/save with atomic write and corrupt-file policy | 3, 4, part of 7 | 5, 6 (reason labels), 12 | 1, 2 |
| 4 | Cycle orchestration + emit: gather-per-repo, chain application with per-reason counters, frozen message and body construction, publish, memory update, whole-cycle vs per-repo failure policy | 1, 5, 7 | 2, 3, 9, 10, 11, 12 | 1, 2, 3 |
| 5 | Binary wiring: env binding, poll loop, HTTP surface incl. forced cycle with single-cycle lock, scaffold-endpoint removal, README | 1, 8 | 1, 14, 15, 17, 18 | 4 |

Rationale: prompts 1 and 2 are leaf packages with no dependency on each other beyond shared domain types, so they can be verified in isolation before anything orchestrates them; prompt 3 needs both to define its filter input; prompt 4 is the only place the frozen contract is assembled, so it stays a single prompt to keep the contract's assertions together; prompt 5 touches only the binary and docs, so it cannot invalidate anything proven earlier.

## Do-Nothing Option

Doing nothing leaves detection as a manual slash-command run. That is workable — it demonstrably drained ~80 repos in one pass — but it only ever fires when an operator remembers, has no memory between runs, and cannot enforce the per-repo consent gate the update agent is about to rely on for autonomous PR creation. As the update agent moves toward unattended operation, an operator-triggered emitter becomes the single point at which the pipeline stops being autonomous, and the consent gate stays unenforced in the one place it matters. The current approach is acceptable only for as long as the update agent stays hand-driven too.
