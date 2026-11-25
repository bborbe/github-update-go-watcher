# github-update-go-watcher

Polls the GitHub API for Go repos whose `go.mod` go-directive is behind the latest stable Go release, and publishes one `CreateTaskCommand` to Kafka per affected repo so the [`github-update-go-agent`](https://github.com/bborbe/github-update-go-agent) picks each up and opens the bump PR autonomously.

The watcher is the **producer** half of the pipeline. It never modifies the target repo — no clone, no commit, no push. The agent owns update execution.

This is **Layer 2** of the Go-update pipeline:

| Layer | Component | Role |
|---|---|---|
| 1 — detect | [`go-version-watcher`](https://github.com/bborbe/go-version-watcher) | polls go.dev, emits one global "new Go released" signal |
| **2 — fan out** | **this repo** | one signal → one task per affected repo |
| 3 — execute | [`github-update-go-agent`](https://github.com/bborbe/github-update-go-agent) | bump, deps, vuln scan, `make precommit` gate, PR |

## Scope — two gates, both must pass

A repo is only acted on when **both** hold:

1. **`REPO_ALLOWLIST`** (env, host-qualified, validated via `github.com/bborbe/maintainer/repoallowlist`) — the operator's scope. Empty means allow-all within `OWNER`.
2. **`.maintainer.yaml: goUpdate.autoUpdate: true`** in the target repo — the repo owner's consent.

The second is a **trust gate**: a repo with `autoUpdate: false`, explicitly set, is skipped and stays silent. A repo whose owner has never answered at all — no `.maintainer.yaml`, no `goUpdate:` section, no `autoUpdate` key, or a non-boolean value — is also skipped, but additionally gets a one-time decision task filed asking the owner to answer (see [Decision task contract](#decision-task-contract)). Opt-in, never opt-out — this watcher causes code to be committed to the target repo, so it does not act without explicit consent, and it never defaults an unanswered repo to consent.

Same shape as `github-release-watcher`'s `release.autoRelease` gate. See the [Watcher Writing Guide] rule: gate on `.maintainer.yaml` when the watcher initiates repo-modifying work with no per-change human trigger.

## Cheap signal

Detection is deliberately shallow: fetch `go.mod` via the GitHub contents API and compare its go-directive against latest stable. **No clone.**

Full dependency and vulnerability determination is the agent's job — it clones anyway during planning, and closes a repo as a no-op when there is nothing to change. A repo that is current on Go but has stale deps is reached on demand via `/github-update-go-repo-trigger <owner/repo>`; the cheap fleet signal cannot see that without cloning — with one exception, the **stale ignore**: an advisory OSV has withdrawn is visible from the repo's ignore files alone, and is detected by the [stale-ignore signal](#stale-ignore-signal).

## Monthly full update

Independently of the Go-version signal, the watcher runs a monthly pass: every repo that passes the allowlist, `go.mod` and consent gates receives one `github-update-go` task per calendar month, whether or not its Go directive is current and whether or not its HEAD moved. The pass runs inside the existing poll cycle (first cycle of the month emits, later cycles skip with `month_unchanged`) and can be forced with `POST /trigger?monthly=1&month=YYYY-MM`. Each monthly task names its month in the title (`Monthly Deps Update <owner>-<repo> <YYYY-MM>`) and carries a month-keyed identifier, so a re-emit in the same month is a downstream no-op. The month deliberately rides in the title and the identifier rather than a frontmatter field: the task store rejects unknown frontmatter keys with `400 unknown_field`. The Go-version pass is unchanged.

## Stale-ignore signal

This is the watcher's **second, independent signal**. The first is "declared Go version behind stable"; this one is "an ignore suppresses an advisory that no longer exists". Both cause `github-update-go-agent` to open a repo-modifying PR, so both honour the same [two gates](#scope--two-gates-both-must-pass) — the operator allowlist and `goUpdate.autoUpdate: true`. A repo that has refused consent is never auto-cleaned, so a Go-current repo with consent refused keeps its stale ignore until an operator clears it by hand. That is a deliberate limitation, not an oversight.

It runs **inside the existing poll cycle** — no extra cron, deployment or configuration. Its cost is bounded by two constants: at most **10** repos per cycle and a **24-hour** per-repo rescan interval, so a fleet of a few hundred repos is covered about once a day. Per selected repo it reads at most four files (three GitHub calls to gather the repo plus up to four surface reads).

The three ignore **surfaces**, across four files:

- the `VULNCHECK_IGNORE` assignment in `Makefile` and `Makefile.precommit` (including a `\`-continued multi-line assignment)
- `.trivyignore` — one ID per line, `#` comments and trailing remarks ignored
- `.osv-scanner.toml` — every `id = "…"` inside an `[[IgnoredVulns]]` block

A missing file is an empty surface, not an error. The fleet's fourth surface — the `TRIVY_IGNORE` template in `bborbe/coding` — is out of reach of a repo-level scan and is covered only when that repo itself is scanned.

The **verdict** is exactly one: `withdrawn`. Each suppressed ID is looked up on `api.osv.dev`; an ID whose own record 404s is resolved through the Go vulnerability index, because OSV merges some CVEs into a `GO-*` record and only the merged record carries the withdrawal. A 404 that resolves to no record, and any transport or decode failure, is **unresolved** — never stale, and never an emission. Resolved verdicts are cached in the cursor for a day; unresolved IDs are retried next cycle.

What it does **not** do: it does not classify an ignore as stale for any other reason (package absent, version no longer affected), it does not remove or comment on anything, and it does not triage firing vulnerabilities. Those are the agent's planning-time job and [`/github-update-go-vuln-watcher`](https://github.com/bborbe/github-update-go-vuln-watcher)'s role respectively.

**Duplicates.** One task is filed per repo per stale set, identified by repo, a monotonic generation and the sorted set of stale IDs. An unchanged set is re-filed at most once every **7 days**, with a new identifier so the record actually runs; a task that dies without removing the ignore is therefore re-filed rather than freezing the repo, and an older task may still be open when the re-emit lands. Reconciling duplicates on the board is out of scope — the agent re-reads the surfaces from the repo, so an older task is a no-op or closes itself.

The emitted task reuses `task_type: github-update-go` and `assignee: github-update-go-agent`, so no downstream Config change is needed. Its title is `Remove Stale Ignores <owner>-<repo>`.

## Trigger model

Poll-primary, on its own interval — no dependency on Layer 1's signal for correctness. A poll is self-healing: a missed cycle, a crash, or a restart mid-scan all recover on the next tick, because "is this repo behind?" is a pure function of current state. The Layer-1 signal may later be consumed as a latency accelerator, but is not required.

## Status

Implemented. Not yet deployed — deployment (the Helm chart in `bborbe/nuke`) is a separate step outside this repo. Once deployed, the binary watches `OWNER` for Go repos behind the current stable release and publishes `CreateTaskCommand` to Kafka on a 10-minute interval (configurable via `POLL_INTERVAL`). Forced scans available via `POST /trigger`.

## Environment variables

| Name | Required | Default | Meaning |
|---|---|---|---|
| `OWNER` | yes | — | GitHub owner/org to scan (e.g. `bborbe`) |
| `STAGE` | yes | — | Deployment stage (`dev`\|`prod`), stamped on every emitted task |
| `TARGET_VAULT` | yes | — | Vault slug stamped on every emitted task (e.g. `openclaw`). The controller refuses an empty `target_vault` instead of falling back, so an unset value fails at startup rather than silently disabling the pipeline |
| `KAFKA_BROKERS` | yes | — | Comma-separated list of Kafka brokers |
| `REPO_ALLOWLIST` | no | empty (allow-all) | Comma-separated host-qualified repo allowlist (`github.com/owner/repo`); empty = allow-all within `OWNER` |
| `CURSOR_PATH` | no | `/data/cursor.json` | Persisted-memory path (mount a PVC here via the Helm chart in `bborbe/nuke`) |
| `POLL_INTERVAL` | no | `10m` | Poll interval as a Go duration (e.g. `5m`, `1h`) |
| `TOPIC_PREFIX` | no | empty | Kafka topic prefix for CQRS topic construction |
| `LISTEN` | no | `:9090` | HTTP listen address |
| `APP_ID` | yes* | — | GitHub App ID |
| `INSTALLATION_ID` | yes* | — | GitHub App Installation ID |
| `PEM_KEY` | yes* | — | GitHub App PEM key (populated from a k8s Secret; never logged) |
| `GITHUB_API_BASE_URL` | no | `https://api.github.com` | GitHub API base URL (a test server or a GitHub Enterprise host) |
| `SENTRY_DSN` | no | — | Sentry DSN for error reporting |

\* `APP_ID`, `INSTALLATION_ID`, and `PEM_KEY` are each individually marked `required:"false"` on the binary's argument struct (so the process can start and print `--help` without them), but `ResolveGitHubClient` refuses to run without all three set — the binary fails at startup with "GitHub App credentials not configured" if any is missing. Set all three or none.

## Metrics

All metrics are prefixed with `github_update_go_watcher_`.

| Metric | Labels | Meaning |
|---|---|---|
| `poll_cycle_total` | `result` | Total cycles by outcome. Labels: `success`, `rate_limited`, `github_error`, `go_version_error` |
| `published_total` | `status` | Tasks published to Kafka. Labels: `create`, `error` |
| `repos_scanned_total` | — | Total repos scanned across all cycles |
| `filter_skipped_total` | `reason` | Repos skipped by the filter chain. Labels: see Skip reasons |
| `ignore_scan_total` | `result` | Stale-ignore scans by outcome. Labels: `scanned`, `clean`, `stale`, `unresolved`, `error`, `rate_limited` |

## Skip reasons

| Label | What triggers it |
|---|---|
| `scope` | Repo not in the `REPO_ALLOWLIST` |
| `no_gomod` | Repo has no `go.mod` file |
| `gomod_unparsable` | `go.mod` exists but its go directive is unreadable |
| `go_current` | Declared Go version is at or ahead of stable |
| `auto_update_disabled` | Repo has `goUpdate.autoUpdate: false` explicitly set in `.maintainer.yaml` |
| `auto_update_undecided` | `.maintainer.yaml` is absent, has no `goUpdate:` section, has no `autoUpdate` key, or the key holds a non-boolean value — the owner has never answered. A decision task is filed once per repo (see [Decision task contract](#decision-task-contract)) |
| `sha_unchanged` | Repo HEAD SHA and target Go version are both unchanged since the last successful cycle (not evaluated on forced cycles). Also the fall-through for a claim cleared against a non-progressing record at an unchanged HEAD: the re-emit would be identity-identical, so the repo keeps its dedup key and is skipped here rather than re-emitted for nothing |
| `open_update_pr` | Repo has an open `fix/update-go-*` pull request — an update is already in flight, so no new task is emitted (always-on gate, spec 003) |
| `emission_in_flight` | Repo was already emitted for the current target Go version and none of the four clearing events has occurred (convergence, an open `fix/update-go-*` PR, the 14-day backstop, or the emitted task no longer being able to progress — terminal, or parked at `human_review`). Evaluated before the filter chain, so a claimed repo reports this reason rather than `sha_unchanged` |
| `month_unchanged` | Monthly pass only: the repo's monthly full-update task was already emitted for this calendar month. The monthly pass ignores the Go version and HEAD movement, so this is the only gate that stops a second emission in the same month |

## Emitted task contract

**Frozen against the shipped `github-update-go-agent` consumer — changing any of this breaks it.**

The watcher publishes one `CreateTaskCommand` per qualifying repo. The command's frontmatter carries exactly these twelve keys:

| Key | Value |
|---|---|
| `task_type` | `github-update-go` |
| `assignee` | `github-update-go-agent` (never `go-update-agent` — that agent does not exist) |
| `phase` | `planning` |
| `status` | `in_progress` |
| `stage` | `<STAGE>` (`dev` or `prod`) |
| `task_identifier` | deterministic UUID5 derived from `(owner, repo, target Go version, HEAD SHA)` |
| `title` | `Update Go <owner>-<repo> <sha[:7]>` (dash, not slash — the vault filename is derived from this verbatim) |
| `repo` | `<owner>/<repo>` |
| `clone_url` | `git@github.com:<owner>/<repo>.git` |
| `ref` | full HEAD SHA |
| `current_go` | normalised three-part current Go version (e.g. `1.24.0`) |
| `latest_go` | current stable three-part Go version (e.g. `1.26.6`) |

Body (byte-for-byte, two spaces either side of the middot `·`):
```markdown
# Update Go: <owner>/<repo>

**Current Go:** <X.Y.Z>  ·  **Latest Go:** <X.Y.Z>
**HEAD:** <sha[:7]>
**Repo:** [<owner>/<repo>](https://github.com/<owner>/<repo>)
```

## Decision task contract

When a repo's consent is undecided (see [Skip reasons](#skip-reasons)), the watcher publishes a second, distinct `CreateTaskCommand` shape — a decision task addressed to a human, not to the update agent. Re-emitted every cycle the repo stays undecided; the task identifier is derived from `(owner, repo)` only (no HEAD SHA), so a re-emit is a downstream no-op, not a duplicate.

The command's frontmatter carries exactly these ten keys:

| Key | Value |
|---|---|
| `task_type` | `github-update-go-decision` |
| `assignee` | `bborbe` (never `github-update-go-agent` — this is a human decision, not agent work) |
| `phase` | `human_review` |
| `status` | `in_progress` |
| `stage` | `<STAGE>` (`dev` or `prod`) |
| `task_identifier` | deterministic UUID5 derived from `(owner, repo)` only |
| `title` | `Go Update Decision <owner>-<repo>` (no HEAD SHA) |
| `repo` | `<owner>/<repo>` |
| `current_go` | normalised three-part current Go version (e.g. `1.24.0`) |
| `latest_go` | current stable three-part Go version (e.g. `1.26.6`) |

Body: a short explanation plus both `.maintainer.yaml` answers (`autoUpdate: true` to opt in, `autoUpdate: false` to opt out) — either answer stops future decision tasks for that repo.

### One-time migration for records already on the board

Decision records used to rest at `phase: planning`, which the `UnreachablePhase` alert reports as a pair no executor Config accepts — and no Config ever will, because these records are human work. Decision records now rest at `phase: human_review`, which that alert excludes.

Records already on the board are **not** rewritten by the next cycle: the identifier is `(owner, repo)`-keyed, so the re-emit is an identity-identical create and the controller treats it as a no-op. Each one has to be moved once, in place. Note the two names — this watcher stamps `TARGET_VAULT: openclaw`, which is the controller's vault name; the same vault is registered with `vault-cli` as `private-agent`, and `--vault openclaw` fails with `vault not found`.

```bash
vault-cli --vault private-agent task set "Go Update Decision <owner>-<repo>" phase human_review
```

The move is safe and permanent — a later identity-identical re-emit does not touch the file, so the phase stays where it was set. Answering the decision in `.maintainer.yaml` instead also stops the record being re-emitted, but leaves the existing record at `planning` and the alert firing, so do both.

## Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/healthz` | GET | Liveness probe — always 200 OK |
| `/readiness` | GET | Readiness probe — always 200 OK |
| `/metrics` | GET | Prometheus scrape endpoint |
| `/trigger` | POST | Force an immediate poll cycle. `?force=<bool>` (default false) controls whether SHA-unchanged repos are re-evaluated. `?monthly=1` additionally runs the monthly pass; `&month=YYYY-MM` overrides the month (UTC, default the current month, bounded to current ± 1 — anything else is 400). Returns 202 Accepted or 409 Conflict if a cycle is already running |
| `/setloglevel/{level}` | GET | Raise glog verbosity to `level`, auto-resets after 5 minutes |
| `/gc` | GET | Trigger Go garbage collection |
| `/testloglevel` | GET | Emit test log lines at every verbosity level |
| `/sentryalert` | POST | Send a test event to Sentry |

## Forced-emit end-to-end verification

To manually trigger a scan and verify the full detect→Kafka→controller→vault-file path:

1. Confirm the target repo is listed in `REPO_ALLOWLIST` (or `REPO_ALLOWLIST` is empty) and carries `goUpdate.autoUpdate: true` in `.maintainer.yaml`.
2. `curl -X POST '<admin-url>/trigger?force=true'` — expect `202 {"status":"accepted"}`.
3. If a cycle is already running, the endpoint returns `409 Conflict` — wait for the running cycle to complete and retry.
4. Confirm `github_update_go_watcher_published_total{status="create"}` incremented on `GET /metrics`.
5. Confirm the vault task file appeared at `Update Go <owner>-<repo> <sha[:7]>.md`.

This verifies the complete path that unit tests cannot reach: GitHub API → filter chain → Kafka → command consumer → vault file write.

## Development

```bash
make test      # Run tests
make precommit # Full development workflow
```

## Build

The image builds either on this machine or inside a cluster.

```bash
make build                                                   # local Docker daemon
make build BUILD_TARGET=nukeprod REF=<40-char-sha> VERSION=<tag>  # Kubernetes Job in the cluster
```

`BUILD_TARGET` defaults to `local`; any other value names a cluster alias (`sun`, `nukedev`, `nukeprod`) or a kubectl context and builds inside that cluster through `hack/build-cluster.sh`. The cluster arm requires `VERSION` on the command line — the Makefile default is the newest release tag, and pushing that over a released image is irreversible.

Both arms push `docker.io/bborbe/github-update-go-watcher`. `upload`, `clean` and `buca` are local-only and are refused for a cluster target; the cluster Job pushes from inside the cluster and leaves nothing on this host.
