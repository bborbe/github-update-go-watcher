// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("GoDevClient", func() {
	var ctx context.Context
	var cancel context.CancelFunc
	var server *httptest.Server
	var httpClient *http.Client
	var client pkg.GoDevClient

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// default: overridden per test
		}))
		httpClient = server.Client()
		client = pkg.NewGoDevClient(httpClient, server.URL)
	})

	AfterEach(func() {
		cancel()
		server.Close()
	})

	Describe("LatestStable", func() {
		It("returns the maximum stable version from the JSON list", func() {
			releases := []pkg.GoDevRelease{
				{Version: "go1.26.6", Stable: true},
				{Version: "go1.27rc1", Stable: false},
			}
			body, err := json.Marshal(releases)
			Expect(err).NotTo(HaveOccurred())
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			})

			v, err := client.LatestStable(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Major).To(Equal(1))
			Expect(v.Minor).To(Equal(26))
			Expect(v.Patch).To(Equal(6))
		})

		It("returns higher stable version when later entry is greater", func() {
			releases := []pkg.GoDevRelease{
				{Version: "go1.26.6", Stable: true},
				{Version: "go1.27.0", Stable: true},
			}
			body, err := json.Marshal(releases)
			Expect(err).NotTo(HaveOccurred())
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			})

			v, err := client.LatestStable(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Major).To(Equal(1))
			Expect(v.Minor).To(Equal(27))
			Expect(v.Patch).To(Equal(0))
		})

		It("returns error when all entries are unparseable", func() {
			releases := []pkg.GoDevRelease{
				{Version: "banana", Stable: true},
			}
			body, err := json.Marshal(releases)
			Expect(err).NotTo(HaveOccurred())
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			})

			_, err = client.LatestStable(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no stable go version"))
		})

		It("returns error on non-200 status", func() {
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			})

			_, err := client.LatestStable(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("500"))
		})

		It("returns error on non-JSON body", func() {
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not json"))
			})

			_, err := client.LatestStable(ctx)
			Expect(err).To(HaveOccurred())
		})

		It("returns error when context is cancelled", func() {
			cancel()
			_, err := client.LatestStable(ctx)
			Expect(err).To(HaveOccurred())
		})
	})
})
