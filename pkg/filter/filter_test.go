// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("TaskCreationFilterList", func() {
	Describe("empty composite", func() {
		It("never skips", func() {
			filters := filter.TaskCreationFilterList{}
			reason := filters.Skip(filter.Candidate{})
			Expect(reason).To(BeEmpty())
		})
	})

	Describe("short-circuits on first non-empty reason", func() {
		It("returns first non-empty reason and does not consult later filters", func() {
			secondRan := false
			filters := filter.TaskCreationFilterList{
				filter.TaskCreationFilterFunc(func(c filter.Candidate) string {
					return "first"
				}),
				filter.TaskCreationFilterFunc(func(c filter.Candidate) string {
					secondRan = true
					return "second"
				}),
			}
			reason := filters.Skip(filter.Candidate{})
			Expect(reason).To(Equal("first"))
			Expect(secondRan).To(BeFalse())
		})
	})

	Describe("TaskCreationFilterFunc", func() {
		It("satisfies TaskCreationFilter", func() {
			f := filter.TaskCreationFilterFunc(func(c filter.Candidate) string {
				return "test"
			})
			var _ filter.TaskCreationFilter = f
			Expect(f.Skip(filter.Candidate{})).To(Equal("test"))
		})
	})
})

var _ = Describe("ParseRepoAllowlist", func() {
	It("empty input returns nil", func() {
		Expect(filter.ParseRepoAllowlist("")).To(BeNil())
	})

	It("splits and trims entries", func() {
		result := filter.ParseRepoAllowlist("a/b/c, d/e/f ")
		Expect(result).To(Equal([]string{"a/b/c", "d/e/f"}))
	})

	It("drops empty entries", func() {
		result := filter.ParseRepoAllowlist("a/b/c,,")
		Expect(result).To(Equal([]string{"a/b/c"}))
	})
})

var _ = Describe("RepoAllowlistFilter", func() {
	It("nil allowlist passes anything", func() {
		f := filter.NewRepoAllowlistFilter(nil)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo"})
		Expect(reason).To(BeEmpty())
	})

	It("literal match passes", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/repo"})
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo"})
		Expect(reason).To(BeEmpty())
	})

	It("non-match returns scope", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/other/repo"})
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo"})
		Expect(reason).To(Equal("scope"))
	})

	It("wildcard owner passes bborbe repo", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"})
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/disk-status"})
		Expect(reason).To(BeEmpty())
	})

	It("wildcard owner returns scope for other owner", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"})
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/other/repo"})
		Expect(reason).To(Equal("scope"))
	})

	It("exclusion returns scope for excluded repo", func() {
		f := filter.NewRepoAllowlistFilter(
			[]string{"github.com/bborbe/*", "!github.com/bborbe/skeleton"},
		)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/skeleton"})
		Expect(reason).To(Equal("scope"))
	})

	It("exclusion sibling passes", func() {
		f := filter.NewRepoAllowlistFilter(
			[]string{"github.com/bborbe/*", "!github.com/bborbe/skeleton"},
		)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/other"})
		Expect(reason).To(BeEmpty())
	})
})

var _ = Describe("GoModPresentFilter", func() {
	It("GoModPresent false returns no_gomod", func() {
		f := filter.NewGoModPresentFilter()
		reason := f.Skip(filter.Candidate{GoModPresent: false})
		Expect(reason).To(Equal("no_gomod"))
	})

	It("GoModPresent true returns empty", func() {
		f := filter.NewGoModPresentFilter()
		reason := f.Skip(filter.Candidate{GoModPresent: true})
		Expect(reason).To(BeEmpty())
	})
})

var _ = Describe("GoModParsableFilter", func() {
	It("present and unparsable returns gomod_unparsable", func() {
		f := filter.NewGoModParsableFilter()
		reason := f.Skip(filter.Candidate{GoModPresent: true, GoModParsable: false})
		Expect(reason).To(Equal("gomod_unparsable"))
	})

	It("present and parsable returns empty", func() {
		f := filter.NewGoModParsableFilter()
		reason := f.Skip(filter.Candidate{GoModPresent: true, GoModParsable: true})
		Expect(reason).To(BeEmpty())
	})

	It("absent returns empty (present filter owns that case)", func() {
		f := filter.NewGoModParsableFilter()
		reason := f.Skip(filter.Candidate{GoModPresent: false, GoModParsable: false})
		Expect(reason).To(BeEmpty())
	})
})

var _ = Describe("GoBehindFilter", func() {
	It("GoBehind true returns empty", func() {
		f := filter.NewGoBehindFilter()
		reason := f.Skip(filter.Candidate{GoBehind: true})
		Expect(reason).To(BeEmpty())
	})

	It("GoBehind false returns go_current", func() {
		f := filter.NewGoBehindFilter()
		reason := f.Skip(filter.Candidate{GoBehind: false})
		Expect(reason).To(Equal("go_current"))
	})
})

var _ = Describe("AutoUpdateFilter", func() {
	DescribeTable("consent matrix",
		func(autoUpdate bool, expected string) {
			f := filter.NewAutoUpdateFilter()
			reason := f.Skip(filter.Candidate{AutoUpdate: autoUpdate})
			Expect(reason).To(Equal(expected))
		},
		Entry("true passes", true, ""),
		Entry("false returns auto_update_disabled", false, "auto_update_disabled"),
	)
})

var _ = Describe("SHAUnchangedFilter", func() {
	var cursor *fakeCursor

	BeforeEach(func() {
		cursor = &fakeCursor{shas: make(map[string]string)}
	})

	It("matching SHA returns sha_unchanged", func() {
		cursor.shas["github.com/bborbe/repo"] = "abc123"
		f := filter.NewSHAUnchangedFilter(cursor)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo", HeadSHA: "abc123"})
		Expect(reason).To(Equal("sha_unchanged"))
	})

	It("different SHA returns empty", func() {
		cursor.shas["github.com/bborbe/repo"] = "abc123"
		f := filter.NewSHAUnchangedFilter(cursor)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo", HeadSHA: "different"})
		Expect(reason).To(BeEmpty())
	})

	It("empty recorded SHA (cold) returns empty", func() {
		f := filter.NewSHAUnchangedFilter(cursor)
		reason := f.Skip(filter.Candidate{RepoKey: "github.com/bborbe/repo", HeadSHA: "abc123"})
		Expect(reason).To(BeEmpty())
	})
})

type fakeCursor struct {
	shas map[string]string
}

func (f *fakeCursor) LastSeenSHA(repoKey string) string {
	if f.shas == nil {
		return ""
	}
	return f.shas[repoKey]
}

var _ = Describe("Closed set assertion", func() {
	It("every constructor returns a member of FilterSkipReasons", func() {
		validReasons := map[string]bool{
			"scope":                true,
			"no_gomod":             true,
			"gomod_unparsable":     true,
			"go_current":           true,
			"auto_update_disabled": true,
			"sha_unchanged":        true,
		}
		candidate := filter.Candidate{
			RepoKey:       "github.com/bborbe/repo",
			HeadSHA:       "abc123",
			GoModPresent:  true,
			GoModParsable: true,
			GoBehind:      true,
			AutoUpdate:    true,
		}
		filters := []filter.TaskCreationFilter{
			filter.NewRepoAllowlistFilter(nil),
			filter.NewGoModPresentFilter(),
			filter.NewGoModParsableFilter(),
			filter.NewGoBehindFilter(),
			filter.NewAutoUpdateFilter(),
			filter.NewSHAUnchangedFilter(&fakeCursor{}),
		}
		for _, f := range filters {
			reason := f.Skip(candidate)
			if reason != "" {
				Expect(validReasons[reason]).To(BeTrue(),
					"reason %q is not in FilterSkipReasons", reason)
			}
		}
	})
})

var _ = Describe("Full chain ordering", func() {
	It("returns earliest reason in chain order", func() {
		// out-of-scope AND no go.mod AND not opted in -> scope
		filters := filter.TaskCreationFilterList{
			filter.NewRepoAllowlistFilter([]string{"github.com/other"}),
			filter.NewGoModPresentFilter(),
			filter.NewGoModParsableFilter(),
			filter.NewGoBehindFilter(),
			filter.NewAutoUpdateFilter(),
			filter.NewSHAUnchangedFilter(&fakeCursor{}),
		}
		candidate := filter.Candidate{
			RepoKey:       "github.com/bborbe/repo",
			HeadSHA:       "abc123",
			GoModPresent:  false,
			GoModParsable: false,
			GoBehind:      false,
			AutoUpdate:    false,
		}
		reason := filters.Skip(candidate)
		Expect(reason).To(Equal("scope"))
	})
})
