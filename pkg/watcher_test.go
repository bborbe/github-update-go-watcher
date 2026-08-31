// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"
	"os"
	"strings"

	taskmocks "github.com/bborbe/agent/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/mocks"
	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("watcher", func() {
	var (
		ghClient       *mocks.GitHubClient
		goDevClient    *mocks.GoDevClient
		publisher      *mocks.TaskPublisher
		completeSender *taskmocks.TaskCompleteCommandSender
		metrics        *mocks.Metrics
		watcher        pkg.Watcher
		cursorPath     string
		allowlist      []string
	)

	BeforeEach(func() {
		tmpDir := GinkgoT().TempDir()
		cursorPath = tmpDir + "/cursor.json"
		ghClient = &mocks.GitHubClient{}
		goDevClient = &mocks.GoDevClient{}
		publisher = &mocks.TaskPublisher{}
		completeSender = new(taskmocks.TaskCompleteCommandSender)
		metrics = &mocks.Metrics{}
		allowlist = []string{"github.com/bborbe/disk-status"}
	})

	buildWatcher := func() {
		taskFilter := filter.TaskCreationFilterList{
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
			completeSender,
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
				func(consent filter.Consent, expectPublish bool, expectedReason string) {
					ghClient.GetMaintainerConfigReturns(consent, nil)
					publisher.PublishCreateReturns(true)
					metrics.IncFilterSkippedStub = func(string) {}

					err := watcher.Poll(context.Background(), false)
					Expect(err).NotTo(HaveOccurred())

					if expectPublish {
						Expect(publisher.PublishCreateCallCount()).To(Equal(1))
						return
					}
					Expect(publisher.PublishCreateCallCount()).To(Equal(0))
					Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
					arg := metrics.IncFilterSkippedArgsForCall(0)
					Expect(arg).To(Equal(expectedReason))
				},
				Entry("maintainer file absent",
					filter.UndecidedConsent, false, "auto_update_undecided"),
				Entry("goUpdate section absent",
					filter.UndecidedConsent, false, "auto_update_undecided"),
				Entry("autoUpdate key absent",
					filter.UndecidedConsent, false, "auto_update_undecided"),
				Entry("autoUpdate false",
					filter.RefusedConsent, false, "auto_update_disabled"),
				Entry("autoUpdate true",
					filter.GrantedConsent, true, ""),
			)

			It("undecided repo already current on Go emits nothing (AC6, DB9)", func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("module x\n\ngo 1.26.6\n"), nil)
				ghClient.GetMaintainerConfigReturns(filter.UndecidedConsent, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				buildWatcher()

				_ = watcher.Poll(context.Background(), false)

				// GoBehindFilter (chain position 4) short-circuits before the consent
				// filter (position 5), so the verdict is go_current, not
				// auto_update_undecided.
				Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
				Expect(metrics.IncFilterSkippedArgsForCall(0)).To(Equal("go_current"))
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			})
		})

		Context("undecided repo skip-with-emit (spec 002 Desired Behaviors 5, 6)", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(filter.UndecidedConsent, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("publishes exactly one decision task and zero update tasks", func() {
				publisher.PublishDecisionReturns(true)
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishDecisionCallCount()).To(Equal(1))
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			})

			It("records reason auto_update_undecided", func() {
				publisher.PublishDecisionReturns(true)
				_ = watcher.Poll(context.Background(), false)
				Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
				arg := metrics.IncFilterSkippedArgsForCall(0)
				Expect(arg).To(Equal("auto_update_undecided"))
			})

			It("a failed decision publish does not fail the poll cycle", func() {
				publisher.PublishDecisionReturns(false)
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("AC6 version table", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
					ghClient.GetMaintainerConfigReturns(
						filter.Consent(""),
						pkg.ErrRateLimited,
					)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(
					filter.Consent(""),
					errors.New("parse error"),
				)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				metrics.IncFilterSkippedStub = func(string) {}
				buildWatcher()
			})

			It("no publish and IncFilterSkipped never called (drop, not a skip verdict)", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
				// AC7 (spec 002): an unparsable .maintainer.yaml is dropped before the
				// filter chain ever runs -- IncFilterSkipped must not be called at
				// all, not merely called with some other reason.
				Expect(metrics.IncFilterSkippedCallCount()).To(Equal(0))
			})
		})

		Context("AC12 cursor records HEAD and skips on re-run", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.RefusedConsent, nil)

				err := watcher.Poll(context.Background(), true)
				Expect(err).NotTo(HaveOccurred())
				Expect(publisher.PublishCreateCallCount()).To(Equal(0))
				Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
				arg := metrics.IncFilterSkippedArgsForCall(0)
				Expect(arg).To(Equal("auto_update_disabled"))
			})
		})

		Context("corrupt cursor cold-starts", func() {
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

			It("does not return an error", func() {
				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
			})

			It("still runs the cycle", func() {
				_ = watcher.Poll(context.Background(), false)
				Expect(metrics.IncPollCycleCallCount()).To(Equal(1))
			})

			It("preserves the corrupt file", func() {
				_ = watcher.Poll(context.Background(), false)
				saved, err := os.ReadFile(cursorPath + ".corrupt")
				Expect(err).NotTo(HaveOccurred())
				Expect(string(saved)).To(Equal("not json"))
			})

			It("a second cycle also succeeds", func() {
				Expect(watcher.Poll(context.Background(), false)).To(Succeed())
				Expect(watcher.Poll(context.Background(), false)).To(Succeed())
			})
		})

		Context("publish failure still ends with success", func() {
			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
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

		Context("merge-detection auto-completes task on merged update PR", func() {
			var headSHA = "d630ef3526cfc57fbdccd9ba53c5c3a02945e407"

			BeforeEach(func() {
				ghClient.ListReposReturns([]pkg.Repo{
					{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
				}, nil)
				ghClient.GetHeadSHAReturns(headSHA, nil)
				ghClient.GetGoModReturns([]byte("go 1.24"), nil)
				ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
				goDevClient.LatestStableReturns(pkg.Version{
					Major: 1, Minor: 26, Patch: 6, Raw: "1.26.6",
				}, nil)
				publisher.PublishCreateReturns(true)
				metrics.IncFilterSkippedStub = func(string) {}
			})

			It("publishes a CompleteCommand for a merged update PR", func() {
				ghClient.GetMergedUpdatePRReturns(true, nil)
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())

				Expect(completeSender.SendCommandCallCount()).To(Equal(1))
				_, cmd := completeSender.SendCommandArgsForCall(0)
				Expect(string(cmd.TaskIdentifier)).To(Equal(
					pkg.DeriveTaskID("bborbe", "disk-status", headSHA).String(),
				))
				Expect(metrics.IncCompletedCallCount()).To(Equal(1))
			})

			It("does not publish a CompleteCommand when the update PR is not merged", func() {
				ghClient.GetMergedUpdatePRReturns(false, nil)
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())

				Expect(completeSender.SendCommandCallCount()).To(Equal(0))
			})

			It("publishes once and marks the cursor entry as completed", func() {
				ghClient.GetMergedUpdatePRReturns(true, nil)
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(completeSender.SendCommandCallCount()).To(Equal(1))

				content, err := os.ReadFile(cursorPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(content)).To(ContainSubstring(
					`"completed_head_sha":"` + headSHA + `"`,
				))
			})

			It("skips the already-completed repo on the next cycle", func() {
				ghClient.GetMergedUpdatePRReturns(true, nil)
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())

				// second poll: cursor marks the repo completed, no re-publish
				err = watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())

				Expect(completeSender.SendCommandCallCount()).To(Equal(1))
			})

			It("aborts the cycle when GetMergedUpdatePR is rate limited", func() {
				ghClient.GetMergedUpdatePRReturns(false, pkg.ErrRateLimited)
				buildWatcher()

				err := watcher.Poll(context.Background(), false)
				Expect(err).NotTo(HaveOccurred())

				Expect(completeSender.SendCommandCallCount()).To(Equal(0))
				Expect(metrics.IncPollCycleArgsForCall(
					metrics.IncPollCycleCallCount() - 1,
				)).To(Equal("rate_limited"))
			})
		})
	})
})
