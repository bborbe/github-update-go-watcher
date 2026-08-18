// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/bborbe/kafka"
	"github.com/bborbe/maintainer/repoallowlist"
	libmetrics "github.com/bborbe/metrics"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/auth"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

const serviceName = "github-update-go-watcher"

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	Listen        string `required:"false" arg:"listen"         env:"LISTEN"         usage:"HTTP listen address"                                                                             default:":9090"`
	Stage         string `required:"true"  arg:"stage"          env:"STAGE"          usage:"Deployment stage (dev|prod), stamped on every emitted task"`
	UpdateScope   string `required:"false" arg:"update-scope"   env:"UPDATE_SCOPE"   usage:"Fleet-wide update scope stamped on every emitted task (golang|deps); empty = both"`
	Owner         string `required:"true"  arg:"owner"          env:"OWNER"          usage:"GitHub owner / org to scan (e.g. bborbe)"`
	RepoAllowlist string `required:"false" arg:"repo-allowlist" env:"REPO_ALLOWLIST" usage:"Comma-separated host-qualified repo allowlist (host/owner/repo); empty = allow-all within OWNER"`
	PollInterval  string `required:"false" arg:"poll-interval"  env:"POLL_INTERVAL"  usage:"Poll interval (Go duration)"                                                                     default:"10m"`
	CursorPath    string `required:"false" arg:"cursor-path"    env:"CURSOR_PATH"    usage:"Persisted-memory path (mount a PVC)"                                                             default:"/data/cursor.json"`
	KafkaBrokers  string `required:"true"  arg:"kafka-brokers"  env:"KAFKA_BROKERS"  usage:"Comma separated list of Kafka brokers"`

	TopicPrefix base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`

	AppID          int64  `required:"false" arg:"app-id"          env:"APP_ID"          usage:"GitHub App ID"`
	InstallationID int64  `required:"false" arg:"installation-id" env:"INSTALLATION_ID" usage:"GitHub App Installation ID"`
	PEMKey         string `required:"false" arg:"pem-key"         env:"PEM_KEY"         usage:"GitHub App PEM key (populated from a k8s Secret)" display:"length"`

	BuildGitVersion string            `required:"false" arg:"build-git-version" env:"BUILD_GIT_VERSION" usage:"Build Git version"         default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"  env:"BUILD_GIT_COMMIT"  usage:"Build Git commit hash"     default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"        env:"BUILD_DATE"        usage:"Build timestamp (RFC3339)"`

	TriggerHandler http.Handler
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	libmetrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildGitVersion, a.BuildGitCommit, a.BuildDate)

	pollInterval, err := time.ParseDuration(a.PollInterval)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse poll interval %q", a.PollInterval)
	}

	allowlist := filter.ParseRepoAllowlist(a.RepoAllowlist)
	if err := repoallowlist.Validate(ctx, allowlist); err != nil {
		return errors.Wrapf(ctx, err, "validate repo allowlist")
	}
	if len(allowlist) == 0 {
		glog.V(2).Infof("repo-allowlist empty: allow-all within owner=%s", a.Owner)
	} else {
		glog.V(2).Infof("repo-allowlist count=%d", len(allowlist))
	}

	httpClient, err := auth.ResolveGitHubClient(ctx, auth.Credentials{
		AppID:          a.AppID,
		InstallationID: a.InstallationID,
		PEMKey:         []byte(a.PEMKey),
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "resolve GitHub client")
	}
	defer httpClient.CloseIdleConnections()

	syncProducer, err := kafka.NewSyncProducerWithName(
		ctx,
		kafka.ParseBrokersFromString(a.KafkaBrokers),
		serviceName,
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "create kafka sync producer")
	}
	defer func() {
		if cerr := syncProducer.Close(); cerr != nil {
			glog.Warningf("close kafka sync producer: %v", cerr)
		}
	}()

	metrics := pkg.NewMetrics(nil)
	sender := factory.CreateKafkaSender(syncProducer, a.TopicPrefix)
	w := factory.CreateWatcher(
		httpClient,
		factory.CreateGoDevHTTPClient(),
		sender,
		metrics,
		a.CursorPath,
		a.Owner,
		a.Stage,
		a.UpdateScope,
		factory.CreateStaticFilters(allowlist),
	)
	gate := pkg.NewCycleGate()
	a.TriggerHandler = factory.CreateTriggerHandler(ctx, w, gate)

	glog.V(2).Infof(
		"github-update-go-watcher starting stage=%s owner=%s interval=%s cursor=%s listen=%s",
		a.Stage, a.Owner, a.PollInterval, a.CursorPath, a.Listen,
	)

	return service.Run(
		ctx,
		a.pollLoop(w, gate, pollInterval),
		a.createHTTPServer(sentryClient),
	)
}

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

func (a *application) createHTTPServer(sentryClient libsentry.Client) run.Func {
	return func(ctx context.Context) error {
		router := factory.CreateRouter(ctx, a.TriggerHandler, sentryClient)
		glog.V(2).Infof("http server listening on %s", a.Listen)
		return libhttp.NewServer(a.Listen, router)(ctx)
	}
}
