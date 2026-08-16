// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/bborbe/maintainer/maintainerconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/mocks"
	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("watcher", func() {
	var (
		ghClient    *mocks.GitHubClient
		goDevClient *mocks.GoDevClient
		publisher   *mocks.TaskPublisher
		metrics     *mocks.Metrics
		watcher     pkg.Watcher
		cursorPath  string
		allowlist   []string
	)

	BeforeEach(func() {
		tmpDir := GinkgoT().TempDir()
		cursorPath = tmpDir + "/cursor.json"
		ghClient = &mocks.GitHubClient{}
		goDevClient = &mocks.GoDevClient{}
		publisher = &mocks.TaskPublisher{}
		metrics = &mocks.Metrics{}
		allowlist = []string{"github.com/bborbe/disk-status"}
	})

	buildWatcher := func() {
		taskFilter := filter.TaskCreationFilters{
			filter.NewRepoAllowlistFilter(allowlist),
			filter.NewGoModPresentFilter(),
			filter.NewGoModParsableFilter(),
			filter.NewGoBehindFilter(),
			filter.NewAutoUpdateFilter(),
		}
		watcher = pkg.NewWatcher(
			ghClient,
			goDevClient,
			publisher,
			metrics,
			cursorPath,
			"bborbe",
			taskFilter,
		)
	}

	Describe("Poll", func() {
		Context("AC5 consent gate", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				buildWatcher()
			})

			DescribeTable("consent matrix",
				func(cfg *maintainerconfig.MaintainerConfig, expectPublish bool) {
					ghClient.GetMaintainerConfigReturns(cfg, nil)
					publisher.PublishCreateReturns(true)
					metrics.IncFilterSkippedStub = func(string) {}

					err := watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())

					if expectPublish {
						Expect(publisher.PublishCreateCallCount()).To(Equal(1))
					} else {
						Expect(publisher.PublishCreateCallCount()).To(Equal(0))
						if cfg == nil || !cfg.GoUpdate.AutoUpdate {
							Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
							arg := metrics.IncFilterSkippedArgsForCall(0)
							Expect(arg).To(Equal("auto_update_disabled"))
						}
					}
				},
				Entry("maintainer file absent", nil, false),
				Entry("goUpdate section absent", &maintainerconfig.MaintainerConfig{}, false),
				Entry("autoUpdate key absent", &maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{},
				}, false),
				Entry("autoUpdate false", &maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: false},
				}, false),
				Entry("autoUpdate true", &maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, true),
			)
		})

		Context("AC6 version table", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				buildWatcher()
			})

			DescribeTable(
				"version scenarios",
				func(goModContent []byte, latest pkg.Version, expectPublish bool, skipReason string) {
					ghClient.GetGoModReturns(goModContent, nil)
					metrics.IncFilterSkippedStub = func(string) {}

					err := watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())

					if expectPublish {
						Expect(publisher.PublishCreateCallCount()).To(Equal(1))
					} else {
						Expect(publisher.PublishCreateCallCount()).To(Equal(0))
						if skipReason != "" {
							Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
							arg := metrics.IncFilterSkippedArgsForCall(0)
							Expect(arg).To(Equal(skipReason))
						}
					}
				},
				Entry("two-part directive behind stable",
					[]byte("go 1.24"), pkg.Version{Major: 1, Minor: 26, Patch: 6}, true, ""),
				Entry(
					"directive equal to stable",
					[]byte(
						"go 1.26.6",
					),
					pkg.Version{Major: 1, Minor: 26, Patch: 6},
					false,
					"go_current",
				),
				Entry(
					"directive ahead of stable",
					[]byte(
						"go 1.27",
					),
					pkg.Version{Major: 1, Minor: 26, Patch: 6},
					false,
					"go_current",
				),
				Entry("no go.mod",
					nil, pkg.Version{Major: 1, Minor: 26, Patch: 6}, false, "no_gomod"),
				Entry(
					"unparsable go directive",
					[]byte(
						"go banana",
					),
					pkg.Version{Major: 1, Minor: 26, Patch: 6},
					false,
					"gomod_unparsable",
				),
			)
		})

		Context("AC7 LatestStable called exactly once per cycle", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "repo1", DefaultBranch: "main"},
					{Owner: "bborbe", Name: "repo2", DefaultBranch: "main"},
					{Owner: "bborbe", Name: "repo3", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("abc123", nil)
				ghClient.GetGoModReturns([]byte("go 1.20"), nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				buildWatcher()
			})

			It("calls LatestStable exactly once", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(goDevClient.LatestStableCallCount()).To(Equal(1))
			})
		})

		Context("AC8 goDevClient error aborts before ListRepos", func() {
			BeforeEach(func() {
				goDevClient.LatestStableReturns(pkg.Version{}, errors.New("banana"))
				buildWatcher()
			})

			It("no ListRepos call", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(ghClient.ListReposCallCount()).To(Equal(0))
			})

			It("no publish", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			})

			It("IncPollCycle called with go_version_error", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(metrics.IncPollCycleCallCount()).To(Equal(1))
				arg := metrics.IncPollCycleArgsForCall(0)
				Expect(arg).To(Equal("go_version_error"))
			})
		})

		Context("AC9 rate limit preserves cursor", func() {
			BeforeEach(func() {
				cursorContent := `{"repos":{"github.com/bborbe/disk-status":` +
					`{"last_seen_head_sha":"old-sha"}}}`
				err := os.WriteFile(cursorPath, []byte(cursorContent), 0644)
				Expect(err).NotTo(HaveOccurred())

				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
			})

			DescribeTable("rate limit at each step",
				func(setupCalls func()) {
					setupCalls()
					buildWatcher()
					originalBytes, err := os.ReadFile(cursorPath)
					Expect(err).NotTo(HaveOccurred())

					err = watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())

					afterBytes, err := os.ReadFile(cursorPath)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(afterBytes)).To(Equal(string(originalBytes)))

					Expect(metrics.IncPollCycleCallCount()).To(Equal(1))
					arg := metrics.IncPollCycleArgsForCall(0)
					Expect(arg).To(Equal("rate_limited"))
				},
				Entry("GetHeadSHA", func() {
					ghClient.GetHeadSHAReturns("", pkg.ErrRateLimited)
				}),
				Entry("ListRepos", func() {
					ghClient.ListReposReturns(nil, pkg.ErrRateLimited)
				}),
				Entry("GetMaintainerConfig", func() {
					ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
					ghClient.GetGoModReturns([]byte("go 1.24"), nil)
					ghClient.GetMaintainerConfigReturns(nil, pkg.ErrRateLimited)
				}),
			)
		})

		Context("AC10 per-repo drop logs and continues", func() {
			BeforeEach(func() {
				// Use wildcard allowlist so both test repos pass the scope filter
				allowlist = []string{"github.com/bborbe/*"}
				// Two repos that both pass the allowlist filter
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
					{Owner: "bborbe", Name: "other-repo", DefaultBranch: "main"},
				}, nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				metrics.IncFilterSkippedStub = func(string) {}
			})

			It("drops first repo but processes second", func() {
				// disk-status: GetHeadSHA errors (non-rate-limit) -> dropped from cycle
				// other-repo: GetHeadSHA succeeds -> processed and published
				ghClient.GetHeadSHAStub = func(ctx context.Context, repo pkg.Repo) (string, error) {
					if repo.Name == "disk-status" {
						return "", errors.New("head sha error")
					}
					return "abc123", nil
				}
				ghClient.GetGoModStub = func(ctx context.Context, repo pkg.Repo) ([]byte, error) {
					if repo.Name == "disk-status" {
						return nil, errors.New("gomod error")
					}
					return []byte("go 1.24"), nil
				}
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				// Only other-repo was published (disk-status was dropped)
				Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			})
		})

		Context("AC11 unparsable maintainer config is not auto_update_disabled", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(nil, errors.New("parse error"))
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("no publish and no auto_update_disabled recorded", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
				// IncFilterSkipped should not have been called with auto_update_disabled
				for i := 0; i < metrics.IncFilterSkippedCallCount(); i++ {
					arg := metrics.IncFilterSkippedArgsForCall(i)
					Expect(arg).NotTo(Equal("auto_update_disabled"))
				}
			})
		})

		Context("AC12 cursor records HEAD and skips on re-run", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("first poll publishes and records cursor", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(1))

				content, err := os.ReadFile(cursorPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(content)).To(ContainSubstring(
					"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
				))
			})

			It("second poll with same HEAD skips (within same watcher)", func() {
				// First poll: publishes and records cursor
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(1))

				// Second poll with same HEAD: skips because cursor recorded this SHA
				err = watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(1))
				// Second call to PublishCreate was skipped, count stayed at 1
			})

			It("no temp files in cursor dir", func() {
				_ = watcher.Poll(context.Background(), false)
				// cursorPath is a file; read the parent directory for temp files
				dir := strings.TrimSuffix(cursorPath, "/cursor.json")
				entries, err := os.ReadDir(dir)
				Expect(err).NotTo(HaveOccurred())
				for _, e := range entries {
					Expect(e.Name()).NotTo(ContainSubstring(".tmp"))
				}
			})
		})

		Context("forced cycle bypasses SHAUnchangedFilter", func() {
			BeforeEach(func() {
				cursorContent := `{"repos":{"github.com/bborbe/disk-status":` +
					`{"last_seen_head_sha":"d630ef3526cfc57fbdccd9ba53c5c3a02945e407"}}}`
				err := os.WriteFile(cursorPath, []byte(cursorContent), 0644)
				Expect(err).NotTo(HaveOccurred())

				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("force=true publishes", func() {
				err := watcher.Poll(context.Background(), true)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			})

			It("force=false does not publish", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			})

			It("force=true still respects consent gate", func() {
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: false},
				}, nil)

				err := watcher.Poll(context.Background(), true)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
				Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
				arg := metrics.IncFilterSkippedArgsForCall(0)
				Expect(arg).To(Equal("auto_update_disabled"))
			})
		})

		Context("corrupt cursor returns error", func() {
			BeforeEach(func() {
				err := os.WriteFile(cursorPath, []byte("not json"), 0644)
				Expect(err).NotTo(HaveOccurred())

				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				buildWatcher()
			})

			It("returns error", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).To(HaveOccurred())
			})

			It("no publish", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			})

			It("no poll cycle metric", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(metrics.IncPollCycleCallCount()).To(Equal(0))
			})
		})

		Context("publish failure still ends with success", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(false)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("cursor not updated", func() {
				_ = watcher.Poll(context.Background(), false)
				content, err := os.ReadFile(cursorPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(
					string(content),
				).NotTo(ContainSubstring("d630ef3526cfc57fbdccd9ba53c5c3a02945e407"))
			})

			It("IncPollCycle success", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(metrics.IncPollCycleCallCount()).To(Equal(1))
				arg := metrics.IncPollCycleArgsForCall(0)
				Expect(arg).To(Equal("success"))
			})
		})

		Context("cancellation mid-cycle", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "repo1", DefaultBranch: "main"},
					{Owner: "bborbe", Name: "repo2", DefaultBranch: "main"},
					{Owner: "bborbe", Name: "repo3", DefaultBranch: "main"},
				}, nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
			})

			It("cancels after first repo processed", func() {
				canceled := false
				ghClient.GetHeadSHAStub = func(ctx context.Context, repo pkg.Repo) (string, error) {
					if canceled {
						return "", context.Canceled
					}
					if repo.Name == "repo1" {
						canceled = true
						return "abc123", nil
					}
					return "", context.Canceled
				}
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				publisher.PublishCreateReturns(true)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()

				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				_ = watcher.Poll(ctx, false)
				// First repo was processed (publisher was called once for repo1)
				Expect(publisher.PublishCreateCallCount()).To(BeNumerically("<", 3))
			})
		})

		Context("metric label containment", func() {
			var pollCycleArgs []string
			var filterSkipArgs []string

			BeforeEach(func() {
				pollCycleArgs = nil
				filterSkipArgs = nil
				metrics.IncPollCycleStub = func(s string) { pollCycleArgs = append(pollCycleArgs, s) }
				metrics.IncFilterSkippedStub = func(s string) { filterSkipArgs = append(filterSkipArgs, s) }
			})

			It("IncPollCycle labels are in PollCycleResults", func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				buildWatcher()

				_ = watcher.Poll(context.Background(), false)

				validLabels := map[string]bool{
					"success":          true,
					"rate_limited":     true,
					"github_error":     true,
					"go_version_error": true,
				}
				for _, arg := range pollCycleArgs {
					Expect(validLabels[arg]).To(BeTrue(),
						"IncPollCycle(%q) not in PollCycleResults", arg)
				}
			})

			It("IncFilterSkipped labels are in FilterSkipReasons", func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns(nil, nil) // no go.mod -> no_gomod
				ghClient.GetMaintainerConfigReturns(&maintainerconfig.MaintainerConfig{
					GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true},
				}, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				buildWatcher()

				_ = watcher.Poll(context.Background(), false)

				validLabels := map[string]bool{
					"scope":                true,
					"no_gomod":             true,
					"gomod_unparsable":     true,
					"go_current":           true,
					"auto_update_disabled": true,
					"sha_unchanged":        true,
				}
				for _, arg := range filterSkipArgs {
					Expect(validLabels[arg]).To(BeTrue(),
						"IncFilterSkipped(%q) not in FilterSkipReasons", arg)
				}
			})
		})
	})
})
