// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"strings"

	agentlib "github.com/bborbe/agent"
	taskmocks "github.com/bborbe/agent/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/mocks"
	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("monthly emission", func() {
	var (
		ghClient       *mocks.GitHubClient
		goDevClient    *mocks.GoDevClient
		publisher      *mocks.TaskPublisher
		metrics        *mocks.Metrics
		watcher        pkg.Watcher
		skipReasons    []string
		cursorPath     string
		allowlist      []string
		terminalClaims *mocks.TerminalClaims
	)

	BeforeEach(func() {
		cursorPath = GinkgoT().TempDir() + "/cursor.json"
		ghClient = &mocks.GitHubClient{}
		goDevClient = &mocks.GoDevClient{}
		publisher = &mocks.TaskPublisher{}
		metrics = &mocks.Metrics{}
		skipReasons = nil
		allowlist = []string{"github.com/bborbe/disk-status"}
		terminalClaims = &mocks.TerminalClaims{}
		terminalClaims.ClearableReturns(false, nil)

		ghClient.ListReposReturns([]pkg.Repo{
			{Owner: "bborbe", Name: "disk-status", DefaultBranch: "main"},
		}, nil)
		ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
		// The repo is already current on Go: the Go-version chain skips it with
		// go_current, so any emission observed below is the monthly pass.
		ghClient.GetGoModReturns([]byte("module x\n\ngo 1.27.2\n"), nil)
		ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
		goDevClient.LatestStableReturns(pkg.Version{
			Major: 1, Minor: 27, Patch: 2, Raw: "1.27.2",
		}, nil)
		publisher.PublishCreateReturns(true)
		publisher.PublishMonthlyReturns(true)
		metrics.IncFilterSkippedStub = func(reason string) {
			skipReasons = append(skipReasons, reason)
		}

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
			new(taskmocks.TaskCompleteCommandSender),
			metrics,
			cursorPath,
			"bborbe",
			taskFilter,
			libtime.NewCurrentDateTime(),
			terminalClaims,
			&mocks.OSVClient{},
			allowlist,
		)
	})

	It("emits once for a Go-current repo with an unchanged HEAD", func() {
		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())

		Expect(publisher.PublishMonthlyCallCount()).To(Equal(1))
		_, _, month := publisher.PublishMonthlyArgsForCall(0)
		Expect(month).To(Equal("2026-11"))
		// The Go-version chain still skipped the repo.
		Expect(publisher.PublishCreateCallCount()).To(Equal(0))
	})

	It("does not emit twice in the same month and logs month_unchanged", func() {
		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())
		Expect(publisher.PublishMonthlyCallCount()).To(Equal(1))

		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())

		Expect(publisher.PublishMonthlyCallCount()).To(Equal(1))
		Expect(skipReasons).To(ContainElement("month_unchanged"))
	})

	It("emits again for the next month", func() {
		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())
		Expect(watcher.Poll(context.Background(), false, "2026-12")).To(Succeed())

		Expect(publisher.PublishMonthlyCallCount()).To(Equal(2))
		_, _, firstMonth := publisher.PublishMonthlyArgsForCall(0)
		_, _, secondMonth := publisher.PublishMonthlyArgsForCall(1)
		Expect([]string{firstMonth, secondMonth}).To(Equal([]string{"2026-11", "2026-12"}))
	})

	It("keeps the pre-existing behaviour when no month is passed", func() {
		Expect(watcher.Poll(context.Background(), false, "")).To(Succeed())

		Expect(publisher.PublishMonthlyCallCount()).To(Equal(0))
		Expect(publisher.PublishCreateCallCount()).To(Equal(0))
	})

	It("records the month only after a successful publish", func() {
		publisher.PublishMonthlyReturns(false)
		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())

		publisher.PublishMonthlyReturns(true)
		Expect(watcher.Poll(context.Background(), false, "2026-11")).To(Succeed())

		// The failed publish recorded nothing, so the retry emitted again.
		Expect(publisher.PublishMonthlyCallCount()).To(Equal(2))
	})
})

var _ = Describe("monthly task identity", func() {
	It("is stable for the same repo and month, and differs across months", func() {
		first := pkg.DeriveMonthlyTaskID("bborbe", "disk-status", "2026-11")
		again := pkg.DeriveMonthlyTaskID("bborbe", "disk-status", "2026-11")
		next := pkg.DeriveMonthlyTaskID("bborbe", "disk-status", "2026-12")

		Expect(first).To(Equal(again))
		Expect(first).NotTo(Equal(next))
		Expect(int(first.Version())).To(Equal(5))
	})

	It("never collides with the Go-version identity", func() {
		monthly := pkg.DeriveMonthlyTaskID("bborbe", "disk-status", "2026-11")
		goVersion := pkg.DeriveTaskID("bborbe", "disk-status", "1.27.2", "d630ef3")
		Expect(monthly).NotTo(Equal(goVersion))
	})
})

var _ = Describe("monthly task builder", func() {
	var candidate pkg.Candidate

	BeforeEach(func() {
		candidate = pkg.Candidate{
			Repo:     pkg.Repo{Owner: "bborbe", Name: "disk-status"},
			HeadSHA:  "d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
			LatestGo: pkg.Version{Major: 1, Minor: 27, Patch: 2, Raw: "1.27.2"},
		}
	})

	It("carries the month in the title and the month-keyed identifier", func() {
		cmd := pkg.BuildMonthlyCreateCommand(candidate, pkg.TaskConfig{Stage: "dev"}, "2026-11")

		// The month rides in the title and the identifier, never in frontmatter:
		// the task store rejects unknown frontmatter keys (400 unknown_field).
		Expect(cmd.Frontmatter).NotTo(HaveKey("update_month"))
		Expect(cmd.Frontmatter["task_type"]).To(Equal("github-update-go"))
		Expect(string(cmd.TaskIdentifier)).To(Equal(
			pkg.DeriveMonthlyTaskID("bborbe", "disk-status", "2026-11").String(),
		))
		Expect(cmd.Title).To(Equal("Monthly Deps Update bborbe-disk-status 2026-11"))
		Expect(strings.Contains(cmd.Title, "/")).To(BeFalse())
	})

	It("is a valid CreateCommand", func() {
		cmd := pkg.BuildMonthlyCreateCommand(candidate, pkg.TaskConfig{Stage: "dev"}, "2026-11")
		Expect(cmd.Validate(context.Background())).To(Succeed())
		Expect(agentlib.TaskIdentifier(cmd.TaskIdentifier)).NotTo(BeEmpty())
	})
})
