// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("github client transport wiring", func() {
	It("logs a non-2xx response through the production client chain", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		}))
		DeferCleanup(srv.Close)

		var (
			mu    sync.Mutex
			lines []string
		)
		client := pkg.NewGitHubClientWithLogForTest(
			srv.Client(),
			srv.URL+"/",
			func(line string) {
				mu.Lock()
				defer mu.Unlock()
				lines = append(lines, line)
			},
		)
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).To(HaveOccurred())

		mu.Lock()
		defer mu.Unlock()
		Expect(lines).NotTo(BeEmpty())
		Expect(strings.Join(lines, "\n")).To(ContainSubstring("github non-2xx"))
	})

	It("keeps the client working after the transport is wrapped", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		}))
		DeferCleanup(srv.Close)
		client := pkg.NewGitHubClient(srv.Client(), srv.URL+"/")
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())
	})
})
