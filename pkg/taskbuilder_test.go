// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("taskbuilder", func() {
	var (
		ctx        context.Context
		candidate  pkg.Candidate
		cfg        pkg.TaskConfig
		goldenBody string
	)

	BeforeEach(func() {
		ctx = context.Background()
		candidate = pkg.Candidate{
			Repo: pkg.Repo{
				Owner:         "bborbe",
				Name:          "disk-status",
				DefaultBranch: "main",
			},
			HeadSHA:       "d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
			GoModPresent:  true,
			GoModParsable: true,
			CurrentGo: pkg.Version{
				Major: 1,
				Minor: 24,
				Patch: 0,
				Raw:   "1.24",
			},
			LatestGo: pkg.Version{
				Major: 1,
				Minor: 26,
				Patch: 6,
				Raw:   "1.26.6",
			},
			Consent: filter.GrantedConsent,
		}
		cfg = pkg.TaskConfig{Stage: "dev"}
		goldenBody = "# Update Go: bborbe/disk-status\n\n" +
			"**Current Go:** 1.24.0  ·  **Latest Go:** 1.26.6\n" +
			"**HEAD:** d630ef3\n" +
			"**Repo:** [bborbe/disk-status](https://github.com/bborbe/disk-status)\n"
	})

	Describe("ComputeTaskTitle", func() {
		It("returns dash form", func() {
			title := pkg.ComputeTaskTitle(candidate)
			Expect(title).To(Equal("Update Go bborbe-disk-status d630ef3"))
		})
	})

	Describe("BuildCreateCommand", func() {
		var cmd interface {
			Validate(ctx context.Context) error
		}

		JustBeforeEach(func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			cmd = &realCmd
		})

		It("returns nil from Validate", func() {
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("Title matches dash form", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Title).To(Equal("Update Go bborbe-disk-status d630ef3"))
		})

		It("Title is stored in frontmatter", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["title"]).To(Equal(realCmd.Title))
		})

		It("has exactly 12 frontmatter keys", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(len(realCmd.Frontmatter)).To(Equal(12))
		})

		It("task_type is github-update-go", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["task_type"]).To(Equal("github-update-go"))
		})

		It("assignee is github-update-go-agent", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["assignee"]).To(Equal("github-update-go-agent"))
		})

		It("phase is planning", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["phase"]).To(Equal("planning"))
		})

		It("status is in_progress", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["status"]).To(Equal("in_progress"))
		})

		It("stage matches config", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["stage"]).To(Equal("dev"))
		})

		It("omits update_scope when not configured (agent defaults to both)", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			_, present := realCmd.Frontmatter["update_scope"]
			Expect(present).To(BeFalse())
		})

		It("emits update_scope when configured", func() {
			cfg.UpdateScope = "golang"
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["update_scope"]).To(Equal("golang"))
		})

		It("task_identifier is derived from owner/repo/HEAD", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			expected := pkg.DeriveTaskID(
				"bborbe",
				"disk-status",
				"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
			).String()
			Expect(string(realCmd.TaskIdentifier)).To(Equal(expected))
		})

		It("repo is short form", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["repo"]).To(Equal("bborbe/disk-status"))
		})

		It("clone_url is git@github.com form", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["clone_url"]).To(Equal(
				"git@github.com:bborbe/disk-status.git",
			))
		})

		It("ref is full HEAD SHA", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["ref"]).To(Equal(
				"d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
			))
		})

		It("current_go is normalised three-part", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["current_go"]).To(Equal("1.24.0"))
		})

		It("latest_go is three-part", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Frontmatter["latest_go"]).To(Equal("1.26.6"))
		})

		It("Body matches golden with middot separator", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Body).To(Equal(goldenBody))
		})

		It("Body contains no raw go.mod tokens", func() {
			realCmd := pkg.BuildCreateCommand(candidate, cfg)
			Expect(realCmd.Body).NotTo(ContainSubstring("module "))
			Expect(realCmd.Body).NotTo(ContainSubstring("require"))
		})
	})

	Describe("ComputeDecisionTaskTitle", func() {
		It("returns the frozen decision-task title form", func() {
			title := pkg.ComputeDecisionTaskTitle(candidate)
			Expect(title).To(Equal("Go Update Decision bborbe-disk-status"))
		})
	})

	Describe("BuildDecisionCommand", func() {
		It("returns nil from Validate", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("sets task_type to github-update-go-decision", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Frontmatter["task_type"]).To(Equal("github-update-go-decision"))
		})

		It("sets assignee to bborbe, never the update agent", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Frontmatter["assignee"]).To(Equal("bborbe"))
			Expect(cmd.Frontmatter["assignee"]).NotTo(Equal("github-update-go-agent"))
		})

		It("has exactly 10 frontmatter keys", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(len(cmd.Frontmatter)).To(Equal(10))
		})

		It("derives task_identifier from owner/repo only, independent of HeadSHA", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			other := candidate
			other.HeadSHA = "0000000000000000000000000000000000000000"
			otherCmd := pkg.BuildDecisionCommand(other, cfg)
			Expect(otherCmd.TaskIdentifier).To(Equal(cmd.TaskIdentifier))
		})

		It("task_identifier does not contain the HeadSHA", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(string(cmd.TaskIdentifier)).NotTo(ContainSubstring(candidate.HeadSHA))
		})

		It("different repos yield different task_identifier", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			other := candidate
			other.Repo.Name = "other-repo"
			otherCmd := pkg.BuildDecisionCommand(other, cfg)
			Expect(otherCmd.TaskIdentifier).NotTo(Equal(cmd.TaskIdentifier))
		})

		It("body names the current and latest Go versions", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Body).To(ContainSubstring("1.24.0"))
			Expect(cmd.Body).To(ContainSubstring("1.26.6"))
		})

		It("body shows both opt-in and opt-out .maintainer.yaml answers", func() {
			cmd := pkg.BuildDecisionCommand(candidate, cfg)
			Expect(cmd.Body).To(ContainSubstring("autoUpdate: true"))
			Expect(cmd.Body).To(ContainSubstring("autoUpdate: false"))
		})
	})
})
