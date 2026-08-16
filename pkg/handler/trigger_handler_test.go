// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/handler"
)

var _ = Describe("TriggerHandler", func() {
	var (
		watcherFake *fakeWatcher
		gate        pkg.CycleGate
		baseCtx     context.Context
		th          handler.TriggerHandler
	)

	BeforeEach(func() {
		watcherFake = &fakeWatcher{calls: make(chan fakeCall, 10)}
		gate = pkg.NewCycleGate()
		baseCtx = context.Background()
		th = handler.NewTriggerHandler(baseCtx, watcherFake, gate)
	})

	It("POST /trigger returns 202 and body contains accepted", func() {
		req := httptest.NewRequest("POST", "/trigger", nil)
		rec := httptest.NewRecorder()
		th.ServeHTTP(baseCtx, rec, req)

		Expect(rec.Code).To(Equal(http.StatusAccepted))
		var body map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &body)
		Expect(err).NotTo(HaveOccurred())
		Expect(body["status"]).To(Equal("accepted"))
	})

	It("POST /trigger invokes watcher.Poll with force=false", func() {
		req := httptest.NewRequest("POST", "/trigger", nil)
		rec := httptest.NewRecorder()
		th.ServeHTTP(baseCtx, rec, req)

		var call fakeCall
		Eventually(watcherFake.calls).Should(Receive(&call))
		Expect(call.force).To(BeFalse())
	})

	It("POST /trigger?force=true invokes watcher.Poll with force=true", func() {
		req := httptest.NewRequest("POST", "/trigger?force=true", nil)
		rec := httptest.NewRecorder()
		th.ServeHTTP(baseCtx, rec, req)

		var call fakeCall
		Eventually(watcherFake.calls).Should(Receive(&call))
		Expect(call.force).To(BeTrue())
	})

	It("POST /trigger while gate held returns 409", func() {
		// Pre-acquire the gate so TryAcquire returns false
		acquired := gate.TryAcquire()
		Expect(acquired).To(BeTrue())

		req := httptest.NewRequest("POST", "/trigger", nil)
		rec := httptest.NewRecorder()
		th.ServeHTTP(baseCtx, rec, req)

		Expect(rec.Code).To(Equal(http.StatusConflict))
		// watcherFake.Poll was never called
		Expect(watcherFake.calls).To(BeEmpty())
	})

	It("POST /trigger?force=true&repo=attacker/repo calls Poll with force=true", func() {
		req := httptest.NewRequest("POST", "/trigger?force=true&repo=attacker/repo", nil)
		rec := httptest.NewRecorder()
		th.ServeHTTP(baseCtx, rec, req)

		var call fakeCall
		Eventually(watcherFake.calls).Should(Receive(&call))
		Expect(call.force).To(BeTrue())
	})

	It("the goroutine uses baseCtx not the request context", func() {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-watcherFake.calls
		}()

		req := httptest.NewRequest("POST", "/trigger", nil)
		rec := httptest.NewRecorder()
		reqCtx, cancel := context.WithCancel(baseCtx)

		th.ServeHTTP(reqCtx, rec, req)

		// Cancel the request context immediately
		cancel()
		wg.Wait()
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
