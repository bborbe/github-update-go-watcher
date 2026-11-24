---
status: completed
spec: [008-stale-ignore-signal]
created: "2026-11-24T16:20:00Z"
queued: "2026-11-24T14:37:42Z"
completed: "2026-11-24T14:55:38Z"
pr-url: https://github.com/bborbe/github-update-go-watcher/pull/46
---

# Document the stale-ignore signal

<summary>
- The README gains a section describing the second signal: what it detects, which files it reads, how the verdict is reached, and what it does and does not do.
- The README's existing "the cheap signal cannot see that without cloning" limitation is amended so it no longer claims to be blind to a withdrawn advisory.
- The metrics table gains the new counter and its closed set of result labels, so an operator reading the README can interpret a dashboard without reading code.
- The durable in-flight contract gains one sentence stating that the stale-ignore emission is deduplicated by repo, generation and stale set, and is not covered by the Go-bump emission claim.
- The changelog records the feature under an Unreleased section, above the newest release and without touching any released section.
- Nothing in the docs claims the watcher classifies or removes an ignore — that stays the agent's job, which is what keeps the boundary honest.
- The documentation states the deliberate limitation: a repo that has not opted in is never auto-cleaned, so a Go-current repo with consent refused keeps its stale ignore.
</summary>

<objective>
Make the second signal discoverable and correctly bounded in the repo's own documentation, so the next operator reading the README understands why a Go-current repo can still receive a task, and so the durable contract in `docs/dod.md` does not read as if this emission were governed by the Go-bump claim.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:

- `README.md` — `## Cheap signal` starts at line 26 and ends with the sentence that must be amended (line 30): "…the cheap fleet signal cannot see that without cloning." `## Trigger model` starts at line 32, so the new section goes between them. `## Metrics` starts at line 60 and holds a four-row table (`poll_cycle_total`, `published_total`, `repos_scanned_total`, `filter_skipped_total`) followed by `## Skip reasons` at line 71 — the new counter row belongs in that table, and NO skip reason is added (the pass is not a filter).
- `README.md` — the "Scope — two gates, both must pass" section (`:19`) already explains `REPO_ALLOWLIST` and `goUpdate.autoUpdate: true`; the new section must reference those gates rather than restating them.
- `docs/dod.md` — the section `## In-flight signal contract (durable — do not break)` is the durable contract; its first paragraph describes the Go-bump emission claim and its second the open-PR signal. One sentence is appended to that section; nothing existing is reworded.
- `CHANGELOG.md` — the newest heading today is `## v0.11.2` (line 11); there is no `## Unreleased` section. Bullets are `- feat:` / `- fix:` / `- chore:` under a `## vX.Y.Z` heading. There are 33 `## v` headings today.
- `pkg/metrics.go` — `IgnoreScanResults` is the authoritative label set for the new counter (added by the previous prompt); the README row must match it exactly.
- `pkg/staleignore.go` — `maxIgnoreScansPerCycle`, `ignoreRescanInterval` and `staleReemitInterval` are the authoritative numbers the README must quote (10, 24h, 7 days).

Guides to read before writing (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` convention and bullet form.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
</context>

<requirements>

### 1. `README.md` — amend the cheap-signal limitation

Replace the final sentence of `## Cheap signal` (the one ending "the cheap fleet signal cannot see that without cloning.") so it keeps its claim and adds the exception. Target wording:

> A repo that is current on Go but has stale deps is reached on demand via `/github-update-go-repo-trigger <owner/repo>`; the cheap fleet signal cannot see that without cloning — with one exception, the **stale ignore**: an advisory OSV has withdrawn is visible from the repo's ignore files alone, and is detected by the [stale-ignore signal](#stale-ignore-signal).

Do not delete the paragraph; amend it in place.

### 2. `README.md` — new section `## Stale-ignore signal`, between `## Cheap signal` and `## Trigger model`

Write it as flowing prose with a short list, in the voice of the surrounding sections (plain, declarative, no marketing). It must state, accurately:

- This is the watcher's **second, independent signal**. The first is "declared Go version behind stable"; this one is "an ignore suppresses an advisory that no longer exists". Both cause `github-update-go-agent` to open a repo-modifying PR, so both honour the same two gates — the operator allowlist and `goUpdate.autoUpdate: true`. A repo that has refused consent is never auto-cleaned, so a Go-current repo with consent refused keeps its stale ignore until an operator clears it by hand. State that as a deliberate limitation, not an oversight.
- It runs **inside the existing poll cycle** — no extra cron, deployment or configuration. Its cost is bounded by two constants: at most **10** repos per cycle and a **24-hour** per-repo rescan interval, so a fleet of a few hundred repos is covered about once a day. Per selected repo it reads at most four files (three GitHub calls to gather the repo plus up to four surface reads).
- The three ignore **surfaces**, across four files: the `VULNCHECK_IGNORE` assignment in `Makefile` and `Makefile.precommit` (including a `\`-continued multi-line assignment), `.trivyignore` (one ID per line, `#` comments and trailing remarks ignored), and `.osv-scanner.toml` (every `id = "…"` inside an `[[IgnoredVulns]]` block). A missing file is an empty surface, not an error. The fleet's fourth surface — the `TRIVY_IGNORE` template in `bborbe/coding` — is out of reach of a repo-level scan and is covered only when that repo itself is scanned.
- The **verdict** is exactly one: `withdrawn`. Each suppressed ID is looked up on `api.osv.dev`; an ID whose own record 404s is resolved through the Go vulnerability index, because OSV merges some CVEs into a `GO-*` record and only the merged record carries the withdrawal. A 404 that resolves to no record, any transport or decode failure, is **unresolved** — never stale, and never an emission. Resolved verdicts are cached in the cursor for a day; unresolved IDs are retried next cycle.
- What it does **not** do: it does not classify an ignore as stale for any other reason (package absent, version no longer affected), it does not remove or comment on anything, and it does not triage firing vulnerabilities. Those are the agent's planning-time job and `/github-update-go-vuln-watcher`'s role respectively.
- **Duplicates.** One task is filed per repo per stale set, identified by repo, a monotonic generation and the sorted set of stale IDs. An unchanged set is re-filed at most once every **7 days**, with a new identifier so the record actually runs; a task that dies without removing the ignore is therefore re-filed rather than freezing the repo, and an older task may still be open when the re-emit lands. Reconciling duplicates on the board is out of scope — the agent re-reads the surfaces from the repo, so an older task is a no-op or closes itself.
- The emitted task reuses `task_type: github-update-go` and `assignee: github-update-go-agent`, so no downstream Config change is needed. Its title is `Remove Stale Ignores <owner>-<repo>`.

Anchor the section with `## Stale-ignore signal` so the link from requirement 1 (`#stale-ignore-signal`) resolves.

### 3. `README.md` — the metrics table row

Add one row to the table under `## Metrics`, after `filter_skipped_total`:

```
| `ignore_scan_total` | `result` | Stale-ignore scans by outcome. Labels: `scanned`, `clean`, `stale`, `unresolved`, `error`, `rate_limited` |
```

The label set must match `pkg/metrics.go`'s `IgnoreScanResults` exactly, in that order. Do NOT add a row for emissions — `published_total` already counts them, and the section already says so for creates.

Do not add anything to `## Skip reasons` — the stale-ignore pass is not a filter and has no skip reason.

### 4. `docs/dod.md` — one sentence in the in-flight contract

Append one sentence at the end of the section `## In-flight signal contract (durable — do not break)`, as its own paragraph:

> **The stale-ignore emission (spec 008) is outside this contract.** It is deduplicated by `(repo, generation, sorted stale set)` — recorded in the cursor as `ignore_stale_ids`, `ignore_stale_generation` and `ignore_stale_emitted_at` — and it is never suppressed by, and never clears, the Go-bump emission claim described above.

Do not reword, reorder or delete anything already in that section, and do not touch the other sections.

### 5. `CHANGELOG.md` — new `## Unreleased` section above `## v0.11.2`

Insert directly above the `## v0.11.2` heading, separated by a blank line:

```markdown
## Unreleased

- feat: detect repos whose ignore surfaces suppress an advisory OSV has withdrawn, and file one `github-update-go` task for them — including repos that are current on Go, which the Go-version chain structurally cannot reach (spec 008). The pass runs inside the existing poll cycle behind a 24h per-repo rescan interval and a 10-repo per-cycle cap; it reads `VULNCHECK_IGNORE` from `Makefile`/`Makefile.precommit`, `.trivyignore` and `.osv-scanner.toml`; it resolves each suppressed ID on `api.osv.dev` with Go-index alias resolution so a merged CVE alias is still answered, and caches resolved verdicts in the cursor for 24h. Emission is deduplicated by repo plus a monotonic generation over the sorted stale set, so an unchanged set re-files at most once per 7 days with a new task identifier. Every upstream failure degrades to "no emission".
```

Do not add a version heading, do not rename or reorder existing sections, do not edit any released section.

### 6. Self-check before finishing

Re-run `<verification>` and confirm every command passes. Then re-read the new README section against `pkg/staleignore.go` and `pkg/metrics.go` and confirm every number (10, 24h, 7 days), every file name and every label is what the code actually says — a doc that drifts from the constants is worse than no doc.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Docs only: do not change any `.go` file, any constant, any metric name or label in this prompt.
- Do NOT claim the watcher classifies, removes or comments on an ignore, and do not claim it detects package-absent or version-not-affected staleness — the verdict is `withdrawn` and nothing else.
- Do NOT document a fourth surface as scanned; state that the `bborbe/coding` template is out of reach of a repo-level scan.
- Do NOT add an env var, flag, chart value or knob to the docs — there is none to document.
- Do NOT reword, reorder or delete existing text in `docs/dod.md`'s in-flight contract section; append one sentence.
- Do NOT edit any released `CHANGELOG.md` section or add a version heading.
- Do NOT add a skip reason to the README's `## Skip reasons` table.
- All paths in this prompt are repo-relative.
</constraints>

<verification>
Run from the repo root. Every `make` call MUST pass `ROOTDIR=/workspace` — `.dark-factory.yaml` sets `workflow: worktree`, the container masks `.git`, and `Makefile.variables` derives `ROOTDIR` from `git rev-parse --show-toplevel`, so an unqualified `make` dies before running any target.

```
ROOTDIR=/workspace make precommit
```

Must exit 0.

```
grep -c '^## Stale-ignore signal$' README.md
```

Must print exactly `1`.

```
grep -c 'cheap fleet signal cannot see that without cloning — with one exception' README.md
```

Must print exactly `1`.

```
grep -c 'ignore_scan_total' README.md
```

Must print at least `1`.

```
grep -c 'unresolved' README.md
```

Must print at least `1` — the unresolved-never-emits rule is documented.

```
grep -c 'The stale-ignore emission (spec 008) is outside this contract' docs/dod.md
```

Must print exactly `1`.

```
grep -c 'ignore_stale_generation' docs/dod.md
```

Must print exactly `1`.

```
awk '/^## /{print NR": "$0}' CHANGELOG.md | head -2
```

Must print two lines: the first `## Unreleased`, the second `## v0.11.2`.

```
grep -c '^## v' CHANGELOG.md
```

Must print exactly `33` — the same count as before the change, so no release section was added, removed or renamed (`## Unreleased` does not match `^## v`).

```
awk '/^## /{sec=$0} /stale-ignore signal|ignore surfaces suppress/{print "sits under: " sec}' CHANGELOG.md
```

Must print `sits under: ## Unreleased` — the bullet is in the unreleased section, not folded into a release.

```
! grep -q '## Stale-ignore signal' docs/dod.md && echo DOD_UNCHANGED_STRUCTURE
```

Must print `DOD_UNCHANGED_STRUCTURE` — the new signal is documented in the README, and only its contract sentence is in `docs/dod.md`.
</verification>
