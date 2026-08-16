// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mathutil_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg/mathutil"
)

var _ = DescribeTable("Clamp",
	func(value, min, max, expected int) {
		Expect(mathutil.Clamp(value, min, max)).To(Equal(expected))
	},
	Entry("within range", 5, 0, 10, 5),
	Entry("below min", -3, 0, 10, 0),
	Entry("above max", 42, 0, 10, 10),
	Entry("equal to min", 0, 0, 10, 0),
	Entry("equal to max", 10, 0, 10, 10),
	Entry("swapped bounds", 5, 10, 0, 5),
	Entry("swapped bounds clamps low", -1, 10, 0, 0),
)
