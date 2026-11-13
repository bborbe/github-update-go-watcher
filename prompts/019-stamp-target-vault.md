---
status: in-progress
summary: Stamps target_vault on every emitted create-task and complete-task command, so the controller stops refusing the re-emit at validation instead of silently no-oping
created: "2026-11-12T23:50:00Z"
queued: "2026-11-12T23:50:00Z"
---

<!-- Implemented directly on branch fix/stamp-target-vault: the dark-factory
     container cannot reach the model router (127.0.0.1:8788 is not the host
     inside the container, host.docker.internal does not resolve), the same
     reason prompt 018 was implemented by hand. Kept as the design record. -->

<summary>
- Every emitted `CreateTaskCommand` and `CompleteTaskCommand` carries a non-empty `target_vault`, taken from the new required `TARGET_VAULT` env / `--target-vault` flag.
- The watcher's command senders were built with an empty default vault (`pkg/factory/factory.go:58`, `:69`) and `buildFrontmatter` never set `target_vault`, so every command went out with an empty field.
- The controller refuses an empty `target_vault` rather than falling back to its legacy default: `create-task: invalid target_vault … the producer must stamp the owning vault`. The publish therefore succeeded and wrote nothing — measured on prod 2026-11-12 as 23 publishes and 0 task files, with the failure invisible on the watcher side.
- The identity machinery is not involved and is left alone. `UUID5(owner, repo, goVersion, headSHA)` still derives the same identifier for the same HEAD, and the controller's `ReopenTerminal` path already replaces a terminal occupant (observed `reopening terminal task 4e314fb2…` on prod). A terminal record is re-opened once the command reaches the controller at all.
- `TARGET_VAULT` is required rather than defaulted: an empty vault is a deployment error that silently disables the whole pipeline, so it must fail at startup instead of publishing inert commands.
- Both senders get the same value, because the complete command is routed by the same vault slug as the create.
</summary>

<objective>
Stamp `target_vault` on every command the watcher emits. Add `TARGET_VAULT` (required) to the application config, thread it through `CreateKafkaSender` / `CreateKafkaCompleteSender`, and pin the stamping with a unit test that reads the payload the typed sender actually produced.
</objective>

<context>
`pkg/factory/factory.go` builds both senders with an empty default vault. `pkg/taskbuilder.go`'s `buildFrontmatter` sets no `target_vault`. `github.com/bborbe/agent/command/task`'s senders fill `TargetVault` from their `defaultVault` only when it is non-empty, so an empty default ships an empty field.

Deployment: `nuke-prod/github-update-go-watcher/values-prod.yaml` already sets `TARGET_VAULT: openclaw` (the legacy default the controller used to apply), but the deployed v0.9.1 never read it — `git grep TARGET_VAULT v0.9.1 -- '*.go'` returns nothing. `nuke-dev`'s values do not set it and the dev pin is v0.6.5; bumping dev past this release requires adding `TARGET_VAULT` there first, or the pod fails its startup validation.
</context>
