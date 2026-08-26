// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("GitHubClient", func() {
	var (
		server *httptest.Server
		client pkg.GitHubClient
		ctx    context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("ListRepos", func() {
		Context("two pages of repos", func() {
			var handler http.HandlerFunc

			BeforeEach(func() {
				requestCount := 0
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCount++
					if r.URL.Path != "/installation/repositories" {
						http.NotFound(w, r)
						return
					}
					page := r.URL.Query().Get("page")
					switch page {
					case "1":
						w.Header().Set("Content-Type", "application/json")
						w.Header().
							Set("Link", fmt.Sprintf(`<%s/installation/repositories?page=2>; rel="next"`, server.URL))
						fmt.Fprint(w, `{
							"repositories": [
								{"name": "repo1", "owner": {"login": "myowner"}, "default_branch": "main", "private": true, "archived": false},
								{"name": "repo2", "owner": {"login": "myowner"}, "default_branch": "master", "private": false, "archived": false}
							]
						}`)
					case "2":
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{
							"repositories": [
								{"name": "repo3", "owner": {"login": "myowner"}, "default_branch": "main", "private": true, "archived": false}
							]
						}`)
					default:
						http.NotFound(w, r)
					}
				})
			})

			BeforeEach(func() {
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns the union of both pages", func() {
				repos, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(Succeed())
				Expect(repos).To(HaveLen(3))
				Expect(repos[0].Name).To(Equal("repo1"))
				Expect(repos[1].Name).To(Equal("repo2"))
				Expect(repos[2].Name).To(Equal("repo3"))
			})
		})

		Context("drops archived and foreign-owner repos", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{
						"repositories": [
							{"name": "valid", "owner": {"login": "myowner"}, "default_branch": "main", "private": false, "archived": false},
							{"name": "archived", "owner": {"login": "myowner"}, "default_branch": "main", "private": false, "archived": true},
							{"name": "foreign", "owner": {"login": "otherowner"}, "default_branch": "main", "private": false, "archived": false}
						]
					}`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("drops archived and foreign-owned repos", func() {
				repos, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(Succeed())
				Expect(repos).To(HaveLen(1))
				Expect(repos[0].Name).To(Equal("valid"))
			})
		})

		Context("rate-limited response", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", "0")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(
						w,
						`{"message": "API rate limit exceeded", "documentation_url": "https://docs.github.com"}`,
					)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns ErrRateLimited", func() {
				_, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeTrue())
			})
		})

		Context("500 server error", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns a non-rate-limit error", func() {
				_, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeFalse())
			})
		})

		Context("cancelled context", func() {
			var cancel context.CancelFunc

			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
				if cancel != nil {
					cancel()
				}
			})

			It("returns context error", func() {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				_, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("context cancelled"))
			})
		})

		Context("unbounded pagination loop", func() {
			var requestCount int

			BeforeEach(func() {
				requestCount = 0
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCount++
					w.Header().Set("Content-Type", "application/json")
					page := 1
					if p := r.URL.Query().Get("page"); p != "" {
						page, _ = strconv.Atoi(p)
					}
					w.Header().
						Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, server.URL, page+1))
					fmt.Fprintf(
						w,
						`{"repositories": [{"name": "repo%d", "owner": {"login": "myowner"}, "default_branch": "main", "private": false, "archived": false}]}`,
						page,
					)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns error mentioning exceeded pages and stops after maxListPages", func() {
				_, err := client.ListRepos(ctx, "myowner")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("exceeded 100 pages"))
				// Should have made at most maxListPages requests
				Expect(requestCount).To(BeNumerically("<=", 100))
			})
		})
	})

	Describe("GetHeadSHA", func() {
		Context("empty DefaultBranch", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.NotFound(w, r)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns error without issuing a request", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: ""}
				_, err := client.GetHeadSHA(ctx, repo)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("empty DefaultBranch"))
			})
		})

		Context("valid branch", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{
						"name": "main",
						"commit": {"sha": "abc123def456"}
					}`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns the full SHA", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				sha, err := client.GetHeadSHA(ctx, repo)
				Expect(err).To(Succeed())
				Expect(sha).To(Equal("abc123def456"))
			})
		})
	})

	Describe("GetGoMod", func() {
		Context("404 not found", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns nil nil", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				data, err := client.GetGoMod(ctx, repo)
				Expect(err).To(Succeed())
				Expect(data).To(BeNil())
			})
		})

		Context("valid base64 encoded go.mod", func() {
			BeforeEach(func() {
				encoded := base64.StdEncoding.EncodeToString(
					[]byte("module github.com/example\n\ngo 1.21"),
				)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					resp := map[string]interface{}{
						"type":     "file",
						"encoding": "base64",
						"size":     len(encoded),
						"content":  encoded,
					}
					json.NewEncoder(w).Encode(resp)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns decoded bytes", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				data, err := client.GetGoMod(ctx, repo)
				Expect(err).To(Succeed())
				Expect(string(data)).To(ContainSubstring("module github.com/example"))
			})
		})

		Context("oversize content per API metadata", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					resp := map[string]interface{}{
						"type":     "file",
						"encoding": "base64",
						"size":     2 * 1024 * 1024, // 2MB > 1MB limit
						"content":  base64.StdEncoding.EncodeToString([]byte("large content")),
					}
					json.NewEncoder(w).Encode(resp)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns error mentioning size without decoding", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				_, err := client.GetGoMod(ctx, repo)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("too large"))
				// Error should mention the API-reported size, not decoded size
				Expect(err.Error()).To(ContainSubstring("2097152"))
			})
		})
	})

	Describe("GetMaintainerConfig", func() {
		Context("404 not found", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns zero config and nil error", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				cfg, err := client.GetMaintainerConfig(ctx, repo)
				Expect(err).To(Succeed())
				Expect(cfg.GoUpdate.AutoUpdate).To(BeFalse())
			})
		})

		Context("valid config with autoUpdate true", func() {
			BeforeEach(func() {
				encoded := base64.StdEncoding.EncodeToString(
					[]byte("goUpdate:\n  autoUpdate: true"),
				)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					resp := map[string]interface{}{
						"type":     "file",
						"encoding": "base64",
						"size":     len(encoded),
						"content":  encoded,
					}
					json.NewEncoder(w).Encode(resp)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns config with autoUpdate true", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				cfg, err := client.GetMaintainerConfig(ctx, repo)
				Expect(err).To(Succeed())
				Expect(cfg.GoUpdate.AutoUpdate).To(BeTrue())
			})
		})

		Context("empty document (no goUpdate block)", func() {
			BeforeEach(func() {
				encoded := base64.StdEncoding.EncodeToString([]byte(""))
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					resp := map[string]interface{}{
						"type":     "file",
						"encoding": "base64",
						"size":     len(encoded),
						"content":  encoded,
					}
					json.NewEncoder(w).Encode(resp)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns autoUpdate false and nil error", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				cfg, err := client.GetMaintainerConfig(ctx, repo)
				Expect(err).To(Succeed())
				Expect(cfg.GoUpdate.AutoUpdate).To(BeFalse())
			})
		})

		Context("malformed YAML", func() {
			BeforeEach(func() {
				encoded := base64.StdEncoding.EncodeToString([]byte("goUpdate:\n  autoUpdate: ["))
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					resp := map[string]interface{}{
						"type":     "file",
						"encoding": "base64",
						"size":     len(encoded),
						"content":  encoded,
					}
					json.NewEncoder(w).Encode(resp)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns zero config and non-nil error", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				cfg, err := client.GetMaintainerConfig(ctx, repo)
				Expect(err).To(HaveOccurred())
				Expect(cfg.GoUpdate.AutoUpdate).To(BeFalse())
				// Malformed must NOT read as opted-in
			})
		})

		Context("rate-limited response", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", "0")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"message": "API rate limit exceeded"}`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns ErrRateLimited", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				_, err := client.GetMaintainerConfig(ctx, repo)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeTrue())
			})
		})
	})

	Describe("GetMergedUpdatePR", func() {
		Context("merged update PR exists", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/repos/myowner/myrepo/pulls" {
						http.NotFound(w, r)
						return
					}
					if r.URL.Query().Get("state") != "all" {
						http.Error(w, "expected state=all", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `[
						{"number": 1, "state": "merged", "merged": true, "head": {"ref": "fix/update-go-d630ef3"}}
					]`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns true", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				merged, err := client.GetMergedUpdatePR(
					ctx,
					repo,
					"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
				)
				Expect(err).To(Succeed())
				Expect(merged).To(BeTrue())
			})
		})

		Context("only open update PR", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `[
						{"number": 1, "state": "open", "merged": false, "head": {"ref": "fix/update-go-d630ef3"}}
					]`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns false", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				merged, err := client.GetMergedUpdatePR(
					ctx,
					repo,
					"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
				)
				Expect(err).To(Succeed())
				Expect(merged).To(BeFalse())
			})
		})

		Context("no matching PRs", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `[]`)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns false", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				merged, err := client.GetMergedUpdatePR(
					ctx,
					repo,
					"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
				)
				Expect(err).To(Succeed())
				Expect(merged).To(BeFalse())
			})
		})

		Context("rate limited", func() {
			BeforeEach(func() {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", "0")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(
						w,
						`{"message": "API rate limit exceeded", "documentation_url": "https://docs.github.com"}`,
					)
				})
				server = httptest.NewServer(handler)
				client = pkg.NewGitHubClient(server.Client())
				err := pkg.SetBaseURL(client, server.URL+"/")
				Expect(err).To(Succeed())
			})

			AfterEach(func() {
				server.Close()
			})

			It("returns ErrRateLimited", func() {
				repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
				_, err := client.GetMergedUpdatePR(
					ctx,
					repo,
					"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
				)
				Expect(errors.Is(err, pkg.ErrRateLimited)).To(BeTrue())
			})
		})
	})
})
