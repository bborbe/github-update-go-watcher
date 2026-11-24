// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("GetFile", func() {
	var (
		server *httptest.Server
		client pkg.GitHubClient
		ctx    context.Context
		repo   pkg.Repo
	)

	BeforeEach(func() {
		ctx = context.Background()
		repo = pkg.Repo{Owner: "o", Name: "r", DefaultBranch: "main"}
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	newClient := func(handler http.HandlerFunc) {
		server = httptest.NewServer(handler)
		client = pkg.NewGitHubClient(server.Client())
		Expect(pkg.SetBaseURL(client, server.URL+"/")).To(Succeed())
	}

	Context("200 with base64 content", func() {
		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				encoded := base64.StdEncoding.EncodeToString(
					[]byte("VULNCHECK_IGNORE = GO-2026-1234\n"),
				)
				fmt.Fprintf(
					w,
					`{"type":"file","encoding":"base64","size":30,"content":%q}`,
					encoded,
				)
			})
		})

		It("returns the decoded bytes", func() {
			content, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(Succeed())
			Expect(content).To(Equal([]byte("VULNCHECK_IGNORE = GO-2026-1234\n")))
		})
	})

	Context("404", func() {
		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			})
		})

		It("returns (nil, nil)", func() {
			content, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(Succeed())
			Expect(content).To(BeNil())
		})
	})

	Context("403 rate limited", func() {
		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "0")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(
					w,
					`{"message": "API rate limit exceeded", "documentation_url": "https://docs.github.com"}`,
				)
			})
		})

		It("returns ErrRateLimited", func() {
			_, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeTrue())
		})
	})

	Context("500 server error", func() {
		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			})
		})

		It("returns a wrapped non-rate-limit error", func() {
			content, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(HaveOccurred())
			Expect(content).To(BeNil())
			Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeFalse())
		})
	})

	Context("oversize file", func() {
		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"type":"file","encoding":"base64","size":1048577,"content":""}`)
			})
		})

		It("errors naming the path and limit without decoding", func() {
			content, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(HaveOccurred())
			Expect(content).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Makefile"))
			Expect(err.Error()).To(ContainSubstring("1048576"))
		})
	})

	Context("request shape", func() {
		var gotPath, gotRef string

		BeforeEach(func() {
			newClient(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotRef = r.URL.Query().Get("ref")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"type":"file","encoding":"base64","size":0,"content":""}`)
			})
		})

		It("requests the path at the default branch ref", func() {
			_, err := client.GetFile(ctx, repo, "Makefile")
			Expect(err).To(Succeed())
			Expect(gotPath).To(Equal("/repos/o/r/contents/Makefile"))
			Expect(gotRef).To(Equal("main"))
		})
	})
})
