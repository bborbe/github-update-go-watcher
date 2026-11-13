---
status: completed
spec: [006-cluster-build-arm]
summary: Added a cluster build arm to Makefile.docker with local arm preserved, mandatory cluster VERSION guard, and local-only upload/clean/buca refusals; updated CHANGELOG Unreleased
execution_id: 512c61be-732d-552a-8e13-af7c168c3fec-exec-018-spec-006-makefile-dispatch
dark-factory-version: dev
created: "2026-11-09T09:17:30Z"
queued: "2026-11-09T09:29:23Z"
started: "2026-11-09T09:29:25Z"
completed: "2026-11-09T09:31:51Z"
branch: dark-factory/cluster-build-arm
---

<summary>
- `make build` still builds on the local Docker daemon by default; nothing about a bare local build changes.
- Setting a build target other than `local` builds the image inside the named cluster as a Kubernetes Job and pushes it to Docker Hub.
- A cluster build refuses to run unless the caller names the image tag, so the Makefile's newest-release-tag default can never be pushed over a released image.
- The local version and vendoring guards no longer run for a cluster build; they remain exactly as they are for the local one.
- The local guard still refuses an untagged working tree, unchanged.
- Pushing, cleaning and the combined publish command are local-only: a cluster target is refused before any recipe runs, rather than half-running and failing at a local push.
- Both build paths publish to the same Docker Hub repository the fleet mirrors, so they cannot drift.
- The only new inputs are the build target, the git ref and the tag; the repository URL, Dockerfile path and push-secret name stay literal.
</summary>

<objective>
Add a second build arm to `Makefile.docker`: keep today's local `docker build` exactly as it is when `BUILD_TARGET` is unset or `local`, and route any other `BUILD_TARGET` to a Kubernetes Job built and pushed inside that cluster. Scope the existing local guards to the local arm only, make a caller-supplied tag mandatory for the cluster arm, and refuse `upload`, `clean` and `buca` for a cluster target. The runner the cluster arm calls is supplied by the next prompt; this prompt establishes the dispatch that calls it.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:

- `Makefile.docker` — the whole file. Line 1 `DOCKER_REGISTRY ?= docker.io`; lines 3-5 the `VERSION` default (`ifeq ($(VERSION),)` → `VERSION := $(shell git describe --tags \`git rev-list --tags --max-count=1\`)`); line 8 `build: check-version-tag check-go-mod`; the local `docker build` recipe with `-t $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)`; `check-version-tag`; `check-go-mod`; `upload`; `clean`; the `apply` comment mentioning the retired `quant` registry; `buca: build upload clean apply`.
- `Makefile` — includes `Makefile.docker` at line 3 and assigns `SERVICE = github-update-go-watcher` at line 10. `SERVICE` is therefore empty at parse time and set at recipe time; only recipe bodies may read it.
- `example.env` — included by `Makefile` at line 4 and exports `IMAGE=bborbe/github-update-go-watcher` and `DOCKER_REGISTRY=docker.io`. `IMAGE` is an exported variable; do not redefine it at make level.
- `Dockerfile` — no `ARG DOCKER_REGISTRY`; the `--build-arg DOCKER_REGISTRY=…` both arms pass is inert. Do not change the Dockerfile.

Reference implementation (public, optional to read): `https://github.com/bborbe/github-update-go-agent` at `origin/master` has a `Makefile.docker` with the same split. Treat this repo's spec as authoritative where the two differ — in particular the `VERSION` guard here must also reject an empty command-line `VERSION=`, which the reference does not.

Read the coding plugin guide (in-container path):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-makefile-commands.md`

The `hack/build-cluster.sh` the cluster recipe calls is created by the next prompt. Do not create it here; this prompt's verification uses `make -n`, which never executes the recipe.
</context>

<requirements>

### 1. Replace `Makefile.docker` with exactly this content

Write the file so the local arm's command, tag and guard are byte-identical to today's, and the cluster arm is added around them. Preserve the tab indentation on every recipe line — a recipe line indented with spaces breaks `make`. The authoritative target content:

```make
DOCKER_REGISTRY ?= docker.io

ifeq ($(VERSION),)
	VERSION := $(shell git describe --tags `git rev-list --tags --max-count=1`)
endif

# BUILD_TARGET selects where `make build` runs. `local` (the default) uses the
# local Docker daemon; any other value names a cluster alias (sun / nukedev /
# nukeprod) or a kubectl context and builds inside that cluster as a Job
# rendered from hack/build-job.yaml by hack/build-cluster.sh.
BUILD_TARGET ?= local

ifeq ($(BUILD_TARGET),local)
.PHONY: build
build: check-version-tag check-go-mod
	@if [ -z "$(VERSION)" ]; then echo "ERROR: no VERSION set and no git tag found — set VERSION=vX.Y.Z or create a tag" >&2; exit 1; fi
	DOCKER_BUILDKIT=1 \
	docker build \
	--pull \
	--no-cache \
	--rm=true \
	--platform=linux/amd64 \
	--build-arg DOCKER_REGISTRY=$(DOCKER_REGISTRY) \
	--build-arg BUILD_GIT_VERSION=$(VERSION) \
	--build-arg BUILD_GIT_COMMIT=$$(git rev-parse --short HEAD) \
	--build-arg BUILD_DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	-t $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION) \
	-f Dockerfile .

.PHONY: check-version-tag
check-version-tag:
	@if [ -n "$(ALLOW_UNTAGGED_BUILD)" ]; then \
		echo "ALLOW_UNTAGGED_BUILD set — skipping version/tag check"; \
	else \
		head_tag=$$(git describe --tags --exact-match HEAD 2>/dev/null); \
		if [ "$$head_tag" != "$(VERSION)" ]; then \
			echo "ERROR: refusing to build $(VERSION) from this tree." >&2; \
			echo "  HEAD is at tag: $${head_tag:-<untagged>}" >&2; \
			echo "  building as:    $(VERSION)" >&2; \
			echo "  An image stamped vX.Y.Z must be built from the vX.Y.Z tag." >&2; \
			echo "  Fix: git checkout $(VERSION)   (or set ALLOW_UNTAGGED_BUILD=1 for a scratch build)" >&2; \
			exit 1; \
		fi; \
	fi
.PHONY: check-go-mod
check-go-mod:
	@if [ -f "go.mod" ]; then \
		echo "go.mod found, running go mod vendor..."; \
		go mod vendor; \
	else \
		echo "go.mod not found, skipping go mod vendor."; \
	fi

.PHONY: upload
upload:
	docker push $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)

.PHONY: clean
clean:
	docker rmi $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION) || true
	docker image prune -f || true
	docker builder prune --all -f || true
	rm -rf vendor
else
# The cluster arm builds a named REF in a fresh clone, so the local-tree guards
# (check-version-tag / check-go-mod) are compiled out above. VERSION must come
# from the caller: the default at the top of this file is the newest release
# tag, and pushing that over a released image is irreversible. `$(origin
# VERSION)` is expanded when this recipe is expanded, after Makefile has
# assigned the default, so `file` is rejected along with an empty value; only a
# non-empty `command line` / `environment` VERSION is accepted. The `$(error …)`
# fires under `make -n` too, unlike a shell `if`.
.PHONY: build
build:
	@$(if $(and $(filter command line environment,$(origin VERSION)),$(strip $(VERSION))),,$(error the cluster arm needs an explicit VERSION; pass VERSION=<scratch-tag>))
	BUILD_TARGET='$(BUILD_TARGET)' \
	REPO_URL='https://github.com/bborbe/github-update-go-watcher.git' \
	REF='$(REF)' \
	TAG='$(VERSION)' \
	DOCKERFILE='Dockerfile' \
	IMAGE='$(DOCKER_REGISTRY)/bborbe/$(SERVICE)' \
	PUSH='true' \
	PUSH_SECRET='buildkit-push' \
	hack/build-cluster.sh

.PHONY: upload clean
upload clean:
	@$(error $@ is local-only; BUILD_TARGET=$(BUILD_TARGET) builds and pushes inside the cluster)
endif

# Publish-only: no k8s in this repo. Deployment is the github-update-go-watcher
# Helm chart, mirrored into the bborbe/nuke registry and pinned by tag there.
# apply is a no-op placeholder.
.PHONY: apply
apply:
	@echo "skip apply — publish-only"

# Operator-run publish to Docker Hub (requires `docker login`). CI runs
# `make precommit`, never `buca`, so the unconditional push is safe.
# upload, clean and buca assume a local Docker daemon; for a cluster target the
# image is pushed from inside the Job and nothing lands on this host, so they
# are refused rather than silently no-oping. buca's prerequisites are
# target-conditional so a cluster buca reaches its own refusal instead of
# traversing build into the VERSION guard.
.PHONY: buca
buca: $(if $(filter local,$(BUILD_TARGET)),build upload clean apply)
ifneq ($(BUILD_TARGET),local)
	@$(error buca is local-only; BUILD_TARGET=$(BUILD_TARGET) builds and pushes inside the cluster)
endif
```

### 2. Structural rules this file must satisfy

- **`BUILD_TARGET ?= local`** is a make-level default, placed after the `VERSION` default and before the `ifeq`. A bare `make build` therefore takes the local branch.
- **The local branch is unchanged.** `build: check-version-tag check-go-mod`, the `@if [ -z "$(VERSION)" ]` line, the whole `docker build` invocation, `-t $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)`, `check-version-tag`, `check-go-mod`, `upload` and `clean` are byte-identical to the current file. Do not reorder or reword them.
- **The cluster branch is entered for every `BUILD_TARGET` other than `local`.** Its `build` target has NO prerequisites, so `check-version-tag` and `check-go-mod` are compiled out.
- **The `VERSION` refusal is a recipe-expansion `$(error …)` inside the cluster `build` recipe**, not a file-level `$(error …)` and not a shell `if`. It must reject `$(origin VERSION)` of `file` (the default) AND an empty stripped value. The exact guard is the `@$(if $(and $(filter command line environment,$(origin VERSION)),$(strip $(VERSION))),,$(error …))` line above. Keep the message naming `VERSION`.
- **The dispatch passes `IMAGE` as a recipe-local environment assignment** (`IMAGE='$(DOCKER_REGISTRY)/bborbe/$(SERVICE)' \`), never as a make-level `IMAGE :=` or `override`. It is the only place `SERVICE` is read, and it is read at recipe time (safe; `SERVICE` is assigned in `Makefile` after the include).
- **The repository URL, Dockerfile path and push-secret name are literals** in the dispatch (`https://github.com/bborbe/github-update-go-watcher.git`, `Dockerfile`, `buildkit-push`). Do not turn them into variables.
- **`upload` and `clean` refuse a cluster target via a recipe-scoped `$(error …)`** (the combined `upload clean:` rule in the `else` branch), so the refusal fires under `make -n` where no recipe runs. Do not use a recipe-level `exit 1`.
- **`buca`'s prerequisites are target-conditional**: `buca: $(if $(filter local,$(BUILD_TARGET)),build upload clean apply)`. The local chain is exactly `build upload clean apply`; a cluster `buca` has no prerequisites and reaches its own `$(error …)` instead of traversing `build` into the `VERSION` refusal. The cluster refusal lives in the `ifneq ($(BUILD_TARGET),local)` recipe line.
- **`apply` is defined once, outside the `ifeq`.** Its comment names `bborbe/nuke`, not `quant`.
- **No new knobs.** Only `BUILD_TARGET`, `REF` and `VERSION` are inputs. Do not add `BUILD_NAMESPACE`, `BUILD_TIMEOUT`, `BUILD_REPO_URL`, `BUILD_DOCKERFILE`, `BUILD_PUSH_SECRET` or any `ALLOW_*` escape hatch for the cluster arm.

### 3. Do not touch anything else

- Do not change the `Dockerfile`, `Makefile`, `example.env`, or any Go file.
- Do not create `hack/` here; the next prompt supplies it.
- The `apply` comment's `quant` reference is removed by this rewrite; the remaining `quant` references in `Makefile` and `README.md` are handled by the docs prompt.
</requirements>

<constraints>
- The local arm's command, tag and guard must not change. A bare `make build` on an untagged `HEAD` must still fail — that refusal is what keeps a released tag from being overwritten by a branch build.
- `check-go-mod` stays on the local branch only. The cluster arm builds a named ref, not the local `HEAD`, so vendoring into the local tree there is meaningless.
- The `VERSION` guard must distinguish a caller's tag from the file default, and must live inside the `build` recipe. Reject `file` as well as an empty value — `$(if $(filter command line environment,$(origin VERSION)),,)` alone accepts `VERSION=`, whose origin is `command line` with an empty value. Use the `$(and … $(strip $(VERSION)) …)` form given above.
- Do not read `$(SERVICE)` at parse time. `Makefile` includes `Makefile.docker` at line 3 and assigns `SERVICE` at line 10; a parse-time `$(error …)` or `ifeq` that interpolates `$(SERVICE)` would see it empty and compose a malformed image reference. Recipe bodies are fine.
- `IMAGE` is already an exported variable from `example.env`. Pass the cluster image as a recipe-local environment assignment, never as a make-level `IMAGE :=`, which would silently redefine the exported variable for every other target.
- The local-only refusal for `upload` / `clean` / `buca` is a `$(error …)` keyed on `BUILD_TARGET`, not a recipe-level `exit 1`; a recipe-level refusal is invisible to `make -n`.
- `BUILD_TARGET ?= local` stays; a bare `make build` is a local build. Do not make the cluster the default.
- The image repository stays `$(DOCKER_REGISTRY)/bborbe/$(SERVICE)` under `DOCKER_REGISTRY ?= docker.io` — `docker.io/bborbe/github-update-go-watcher`. Do not rename it or add a cluster-specific image variable.
- No Dockerfile change. `linux/amd64` only.
- Do NOT commit — dark-factory handles git.
- Existing tests and `make precommit` must still pass.
</constraints>

<verification>
Run from the repo root. These probes are all non-git and safe in the build container; the real local build and the real cluster build are operator-rung checks in the spec, not here.

```
make precommit
```

Must exit 0.

```
grep -c '^BUILD_TARGET' Makefile.docker
grep -cE '^[[:space:]]*hack/build-cluster\.sh' Makefile.docker
grep -c 'ERROR: refusing to build' Makefile.docker
grep -cF -- '-t $(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)' Makefile.docker
```

Each must print exactly `1` (the first is anchored at column 0 so the header comment cannot satisfy it; the second proves the cluster recipe invokes the runner; the third proves the local refusal text survived; the fourth proves the local tag line survived).

```
make -p -n build 2>/dev/null | grep -E '^build:'
```

Must print `build: check-version-tag check-go-mod` — the local arm still carries both guards.

```
make -p -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe 2>/dev/null | grep -E '^build:'
```

Must print `build:` with NO `check-version-tag` and NO `check-go-mod` — the guards are compiled out for the cluster arm.

```
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe
```

Must exit 0 and the output must contain `IMAGE='docker.io/bborbe/github-update-go-watcher'` and `TAG='probe'` and `hack/build-cluster.sh`. Confirm with:

```
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe | grep -c "IMAGE='docker.io/bborbe/github-update-go-watcher'"
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe | grep -c "TAG='probe'"
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe | grep -cE '^[[:space:]]*hack/build-cluster\.sh'
```

Each must print exactly `1`.

```
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 >/dev/null 2>&1; test $? -ne 0 && echo CLUSTER_REFUSES_DEFAULT_VERSION
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 2>&1 | grep -c 'VERSION'
```

The first line must print `CLUSTER_REFUSES_DEFAULT_VERSION` — the cluster arm refuses a tag it did not receive from the caller. The second must print at least `1` — the message names `VERSION`.

```
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION= >/dev/null 2>&1; test $? -ne 0 && echo CLUSTER_REFUSES_EMPTY_VERSION
make -n build BUILD_TARGET=nukeprod REF=0123456789abcdef0123456789abcdef01234567 VERSION= 2>&1 | grep -c 'explicit VERSION'
```

The first line must print `CLUSTER_REFUSES_EMPTY_VERSION` — an empty command-line `VERSION=` is rejected too. The second must print `1`.

```
make -n upload BUILD_TARGET=nukeprod >/dev/null 2>&1; test $? -ne 0 && echo UPLOAD_REFUSED
make -n clean BUILD_TARGET=nukeprod >/dev/null 2>&1; test $? -ne 0 && echo CLEAN_REFUSED
make -n buca BUILD_TARGET=nukeprod >/dev/null 2>&1; test $? -ne 0 && echo BUCA_REFUSED
```

All three markers must print — each target is refused for a cluster target.

```
make -n buca BUILD_TARGET=nukeprod 2>&1 | grep -c 'local-only'
```

Must print exactly `1` — `buca` reaches its OWN refusal, not `build`'s `VERSION` refusal, which proves its prerequisites are target-conditional.

```
make -n upload | grep -c docker
make -n clean | grep -c docker
make -n buca | grep -c docker
```

Each must print at least `1` — with no `BUILD_TARGET` the three still print their local Docker recipes.

```
! grep -q 'quant' Makefile.docker && echo NO_QUANT_MAKEFILE_DOCKER
```

Must print `NO_QUANT_MAKEFILE_DOCKER`.

```
grep -Pn '^ +[A-Za-z@]' Makefile.docker || echo NO_SPACE_INDENTED_RECIPES
```

Must print `NO_SPACE_INDENTED_RECIPES` — no recipe line was indented with spaces.
</verification>
