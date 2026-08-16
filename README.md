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

1. **`REPO_ALLOWLIST`** (env, host-qualified, `lib/repoallowlist`) — the operator's scope. Empty means allow-all within `OWNER`.
2. **`.maintainer.yaml: goUpdate.autoUpdate: true`** in the target repo — the repo owner's consent.

The second is a **trust gate**: a repo with no `.maintainer.yaml`, no `goUpdate:` section, or `autoUpdate` absent/false is skipped. Opt-in, never opt-out — this watcher causes code to be committed to the target repo, so it does not act without explicit consent.

Same shape as `github-release-watcher`'s `release.autoRelease` gate. See the [Watcher Writing Guide] rule: gate on `.maintainer.yaml` when the watcher initiates repo-modifying work with no per-change human trigger.

## Cheap signal

Detection is deliberately shallow: fetch `go.mod` via the GitHub contents API and compare its go-directive against latest stable. **No clone.**

Full dependency and vulnerability determination is the agent's job — it clones anyway during planning, and closes a repo as a no-op when there is nothing to change. A repo that is current on Go but has stale deps is reached on demand via `/github-update-go-repo-trigger <owner/repo>`; the cheap fleet signal cannot see that without cloning.

## Trigger model

Poll-primary, on its own interval — no dependency on Layer 1's signal for correctness. A poll is self-healing: a missed cycle, a crash, or a restart mid-scan all recover on the next tick, because "is this repo behind?" is a pure function of current state. The Layer-1 signal may later be consumed as a latency accelerator, but is not required.

## Status

Scaffolded. Watcher logic not yet implemented — see the spec in `specs/`.
