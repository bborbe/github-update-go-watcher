// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
)

var (
	watcherFake *fakeWatcher
	gate        *realGate
	// metricsOnce registers pkg.NewMetrics against prometheus.DefaultRegisterer
	// exactly once for this test binary. /metrics is served from
	// prometheus.DefaultRegisterer in production (see createHTTPServer), and
	// registering the same collectors twice against the same registerer
	// panics, so this must not run per-spec.
	metricsOnce sync.Once
)

var _ = Describe("HTTP Endpoints", func() {
	var (
		server  *httptest.Server
		baseCtx context.Context
		cancel  context.CancelFunc
	)

	BeforeEach(func() {
		baseCtx, cancel = context.WithCancel(context.Background())
		watcherFake = &fakeWatcher{calls: make(chan fakeCall, 10)}
		gate = &realGate{ch: make(chan struct{}, 1)}

		// /metrics is served from prometheus.DefaultRegisterer in production
		// (see createHTTPServer / factory.CreateRouter), so the endpoint-
		// contract test registers the real watcher metrics there too, once
		// per test binary — re-registering the same collectors on every
		// spec would panic on duplicate registration.
		metricsOnce.Do(func() {
			pkg.NewMetrics(prometheus.DefaultRegisterer)
		})

		triggerHandler := factory.CreateTriggerHandler(baseCtx, watcherFake, gate)
		router := factory.CreateRouter(baseCtx, triggerHandler, nil)
		server = httptest.NewServer(router)
	})

	AfterEach(func() {
		server.Close()
		cancel()
	})

	httpGet := func(path string) *http.Response {
		req, err := http.NewRequest("GET", server.URL+path, nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	Describe("GET /healthz", func() {
		It("returns 200", func() {
			resp := httpGet("/healthz")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("GET /readiness", func() {
		It("returns 200", func() {
			resp := httpGet("/readiness")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("GET /metrics", func() {
		It("returns 200 with all four metrics and every label pre-initialised to 0", func() {
			resp := httpGet("/metrics")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			text := string(body)

			for _, result := range pkg.PollCycleResults {
				Expect(text).To(ContainSubstring(
					`github_update_go_watcher_poll_cycle_total{result="` + result + `"} 0`,
				))
			}
			for _, status := range pkg.PublishStatuses {
				Expect(text).To(ContainSubstring(
					`github_update_go_watcher_published_total{status="` + status + `"} 0`,
				))
			}
			for _, reason := range pkg.FilterSkipReasons {
				Expect(text).To(ContainSubstring(
					`github_update_go_watcher_filter_skipped_total{reason="` + reason + `"} 0`,
				))
			}
			Expect(text).To(ContainSubstring("github_update_go_watcher_repos_scanned_total 0"))
		})
	})

	Describe("POST /trigger", func() {
		It("returns 202 and body contains accepted", func() {
			req, err := http.NewRequest("POST", server.URL+"/trigger", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
		})
	})

	Describe("POST /trigger while gate held", func() {
		It("returns 409", func() {
			gate.TryAcquire() // hold the gate
			defer gate.Release()

			req, err := http.NewRequest("POST", server.URL+"/trigger", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusConflict))
		})
	})

	Describe("POST /trigger?force=true&repo=attacker/repo", func() {
		It("calls Poll with force=true and ignores repo", func() {
			req, err := http.NewRequest(
				"POST",
				server.URL+"/trigger?force=true&repo=attacker/repo",
				nil,
			)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted))

			var call fakeCall
			Eventually(watcherFake.calls).Should(Receive(&call))
			Expect(call.force).To(BeTrue())
		})
	})

	Describe("GET /setloglevel/2", func() {
		It("returns 200", func() {
			resp := httpGet("/setloglevel/2")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("GET /resetdb", func() {
		It("returns 404", func() {
			resp := httpGet("/resetdb")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})

type fakeCall struct {
	ctx   context.Context
	force bool
}

type fakeWatcher struct {
	calls chan fakeCall
}

func (w *fakeWatcher) Poll(ctx context.Context, force bool) error {
	w.calls <- fakeCall{ctx: ctx, force: force}
	return nil
}

type realGate struct {
	ch chan struct{}
}

func (g *realGate) TryAcquire() bool {
	select {
	case g.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *realGate) Release() {
	select {
	case <-g.ch:
	default:
	}
}
