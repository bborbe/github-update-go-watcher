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

var _ = Describe("ParseGoModVersion", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	DescribeTable("valid go.mod content",
		func(content []byte, major, minor, patch int) {
			v, err := pkg.ParseGoModVersion(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Major).To(Equal(major))
			Expect(v.Minor).To(Equal(minor))
			Expect(v.Patch).To(Equal(patch))
		},
		Entry("three-part directive",
			[]byte("module foo\n\ngo 1.26.6\n"),
			1, 26, 6),
		Entry("two-part directive normalises to patch 0",
			[]byte("module foo\n\ngo 1.26\n"),
			1, 26, 0),
		Entry("leading whitespace before go",
			[]byte("  go 1.26.6\n"),
			1, 26, 6),
		Entry("tab before go",
			[]byte("\tgo 1.27\n"),
			1, 27, 0),
		Entry("go directive after require block start",
			[]byte("module foo\n\nrequire (\n\tgithub.com/bar v1.0.0\n\tgo 1.26.6\n)\n"),
			1, 26, 6),
	)

	DescribeTable("two-part Number() returns three-part form",
		func(content []byte) {
			v, err := pkg.ParseGoModVersion(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Number()).To(Equal("1.26.0"))
		},
		Entry("go 1.26", []byte("go 1.26\n")),
	)

	DescribeTable("invalid content",
		func(content []byte) {
			_, err := pkg.ParseGoModVersion(ctx, content)
			Expect(err).To(HaveOccurred())
		},
		Entry("commented-out go directive only",
			[]byte("// go 1.26\n")),
		Entry("invalid go value",
			[]byte("go banana\n")),
		Entry("go with no value",
			[]byte("go\n")),
		Entry("empty content",
			[]byte("")),
		Entry("only non-go lines",
			[]byte("module foo\nrequire github.com/bar v1.0.0\n")),
		Entry("go token in require block but not as first field",
			[]byte("require (\n\tgopkg.in/yaml.v3 v3.0.1\n)\n")),
	)
})
