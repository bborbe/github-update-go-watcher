---
status: approved
approved: "2026-11-09T08:46:31Z"
branch: dark-factory/cluster-build-arm
---

## Summary

- Add a second build path to this repo: alongside today's local `docker build`, `make build BUILD_TARGET=nukeprod` builds the image inside the cluster as a Kubernetes Job and pushes it to Docker Hub.
- Copy five `hack/` files: the runner, the Job template and the cluster alias map from `agent-task-executor` (its Job carries `automountServiceAccountToken: false`), plus the bootstrap script and its template from `go-skeleton`.
- Compile the two existing `build` prerequisites out of the cluster arm: `check-version-tag` refuses a scratch tag, and `check-go-mod` vendors into the local working tree — neither is meaningful for a build that runs from a clone inside a cluster.
- Keep the local arm's command, tag and guard exactly as they are, and keep `upload`, `clean` and `buca` local-only.
- No Dockerfile change: this repo's `go build` carries no `-mod` flag, so it already resolves in module mode from a clone with no `vendor/`.

## Problem

`github-update-go-watcher` is a publish-only source repo: `Makefile.docker` runs one `docker build` on the invoking machine, `upload` pushes it, and `buca` chains both. The image is consumed by the fleet — `bborbe/nuke`'s `github-update-go-watcher/Makefile` lists `github-update-go-watcher:$(WATCHER_TAG)` in `MIRROR_IMAGES` and mirrors it into `docker.prod.nuke.benjamin-borbe.de:443` — so every release of this repo currently needs a human with a working Docker daemon at the keyboard.

Four properties of this repo make a naive paste of the sibling pattern fail at runtime rather than at review:

- **`build` is chained to a version guard.** `Makefile.docker:8` declares `build: check-version-tag check-go-mod`, and `check-version-tag` refuses unless `git describe --tags --exact-match HEAD` equals `$(VERSION)`. A cluster build of a named commit with a scratch tag therefore exits 1 before any Job is created. The escape hatch that exists today (`ALLOW_UNTAGGED_BUILD=1`) is per-invocation and does not change the guard's placement.
- **`build` is also chained to `go mod vendor`.** `check-go-mod` writes `vendor/` into the invoking working tree. A cluster build builds a *named ref*, not the local `HEAD`, so preparing the local tree there is both meaningless and wrong.
- **`VERSION` has a default, and that default is what a cluster arm must not silently publish.** `Makefile.docker:3-5` assigns `VERSION := $(shell git describe --tags \`git rev-list --tags --max-count=1\`)` when the caller supplied nothing, so a cluster arm reading `$(VERSION)` unchanged would push the *latest release tag* for a build the caller never named — replacing a released image.
- **The image reference is composed from `SERVICE`, which is assigned after the include.** `Makefile` includes `Makefile.docker` at line 3 and sets `SERVICE = github-update-go-watcher` at line 10, while `Makefile.docker` tags `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)`. The tag resolves correctly because recipe bodies expand at recipe time; a parse-time guard or `$(error …)` in `Makefile.docker` that reads `$(SERVICE)` would see it empty.

Two further properties only matter once the arm exists:

- The local arm tags `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)` where `DOCKER_REGISTRY ?= docker.io` — so the local arm already publishes to Docker Hub. The cluster push credential authenticates against Docker Hub too (`hack/dockerconfig.json.tpl` carries exactly one `auths` entry, `https://index.docker.io/v1/`), so the two arms compose the *same* repository and need no separate cluster image variable.
- `upload`, `clean` and `buca` all assume a local Docker daemon. Left unguarded, `make buca BUILD_TARGET=nukeprod` builds successfully in the cluster and then fails at the local push.

## Goal

`make build` on this repo produces the image locally exactly as it does today, and `make build BUILD_TARGET=nukeprod REF=<sha> VERSION=<tag>` produces it inside the cluster as a Kubernetes Job pushing `docker.io/bborbe/github-update-go-watcher:<tag>` — the repository nuke's mirror step reads. Both arms work from the same checkout, the cluster arm works from a fresh clone with no local preparation, and a cluster build can never be made to publish a tag the caller did not name.

## Non-goals

- Changing the local arm's command, tag or `check-version-tag` refusal. All stay as they are; the guard is re-scoped, never removed.
- Making the cluster the Makefile default. `BUILD_TARGET ?= local` stays, so a bare `make build` is a local build.
- Renaming the image. `SERVICE = github-update-go-watcher` under `DOCKER_REGISTRY ?= docker.io` is what nuke mirrors.
- The nuke `WATCHER_TAG` pin bump and its `MIRROR_IMAGES` entry. The mirror entry already exists; moving the pin is a deploy step that follows the release.
- Building at release or tag time. This spec adds the capability; *when* a build is triggered is a separate concern.
- Extracting the shared runner into an upstream template. The files are copied, not shared.
- Multi-arch images. `linux/amd64` only, matching the cluster nodes.
- Extra build knobs beyond `BUILD_TARGET`, `REF` and `VERSION`. The repository URL, the Dockerfile path and the push-secret name are literals in the dispatch.
- A CI / GitHub Actions build, or running `docker build` on a cluster host over SSH. Both would need a new credential path and a new trigger; the fleet already has a Job-based build lane whose push credential is scoped to exactly this job, so the arm reuses it.

## Acceptance Criteria

- [ ] **The version guard still refuses on an untagged `HEAD`** — `make check-version-tag` exits non-zero and prints `ERROR: refusing to build` naming the tag mismatch, and `ALLOW_UNTAGGED_BUILD=1 make check-version-tag` exits 0. Evidence: both exit codes and the refusal text. This half runs in-container.
- [ ] **Post-Deploy (Rung-2):** the local arm still builds and tags as today, on the post-change tree — on this branch with no `BUILD_TARGET` set, `ALLOW_UNTAGGED_BUILD=1 make build VERSION=probe` exits 0 and `docker image inspect docker.io/bborbe/github-update-go-watcher:probe` exits 0. Evidence: the build exit code and the inspected `.Id`. ⚠️ Run it on the branch, not on `v0.8.2`: that tag predates the change, so a build there exercises none of it and the regression gate would be inert.
  - `deploy_check:` `docker version --format '{{.Server.Version}}'`
  - `deploy_target:` `local-docker-daemon`
- [ ] **Post-Deploy (Rung-2):** the cluster arm builds and pushes from a fresh clone — `make build BUILD_TARGET=nukeprod REF=<full 40-character sha> VERSION=<scratch-tag>` exits 0, and `crane digest docker.io/bborbe/github-update-go-watcher:<scratch-tag>` resolves to a digest. Evidence: the exit code, the Job name, the build pod's `spec.nodeName`, the resolved digest, and `docker image inspect` on the same reference failing on this Mac with `No such image` — a stopped daemon or a malformed reference does not satisfy this. ⚠️ This is the only *cluster* behavioural AC — AC2 is behavioural too, but local. Every remaining one is a string probe on `make -n` output, so an arm that echoes the assignment block without calling `hack/build-cluster.sh` passes them all and fails only here.
  - `deploy_check:` `kubectlnukeprod -n build get secret buildkit-push -o jsonpath='{.metadata.name}'`
  - `deploy_target:` `nukeprod-cluster/build-namespace`
- [ ] **The cluster arm pushes the Docker Hub repository nuke's mirror step reads** — `make -n build BUILD_TARGET=nukeprod REF=<sha> VERSION=probe` prints the dispatch line carrying `IMAGE='docker.io/bborbe/github-update-go-watcher'` and `TAG='probe'`. Evidence: the printed line, and the digest above resolving at exactly that repository. ⚠️ `make -n` alone proves intent, not destination; the digest resolving at that reference is the load-bearing half.
- [ ] **`hack/` holds the five files, with their real bodies, and the cluster branch invokes the runner** — on the commit under verification, `git ls-tree --name-only HEAD hack/` lists `build-cluster.sh`, `cluster-alias.sh`, `build-job.yaml`, `build-cluster-bootstrap.sh` and `dockerconfig.json.tpl`; `grep -c '^BUILD_TARGET' Makefile.docker` is ≥ 1, `grep -cE '^[[:space:]]*hack/build-cluster\.sh' Makefile.docker` is ≥ 1, and `git ls-tree HEAD hack/ | grep -c '^100755'` is 3 — the three scripts are invoked as `hack/<script>` and lose the exec bit in a plain `cp`. Evidence: the five names, the mode count, both Makefile counts, and one content probe per copied file so a stub cannot pass — `grep -c 'SOURCE_DATE_EPOCH' hack/build-cluster.sh` ≥ 1, `grep -c 'automountServiceAccountToken: false' hack/build-job.yaml` = 1, `grep -c 'auths' hack/dockerconfig.json.tpl` = 1, `grep -c 'KUBECONFIG_PATH' hack/cluster-alias.sh` ≥ 1, `grep -c 'create secret' hack/build-cluster-bootstrap.sh` ≥ 1, and — because AC3's single behavioural probe passes `BUILD_TARGET=nukeprod` — `grep -c 'nukeprod' hack/cluster-alias.sh` ≥ 1. ⚠️ Two traps here: the copied header comments name `BUILD_TARGET`, so a bare `grep -c BUILD_TARGET` passes on a comment alone (the `^` anchor is the probe); and `bash -n` on an empty file exits 0, so name-plus-mode proves nothing about the body — the content probes are what move the floor from "empty file with the exec bit" to "the real copied script".
- [ ] **The version guard is off the cluster arm's prerequisite list and still on the local one** — on an untagged `HEAD`, `make -p -n build BUILD_TARGET=nukeprod REF=<sha> VERSION=probe 2>/dev/null | grep -E '^build:'` prints `build:` with no `check-version-tag`, while the same probe without `BUILD_TARGET` prints `build: check-version-tag check-go-mod`. Evidence: the two `build:` lines and the refusal from the first criterion. ⚠️ `make -n` alone cannot show this — it never executes a recipe, so an unwrapped and a wrapped guard both exit 0 under it.
- [ ] **The cluster arm refuses a tag it did not receive from the caller** — `make -n build BUILD_TARGET=nukeprod REF=<sha>` with no `VERSION` on the command line exits non-zero and prints a message naming `VERSION`, and the same invocation with `VERSION=probe` exits 0. Evidence: both exit codes and the message. ⚠️ The refusal must be a `$(error …)` inside the cluster branch of the `build` recipe — not a shell `if` (invisible to `-n`) and not a file-level `$(error …)` (which would also fire for `upload` / `clean` / `buca`, breaking the next criterion). It must reject `$(origin VERSION)` of `file` — the default at `Makefile.docker:3-5` — as well as empty, accepting only `command line` / `environment`.
- [ ] **`upload`, `clean` and `buca` stay local and are refused for a cluster target** — `make -n upload BUILD_TARGET=nukeprod` exits non-zero naming the target as local-only, and so do `clean` and `buca`; the same three with no `BUILD_TARGET` print their `docker` recipes. Evidence: six exit codes, plus `make -n buca BUILD_TARGET=nukeprod 2>&1 | grep -c 'local-only'` = 1, and the same three probes with no `BUILD_TARGET` printing their recipes. The refusal is a `$(error …)` keyed on `BUILD_TARGET`, so it fires under `-n` where no recipe runs. ⚠️ Two constraints make this jointly satisfiable with the previous criterion. First, the `VERSION` refusal is scoped to the `build` recipe, not file-level — a file-level `$(error)` would fire for these three too. Second, **`buca`'s prerequisites must be target-conditional** — `buca: build upload clean apply` traverses `build` first, so `make -n buca BUILD_TARGET=nukeprod` would hit `build`'s `VERSION` error and never reach `buca`'s own. Write it as `buca: $(if $(filter local,$(BUILD_TARGET)),build upload clean apply)`, which leaves the local `buca` chain byte-identical and lets the cluster `buca` reach its own refusal.
- [ ] **The documented build path matches reality** — `README.md` carries a *Build* section showing both invocations on one line each, stating that `buca` is local-only and that `VERSION` is required on the cluster arm; the four stale `quant/` references are corrected to name `bborbe/nuke` (`Makefile:9`, `Makefile.docker:59-60`, `README.md:38`, `README.md:48` — the component moved to nuke on 2026-08-21, and the new Build section would contradict them); `.gitignore` gains the `specs` lock pattern; `CHANGELOG.md` carries a `## Unreleased` section with one bullet for this change. Evidence: `grep -c '^## Build' README.md` = 1, `grep -c 'make build BUILD_TARGET=nukeprod' README.md` ≥ 1, `grep -c 'make build' README.md` ≥ 2, `grep -ciE 'buca.*local' README.md` ≥ 1, `grep -c 'quant' README.md` = 0, `grep -c 'quant' Makefile Makefile.docker` = 0, `grep -c '^/specs/in-progress/\*\.lock$' .gitignore` = 1, and `awk '/^## Unreleased/{f=1;next} /^## /{f=0} f&&/^- /{c++} END{print c+0}' CHANGELOG.md` = 1. ⚠️ The CHANGELOG heading alone passes on an empty section.

## Verification

### Container-executable (runs inside the build container at prompt time)

- `make precommit` — lint, format, vet, test and security checks clean
- `bash -n hack/build-cluster.sh && bash -n hack/cluster-alias.sh && bash -n hack/build-cluster-bootstrap.sh` — the copied scripts parse
- `grep -c '^BUILD_TARGET' Makefile.docker` returns ≥ 1
- `grep -cE '^[[:space:]]*hack/build-cluster\.sh' Makefile.docker` returns ≥ 1
- `git ls-tree --name-only HEAD hack/` lists the five expected files
- `git ls-tree HEAD hack/ | grep -c '^100755'` returns 3
- `make check-version-tag` on an untagged HEAD exits non-zero; `ALLOW_UNTAGGED_BUILD=1 make check-version-tag` exits 0
- `make -p -n build 2>/dev/null | grep -E '^build:'` lists `check-version-tag check-go-mod`
- `make -p -n build BUILD_TARGET=nukeprod REF=<sha> VERSION=probe 2>/dev/null | grep -E '^build:'` lists neither
- `make -n build BUILD_TARGET=nukeprod REF=<sha> VERSION=probe` prints `hack/build-cluster.sh` and `IMAGE='docker.io/bborbe/github-update-go-watcher'`
- `make -n build BUILD_TARGET=nukeprod REF=<sha>` exits non-zero naming `VERSION`
- `make -n upload BUILD_TARGET=nukeprod`, `make -n clean BUILD_TARGET=nukeprod`, `make -n buca BUILD_TARGET=nukeprod` each exit non-zero, and `make -n buca BUILD_TARGET=nukeprod 2>&1 | grep -c 'local-only'` returns 1 — the last probe is what proves `buca`'s prerequisites are target-conditional rather than traversing `build` first
- `grep -c 'SOURCE_DATE_EPOCH' hack/build-cluster.sh` returns ≥ 1; `grep -c 'automountServiceAccountToken: false' hack/build-job.yaml` returns 1; `grep -c 'auths' hack/dockerconfig.json.tpl` returns 1; `grep -c 'KUBECONFIG_PATH' hack/cluster-alias.sh` returns ≥ 1; `grep -c 'create secret' hack/build-cluster-bootstrap.sh` returns ≥ 1; `grep -c 'nukeprod' hack/cluster-alias.sh` returns ≥ 1
- `grep -c '^## Build' README.md` returns 1; `grep -c 'make build BUILD_TARGET=nukeprod' README.md` returns ≥ 1; `grep -c 'quant' README.md` returns 0 and so do `grep -c 'quant' Makefile` and `grep -c 'quant' Makefile.docker` (each file reports its own count); `grep -c '^/specs/in-progress/\*\.lock$' .gitignore` returns 1
- `awk '/^## Unreleased/{f=1;next} /^## /{f=0} f&&/^- /{c++} END{print c+0}' CHANGELOG.md` returns 1

### Operator-executable (runs on the host after PR merge)

- `make build BUILD_TARGET=nukeprod REF=<sha> VERSION=<scratch-tag>` — one real cluster build; Job name, pod node and digest recorded
- `crane digest docker.io/bborbe/github-update-go-watcher:<scratch-tag>` — the push landed
- `docker image inspect docker.io/bborbe/github-update-go-watcher:<scratch-tag>` — fails on this Mac with `No such image`
- on the post-change branch: `ALLOW_UNTAGGED_BUILD=1 make build VERSION=probe` then `docker image inspect docker.io/bborbe/github-update-go-watcher:probe` — the local arm still works

## Desired Behavior

1. **A build target selects the arm.** `BUILD_TARGET` defaults to `local`. With `local`, `make build` behaves exactly as today: it runs `check-version-tag` and `check-go-mod`, then `docker build`, tagging `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)`.
2. **Any other value routes to a cluster Job.** With `BUILD_TARGET` set to a cluster alias or kubectl context, `make build` renders `hack/build-job.yaml` and applies it through `hack/build-cluster.sh`, passing the repository URL, the Dockerfile path and the push-secret name as literals, and `IMAGE='$(DOCKER_REGISTRY)/bborbe/$(SERVICE)'` and `TAG='$(VERSION)'` from the make variables.
3. **The cluster arm needs a caller-supplied tag.** When `VERSION` came from the Makefile's own default rather than from the caller, the cluster arm refuses before any Job is created, naming `VERSION`. A tag nobody chose can never be pushed over a release.
4. **Both arms publish to one repository.** `$(DOCKER_REGISTRY)/bborbe/$(SERVICE)` composes `docker.io/bborbe/github-update-go-watcher` under the current defaults, so the two arms cannot drift and no cluster-specific image variable is introduced.
5. **The guards are local-only.** `check-version-tag` and `check-go-mod` remain prerequisites of `build` for `local` and are compiled out for any other target. `upload`, `clean` and `buca` are local-only and are refused before any recipe runs for a cluster target — including `buca`, whose prerequisite list is target-conditional so it cannot traverse `build` into the `VERSION` refusal first. This is a deliberate divergence from the fleet's other arms, which make those three a graceful no-op: refusing is chosen so a half-run pipeline cannot report success.
6. **The cluster build works from a clone.** The image build already resolves Go dependencies in module mode, so a Job that clones the repo with no `vendor/` directory builds the same way a developer's checkout does. No Dockerfile change is needed; the arm must not introduce one.
7. **The documented build path matches reality.** The README shows both arms and states the `VERSION` requirement and the `buca` limitation; the CHANGELOG records the change.

## Constraints

- The local arm's command, tag and guard must not change. A bare `make build` on an untagged `HEAD` must still fail — that refusal is what keeps a released tag from being overwritten by a branch build.
- `check-go-mod` stays on the local branch only. The cluster arm builds a named ref, not the local `HEAD`, so vendoring into the local tree there is meaningless.
- **The `VERSION` guard must distinguish a caller's tag from the file default, and must live inside the `build` recipe.** `Makefile.docker:3-5` assigns the default when the caller supplied nothing, so by the time any later line runs, `$(origin VERSION)` is `file` for a defaulted value and `command line` / `environment` for a caller's. Reject `file` as well as an empty value — `$(if $(filter command line environment,$(origin VERSION)),,)` alone accepts `VERSION=`, whose origin is `command line` with an empty value, and the runner's own `TAG is required` refusal is invisible to `make -n`. Write it as `$(if $(and $(filter command line environment,$(origin VERSION)),$(strip $(VERSION))),,$(error …))` inside the cluster branch of the `build` recipe — a recipe-expansion `$(error)` fires under `make -n`, while a shell `if` in the recipe body does not, and a file-level `$(error)` would also fire for `upload` / `clean` / `buca` and break their own refusal. The check is keyed on `BUILD_TARGET` being non-`local`, so a bare local build is unaffected.
- **Do not read `$(SERVICE)` at parse time.** `Makefile` includes `Makefile.docker` at line 3 and assigns `SERVICE` at line 10. Recipe bodies are fine (they expand at recipe time); a parse-time `$(error …)` or `ifeq` that interpolates `$(SERVICE)` would see it empty and compose a malformed image reference.
- **`IMAGE` is already a variable this Makefile exports.** `Makefile:4` includes `example.env`, whose `IMAGE=bborbe/github-update-go-watcher` is exported. Pass the cluster image as a recipe-local environment assignment (`IMAGE='$(DOCKER_REGISTRY)/bborbe/$(SERVICE)' \`), never as a make-level `IMAGE :=`, which would silently redefine the exported variable for every other target.
- The local-only refusal for `upload` / `clean` / `buca` is a `$(error …)` keyed on `BUILD_TARGET`, not a recipe-level `exit 1`. A recipe-level refusal is invisible to `make -n`, so the criterion could not observe it.
- The three shared `hack/` files are copied unmodified at copy time from `agent-task-executor` at `origin/master`: `hack/build-cluster.sh`, `hack/cluster-alias.sh`, `hack/build-job.yaml`. Any later deviation is a deliberate, documented edit, not drift. That repository is public, so a `git clone --depth=1 https://github.com/bborbe/agent-task-executor` reaches them. `cluster-alias.sh` is required — the runner sources it and fails at that line without it.
- `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` are copied unmodified at copy time from `go-skeleton` at `origin/master` (`https://github.com/bborbe/go-skeleton`); it is the only repo carrying them. They are not called by the arm: they are the reproducible provisioning path for the `build` namespace and the push secret, so a cluster whose prerequisites are missing can be rebuilt from the repo rather than from memory. The template holds a TeamVault *lookup key*, not a credential.
- **File modes are preserved.** `build-cluster.sh`, `cluster-alias.sh` and `build-cluster-bootstrap.sh` are executable upstream (`100755`) and are invoked as `hack/build-cluster.sh`; a copy that loses the bit makes the dispatch fail at exec.
- **The Dockerfile needs no change, and the template's registry build-arg is inert here.** The base images are hardcoded public (`golang:1.27.3`, `alpine:3.24`), and this repo's `Dockerfile` declares no `ARG DOCKER_REGISTRY`, so the `--build-arg DOCKER_REGISTRY=…` that both arms pass is a no-op. Do not add a registry indirection to the Dockerfile to "use" it.
- `IMAGE`, the registry and the tag must each satisfy the runner's character whitelist; no value may carry a newline. The runner's clone runs `git fetch --depth=1 --tags origin "$REF"`, so `REF` is passed as the full 40-character SHA or a tag — a short SHA cannot be fetched.
- `.gitignore` keeps every entry it has today, including `/vendor`, `/specs/log` and `/prompts/log`, and gains `/specs/in-progress/*.lock`. The consumer is dark-factory itself: it writes `specs/in-progress/<id>-<name>.md.lock` while a spec is in progress, and `/prompts/log` covers only the prompt lane — so without this line the first approved spec leaves an untracked lock file in the tree.
- `README.md` is the in-repo home for the two-arm topology and the nuke coupling; the spec dies after implementation, the README does not.
- No new scenario. The behaviour is reachable by checks on the rendered invocation plus one real cluster build.

## Assumptions

These must hold before the cluster criterion can pass; none is created by this spec.

- **The `build` namespace and the `buildkit-push` secret exist on `nukeprod` — verified 2026-11-09.** `kubectlnukeprod get namespace build` and `kubectlnukeprod -n build get secret buildkit-push -o jsonpath='{.metadata.name}'` both returned their names, so the Rung-2 gate is satisfiable without a bootstrap step. The sibling tasks ran against `sun`, which is host-unreachable at the time of writing, which is why `nukeprod` is the alias here; `hack/cluster-alias.sh` maps it to `~/.kube/nuke-prod`.
- That credential covers Docker Hub — `go-skeleton/hack/dockerconfig.json.tpl`, from which the bootstrap renders it, carries exactly one `auths` entry, `https://index.docker.io/v1/`.
- The repo is public, so the Job's anonymous `git fetch` succeeds; the init container carries no clone credential.
- `crane` is installed on the host running the criteria.
- `REF` exists in the local repository **and on `origin`**: the runner derives `SOURCE_DATE_EPOCH` from the local repository, and the Job's init container fetches the ref from the remote. A commit that exists only locally fails at the clone.
- The build pod can reach the Go module proxy. The Dockerfile already builds in module mode with no `vendor/`, so this is not new load — but it is load the cluster arm inherits.
- **The two arms obtain identical dependency versions by different routes** — the local arm from the `vendor/` that `check-go-mod` writes, the cluster arm from the module proxy. Both are derived from `go.mod`, so the versions match; the divergence is recorded here so a future reader does not treat it as drift.

## Failure Modes

| Trigger | Expected behavior | Recovery | Reversibility |
|---|---|---|---|
| `BUILD_TARGET` names an unknown cluster or context | The runner fails before applying anything and lists the accepted values | Re-run with a listed target | Nothing created |
| `VERSION` omitted on the cluster arm | `$(error …)` naming `VERSION`, before the Job is rendered | Re-run passing `VERSION=<scratch-tag>` | Nothing created |
| `REF` is not in the local repository | The runner dies resolving `SOURCE_DATE_EPOCH` | `git fetch`, then re-run with the full 40-character SHA | Nothing created |
| `REF` is a short SHA, or exists locally but was never pushed | The clone init container fails: `git fetch --depth=1 origin <short>` cannot find the ref | Push the branch or commit, then re-run with the full SHA | Nothing created |
| The clone's commit epoch differs from the locally derived one | The build container refuses before `buildctl` runs and names both epochs | `git fetch`, then re-run | Nothing created |
| A Job with the derived name already exists in `build` | The runner refuses and names the existing Job rather than overwriting it | Delete the finished Job, then re-run | Nothing overwritten |
| The runner is interrupted after `apply` | The Job keeps running in the cluster while the local invocation exits; the next run then hits the existing-Job refusal | `kubectlnukeprod -n build get jobs`, read the pod's log, delete the Job once finished | Reversible: delete the Job |
| The build pod cannot reach the module proxy | The Job fails; the runner prints the last 100 log lines and the Job name | `kubectlnukeprod -n build logs job/<job-name>`; the local arm is unaffected | Reversible: delete the Job |
| The build pod is OOM-killed or throttled | The Job reports `failed`; the pod status carries `OOMKilled` or a CPU-throttling event | Raise the template's limits in the same change, or build locally for that one image | Reversible: delete the Job |
| The registry throttles or rejects the push | The build fails at the push step with the registry's own error | `kubectlnukeprod -n build get secret buildkit-push -o jsonpath='{.metadata.name}'`, then re-run | Nothing pushed |
| A caller-supplied `VERSION` names an already-released tag | No guard — the push replaces that tag irreversibly | Re-push the image from the released tag, then cut a new release | **Irreversible**: the overwritten tag is gone |
| The local arm regresses | `make build` from the post-change tree fails or tags differently | Revert the arm; the local path is the regression gate | Reversible: revert the commit |

## Security / Abuse Cases

- The runner substitutes caller-supplied values into a manifest and into embedded shell, so every value is checked against a character whitelist and rejected for embedded newlines before rendering. Do not add a value path that bypasses that check.
- The repository URL, the Dockerfile path and the push-secret name are literals in the dispatch, not configurable inputs. A configurable clone source on a build that mounts a push credential would let a caller point the credential-bearing Job at another repo.
- The build Job mounts a push credential from a `build`-namespace secret and runs with `automountServiceAccountToken: false`. The credential is scoped to pushing an image; it grants nothing else.
- The build container runs as an unprivileged user with the sandbox profiles relaxed because rootless BuildKit requires it. That relaxation is deliberate and must not be copied to the clone init container.
- `VERSION` is caller-controlled and becomes an image tag and a Job name. The tag is whitelisted and the Job name is sanitized to a valid DNS name, truncated, and stripped of trailing separators.
- The bootstrap script reads a credential from TeamVault into a `600` temp file for the length of one `create secret --dry-run` and removes it on every exit path. It is never echoed and never written into the repo.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `Makefile.docker` dispatch: the `BUILD_TARGET` split, the recipe-scoped `VERSION` refusal, local-only `upload`/`clean`/`buca`, and the guards compiled out for a cluster target | 1, 2, 3, 4, 5 | 1, 4, 6, 7, 8 | — |
| 2 | The build path itself: the five `hack/` files with their exec bits | 2, 6 | 2, 3, 5 | prompt 1 (the dispatch calls the runner) |
| 3 | Docs: the README build section, the `.gitignore` lock line and the CHANGELOG bullet | 7 | 9 | — |

Rationale: prompt 1 establishes the dispatch and the guard scoping that make any of it reachable; prompt 2 supplies the runner the dispatch calls; prompt 3 is independent and touches no build logic. The spec is above the 50-point size heuristic — 7 DBs × 9 ACs = 63 — and the three-prompt split is what keeps each unit bounded, and a completed reference implementation (`github-update-go-agent`'s `009-cluster-build-arm`) bounds prompt-creator research.

## Do-Nothing Option

Leaving this repo without a cluster arm keeps its image tied to the Mac. Every release cut while the operator is away from this machine, and every future unattended pipeline, needs a human with a Docker daemon for this one repo — while its siblings build themselves. The cost is not the one build; it is that the fleet's release path has a hole shaped exactly like the repo nobody is watching.
