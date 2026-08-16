// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("Version", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("ParseGoRelease", func() {
		DescribeTable("valid inputs",
			func(input string, major, minor, patch int) {
				v, err := pkg.ParseGoRelease(ctx, input)
				Expect(err).NotTo(HaveOccurred())
				Expect(v.Major).To(Equal(major))
				Expect(v.Minor).To(Equal(minor))
				Expect(v.Patch).To(Equal(patch))
			},
			Entry("go1.26.6", "go1.26.6", 1, 26, 6),
			Entry("go1.27", "go1.27", 1, 27, 0),
			Entry("go1.27.0", "go1.27.0", 1, 27, 0),
		)

		DescribeTable("invalid inputs",
			func(input string) {
				_, err := pkg.ParseGoRelease(ctx, input)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(input))
			},
			Entry("no go prefix", "1.26.6"),
			Entry("go without numbers", "go1"),
			Entry("empty string", ""),
			Entry("non-numeric", "gox.y.z"),
		)
	})

	Describe("ParseGoDirective", func() {
		DescribeTable("valid inputs",
			func(input string, major, minor, patch int) {
				v, err := pkg.ParseGoDirective(ctx, input)
				Expect(err).NotTo(HaveOccurred())
				Expect(v.Major).To(Equal(major))
				Expect(v.Minor).To(Equal(minor))
				Expect(v.Patch).To(Equal(patch))
			},
			Entry("three-part version", "1.26.6", 1, 26, 6),
			Entry("two-part version normalises to patch 0", "1.26", 1, 26, 0),
		)

		It("two-part version Number() returns three-part form", func() {
			v, err := pkg.ParseGoDirective(ctx, "1.26")
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Number()).To(Equal("1.26.0"))
		})

		DescribeTable("invalid inputs",
			func(input string) {
				_, err := pkg.ParseGoDirective(ctx, input)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(input))
			},
			Entry("has go prefix", "go1.26"),
			Entry("single number only", "1"),
			Entry("non-numeric", "abc"),
		)
	})

	Describe("Compare", func() {
		It("returns negative when major is less", func() {
			a := pkg.Version{Major: 1, Minor: 25, Patch: 0}
			b := pkg.Version{Major: 1, Minor: 26, Patch: 0}
			Expect(a.Compare(b)).To(BeNumerically("<", 0))
		})

		It("returns negative when minor is less", func() {
			a := pkg.Version{Major: 1, Minor: 26, Patch: 5}
			b := pkg.Version{Major: 1, Minor: 27, Patch: 0}
			Expect(a.Compare(b)).To(BeNumerically("<", 0))
		})

		It("returns negative when patch is less", func() {
			a := pkg.Version{Major: 1, Minor: 26, Patch: 0}
			b := pkg.Version{Major: 1, Minor: 26, Patch: 1}
			Expect(a.Compare(b)).To(BeNumerically("<", 0))
		})

		It("returns zero for equal versions", func() {
			a := pkg.Version{Major: 1, Minor: 26, Patch: 6}
			b := pkg.Version{Major: 1, Minor: 26, Patch: 6}
			Expect(a.Compare(b)).To(Equal(0))
		})

		It("returns positive when v > other", func() {
			a := pkg.Version{Major: 2, Minor: 0, Patch: 0}
			b := pkg.Version{Major: 1, Minor: 99, Patch: 99}
			Expect(a.Compare(b)).To(BeNumerically(">", 0))
		})
	})

	Describe("Less", func() {
		It("reports true when v < other", func() {
			a := pkg.Version{Major: 1, Minor: 25, Patch: 9}
			b := pkg.Version{Major: 1, Minor: 26, Patch: 0}
			Expect(a.Less(b)).To(BeTrue())
		})

		It("reports false when v == other", func() {
			a := pkg.Version{Major: 1, Minor: 26, Patch: 6}
			b := pkg.Version{Major: 1, Minor: 26, Patch: 6}
			Expect(a.Less(b)).To(BeFalse())
		})
	})

	Describe("String", func() {
		It("returns three-part canonical form", func() {
			v := pkg.Version{Major: 1, Minor: 26, Patch: 0}
			Expect(v.String()).To(Equal("go1.26.0"))
		})

		It("derived from parsed components not Raw", func() {
			// Raw might be two-part but String() is always three-part
			v, err := pkg.ParseGoDirective(ctx, "1.26")
			Expect(err).NotTo(HaveOccurred())
			Expect(v.String()).To(Equal("go1.26.0"))
		})
	})
})
