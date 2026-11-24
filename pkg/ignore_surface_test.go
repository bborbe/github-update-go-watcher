// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

func readFixture(name string) []byte {
	content, err := os.ReadFile("testdata/ignore/" + name)
	Expect(err).NotTo(HaveOccurred())
	return content
}

var _ = Describe("IgnoreSurface", func() {
	Describe("ExtractAdvisoryIDs", func() {
		DescribeTable("extracts the three families",
			func(text string, expected []string) {
				Expect(pkg.ExtractAdvisoryIDs(text)).To(Equal(expected))
			},
			Entry("GO", "GO-2026-4923", []string{"GO-2026-4923"}),
			Entry("CVE", "CVE-2026-33817", []string{"CVE-2026-33817"}),
			Entry("GHSA", "GHSA-xmrv-pmrh-hhx2", []string{"GHSA-xmrv-pmrh-hhx2"}),
			Entry("word boundary next to punctuation",
				"(CVE-2026-1234), [GO-2026-5678]",
				[]string{"CVE-2026-1234", "GO-2026-5678"}),
			Entry("near-miss GO too short", "GO-2026-12", nil),
			Entry("near-miss CVE too short", "CVE-2026-123", nil),
			Entry("near-miss GHSA uppercase", "GHSA-XMRV-PMRH-HHX2", nil),
			Entry("near-miss GHSA incomplete", "GHSA-xmrv-pmrh", nil),
			Entry("deduplicates first-seen order",
				"GO-2026-1111 CVE-2026-2222 GO-2026-1111",
				[]string{"GO-2026-1111", "CVE-2026-2222"}),
			Entry("no ids", "nothing here", nil),
		)
	})

	Describe("ParseVulncheckIgnore", func() {
		DescribeTable("parses Makefile-style assignments",
			func(content string, expected []string) {
				Expect(pkg.ParseVulncheckIgnore([]byte(content))).To(Equal(expected))
			},
			Entry("continuation fixture", string(readFixture("makefile-continuation.txt")),
				[]string{
					"GO-2026-4923", "GO-2026-4514", "GO-2022-0470",
					"GO-2026-4772", "GO-2026-4771",
				}),
			Entry("single-line fixture", string(readFixture("makefile-single.txt")),
				[]string{"CVE-2026-33817"}),
			Entry("no assignment", "all:\n\techo hi\n", nil),
			Entry("commented-out assignment", "# VULNCHECK_IGNORE ?= GO-2026-1234\n", nil),
			Entry("unrelated line not swallowed",
				"VULNCHECK_IGNORE = GO-2026-1234\nOTHER = CVE-2026-9999\n",
				[]string{"GO-2026-1234"}),
			Entry("last continuation line has no backslash",
				"VULNCHECK_IGNORE ?= GO-2026-1111 \\\n\tGO-2026-2222\nOTHER = CVE-2026-3333\n",
				[]string{"GO-2026-1111", "GO-2026-2222"}),
			Entry("question-mark operator", "VULNCHECK_IGNORE ?= GO-2026-1234\n",
				[]string{"GO-2026-1234"}),
		)
	})

	Describe("ParseTrivyIgnore", func() {
		It("parses comments, blanks and trailing remarks", func() {
			Expect(pkg.ParseTrivyIgnore(readFixture("trivyignore.txt"))).To(Equal([]string{
				"CVE-2026-34040",
				"CVE-2026-33997",
				"CVE-2026-33817",
				"GHSA-xmrv-pmrh-hhx2",
				"GHSA-abcd-efgh-ijkl",
			}))
		})

		It("returns empty for an all-comments file", func() {
			Expect(pkg.ParseTrivyIgnore([]byte("# CVE-2026-1234\n\n# GO-2026-5678\n"))).
				To(BeEmpty())
		})

		It("deduplicates the same ID on two lines", func() {
			Expect(pkg.ParseTrivyIgnore([]byte("CVE-2026-1234\nCVE-2026-1234\n"))).
				To(Equal([]string{"CVE-2026-1234"}))
		})
	})

	Describe("ParseOsvScannerToml", func() {
		It("parses only [[IgnoredVulns]] id lines", func() {
			Expect(pkg.ParseOsvScannerToml(readFixture("osv-scanner.toml"))).To(Equal(
				[]string{"GO-2026-5932", "CVE-2022-31022"},
			))
		})

		It("ignores advisory-shaped strings in reasons and other blocks", func() {
			content := `[[IgnoredVulns]]
id = "GO-2026-2222"
reason = "mentions GO-2026-0000 in prose"

[[Packages]]
id = "GO-2026-1111"
`
			Expect(pkg.ParseOsvScannerToml([]byte(content))).
				To(Equal([]string{"GO-2026-2222"}))
		})
	})

	Describe("ParseIgnoreSurface", func() {
		It("dispatches Makefile to the vulncheck parser", func() {
			Expect(pkg.ParseIgnoreSurface("Makefile", []byte("VULNCHECK_IGNORE = GO-2026-1234\n"))).
				To(Equal([]string{"GO-2026-1234"}))
		})

		It("dispatches Makefile.precommit to the vulncheck parser", func() {
			content := []byte("VULNCHECK_IGNORE = CVE-2026-1234\n")
			Expect(pkg.ParseIgnoreSurface("Makefile.precommit", content)).
				To(Equal([]string{"CVE-2026-1234"}))
		})

		It("dispatches .trivyignore to the trivy parser", func() {
			Expect(pkg.ParseIgnoreSurface(".trivyignore", []byte("GO-2026-1234\n"))).
				To(Equal([]string{"GO-2026-1234"}))
		})

		It("dispatches .osv-scanner.toml to the toml parser", func() {
			content := "[[IgnoredVulns]]\nid = \"GO-2026-1234\"\n"
			Expect(pkg.ParseIgnoreSurface(".osv-scanner.toml", []byte(content))).
				To(Equal([]string{"GO-2026-1234"}))
		})

		It("returns empty for an unsupported path", func() {
			Expect(pkg.ParseIgnoreSurface("README.md", []byte("GO-2026-1234\n"))).To(BeEmpty())
		})
	})
})
