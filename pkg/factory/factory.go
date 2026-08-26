// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"net/http"
	"time"

	"github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libhttp "github.com/bborbe/http"
	"github.com/bborbe/kafka"
	"github.com/bborbe/log"
	libsentry "github.com/bborbe/sentry"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
	"github.com/bborbe/github-update-go-watcher/pkg/handler"
)

// CreateTestLoglevelHandler creates an HTTP handler that tests different glog verbosity levels.
func CreateTestLoglevelHandler() http.Handler {
	return handler.NewTestLoglevelHandler()
}

// CreateSentryAlertHandler creates an HTTP handler that sends test alerts to Sentry.
func CreateSentryAlertHandler(sentryClient libsentry.Client) http.Handler {
	return handler.NewSentryAlertHandler(sentryClient)
}

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
	syncProducer kafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) task.CreateCommandSender {
	sender := cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)
	return task.NewCreateCommandSender(sender, "")
}

// CreateKafkaCompleteSender constructs the typed complete-task command sender
// backed by the same Kafka sync producer. Publishes CompleteCommands for
// repos whose update PR merged (merge-detection → task completion).
func CreateKafkaCompleteSender(
	syncProducer kafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) task.CompleteCommandSender {
	sender := cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)
	return task.NewCompleteCommandSender(sender, "")
}

// CreateStaticFilters builds the cycle-invariant chain in its frozen order.
// SHAUnchangedFilter is composed in per cycle inside Watcher.Poll because it
// needs a fresh CursorReader and is omitted on a forced cycle.
func CreateStaticFilters(allowlist []string) filter.TaskCreationFilter {
	return filter.TaskCreationFilterList{
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
	completeSender task.CompleteCommandSender,
	metrics pkg.Metrics,
	cursorPath string,
	owner string,
	stage string,
	updateScope string,
	taskCreationFilter filter.TaskCreationFilter,
) pkg.Watcher {
	ghClient := pkg.NewGitHubClient(githubHTTPClient)
	goDevClient := pkg.NewGoDevClient(goDevHTTPClient, pkg.DefaultGoDevURL)
	publisher := pkg.NewTaskPublisher(sender, metrics, pkg.TaskConfig{
		Stage:       stage,
		UpdateScope: updateScope,
	})
	return pkg.NewWatcher(
		ghClient,
		goDevClient,
		publisher,
		completeSender,
		metrics,
		cursorPath,
		owner,
		taskCreationFilter,
	)
}

// CreateTriggerHandler wraps the forced-cycle handler in an http.Handler
// adapter so it can be registered with gorilla/mux.
func CreateTriggerHandler(
	ctx context.Context,
	watcher pkg.Watcher,
	gate pkg.CycleGate,
) http.Handler {
	return handler.NewTriggerHandlerHTTPAdapter(ctx, watcher, gate)
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
