// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/prometheus/client_golang/prometheus"
)

//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics is the four observable counters required of every watcher.
type Metrics interface {
	// IncPollCycle — result: "success" | "rate_limited" | "github_error" | "go_version_error"
	IncPollCycle(result string)

	// IncPublished — status: "create" | "error"
	IncPublished(status string)

	// IncReposScanned adds n repos scanned in one cycle (no labels).
	IncReposScanned(n int)

	// IncFilterSkipped — reason: "scope" | "no_gomod" | "gomod_unparsable" |
	// "go_current" | "auto_update_disabled" | "sha_unchanged"
	IncFilterSkipped(reason string)
}

const metricNamespace = "github_update_go_watcher"

// PollCycleResults, PublishStatuses and FilterSkipReasons are the closed label
// sets. They are exported so tests and the README stay in lockstep with the
// pre-initialisation loop below.
var (
	PollCycleResults = []string{
		"success",
		"rate_limited",
		"github_error",
		"go_version_error",
	}
	PublishStatuses   = []string{"create", "error"}
	FilterSkipReasons = []string{
		"scope",
		"no_gomod",
		"gomod_unparsable",
		"go_current",
		"auto_update_disabled",
		"sha_unchanged",
	}
)

// NewMetrics returns the Prometheus-backed Metrics registered against the
// supplied Registerer. Pass nil for prometheus.DefaultRegisterer. Every label
// value is pre-initialised to 0 so /metrics exposes the full series set before
// the first cycle runs.
//
// Registration goes through the injected Registerer — never a package-level
// init() and never prometheus.MustRegister directly.
func NewMetrics(registerer prometheus.Registerer) Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &metricsImpl{
		pollCycle: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metricNamespace,
				Name:      "poll_cycle_total",
				Help:      "Total number of poll cycles by result",
			},
			[]string{"result"},
		),
		published: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metricNamespace,
				Name:      "published_total",
				Help:      "Total number of published events by status",
			},
			[]string{"status"},
		),
		reposScanned: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: metricNamespace,
				Name:      "repos_scanned_total",
				Help:      "Total number of repos scanned",
			},
		),
		filterSkipped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metricNamespace,
				Name:      "filter_skipped_total",
				Help:      "Total number of skipped repos by reason",
			},
			[]string{"reason"},
		),
	}
	registerer.MustRegister(m.pollCycle)
	registerer.MustRegister(m.published)
	registerer.MustRegister(m.reposScanned)
	registerer.MustRegister(m.filterSkipped)

	// Pre-initialise all label combinations to 0
	for _, label := range PollCycleResults {
		m.pollCycle.WithLabelValues(label).Add(0)
	}
	for _, label := range PublishStatuses {
		m.published.WithLabelValues(label).Add(0)
	}
	for _, label := range FilterSkipReasons {
		m.filterSkipped.WithLabelValues(label).Add(0)
	}

	return m
}

type metricsImpl struct {
	pollCycle     *prometheus.CounterVec
	published     *prometheus.CounterVec
	reposScanned  prometheus.Counter
	filterSkipped *prometheus.CounterVec
}

func (m *metricsImpl) IncPollCycle(result string) {
	m.pollCycle.WithLabelValues(result).Inc()
}

func (m *metricsImpl) IncPublished(status string) {
	m.published.WithLabelValues(status).Inc()
}

func (m *metricsImpl) IncReposScanned(n int) {
	m.reposScanned.Add(float64(n))
}

func (m *metricsImpl) IncFilterSkipped(reason string) {
	m.filterSkipped.WithLabelValues(reason).Inc()
}
