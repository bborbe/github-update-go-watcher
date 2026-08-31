// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("Factory", func() {
	Describe("CreateStaticFilters", func() {
		It("skips a Candidate with GoModPresent=false for reason no_gomod", func() {
			f := factory.CreateStaticFilters(nil)
			candidate := filter.Candidate{
				RepoKey:      "github.com/example/repo",
				GoModPresent: false,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(Equal("no_gomod"))
		})

		It("passes a fully-qualifying Candidate", func() {
			f := factory.CreateStaticFilters(nil)
			candidate := filter.Candidate{
				RepoKey:       "github.com/example/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(BeEmpty())
		})
	})

	Describe("CreateGoDevHTTPClient", func() {
		It("has a non-zero timeout", func() {
			client := factory.CreateGoDevHTTPClient()
			Expect(client.Timeout).To(BeNumerically(">", 0))
		})
	})

	Describe("CreateStaticFilters with allowlist", func() {
		It("skips a repo outside the allowlist for reason scope", func() {
			f := factory.CreateStaticFilters([]string{"github.com/allowed/repo"})
			candidate := filter.Candidate{
				RepoKey:       "github.com/other/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(Equal("scope"))
		})

		It("passes a repo within the allowlist", func() {
			f := factory.CreateStaticFilters([]string{"github.com/example/repo"})
			candidate := filter.Candidate{
				RepoKey:       "github.com/example/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(BeEmpty())
		})
	})

	Describe("DefaultCursorPath", func() {
		It("defaults to the PVC mount point", func() {
			Expect(pkg.DefaultCursorPath).To(Equal("/data/cursor.json"))
		})
	})
})
