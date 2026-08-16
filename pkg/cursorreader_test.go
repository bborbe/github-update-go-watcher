// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("NewCursorReader", func() {
	It("nil cursor returns empty string", func() {
		r := pkg.NewCursorReader(nil)
		Expect(r.LastSeenSHA("github.com/bborbe/repo")).To(Equal(""))
	})

	It("cursor with nil Repos returns empty string", func() {
		r := pkg.NewCursorReader(&pkg.Cursor{Repos: nil})
		Expect(r.LastSeenSHA("github.com/bborbe/repo")).To(Equal(""))
	})

	It("missing key returns empty string", func() {
		c := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				"github.com/bborbe/other": {LastSeenHeadSHA: "abc"},
			},
		}
		r := pkg.NewCursorReader(c)
		Expect(r.LastSeenSHA("github.com/bborbe/repo")).To(Equal(""))
	})

	It("nil RepoState under key returns empty string", func() {
		c := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				"github.com/bborbe/repo": nil,
			},
		}
		r := pkg.NewCursorReader(c)
		Expect(r.LastSeenSHA("github.com/bborbe/repo")).To(Equal(""))
	})

	It("present key returns recorded SHA", func() {
		c := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				"github.com/bborbe/repo": {LastSeenHeadSHA: "abc123"},
			},
		}
		r := pkg.NewCursorReader(c)
		Expect(r.LastSeenSHA("github.com/bborbe/repo")).To(Equal("abc123"))
	})
})
