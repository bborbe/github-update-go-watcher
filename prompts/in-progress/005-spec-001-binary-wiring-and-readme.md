---
status: approved
spec: ["001"]
created: "2026-08-16T12:53:45Z"
queued: "2026-08-16T13:17:03Z"
---

<summary>
- Turns the assembled pieces into a running service: it starts, fires one scan immediately, then scans again on its own interval (ten minutes by default).
- Configuration moves to environment variables: which GitHub owner to watch, which repos are in scope, where to keep its memory, which stage to stamp on work items.
- The memory file location has a working default, so the service starts with nothing extra configured and the deployment's mounted volume lines up with it.
- An operator can force a scan on demand through an admin endpoint; asking for a second scan while one is running is refused rather than queued.
- Exactly one scan runs at a time, whether triggered by the timer or by the operator — so the memory file always has a single writer.
- The forced scan takes no repo or owner parameter, so reaching the endpoint grants no ability to target a repo the operator did not configure.
- Leftover key-value-store endpoints from the project scaffold are removed, along with the on-disk database they needed.
- The README documents the environment variables, the four metrics, the six skip reasons, the frozen work-item shape, and the end-to-end verification procedure.
</summary>

<objective>
Wire the watcher into the binary: environment binding, the poll loop, the single-cycle lock shared by the timer and the admin endpoint, the HTTP surface (health, readiness, metrics, log level, forced cycle), removal of the scaffold's key-value-store endpoints and on-disk database, and a README that documents the operable surface.
</objective>

<context>
Read `docs/dod.md`.

Read these coding plugin docs before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-k8s-binary-conventions.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-service-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/readme-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`

Read these repo files before writing code:
- `main.go` — the current scaffold `application` struct and `createHTTPServer`. This prompt rewrites both.
- `main_test.go` — the gexec compile test. Leave it as-is.
- `pkg/factory/factory.go` — currently has `CreateTestLoglevelHandler` and `CreateSentryAlertHandler`. Keep both; add to this file.
- `pkg/handler/test-loglevel.go`, `pkg/handler/sentry-alert.go`, `pkg/handler/handler_suite_test.go` — existing; leave them.
- `pkg/watcher.go` — `Watcher.Poll(ctx context.Context, force bool) error`, `NewWatcher(...)`.
- `pkg/cursor.go` — `DefaultCursorPath = "/data/cursor.json"`.
- `pkg/metrics.go` — `NewMetrics(prometheus.Registerer)`, `PollCycleResults`, `PublishStatuses`, `FilterSkipReasons`.
- `pkg/filter/` — the five cycle-invariant filter constructors.
- `pkg/auth/auth.go` — `ResolveGitHubClient(ctx, Credentials) (*http.Client, error)`.
- `Makefile` — the `run` target currently passes `-datadir` and `-batch-size`, both of which this prompt removes from the binary.
- `example.env`, `README.md`.

**Sibling entry-point check (already run):** this repo has exactly one binary entry point — `main.go` at the repo root. There is no `cmd/` directory and no second `application.Run`. `grep -rn "factory.Create" --include=*.go .` and `grep -rn "func (.*) Run(ctx" --include=*.go .` before you start; if either surfaces a call site outside `main.go`, update it in this prompt too.

Library API facts (verified — do not re-derive from memory):
- `github.com/bborbe/service` — `service.Main(ctx, app, &app.SentryDSN, &app.SentryProxy)` and `service.Run(ctx context.Context, funcs ...run.Func) error` (cancel-on-first-finish across all funcs).
- `github.com/bborbe/http` (import `libhttp`) — `libhttp.NewPrintHandler(format string, a ...any) http.Handler`, `libhttp.NewServer(addr string, handler http.Handler)` with `.Run(ctx)`, `libhttp.NewGarbageCollectorHandler()`, `libhttp.NewJSONErrorHandler(withError libhttp.WithError) http.Handler`, `libhttp.WrapWithStatusCode(err error, code int) libhttp.ErrorWithStatusCode`, and `type WithError interface` whose method is `ServeHTTP(ctx context.Context, resp http.ResponseWriter, req *http.Request) error`.
- `github.com/bborbe/kafka` (import `libkafka`) — `libkafka.ParseBrokersFromString(value string) libkafka.Brokers`, `libkafka.NewSyncProducerWithName(ctx, brokers, name)`.
- `github.com/bborbe/cqrs/base` — `type TopicPrefix string`. `github.com/bborbe/cqrs/cdb` — `cdb.NewCommandObjectSender(syncProducer libkafka.SyncProducer, prefix base.TopicPrefix, logSamplerFactory log.SamplerFactory) cdb.CommandObjectSender`.
- `github.com/bborbe/agent/command/task` — `task.NewCreateCommandSender(commandObjectSender cdb.CommandObjectSender, defaultVault string) task.CreateCommandSender`.
- `github.com/bborbe/log` — `log.DefaultSamplerFactory`, `log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute))`.
- `github.com/bborbe/parse` (import `libparse`) — `libparse.ParseBoolDefault(ctx context.Context, value interface{}, defaultValue bool) bool`.
- `github.com/bborbe/metrics` (import `libmetrics`) — `libmetrics.NewBuildInfoMetrics().SetBuildInfo(version, commit, buildDate)`.
- `github.com/bborbe/time` (import `libtime`) — `*libtime.DateTime` is the type already bound to `BUILD_DATE` in the scaffold.
- Poll interval is bound as a `string` and parsed with `time.ParseDuration`. Do NOT declare the field as `time.Duration` — the argument binder cannot unmarshal it.
</context>

<requirements>

### 1. Add the remaining dependencies

```
go get github.com/bborbe/cqrs@v0.6.0
go get github.com/bborbe/parse
```

Then `go mod tidy` (runs as part of `make precommit`).

### 2. `pkg/cyclegate.go` — single-cycle lock

Package `pkg`. This is the mechanism that guarantees the persisted-memory file has exactly one writer, shared by the interval loop and the forced-cycle endpoint.

```go
//counterfeiter:generate -o ../mocks/cycle_gate.go --fake-name CycleGate . CycleGate

// CycleGate enforces "exactly one poll cycle at a time" across the interval
// loop and the forced-cycle HTTP endpoint. It is non-blocking by design: a
// caller that cannot acquire the slot backs off instead of queueing, so a
// burst of forced-cycle requests cannot pile up behind a slow cycle.
type CycleGate interface {
	// TryAcquire reports whether the caller now holds the single cycle slot.
	// A caller that receives true MUST call Release when its cycle finishes.
	TryAcquire() bool
	// Release frees the slot. Calling Release without holding it is a no-op.
	Release()
}

// NewCycleGate returns a CycleGate backed by a capacity-1 channel.
func NewCycleGate() CycleGate {
	return &cycleGate{slot: make(chan struct{}, 1)}
}

type cycleGate struct {
	slot chan struct{}
}

func (g *cycleGate) TryAcquire() bool {
	select {
	case g.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *cycleGate) Release() {
	select {
	case <-g.slot:
	default:
	}
}
```

Test `pkg/cyclegate_test.go` (`package pkg_test`): first `TryAcquire` true; second false; after `Release` the next `TryAcquire` is true again; `Release` without holding is a no-op and does not panic; two concurrent goroutines racing `TryAcquire` yield exactly one winner (run with a `sync.WaitGroup` and an atomic counter).

### 3. `pkg/handler/trigger_handler.go` — forced cycle

Package `handler`.

```go
// TriggerHandler handles POST /trigger.
//
// It runs the forced cycle IN-PROCESS: the request acquires the single-cycle
// slot, starts the cycle on a background goroutine bound to the application's
// long-lived context, and returns 202 immediately. There is no Kafka command
// topic, no command consumer and no key-value store behind this endpoint —
// that surface is not worth its cost for an endpoint whose only caller is an
// operator's curl (spec § Deliberate deviation from the template).
//
// Security: the handler reads ONLY the optional ?force=<bool> query parameter.
// It takes no owner, repo or scope parameter, so a forced cycle can only
// re-examine repos that already pass the allowlist and the per-repo opt-in
// gate. Unknown query parameters are ignored.
type TriggerHandler = libhttp.WithError

// NewTriggerHandler returns the forced-cycle handler. baseCtx is the
// application's long-lived context: the background cycle must NOT run under
// the request context, which is cancelled the moment the 202 is written.
func NewTriggerHandler(
	baseCtx context.Context,
	watcher pkg.Watcher,
	gate pkg.CycleGate,
) TriggerHandler
```

`ServeHTTP(ctx context.Context, resp http.ResponseWriter, req *http.Request) error`:

1. `force := libparse.ParseBoolDefault(ctx, req.URL.Query().Get("force"), false)`
2. If `!h.gate.TryAcquire()`, return
   ```go
   libhttp.WrapWithStatusCode(
       errors.Errorf(ctx, "a poll cycle is already running"),
       http.StatusConflict,
   )
   ```
3. Otherwise start the cycle and return 202:
   ```go
   go func() {
       defer h.gate.Release()
       if err := h.watcher.Poll(h.baseCtx, force); err != nil {
           glog.Errorf("forced poll cycle failed force=%t err=%v", force, err)
       }
   }()
   glog.Warningf("forced poll cycle accepted force=%t", force)
   resp.Header().Set("Content-Type", "application/json")
   resp.WriteHeader(http.StatusAccepted)
   return json.NewEncoder(resp).Encode(map[string]interface{}{
       "status": "accepted",
   })
   ```

Test `pkg/handler/trigger_handler_test.go` (`package handler_test`) using `mocks.Watcher` and `mocks.CycleGate` (or a real `pkg.NewCycleGate()`), driven through `libhttp.NewJSONErrorHandler(...)` and `httptest.NewRecorder()`:
- `POST /trigger` with a free gate → status 202 and body containing `{"status":"accepted"}`.
- The background `Poll` is invoked with `force == false` — wait for it with `Eventually(watcher.PollCallCount).Should(Equal(1))`.
- `POST /trigger?force=true` → `Eventually` shows `PollArgsForCall(0)` second value is `true`.
- `POST /trigger` while the gate is already held (acquire it in the test first) → status 409, and `watcher.PollCallCount()` stays 0.
- `POST /trigger?force=true&repo=attacker/repo` → 202, and the handler never reads `repo`: assert `PollArgsForCall(0)` force is `true` and that the handler code contains no reference to a repo parameter (the request-shape assertion is the 202 + the single `Poll` invocation).
- The goroutine does not use the request context: cancel the request context immediately after the handler returns and assert the injected watcher still receives its `Poll` call.

### 4. `pkg/factory/factory.go` — composition

Package `factory`. Keep `CreateTestLoglevelHandler` and `CreateSentryAlertHandler` untouched. Add:

```go
// goDevTimeout bounds the once-per-cycle stable-Go lookup so an unresponsive
// endpoint cannot stall a cycle beyond it. The cycle context cancels it too.
const goDevTimeout = 30 * time.Second

// CreateGoDevHTTPClient returns the plain HTTP client used for the go.dev
// lookup.
//
// It is deliberately NOT the GitHub App-authenticated client: reusing that
// client would send an installation token to a third-party host.
func CreateGoDevHTTPClient() *http.Client {
	return &http.Client{Timeout: goDevTimeout}
}

// CreateKafkaSender constructs the typed create-task command sender backed by
// a Kafka sync producer.
func CreateKafkaSender(
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) task.CreateCommandSender {
	sender := cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)
	return task.NewCreateCommandSender(sender, "")
}

// CreateStaticFilters builds the cycle-invariant chain in its frozen order.
// SHAUnchangedFilter is composed in per cycle inside Watcher.Poll because it
// needs a fresh CursorReader and is omitted on a forced cycle.
func CreateStaticFilters(allowlist []string) filter.TaskCreationFilter {
	return filter.TaskCreationFilters{
		filter.NewRepoAllowlistFilter(allowlist),
		filter.NewGoModPresentFilter(),
		filter.NewGoModParsableFilter(),
		filter.NewGoBehindFilter(),
		filter.NewAutoUpdateFilter(),
	}
}

// CreateWatcher wires all watcher dependencies. Pure composition — no I/O.
func CreateWatcher(
	githubHTTPClient *http.Client,
	goDevHTTPClient *http.Client,
	sender task.CreateCommandSender,
	metrics pkg.Metrics,
	cursorPath string,
	owner string,
	stage string,
	taskCreationFilter filter.TaskCreationFilter,
) pkg.Watcher {
	ghClient := pkg.NewGitHubClient(githubHTTPClient)
	goDevClient := pkg.NewGoDevClient(goDevHTTPClient, pkg.DefaultGoDevURL)
	publisher := pkg.NewTaskPublisher(sender, metrics, pkg.TaskConfig{Stage: stage})
	return pkg.NewWatcher(
		ghClient,
		goDevClient,
		publisher,
		metrics,
		cursorPath,
		owner,
		taskCreationFilter,
	)
}

// CreateTriggerHandler wraps the forced-cycle handler in the JSON error
// handler so a 409 comes back as a JSON body rather than plain text.
func CreateTriggerHandler(
	ctx context.Context,
	watcher pkg.Watcher,
	gate pkg.CycleGate,
) http.Handler {
	return libhttp.NewJSONErrorHandler(handler.NewTriggerHandler(ctx, watcher, gate))
}

// CreateRouter builds the full HTTP route table. main.go's createHTTPServer
// and main_http_test.go both call this — the endpoint-contract test MUST
// exercise the same registration this function produces, not a hand-copied
// route table, or a route added/removed only in main.go would go undetected.
func CreateRouter(
	ctx context.Context,
	triggerHandler http.Handler,
	sentryClient libsentry.Client,
) *mux.Router {
	router := mux.NewRouter()
	router.Path("/healthz").Handler(libhttp.NewPrintHandler("OK"))
	router.Path("/readiness").Handler(libhttp.NewPrintHandler("OK"))
	router.Path("/metrics").Handler(promhttp.Handler())
	router.Path("/trigger").Handler(triggerHandler)
	router.Path("/setloglevel/{level}").
		Handler(log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute)))
	router.Path("/gc").Handler(libhttp.NewGarbageCollectorHandler())
	router.Path("/testloglevel").Handler(CreateTestLoglevelHandler())
	router.Path("/sentryalert").Handler(CreateSentryAlertHandler(sentryClient))
	return router
}
```

Add `pkg/factory/factory_test.go` (`package factory_test`, the suite file already exists) asserting `CreateStaticFilters(nil)` returns a chain that skips a `filter.Candidate` (the filter package's own type, not `pkg.Candidate`) with `GoModPresent:false` for reason `"no_gomod"` and passes a fully-qualifying `filter.Candidate`, and that `CreateGoDevHTTPClient().Timeout` is non-zero.

### 5. `main.go` — rewrite

Keep `package main`, `const serviceName = "github-update-go-watcher"`, and the `main()` body (`service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy)`).

Replace the `application` struct with exactly these fields:

```go
type application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	Listen        string `required:"false" arg:"listen"         env:"LISTEN"         usage:"HTTP listen address"                                                   default:":9090"`
	Stage         string `required:"true"  arg:"stage"          env:"STAGE"          usage:"Deployment stage (dev|prod), stamped on every emitted task"`
	Owner         string `required:"true"  arg:"owner"          env:"OWNER"          usage:"GitHub owner / org to scan (e.g. bborbe)"`
	RepoAllowlist string `required:"false" arg:"repo-allowlist" env:"REPO_ALLOWLIST" usage:"Comma-separated host-qualified repo allowlist (host/owner/repo); empty = allow-all within OWNER"`
	PollInterval  string `required:"false" arg:"poll-interval"  env:"POLL_INTERVAL"  usage:"Poll interval (Go duration)"                                            default:"10m"`
	CursorPath    string `required:"false" arg:"cursor-path"    env:"CURSOR_PATH"    usage:"Persisted-memory path (mount a PVC)"                                    default:"/data/cursor.json"`
	KafkaBrokers  string `required:"true"  arg:"kafka-brokers"  env:"KAFKA_BROKERS"  usage:"Comma separated list of Kafka brokers"`

	TopicPrefix base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`

	AppID          int64  `required:"false" arg:"app-id"          env:"APP_ID"          usage:"GitHub App ID"`
	InstallationID int64  `required:"false" arg:"installation-id" env:"INSTALLATION_ID" usage:"GitHub App Installation ID"`
	PEMKey         string `required:"false" arg:"pem-key"         env:"PEM_KEY"         usage:"GitHub App PEM key (populated from a k8s Secret)" display:"length"`

	BuildGitVersion string            `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"           default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"       default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`

	TriggerHandler http.Handler
}
```

`DataDir`/`DATADIR` and `BatchSize`/`BATCH_SIZE` are removed — the service has no on-disk key-value store. `CURSOR_PATH` defaults to `/data/cursor.json`, matching `pkg.DefaultCursorPath`; the binary must start with `CURSOR_PATH` unset. Add a compile-time-ish assertion in `main.go` is not possible for a struct tag, so instead add a unit assertion in the test in step 7.

`Run(ctx context.Context, sentryClient libsentry.Client) error` body, in order:

1. `libmetrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildGitVersion, a.BuildGitCommit, a.BuildDate)`.
2. `pollInterval, err := time.ParseDuration(a.PollInterval)`; on error `return errors.Wrapf(ctx, err, "parse poll interval %q", a.PollInterval)`.
3. `allowlist := filter.ParseRepoAllowlist(a.RepoAllowlist)`; validate it with `repoallowlist.Validate(ctx, allowlist)` (import `"github.com/bborbe/maintainer/repoallowlist"` — NOT `lib/repoallowlist`) and return a wrapped error on failure so a typo'd entry fails at start rather than silently widening or narrowing scope. Log the entry count (`glog.V(2).Infof("repo-allowlist count=%d", len(allowlist))`); when the count is 0 log that allow-all applies within the owner.
4. `httpClient, err := auth.ResolveGitHubClient(ctx, auth.Credentials{AppID: a.AppID, InstallationID: a.InstallationID, PEMKey: []byte(a.PEMKey)})`; wrap errors; `defer httpClient.CloseIdleConnections()`.
5. `syncProducer, err := libkafka.NewSyncProducerWithName(ctx, libkafka.ParseBrokersFromString(a.KafkaBrokers), serviceName)`; wrap errors; `defer` a close whose error is logged with `glog.Warningf("close kafka sync producer: %v", cerr)`.
6. `metrics := pkg.NewMetrics(nil)` (nil → `prometheus.DefaultRegisterer`, which `promhttp.Handler()` serves).
7. `sender := factory.CreateKafkaSender(syncProducer, a.TopicPrefix)`.
8. `w := factory.CreateWatcher(httpClient, factory.CreateGoDevHTTPClient(), sender, metrics, a.CursorPath, a.Owner, a.Stage, factory.CreateStaticFilters(allowlist))`.
9. `gate := pkg.NewCycleGate()`; `a.TriggerHandler = factory.CreateTriggerHandler(ctx, w, gate)`.
10. `glog.V(2).Infof("github-update-go-watcher starting stage=%s owner=%s interval=%s cursor=%s listen=%s", a.Stage, a.Owner, a.PollInterval, a.CursorPath, a.Listen)`.
11. `return service.Run(ctx, a.pollLoop(w, gate, pollInterval), a.createHTTPServer(sentryClient))`.

Keep `Run` under the 80-line `funlen` cap; extract a helper if needed rather than adding `//nolint`.

`pollLoop` — the timer path shares the same single-cycle gate as the endpoint:

```go
// pollLoop fires one cycle immediately on start and one per tick thereafter.
// It shares the CycleGate with the /trigger endpoint, so a tick that lands
// while a forced cycle is running is skipped rather than run concurrently —
// the persisted-memory file has exactly one writer.
func (a *application) pollLoop(
	w pkg.Watcher,
	gate pkg.CycleGate,
	interval time.Duration,
) run.Func {
	poll := func(ctx context.Context) {
		if !gate.TryAcquire() {
			glog.Warningf("poll cycle skipped: a cycle is already running")
			return
		}
		defer gate.Release()
		// The interval loop is the dedup-engaged path; force=true comes
		// exclusively from the /trigger endpoint.
		if err := w.Poll(ctx, false); err != nil {
			glog.Errorf("poll: %v", err)
		}
	}
	return func(ctx context.Context) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		poll(ctx)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				poll(ctx)
			}
		}
	}
}
```

`createHTTPServer(sentryClient libsentry.Client) run.Func` builds its router via `factory.CreateRouter(ctx, a.TriggerHandler, sentryClient)` (defined in step 4) and wraps it with `libhttp.NewServer(a.Listen, router).Run`. Do NOT re-declare the route table inline in `main.go` — `factory.CreateRouter` is the single source of the route table, so `main_http_test.go` (step 8) exercises the exact same registration the running binary uses.

`/resetdb` and `/resetbucket/{BucketName}` are **removed**, along with the `libboltkv`/`libkv` imports and the `db` parameter. An unregistered path returns 404 from mux, which is the documented contract.

Do NOT add `/resetcursor` or `/setcursor` endpoints — spec Non-goal: no cursor-editing admin endpoints.

### 6. Makefile and example.env

- `Makefile` `run` target: remove `-datadir="data"` and `-batch-size="100"` (those flags no longer exist and the target would fail at startup). Add `-stage="${STAGE}"`, `-owner="${OWNER}"`, `-repo-allowlist="${REPO_ALLOWLIST}"`, `-cursor-path="data/cursor.json"`, `-poll-interval="10m"`. Keep the target name, the `-sentry-dsn` teamvault line, `-listen`, `-kafka-brokers` and `-v=2` as they are. Do not rename, add, or delete any Makefile target.
- `example.env`: add `export OWNER=bborbe`, `export REPO_ALLOWLIST=`, `export STAGE=dev`. If the file is not currently alphabetically sorted, that is pre-existing and out of scope here; just insert the new lines in the file's existing order/convention rather than reordering unrelated lines.

### 7. `main_test.go`

Leave the existing gexec compile spec untouched. Add one spec to the same file asserting the persisted-memory default: a spec that constructs the `application` zero value is not possible from `package main_test`, so instead assert the constant contract from the package under test's dependency:

```go
It("defaults the cursor path to the PVC mount point", func() {
	Expect(pkg.DefaultCursorPath).To(Equal("/data/cursor.json"))
})
```

and add a spec asserting the binary's flag surface no longer carries the removed scaffold flags:

```go
It("does not declare the removed scaffold flags", func() {
	data, err := os.ReadFile("main.go")
	Expect(err).NotTo(HaveOccurred())
	Expect(string(data)).NotTo(ContainSubstring("DATADIR"))
	Expect(string(data)).NotTo(ContainSubstring("BATCH_SIZE"))
})
```

### 8. `main_http_test.go` — endpoint contract

New file, `package main_test`. Build the router via `factory.CreateRouter(ctx, factory.CreateTriggerHandler(ctx, watcherFake, gate), sentryClient)` — the SAME function `createHTTPServer` calls in production — with a `mocks.Watcher` and a real `pkg.NewCycleGate()`. Do NOT hand-copy the route registrations into the test; calling `factory.CreateRouter` is what makes this an integration test of the production dispatch path rather than a test of a parallel copy. Serve it with `httptest.NewServer` and assert:

- `GET /healthz` → 200
- `GET /readiness` → 200
- `GET /metrics` → 200, and the body contains each of these substrings:
  - `github_update_go_watcher_poll_cycle_total{result="success"} 0`
  - one line per value in `pkg.PollCycleResults`, `pkg.PublishStatuses`, `pkg.FilterSkipReasons` at 0
  - `github_update_go_watcher_repos_scanned_total 0`

  Register the metrics for this test against `prometheus.NewRegistry()` and serve them with `promhttp.HandlerFor(reg, promhttp.HandlerOpts{})` so the assertion does not depend on global registry state. Assert **before** any cycle has run.
- `POST /trigger` → 202 with body containing `{"status":"accepted"}`
- `POST /trigger` while the gate is held → 409
- `POST /trigger?force=true&repo=attacker/repo` → 202, and the fake watcher's recorded `Poll` calls show force `true`; assert no repo string reaches the watcher (the `Poll` signature carries no repo, which is the structural guarantee — state it in a comment).
- `GET /setloglevel/2` → 200
- `GET /resetdb` → 404

Each HTTP response's body must be closed (`defer resp.Body.Close()` or equivalent) — `bodyclose` is enabled in `.golangci.yml` and, unlike `gosec`/`errcheck`/`dupl`, is NOT in the test-path exclusion list.

### 9. `README.md`

Rewrite the `## Status` section and add operator documentation. Keep the existing layer table and the two-gates/cheap-signal/trigger-model prose. Add:

- **Environment variables** table with one row per binding above: name, required, default, meaning. Must include `REPO_ALLOWLIST`, `CURSOR_PATH` (documented as defaulting to `/data/cursor.json`, mounted as a PVC by the `quant` Helm chart), `OWNER`, `STAGE`, `POLL_INTERVAL`, `KAFKA_BROKERS`, `TOPIC_PREFIX`, `LISTEN`, `APP_ID`, `INSTALLATION_ID`, `PEM_KEY`, `SENTRY_DSN`.
- **Metrics** section naming all four: `github_update_go_watcher_poll_cycle_total{result}`, `github_update_go_watcher_published_total{status}`, `github_update_go_watcher_repos_scanned_total`, `github_update_go_watcher_filter_skipped_total{reason}`, each with its documented label values.
- **Skip reasons** section listing all six labels — `scope`, `no_gomod`, `gomod_unparsable`, `go_current`, `auto_update_disabled`, `sha_unchanged` — with one line each on what triggers them.
- **Emitted task contract** section reproducing the twelve field keys with their values and the four-line body block verbatim (including the two spaces either side of the middot), marked as frozen against the consuming agent.
- **Endpoints** table: `/healthz`, `/readiness`, `/metrics`, `/trigger` (POST, `?force=<bool>`, 202 / 409), `/setloglevel/{level}`, `/gc`, `/testloglevel`, `/sentryalert`.
- **Forced-emit end-to-end verification procedure** — the operator-facing steps: confirm the target repo is allowlisted and carries `goUpdate.autoUpdate: true`, `curl -X POST '<admin-url>/trigger?force=true'`, expect `202 {"status":"accepted"}`, then confirm `github_update_go_watcher_published_total{status="create"}` advanced on `/metrics` and the vault task file appeared. Note explicitly that this covers the detect→Kafka→controller→vault-file path that unit tests cannot reach, and that a second `POST /trigger` while a cycle runs returns 409.
- Update `## Status` from "Scaffolded. Watcher logic not yet implemented" to reflect the shipped service.

### 10. Verify

Run `make precommit` from the repo root and confirm every grep in `<verification>` below.

</requirements>

<constraints>
- Do NOT ship Kubernetes/Helm deployment wiring. Do not add or modify anything under `k8s/`. Deployment is Helm-driven from the separate `quant` repo.
- Do NOT add cursor-editing admin endpoints (reset/set per repo) — spec Non-goal. The forced-cycle endpoint is the whole admin surface for cycle control.
- Do NOT add a per-cycle emit cap, throttle, or dry-run switch — spec Non-goal, invariant.
- Do NOT add an env flag that disables the opt-in gate or the allowlist — spec Non-goal: an escape hatch on a trust gate is the regression this spec exists to prevent.
- Do NOT add a configurable source URL for the stable-Go lookup — spec Non-goal, invariant.
- Do NOT ship a Kafka command topic, a command consumer, or a key-value store for the forced cycle. The endpoint triggers the cycle in-process (spec § Deliberate deviation from the template). Do not import `github.com/bborbe/cqrs/cdb` for anything other than `NewCommandObjectSender` in `CreateKafkaSender`, and do not import `github.com/bborbe/boltkv` or `github.com/bborbe/kv` at all.
- The forced-cycle endpoint takes NO owner, repo, or scope parameter. It reads only `?force=<bool>`; unknown query parameters are ignored.
- Exactly one cycle runs at a time. The interval loop and the endpoint share one `CycleGate`; a contended forced request returns HTTP 409, a contended tick is skipped with a log line.
- The forced cycle must run under the application's long-lived context, never the HTTP request context.
- The go.dev lookup must NOT reuse the GitHub App-authenticated HTTP client — that would send an installation token to a third-party host.
- The GitHub App private key arrives by environment only, is never logged, and is bound with `display:"length"` so a config dump shows its length, not its value.
- `CURSOR_PATH` defaults to `/data/cursor.json` and the binary must start with it unset. Do NOT require a data-directory variable.
- The scaffold's Makefile targets stay as they are — update the `run` recipe's flags only; do not rename, add, or remove a target.
- Bind the poll interval as a `string` parsed with `time.ParseDuration`. Do NOT declare it as `time.Duration`; the argument binder cannot unmarshal that type.
- The module must never shell out or clone: no `os/exec`, no `exec.Command`, no `go-git` anywhere.
- Never use `fmt.Errorf`. All errors go through `github.com/bborbe/errors` and carry `ctx`.
- Never hand-edit anything under `mocks/`.
- Keep every line under 100 characters and every function under 80 lines / 50 statements (`funlen`); extract helpers rather than adding `//nolint`.
- Every new `.go` file starts with the BSD license header block used by the existing files.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, including the `main_test.go` gexec compile spec.
</constraints>

<verification>
Run from the repo root:

```
make precommit
```

Must exit 0.

```
grep -rn "os/exec\|go-git\|exec.Command" --include=*.go .
```
Expect zero matches.

```
grep -rn "boltkv\|bborbe/kv\"" --include=*.go .
```
Expect zero matches.

```
grep -rn "resetdb\|resetbucket\|DATADIR\|BATCH_SIZE" --include='*.go' . | grep -v '_test\.go:'
```
Expect zero matches. (`main_test.go` and `main_http_test.go` deliberately reference `DATADIR`/`BATCH_SIZE`/`resetdb` to prove they are gone / return 404 — this checks production code only.)

```
grep -rn "github-update-go-agent" --include=*.go .
grep -rn "go-update-agent" --include=*.go . | grep -v "github-update-go-agent"
```
First expects ≥1 match, second expects zero.

```
grep -n "REPO_ALLOWLIST" README.md
grep -n "CURSOR_PATH" README.md
grep -n "auto_update_disabled" README.md
grep -n "/trigger" README.md
grep -n "task_type" README.md
grep -n "Latest Go:" README.md
```
Each expects ≥1 line.

```
grep -n "datadir\|batch-size" Makefile
```
Expect zero matches.

```
go test -mod=mod ./...
```
Must exit 0, including the `main_test.go` compile spec and the endpoint-contract suite.
</verification>
