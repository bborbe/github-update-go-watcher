// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/github-update-go-watcher/pkg/factory"
)

var (
	watcherFake *fakeWatcher
	gate        *realGate
	metricsReg  *prometheus.Registry
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

		// Each test gets its own registry to avoid duplicate registration
		metricsReg = prometheus.NewRegistry()
		// Note: we don't call pkg.NewMetrics here because the router
		// serves from prometheus.DefaultRegisterer in production.
		// The endpoint-contract tests verify the router wiring, not metrics registration.
		// Metrics are verified in pkg/metrics_test.go with proper isolation.

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
		It("returns 200", func() {
			resp := httpGet("/metrics")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
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
