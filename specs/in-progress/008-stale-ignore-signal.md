---
status: prompted
approved: "2026-11-24T14:17:14Z"
generating: "2026-11-24T14:37:59Z"
prompted: "2026-11-24T14:37:59Z"
branch: dark-factory/spec-008
---

## Summary

- The watcher emits a `github-update-go` task only when a repo is **behind on Go** (plus a separate `github-update-go-decision` task when consent is undecided). A repo that is Go-current but carries a **stale ignore** never triggers the agent, so the OSV classifier shipped in `github-update-go-agent` v0.26.0 never runs for it.
- This spec adds a **second, independent pass** to the existing poll cycle: read the repo's three ignore surfaces through the GitHub contents API, look every suppressed advisory ID up on `api.osv.dev`, and emit one task when any ID resolves to a record carrying a top-level `withdrawn` timestamp.
- Alias resolution is mandatory. `api.osv.dev/v1/vulns/CVE-2026-33817` returns **404** because OSV merged the alias into `GO-2026-4923`, and only the `GO-*` record carries `withdrawn: 2026-04-08T13:33:56Z`. An alias-blind check records the CVE as unresolved, so the emitted stale set loses it and the operator's entry is never named. Measured 2026-11-24: **168 repos** under `bborbe` carry `CVE-2026-33817` in `.trivyignore`.
- The pass runs **inside the existing poll loop** — no new cron, no new deployment, no new Config, no new topic. Its upstream cost is bounded by a per-repo rescan interval and a per-cycle scan cap, both constants.
- Emission is deduplicated by a deterministic identifier over `(repo, generation, sorted stale IDs)`; the generation advances only after a 7-day re-emit interval, so a task that dies does not freeze its repo forever, and the re-emit is bounded to one task per repo per interval.

## Problem

`Watcher.Poll` (`pkg/watcher.go:89`) runs two passes per cycle: `processRepos` (emit a Go-bump task) and `completeMergedUpdates` (complete a task whose PR merged). The emit path is gated by the frozen filter chain (`pkg/filter/filter.go:8-20`), and step 4 — `GoBehindFilter` → `go_current` — drops every repo whose declared Go version is already current. Step 6 — `SHAUnchangedFilter` → `sha_unchanged` — drops every repo whose HEAD and target version were already reported.

A withdrawn advisory is an **OSV-side** event: it changes with no commit to the repo, so a repo carrying one moves neither its HEAD nor its Go version. That places the entire population this feature exists to catch behind `go_current` and `sha_unchanged` simultaneously. The existing chain cannot be extended to reach it: a filter added after step 4 is never evaluated for a Go-current repo, and one added before step 4 would run for the whole fleet on every 10-minute cycle.

The cost of the gap is concrete and fleet-wide. `GO-2026-4923` was withdrawn on 2026-04-08. On 2026-11-24 its CVE alias `CVE-2026-33817` is still suppressed in the `.trivyignore` of **168** `bborbe` repositories (read: `gh api -X GET search/code -f q='CVE-2026-33817 filename:.trivyignore org:bborbe' --jq .total_count`), and the ID itself is still in `VULNCHECK_IGNORE` in many of them. A dead suppression masks nothing and is found today only by the manual runbook (`Exclude a No-Fix Vulnerability Across the Fleet` § Removing a stale ignore), run when an operator remembers.

## Reproduction

Pick any repo from the 168 and confirm the two facts the feature turns on:

```bash
# 1. The CVE alias 404s; the GO record is withdrawn.
curl -s -o /dev/null -w '%{http_code}\n' https://api.osv.dev/v1/vulns/CVE-2026-33817   # 404
curl -s https://api.osv.dev/v1/vulns/GO-2026-4923 \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("withdrawn"))'          # 2026-04-08T13:33:56Z

# 2. A watched repo still carries it.
gh api repos/bborbe/hue/contents/.trivyignore --jq .content | base64 -d | grep CVE-2026-33817
```

A cycle over that repo currently emits nothing: its `go.mod` is at the current stable version, so `GoBehindFilter` returns `go_current` before any ignore is ever read. After this change the same cycle emits one task whose stale set is exactly `{CVE-2026-33817, GO-2026-4923}`.

The negative control is `bborbe/run`, which on 2026-11-24 carries 5 CVEs, 6 GHSAs and 17 `GO-*` IDs across all three surfaces and **none** withdrawn — a naive "emit if any ignore exists" implementation flags it, the correct one does not.

## Goal

On its existing schedule, and without any new cron, deployment or configuration surface, the watcher detects that a watched repo's ignore surfaces suppress an advisory OSV has withdrawn, and publishes one `github-update-go` task for that repo — including a repo with nothing else to fix — while a repo whose ignores are all legitimate is never emitted. Every upstream failure degrades to "no emission", never to a guessed verdict.

## Non-goals

- **Do NOT classify or remove anything.** Whether an ignore is stale by *package-absent* or *version-no-longer-affected* is the agent's planning-time job (`specs/in-progress/010-osv-stale-ignore-classification.md` in `bborbe/github-update-go-agent`, shipped v0.26.0). This watcher computes exactly one verdict — `withdrawn` — and only to decide whether to emit.
- **Do NOT change the Go-version-behind path, its filter chain, its cursor semantics, or `Poll`'s schedule.**
- **Do NOT add a new `task_type`, assignee, topic, Kafka command, or controller lane.** The emitted task reuses `task_type: github-update-go` and `assignee: github-update-go-agent`, so no downstream Config change is required.
- **Do NOT close or supersede an earlier stale-ignore task.** When the stale set changes, a new task is filed and the old one may still be open. The agent re-reads the surfaces from the repo, so the older task is either a no-op or closes itself; reconciling duplicates on the board is out of scope.
- **Do NOT scan the fleet's fourth ignore surface** — `TRIVY_IGNORE` in the `bborbe/coding` templates. It lives in a different repository and is prepended at build time; no repo-level scan can see it. It is covered only when `bborbe/coding` itself is scanned as one of the watched repos.
- **Do NOT triage or fix firing (non-ignored) vulnerabilities.** `/github-update-go-vuln-watcher` keeps that role.
- **Do NOT add an env knob, dry-run switch, or per-cycle emit cap.** The two throttles here bound **upstream reads**, not emission; emission stays uncapped and dedup-absorbed, as spec 001's non-goal requires.
- **Do NOT emit for a repo whose owner has not opted in** (`goUpdate.autoUpdate: true`). See Design Decision 3.

## Do-Nothing Option

Leaving this unbuilt keeps the manual runbook as the only detector. The measured cost: 168 repos still suppress `CVE-2026-33817` seven months after its advisory was withdrawn, and nothing will surface them until an operator runs `remove-vuln-ignore.sh` per repo. The next withdrawal will behave the same way. Doing nothing costs no code and no API budget, and it is defensible only if stale ignores are rare — they are not; the last one took a manual sweep across the fleet to clear, and this one has not been cleared at all.

The cheaper alternative — extending the existing filter chain instead of adding a pass — is rejected in Design Decision 1: it cannot reach a Go-current repo, which is the entire population.

## Assumptions

1. **OSV keeps `withdrawn` as a top-level RFC3339 string and keeps returning 404 for a merged alias.** Verified 2026-11-24 against `GO-2026-4923` (200, `withdrawn` set) and `CVE-2026-33817` (404). If OSV renames the field, every record reads as *not withdrawn* and the feature silently stops emitting; Detection is stated in Failure Modes.
2. **`https://vuln.go.dev/index/vulns.json` stays a flat array of `{id, modified, aliases}`.** Verified 2026-11-24: 4 885 entries, 508 KB, and the `CVE-2026-33817` alias resolves to `GO-2026-4923`. Recorded as a test fixture (Verification) so a shape change fails a test rather than a cycle.
3. **`github-update-go-agent` v0.26.0 or later is the deployed consumer.** Its planning step classifies every suppressed ID unconditionally (`pkg/steps_planning.go` `runInspection` → `classifyIgnoredIDs`), and `PlanOutput.hasWorkForScope` returns true when any classification is `stale` (`pkg/update_scope.go:178-192`), so a Go-current repo with a stale ignore advances to execution and removes it. **Operator verification step:** before the first prod deploy of this change, confirm the deployed agent image is ≥ v0.26.0; if it is older, the emitted task closes as "no update needed" and the feature delivers nothing.
4. **A repo owner's ignore files are small.** `GetFile` caps each at 1 MiB, matching `GetGoMod`.

## Desired Behavior

1. **A second pass runs at the end of every poll cycle.** `Watcher.Poll` calls a new `staleIgnores(ctx, cursorState, repos, latestGo)` after `completeMergedUpdates`, inside the same cycle and the same `CycleGate` lock. It returns an abort reason (`"rate_limited"` / `"github_error"` / `""`) exactly as the existing passes do; on an abort the cycle still saves the cursor, so work already learned this cycle is not lost. No new `run.Func`, no new goroutine, no new ticker, no change to `Poll`'s signature.

2. **Eligibility is scope + consent, never Go-version state, and it is evaluated in a fixed order.** Per cycle:
   1. **Allowlist, no API call** — repos are filtered by `Repo.Key()` against the configured allowlist before anything is fetched.
   2. **Throttle selection, no API call** — from the allowlisted set, take the at most `maxIgnoreScansPerCycle` (**10**) repos whose cursor `ignore_scanned_at` is absent or older than `ignoreRescanInterval` (**24h**), oldest first (absent sorts first).
   3. **Gather** — for each selected repo, the existing `gatherCandidate` (HEAD, `go.mod`, `.maintainer.yaml`) — 3 GitHub calls.
   4. **Consent and go.mod gates** — a repo without a `go.mod`, or whose consent is not `GrantedConsent`, is skipped for this cycle **and its `ignore_scanned_at` is still advanced**, so a permanently ineligible repo does not hold a queue slot every cycle.
   `GoBehind`, `SHAUnchanged`, and the emission claim play no part: a repo ahead of stable is scanned, and a repo whose HEAD was already reported is scanned.

3. **Cost is bounded and stated.** Per selected repo: 3 gather calls + up to 4 surface reads = **≤7 GitHub calls**; a cycle therefore issues **≤70** GitHub calls from this pass, plus at most one OSV request per distinct uncached ID. At the fleet's ~210 repos this covers every repo roughly once per day.

4. **Three surfaces, read from up to four files, at the default branch.** Per repo: `Makefile`, `Makefile.precommit`, `.trivyignore`, `.osv-scanner.toml`. A 404 is an empty surface, not an error. Any other failure (network, 5xx, oversize) drops **only that repo** from this cycle and leaves `ignore_scanned_at` unadvanced, so the next eligible cycle retries it. A file larger than 1 MiB drops the repo the same way.

5. **Every suppressed ID is extracted from every surface, in the formats the fleet actually uses.**
   - `Makefile` / `Makefile.precommit`: the `VULNCHECK_IGNORE ?=` (or `=`) assignment, **including `\`-continued continuation lines** — the fleet's canonical form spans two lines. The assignment ends at the first line not ending in `\`. An absent assignment is an empty surface.
   - `.trivyignore`: one ID per line; `#`-comment lines and blank lines are skipped; a trailing `# …` after an ID is ignored.
   - `.osv-scanner.toml`: every `id = "<ID>"` inside an `[[IgnoredVulns]]` block.
   The extraction regex is `\b(GO-\d{4}-\d{4,}|CVE-\d{4}-\d{4,}|GHSA(-[0-9a-z]{4}){3})\b`. Duplicate IDs across surfaces collapse to one. A repo with no IDs on any surface is skipped without any OSV call.

6. **Each distinct ID is resolved on OSV, with alias resolution on 404.**
   - `GET https://api.osv.dev/v1/vulns/<id>`, 10s timeout, body capped at 8 MiB. A non-empty top-level `withdrawn` means **stale**.
   - On **404**, resolve through the Go index `https://vuln.go.dev/index/vulns.json` (a flat JSON array of `{id, modified, aliases}`, capped at 64 MiB). Scan every entry whose `aliases` contain the queried ID and fetch each such record; **if any of them carries `withdrawn`, the queried ID is stale**. A CVE can legitimately alias more than one record, so "any" — not "first" — is the rule. A 404 that resolves to no record, or to records with no `withdrawn`, is **unresolved**, never stale.
   - Any transport, non-404-status, or decode failure is **unresolved**, never stale. **Unresolved never emits.**
   - The index is fetched at most once per process per `goIndexTTL` (**24h**) and cached in memory. It is never fetched when no ID has 404'd.

7. **Verdicts are cached in the cursor with a TTL.** `Cursor.OSVVerdicts` maps an advisory ID to `{withdrawn bool, checked_at RFC3339}`. A cached verdict younger than `osvVerdictTTL` (**24h**) is reused with no network call. Only resolved verdicts (stale or legitimate) are cached; an unresolved ID is retried next cycle. This is what keeps the pass cheap: at steady state the fleet's ~25 distinct suppressed IDs cost ~25 OSV requests per day.

8. **A repo with at least one stale ID is emitted; a repo with none is not.** The stale set is the sorted list of IDs that resolved to a record carrying `withdrawn`. An empty set means no emission. A repo whose every ignore is legitimate or unresolved emits nothing.

9. **Emission is deduplicated by identity plus a monotonic generation.**
   - The cursor records `ignore_stale_ids` (sorted, joined by `,`), `ignore_stale_generation` (int), and `ignore_stale_emitted_at` (RFC3339), all on `RepoState`.
   - A task is published when the stale set differs from the recorded one, or when the set is unchanged and `now - ignore_stale_emitted_at >= staleReemitInterval` (**7 days**). **Both paths increment the generation** — the generation is monotonic per repo and never resets.
   - The task identifier is a UUID5 over the seed `stale-ignore-<owner>-<repo>-g<generation>-<ids>`, in a **new** namespace constant, deliberately distinct from `taskIDNamespace` and `decisionTaskIDNamespace`.
   - Because the generation is part of the identifier and only ever increases, every emission mints a **new** record and therefore actually runs — unlike an identity-identical create, which the controller dedups into a no-op. Resetting the generation to 1 on a set change would be wrong: a set that oscillates (`{A}` → `{A,B}` → `{A}`) would re-derive the first emission's identifier, the controller would dedup it, and the still-stale `A` would never be re-filed.
   - The accepted cost is that an older task may still be open when the re-emit lands; the bound is one new task per repo per 7 days while the set is unchanged.
   - On a successful publish the three cursor fields are updated; on a failed publish they are not, so the next cycle retries.

10. **The emitted task reuses the frozen emit contract, with three differences.** The builder sets exactly the fields `BuildCreateCommand` sets today: `task_type`, `assignee`, `phase`, `status`, `stage`, `repo`, `clone_url`, `ref`, `current_go`, `latest_go`, and `update_scope` when the fleet-wide scope is configured. **`target_vault` is not set by any builder** — it is stamped at send time by the typed sender (`factory.CreateCreateCommandSender` → `task.NewCreateCommandSender`), exactly as it is for the existing create and decision commands. The differences are:
    - `task_identifier` — the stale-ignore identity of DB9, not `DeriveTaskID`.
    - `title` — `Remove Stale Ignores <owner>-<repo>` (dash form, no SHA; the identity is the stale set, not the HEAD).
    - the body — names every stale ID, the surface(s) each appears on, and that OSV reports the advisory as withdrawn.
    `ref`, `current_go` and `latest_go` come from the `Candidate` the pass already gathered in DB2 step 3.

11. **Observability.** One new counter, pre-initialised to zero before the first cycle: `github_update_go_watcher_ignore_scan_total{result}` with results `scanned` | `clean` | `stale` | `unresolved` | `error` | `rate_limited`. Emissions need no new counter — `PublishCreate` already increments `github_update_go_watcher_published_total{status="create"}`. Every emission logs one line at V(2) naming repo, stale IDs, generation and task identifier; every skip logs its reason.

12. **Documentation in the same change.** `README.md` gains a section naming the second signal and the three surfaces, **and line 30's "the cheap fleet signal cannot see that without cloning" limitation is amended** to exclude the withdrawn case. `docs/dod.md`'s in-flight contract gains one sentence: the stale-ignore emission is deduplicated by `(repo, generation, stale set)` and is not covered by the emission claim. `CHANGELOG.md` gains an `## Unreleased` bullet.

## Constraints

- **No new module dependency.** The OSV client is a small `net/http` + `encoding/json` implementation in `pkg/`. Do not import `bborbe/github-update-go-agent` — it is a deployable service, and pulling it in drags its whole graph into the watcher.
- **No new env var, chart value, PVC, topic, or Config lane.** All four durations/caps are package constants.
- **The pass must never return an error that aborts the cycle's earlier work.** Its failures are counted and logged; the cycle completes.
- **The OSV client gets its own `http.Client`** (10s timeout), never the GitHub App-authenticated client — the same rule spec 001 applies to the go.dev client. It is constructed in `factory` alongside `CreateGoDevHTTPClient` and injected, so tests substitute it.
- **Cursor compatibility.** New `RepoState` and `Cursor` fields are `omitempty`; a cursor written by the previous binary loads and cold-starts the pass. A cursor written by this binary is readable by the previous one (unknown keys ignored), **but the previous binary re-marshals the cursor without those keys and erases them** — a rollback loses the scan and verdict state and cold-starts. That is accepted; it costs one rescan, not correctness.
- **No vault, task-store, or filesystem read.** `docs/dod.md` is a durable contract; the pass reads GitHub and OSV only.
- **No `exec`, no shell, no `git` binary.** Same rule as spec 001.
- **`make precommit` must pass**, including `vulncheck`, `osv-scanner` and `trivy`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| OSV returns 404 for an ID and the Go index has no matching alias entry | ID is `unresolved`; it never contributes to the stale set | Retried next cycle (verdict not cached) | `ignore_scan_total{result="unresolved"}`; V(2) log naming the ID | Fully reversible |
| OSV or the index is unreachable / non-2xx / unparsable | ID is `unresolved`; the repo is not emitted | Retried next cycle | `ignore_scan_total{result="error"}`; warning log | Fully reversible |
| **OSV renames or drops `withdrawn` (schema drift)** | Every record decodes with an empty `withdrawn` and reads as legitimate — the feature **fails open and goes silent** | Manual: the next `curl` of a known-withdrawn ID shows the shape change | `ignore_scan_total{result="stale"}` stays at zero fleet-wide for a month, while `clean` climbs | Reversible by a code change |
| GitHub contents read fails (non-404) for one surface | Only that repo is dropped from this cycle; `ignore_scanned_at` is not advanced | Next eligible cycle retries | `ignore_scan_total{result="error"}`; `repo dropped from cycle` log | Reversible |
| A surface file exceeds 1 MiB | Repo dropped this cycle | Next eligible cycle retries | `repo dropped from cycle` log naming the size | Reversible |
| GitHub rate limit during the pass | Pass aborts; cursor is saved; cycle counts `rate_limited` | Next cycle after quota reset | `poll_cycle_total{result="rate_limited"}` | Reversible |
| Process crashes after an emission but before `SaveCursor` | The cursor does not record the emission; the next cycle re-publishes the same identifier, which the controller dedups | Automatic | Duplicate publish log line; no duplicate record | Reversible |
| Emitted task dies without removing the ignore | Re-emitted with a **new generation** after 7 days, so it actually runs | Automatic | `ignore_scan_total{result="stale"}` again after the interval | Reversible |
| Stale set changes (an ID removed, another withdrawn) | The generation increments and the identifier changes, so a fresh task is filed immediately; an older task may still be open | Automatic | `ignore_scan_total{result="stale"}` | Reversible |
| A repo's only ignore is legitimate | No emission, ever | — | `ignore_scan_total{result="clean"}` | — |
| Cursor file from the previous binary | Loads; `ignore_scanned_at` absent, so the repo is eligible and the pass cold-starts | — | Normal first-cycle logs | — |
| Clock skew or a backwards clock | TTLs and `ignore_stale_emitted_at` are wall-clock; a backwards jump delays a re-scan or re-emit, never advances one early | Corrects when the clock does | A repo not rescanned for longer than 2× the interval | Reversible |
| Two cycles overlap | Impossible — `CycleGate.TryAcquire` admits one cycle, and the pass is inside it, so the cursor keeps a single writer | — | `poll cycle skipped: a cycle is already running` | — |

## Acceptance Criteria

- [ ] **AC1** — `VULNCHECK_IGNORE` parsing handles a `\`-continued multi-line assignment and a single-line one and extracts every ID from both. Evidence: table-driven unit test asserting the exact ID slice for each fixture.
- [ ] **AC2** — `.trivyignore` parsing extracts IDs from ID lines and ignores `#` comments, blank lines, and trailing comments. Evidence: unit test asserting the exact ID slice.
- [ ] **AC3** — `.osv-scanner.toml` parsing extracts every `id = "…"` from `[[IgnoredVulns]]` blocks and nothing else. Evidence: unit test asserting the exact ID slice.
- [ ] **AC4** — A record with `withdrawn` set classifies stale; a record without it classifies legitimate. Evidence: unit test against `httptest` fixtures recorded from the real OSV payloads.
- [ ] **AC5** — A 404 that the Go index resolves to a withdrawn `GO-*` record classifies the **queried** ID stale (not the alias). Evidence: unit test using the recorded real `CVE-2026-33817` 404 and `GO-2026-4923` / index fixtures; the returned stale ID equals `CVE-2026-33817`.
- [ ] **AC6** — A 404 the index does not resolve, and any transport error, classify unresolved and never emit. Evidence: unit test asserting `Outcome=unresolved` and a publisher spy with zero calls.
- [ ] **AC7** — A repo with only legitimate ignores produces no publish call. Evidence: unit test with the `bborbe/run` surface contents as fixtures (5 CVEs, 6 GHSAs, 17 GO IDs, none withdrawn) and a publisher spy asserting zero calls.
- [ ] **AC8** — A repo with one stale ignore and no other finding produces exactly one `CreateTaskCommand`, asserted **field by field** against the frozen contract: `task_type=github-update-go`, `assignee=github-update-go-agent`, `phase=planning`, `status=in_progress`, `stage=<cfg>`, `repo`, `clone_url`, `ref`, `current_go`, `latest_go`, title `Remove Stale Ignores <owner>-<repo>` (dash form — `CreateCommand.Validate` rejects `/` in a title), and a body containing the stale ID. Evidence: decoded-command equality in a unit test for the builder's fields; `target_vault` is asserted **after** the command passes through `factory.CreateCreateCommandSender`, which is where it is stamped — a builder-only assertion would read an empty value and fail.
- [ ] **AC9** — A second cycle with an unchanged stale set publishes nothing; a cycle past `staleReemitInterval` publishes again **with a different `task_identifier`**. Evidence: unit test with an injected clock; publisher spy call count plus the decoded cursor fields `ignore_stale_ids`, `ignore_stale_generation`, `ignore_stale_emitted_at`.
- [ ] **AC10** — `ignoreRescanInterval` and `maxIgnoreScansPerCycle` both bind, and an ineligible repo still advances `ignore_scanned_at`. Evidence: unit test counting `GetFile` calls on a fake client across three cycles.
- [ ] **AC11** — `make precommit` passes. Evidence: exit code 0.
- [ ] **AC12** — Live exercise against the real GitHub and OSV APIs. Evidence: the exercise program `hack/staleignore-exercise` (flag form `-repo github.com/bborbe/...`) **writes its own transcript** — every OSV request URL, status and `withdrawn` value, the decoded `CreateTaskCommand`, and the exit code — to a file that is **committed in the repo** and cited by path and blob sha, with the re-run command recorded. A run restricted to `bborbe/github-update-go-watcher` emits one command whose stale set **contains both `CVE-2026-33817` and `GO-2026-4923`** — an alias-blind implementation yields `{GO-2026-4923}` only and fails the CVE clause — and the full observed set is recorded so an unrelated withdrawal surfaces as a premise change. A run restricted to `bborbe/run` (5 CVEs, 6 GHSAs, 17 GO IDs, none withdrawn) emits nothing.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| A | Surfaces: `GetFile` on `GitHubClient`, the three parsers, the ID regex, the continuation trap | 4, 5 | 1, 2, 3 | — |
| B | OSV: fetcher (vuln + index), alias resolution, `withdrawn` verdict, cursor verdict cache and TTLs | 6, 7 | 4, 5, 6 | — |
| C | The pass: eligibility and throttle, `staleIgnores`, identity and generation, `BuildStaleIgnoreCommand`, cursor fields, metrics, wiring in `Poll` and `factory.CreateWatcher` | 1, 2, 3, 8, 9, 10, 11 | 7, 8, 9, 10 | A, B |
| D | Docs: `README.md` (section + line 30 + metrics table), `docs/dod.md` sentence, `CHANGELOG.md` bullet | 12 | — | C |

## Verification

- `go test ./pkg/... -count=1` — AC1–AC10.
- `make precommit` — AC11.
- **Recorded fixtures (AC4–AC7).** The real payloads are committed under `pkg/testdata/`: the `GO-2026-4923` record (with `withdrawn`), the `CVE-2026-33817` 404, and the relevant slice of `vuln.go.dev/index/vulns.json` around that alias. A shape change in OSV's schema fails these tests rather than silently disabling the pass.
- **Live exercise (AC12).** `hack/staleignore-exercise` — a small program that constructs the real `GitHubClient` with a `gh auth token` bearer transport, the real OSV client, a cursor in a temp dir, and a printing `TaskPublisher`, and **writes its own transcript** (every OSV request URL, status and `withdrawn` value, the decoded `CreateTaskCommand`, and the exit code) to a committed file. The program resolves the repo with `GET /repos/<owner>/<name>` through the bearer client, **not** `ListRepos` — the latter is `GET /installation/repositories`, which only an installation token may call, so a user token would 403. Flag form `-repo github.com/bborbe/<repo>`. Run it once with `-repo github.com/bborbe/github-update-go-watcher` and once with `-repo github.com/bborbe/run`. Expected first run: one `published CreateTaskCommand` whose stale set contains `CVE-2026-33817` and `GO-2026-4923`. Expected second run: `ignore_scan_total{result="clean"}` and no publish line. If the binary is run in a container, pass `ROOTDIR=/workspace` — `.git` is masked in-container.

## Design Decisions

1. **A separate pass, not a filter.** Filters receive only a `filter.Candidate` (no consent, no repo content) and are evaluated inside the Go-version chain, which drops Go-current repos at step 4. The signal needs repo file contents and must run for Go-current repos, so it is a sibling pass with its own eligibility.
2. **`withdrawn` only, not the other two stale criteria.** Package-absent and version-not-affected both need the repo's module graph, which is a clone. The watcher never clones (spec 001 non-goal). `withdrawn` is answerable from one HTTP call and is the one criterion provably stale from outside the repo — exactly the "cheap signal" the task asks for.
3. **Consent is required, same gate as the Go bump.** Both signals cause `github-update-go-agent` to open a repo-modifying PR. Honouring `goUpdate.autoUpdate` for one and not the other would be a second, implicit consent path. A repo that has explicitly opted out receives no work item from this watcher; its stale ignores are found by the manual runbook. Recorded as a deliberate limitation: a Go-current repo with `autoUpdate: false` is never auto-cleaned.
4. **Scan throttle as constants, not config.** Spec 001 forbids a per-cycle emit cap; that ban is about emission and is preserved. The bound here is on *upstream reads*, and a constant is the smallest surface that achieves it.
5. **Alias resolution is mandatory, not best-effort.** The dominant real case in this fleet is a CVE alias whose own record 404s (168 repos today). Without the index walk the feature is silently blind on the population it was built for. A 404 that cannot be resolved is `unresolved`, never `legitimate`.
6. **A generation in the identity instead of an emission claim.** The existing claim machinery is keyed on `InFlightTarget` (a Go version) and wired to the task-lifecycle consumer by repo; a stale-ignore emission has no Go version, and sharing the repo-keyed claim would let one signal's clear suppress the other. Putting a generation in the identifier makes the re-emit mint a genuinely new record — an identity-identical create would be a controller no-op and a dead task would freeze the repo forever.

## Security / Abuse Cases

- **Advisory IDs and file contents are attacker-controlled** (any repo owner can put anything in `.trivyignore`). The regex is anchored and length-bounded; IDs are URL-path-escaped before entering a request path; response bodies are size-capped; no body is ever echoed into a log (status and length only).
- **The OSV client sends no credentials.** Separate `http.Client` from the GitHub App client.
- **A hostile index** is capped at 64 MiB and decoded once per 24h.
- **No repo content is executed.** The pass parses text; it never runs a Makefile target.
