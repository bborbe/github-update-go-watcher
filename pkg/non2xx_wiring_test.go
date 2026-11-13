// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("github client transport wiring", func() {
	It("installs the non-2xx logging transport on the production client", func() {
		httpClient := &http.Client{}
		_ = pkg.NewGitHubClient(httpClient)
		Expect(httpClient.Transport).NotTo(BeNil())
		Expect(
			httpClient.Transport,
		).To(BeAssignableToTypeOf(pkg.Non2xxLoggingTransportForTest(nil, nil)))
	})

	It("keeps the client working after the transport is wrapped", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[]`)
		}))
		DeferCleanup(srv.Close)
		client := pkg.NewGitHubClient(srv.Client())
		Expect(pkg.SetBaseURL(client, srv.URL+"/")).To(Succeed())
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())
	})
})
