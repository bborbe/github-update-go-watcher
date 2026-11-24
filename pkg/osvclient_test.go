// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	libtime "github.com/bborbe/time"
	libtimetest "github.com/bborbe/time/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

const osvIndexPath = "/index/vulns.json"

func osvFixture(name string) []byte {
	data, err := os.ReadFile("testdata/osv/" + name)
	Expect(err).NotTo(HaveOccurred())
	return data
}

var _ = Describe("OSVClient", func() {
	var ctx context.Context
	var cancel context.CancelFunc
	var server *httptest.Server
	var clock libtime.CurrentDateTime
	var client pkg.OSVClient
	var mu sync.Mutex
	var requests []string
	var authHeaders []string

	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.URL.Path)
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
	}
	requestPaths := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
	countPath := func(path string) int {
		count := 0
		for _, p := range requestPaths() {
			if p == path {
				count++
			}
		}
		return count
	}
	setHandler := func(handler http.HandlerFunc) {
		server.Config.Handler = handler
	}
	respond := func(w http.ResponseWriter, status int, body []byte) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtimetest.ParseDateTime("2026-11-24T00:00:00Z"))
		mu = sync.Mutex{}
		requests = nil
		authHeaders = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			http.NotFound(w, r)
		}))
		client = pkg.NewOSVClient(
			server.Client(),
			server.URL+"/v1/vulns",
			server.URL+osvIndexPath,
			clock,
		)
	})

	AfterEach(func() {
		cancel()
		server.Close()
	})

	It("returns withdrawn when the record carries a withdrawn timestamp", func() {
		body := osvFixture("GO-2026-4923.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, body)
		})

		verdict, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictWithdrawn))
	})

	It("returns legitimate when the record has no withdrawn key", func() {
		body := []byte(`{"id":"GO-2026-4923","aliases":["CVE-2026-33817"]}`)
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, body)
		})

		verdict, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictLegitimate))
	})

	It("resolves a merged alias 404 through the Go index", func() {
		withdrawn := osvFixture("GO-2026-4923.json")
		notFound := osvFixture("CVE-2026-33817-404.json")
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			switch r.URL.Path {
			case "/v1/vulns/CVE-2026-33817":
				respond(w, http.StatusNotFound, notFound)
			case osvIndexPath:
				respond(w, http.StatusOK, index)
			case "/v1/vulns/GO-2026-4923":
				respond(w, http.StatusOK, withdrawn)
			default:
				http.NotFound(w, r)
			}
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictWithdrawn))

		paths := requestPaths()
		Expect(paths).To(ContainElement("/v1/vulns/CVE-2026-33817"))
		Expect(paths).To(ContainElement("/v1/vulns/GO-2026-4923"))
		cvePos, goPos := -1, -1
		for i, p := range paths {
			switch p {
			case "/v1/vulns/CVE-2026-33817":
				cvePos = i
			case "/v1/vulns/GO-2026-4923":
				goPos = i
			}
		}
		Expect(cvePos).To(BeNumerically("<", goPos))
	})

	It("returns withdrawn when any aliased record is withdrawn, not just the first", func() {
		index := []byte(`[
			{"id": "GO-2026-5000", "modified": "2026-01-01T00:00:00Z", "aliases": ["CVE-2026-33817"]},
			{"id": "GO-2026-4923", "modified": "2026-04-08T13:33:56Z", "aliases": ["CVE-2026-33817"]}
		]`)
		withdrawn := osvFixture("GO-2026-4923.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			switch r.URL.Path {
			case "/v1/vulns/CVE-2026-33817":
				respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
			case osvIndexPath:
				respond(w, http.StatusOK, index)
			case "/v1/vulns/GO-2026-5000":
				respond(w, http.StatusOK, []byte(`{"id":"GO-2026-5000"}`))
			case "/v1/vulns/GO-2026-4923":
				respond(w, http.StatusOK, withdrawn)
			default:
				http.NotFound(w, r)
			}
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictWithdrawn))
	})

	It("returns unresolved with nil error when the 404 alias matches no index entry", func() {
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			switch r.URL.Path {
			case osvIndexPath:
				respond(w, http.StatusOK, index)
			default:
				respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
			}
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-9999-0000")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with nil error when aliased records carry no withdrawal", func() {
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			switch r.URL.Path {
			case osvIndexPath:
				respond(w, http.StatusOK, index)
			case "/v1/vulns/GO-2026-4923":
				respond(w, http.StatusOK, []byte(`{"id":"GO-2026-4923"}`))
			default:
				respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
			}
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error when the index fetch fails", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			if r.URL.Path == osvIndexPath {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error when the index is not JSON", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			if r.URL.Path == osvIndexPath {
				respond(w, http.StatusOK, []byte("not json"))
				return
			}
			respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error when an aliased record fetch fails", func() {
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			switch r.URL.Path {
			case osvIndexPath:
				respond(w, http.StatusOK, index)
			case "/v1/vulns/GO-2026-4923":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
			}
		})

		verdict, err := client.CheckWithdrawn(ctx, "CVE-2026-33817")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error on a 500", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			w.WriteHeader(http.StatusInternalServerError)
		})

		verdict, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error on a transport failure", func() {
		dead := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		deadURL := dead.URL
		dead.Close()
		deadClient := pkg.NewOSVClient(
			dead.Client(),
			deadURL+"/v1/vulns",
			deadURL+osvIndexPath,
			clock,
		)

		verdict, err := deadClient.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error on a non-JSON 200 body", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, []byte("not json"))
		})

		verdict, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).To(HaveOccurred())
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("returns unresolved with an error when the body exceeds the 8 MiB cap", func() {
		body := []byte(strings.Repeat("a", (8<<20)+1))
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, body)
		})

		verdict, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeds limit"))
		Expect(verdict).To(Equal(pkg.OSVVerdictUnresolved))
	})

	It("issues zero index requests when every ID answers 200", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, []byte(`{"id":"GO-2026-4923"}`))
		})

		for _, id := range []string{"GO-2026-4923", "GO-2026-4924"} {
			_, err := client.CheckWithdrawn(ctx, id)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(countPath(osvIndexPath)).To(Equal(0))
	})

	It("fetches the index exactly once for two 404 IDs", func() {
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			if r.URL.Path == osvIndexPath {
				respond(w, http.StatusOK, index)
				return
			}
			respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
		})

		for _, id := range []string{"CVE-9999-0001", "CVE-9999-0002"} {
			_, err := client.CheckWithdrawn(ctx, id)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(countPath(osvIndexPath)).To(Equal(1))
	})

	It("refetches the index after the 24h TTL but not before", func() {
		index := osvFixture("index.json")
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			if r.URL.Path == osvIndexPath {
				respond(w, http.StatusOK, index)
				return
			}
			respond(w, http.StatusNotFound, osvFixture("CVE-2026-33817-404.json"))
		})

		_, err := client.CheckWithdrawn(ctx, "CVE-9999-0001")
		Expect(err).NotTo(HaveOccurred())
		Expect(countPath(osvIndexPath)).To(Equal(1))

		clock.SetNow(libtimetest.ParseDateTime("2026-11-24T23:00:00Z"))
		_, err = client.CheckWithdrawn(ctx, "CVE-9999-0002")
		Expect(err).NotTo(HaveOccurred())
		Expect(countPath(osvIndexPath)).To(Equal(1))

		clock.SetNow(libtimetest.ParseDateTime("2026-11-25T01:00:00Z"))
		_, err = client.CheckWithdrawn(ctx, "CVE-9999-0003")
		Expect(err).NotTo(HaveOccurred())
		Expect(countPath(osvIndexPath)).To(Equal(2))
	})

	It("sends no Authorization header", func() {
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			record(r)
			respond(w, http.StatusOK, []byte(`{"id":"GO-2026-4923"}`))
		})

		_, err := client.CheckWithdrawn(ctx, "GO-2026-4923")
		Expect(err).NotTo(HaveOccurred())

		mu.Lock()
		defer mu.Unlock()
		Expect(authHeaders).NotTo(BeEmpty())
		for _, header := range authHeaders {
			Expect(header).To(BeEmpty())
		}
	})
})
