// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("Consent", func() {
	Describe("String", func() {
		It("returns the raw value", func() {
			Expect(filter.GrantedConsent.String()).To(Equal("granted"))
		})
	})

	Describe("Validate", func() {
		It("accepts every AvailableConsents member", func() {
			ctx := context.Background()
			for _, c := range filter.AvailableConsents {
				Expect(c.Validate(ctx)).To(Succeed())
			}
		})

		It("rejects an unknown value", func() {
			ctx := context.Background()
			Expect(filter.Consent("bogus").Validate(ctx)).To(HaveOccurred())
		})

		It("rejects the zero value", func() {
			ctx := context.Background()
			Expect(filter.Consent("").Validate(ctx)).To(HaveOccurred())
		})
	})
})

var _ = Describe("ParseConsent", func() {
	Describe("three-outcome matrix", func() {
		DescribeTable("returns the correct verdict",
			func(content []byte, expected filter.Consent) {
				ctx := context.Background()
				consent, err := filter.ParseConsent(ctx, content)
				Expect(err).NotTo(HaveOccurred())
				Expect(consent).To(Equal(expected))
			},
			Entry("explicit true",
				[]byte("goUpdate:\n  autoUpdate: true"), filter.GrantedConsent),
			Entry("explicit false",
				[]byte("goUpdate:\n  autoUpdate: false"), filter.RefusedConsent),
			Entry("absent file",
				[]byte(nil), filter.UndecidedConsent),
			Entry("absent goUpdate section",
				[]byte("other: value"), filter.UndecidedConsent),
			Entry("absent autoUpdate key",
				[]byte("goUpdate:\n  somethingElse: true"), filter.UndecidedConsent),
		)
	})

	// NOTE FOR HUMAN AUDIT (open question, resolve at prompt-audit time):
	// spec 002 AC8's evidence text reads "each asserts a non-empty skip
	// reason" for a table that includes "malformed YAML" as one of its 8
	// rows. ParseConsent operates BELOW the filter/skip-reason layer -- it
	// returns a (Consent, error) pair, not a skip-reason string -- and
	// spec 002 AC7 requires this exact malformed-YAML input to produce a
	// DROP (IncFilterSkipped call count 0), not any skip reason at all.
	// Read literally at this layer, AC7 and AC8 would contradict each
	// other for the identical input. The table below resolves the tension
	// by reading AC8's "non-empty skip reason" as "the parse outcome is
	// not granted": every non-malformed row asserts a Consent other than
	// GrantedConsent with a nil error (which becomes skip reason
	// auto_update_undecided once it reaches the filter chain, wired in a
	// later prompt), and the malformed row asserts a non-nil error (which
	// becomes a drop with step maintainer_config in gatherCandidate,
	// never reaching the filter chain at all -- consistent with AC7). If a
	// different reading of AC8 was intended, flag this table for revision.
	Describe("never grants except an explicit boolean true (AC8, Security)", func() {
		DescribeTable("outcome is never granted",
			func(content []byte, expectError bool) {
				ctx := context.Background()
				consent, err := filter.ParseConsent(ctx, content)

				Expect(consent).NotTo(Equal(filter.GrantedConsent))

				if expectError {
					Expect(err).To(HaveOccurred())
					return
				}
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("absent file", []byte(nil), false),
			Entry("absent goUpdate section", []byte("other: value"), false),
			Entry("absent autoUpdate key",
				[]byte("goUpdate:\n  somethingElse: true"), false),
			Entry("autoUpdate as string \"true\"",
				[]byte("goUpdate:\n  autoUpdate: \"true\""), false),
			Entry("autoUpdate as integer 1",
				[]byte("goUpdate:\n  autoUpdate: 1"), false),
			Entry("autoUpdate as yes",
				[]byte("goUpdate:\n  autoUpdate: yes"), false),
			Entry("autoUpdate as explicit null",
				[]byte("goUpdate:\n  autoUpdate: null"), false),
			Entry("malformed YAML",
				[]byte("goUpdate:\n  autoUpdate: ["), true),
		)
	})
})
