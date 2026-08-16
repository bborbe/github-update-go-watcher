// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
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
			AutoUpdate: true,
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
})
