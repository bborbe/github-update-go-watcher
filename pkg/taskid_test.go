// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("DeriveTaskID", func() {
	It("returns identical UUID for same inputs", func() {
		id1 := pkg.DeriveTaskID("bborbe", "disk-status", "abc123")
		id2 := pkg.DeriveTaskID("bborbe", "disk-status", "abc123")
		Expect(id1).To(Equal(id2))
	})

	It("returns different UUID when sha changes", func() {
		id1 := pkg.DeriveTaskID("bborbe", "disk-status", "abc123")
		id2 := pkg.DeriveTaskID("bborbe", "disk-status", "def456")
		Expect(id1).NotTo(Equal(id2))
	})

	It("returns VERSION_5 UUID", func() {
		id := pkg.DeriveTaskID("bborbe", "disk-status", "abc123")
		Expect(id.Version().String()).To(Equal("VERSION_5"))
	})
})

var _ = Describe("DeriveDecisionTaskID", func() {
	It("returns identical UUID for same owner/repo", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id1).To(Equal(id2))
	})

	It("returns different UUID for different repo", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "other-repo")
		Expect(id1).NotTo(Equal(id2))
	})

	It("returns different UUID for different owner", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("other-owner", "disk-status")
		Expect(id1).NotTo(Equal(id2))
	})

	It("returns VERSION_5 UUID", func() {
		id := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id.Version().String()).To(Equal("VERSION_5"))
	})

	It("is independent of HeadSHA (spec 002 Desired Behavior 7)", func() {
		id1 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		id2 := pkg.DeriveDecisionTaskID("bborbe", "disk-status")
		Expect(id1).To(Equal(id2), "identity must not depend on any commit SHA")
	})
})
