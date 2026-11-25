// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

// hostRecordingTransport records every request host and answers 200 with an
// empty JSON array, so no real network call is made.
type hostRecordingTransport struct {
	mu    sync.Mutex
	hosts []string
}

func (t *hostRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.hosts = append(t.hosts, req.URL.Host)
	t.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`[]`)),
		Request:    req,
	}, nil
}

func (t *hostRecordingTransport) recordedHosts() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.hosts...)
}

var _ = Describe("github client transport chain", func() {
	It("retries a 429 with Retry-After once and then succeeds", func() {
		var (
			mu    sync.Mutex
			calls int
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			calls++
			attempt := calls
			mu.Unlock()
			if attempt == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"secondary rate limit"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}))
		DeferCleanup(srv.Close)

		client := pkg.NewGitHubClient(srv.Client(), srv.URL+"/")
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())

		mu.Lock()
		defer mu.Unlock()
		Expect(calls).To(Equal(2))
	})

	It("points the client at the configured API base URL", func() {
		recorder := &hostRecordingTransport{}
		client := pkg.NewGitHubClient(
			&http.Client{Transport: recorder},
			"http://stub.invalid/",
		)
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())

		hosts := recorder.recordedHosts()
		Expect(hosts).NotTo(BeEmpty())
		Expect(hosts[0]).To(Equal("stub.invalid"))
	})

	It("keeps the default base URL when the configured one does not parse", func() {
		recorder := &hostRecordingTransport{}
		client := pkg.NewGitHubClient(
			&http.Client{Transport: recorder},
			"://bad",
		)
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())

		hosts := recorder.recordedHosts()
		Expect(hosts).NotTo(BeEmpty())
		Expect(hosts[0]).To(Equal("api.github.com"))
	})
})
