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

Full dependency and vulnerability determination is the agent's job — it clones anyway during planning, and closes a repo as a no-op when there is nothing to change. A repo that is current on Go but has stale deps is reached on demand via `/github-update-go-repo-trigger <owner/repo>`; the cheap fleet signal cannot see that without cloning.

## Trigger model

Poll-primary, on its own interval — no dependency on Layer 1's signal for correctness. A poll is self-healing: a missed cycle, a crash, or a restart mid-scan all recover on the next tick, because "is this repo behind?" is a pure function of current state. The Layer-1 signal may later be consumed as a latency accelerator, but is not required.

## Status

Implemented. Not yet deployed — deployment (Helm/`quant` chart wiring) is a separate step outside this repo. Once deployed, the binary watches `OWNER` for Go repos behind the current stable release and publishes `CreateTaskCommand` to Kafka on a 10-minute interval (configurable via `POLL_INTERVAL`). Forced scans available via `POST /trigger`.

## Environment variables

| Name | Required | Default | Meaning |
|---|---|---|---|
| `OWNER` | yes | — | GitHub owner/org to scan (e.g. `bborbe`) |
| `STAGE` | yes | — | Deployment stage (`dev`\|`prod`), stamped on every emitted task |
| `KAFKA_BROKERS` | yes | — | Comma-separated list of Kafka brokers |
| `REPO_ALLOWLIST` | no | empty (allow-all) | Comma-separated host-qualified repo allowlist (`github.com/owner/repo`); empty = allow-all within `OWNER` |
| `CURSOR_PATH` | no | `/data/cursor.json` | Persisted-memory path (mount a PVC here via the `quant` Helm chart) |
| `POLL_INTERVAL` | no | `10m` | Poll interval as a Go duration (e.g. `5m`, `1h`) |
| `TOPIC_PREFIX` | no | empty | Kafka topic prefix for CQRS topic construction |
| `LISTEN` | no | `:9090` | HTTP listen address |
| `APP_ID` | yes* | — | GitHub App ID |
| `INSTALLATION_ID` | yes* | — | GitHub App Installation ID |
| `PEM_KEY` | yes* | — | GitHub App PEM key (populated from a k8s Secret; never logged) |
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

## Skip reasons

| Label | What triggers it |
|---|---|
| `scope` | Repo not in the `REPO_ALLOWLIST` |
| `no_gomod` | Repo has no `go.mod` file |
| `gomod_unparsable` | `go.mod` exists but its go directive is unreadable |
| `go_current` | Declared Go version is at or ahead of stable |
| `auto_update_disabled` | Repo has `goUpdate.autoUpdate: false` explicitly set in `.maintainer.yaml` |
| `auto_update_undecided` | `.maintainer.yaml` is absent, has no `goUpdate:` section, has no `autoUpdate` key, or the key holds a non-boolean value — the owner has never answered. A decision task is filed once per repo (see [Decision task contract](#decision-task-contract)) |
| `sha_unchanged` | Repo HEAD SHA has not changed since last successful cycle (not evaluated on forced cycles) |

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
| `task_identifier` | deterministic UUID5 derived from `(owner, repo, HEAD SHA)` |
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
| `phase` | `planning` |
| `status` | `in_progress` |
| `stage` | `<STAGE>` (`dev` or `prod`) |
| `task_identifier` | deterministic UUID5 derived from `(owner, repo)` only |
| `title` | `Go Update Decision <owner>-<repo>` (no HEAD SHA) |
| `repo` | `<owner>/<repo>` |
| `current_go` | normalised three-part current Go version (e.g. `1.24.0`) |
| `latest_go` | current stable three-part Go version (e.g. `1.26.6`) |

Body: a short explanation plus both `.maintainer.yaml` answers (`autoUpdate: true` to opt in, `autoUpdate: false` to opt out) — either answer stops future decision tasks for that repo.

## Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/healthz` | GET | Liveness probe — always 200 OK |
| `/readiness` | GET | Readiness probe — always 200 OK |
| `/metrics` | GET | Prometheus scrape endpoint |
| `/trigger` | POST | Force an immediate poll cycle. `?force=<bool>` (default false) controls whether SHA-unchanged repos are re-evaluated. Returns 202 Accepted or 409 Conflict if a cycle is already running |
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
