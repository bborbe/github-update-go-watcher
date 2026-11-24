// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	libtimetest "github.com/bborbe/time/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("Cursor OSV verdicts", func() {
	var ctx context.Context
	var now libtime.DateTime
	var ttl libtime.Duration

	BeforeEach(func() {
		ctx = context.Background()
		now = libtimetest.ParseDateTime("2026-11-24T00:00:00Z")
		ttl = 24 * libtime.Hour
	})

	It("returns a recorded withdrawn verdict within the TTL", func() {
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictWithdrawn, now)

		verdict, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeTrue())
		Expect(verdict).To(Equal(pkg.OSVVerdictWithdrawn))
	})

	It("returns a recorded legitimate verdict within the TTL", func() {
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictLegitimate, now)

		verdict, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeTrue())
		Expect(verdict).To(Equal(pkg.OSVVerdictLegitimate))
	})

	It("reports no verdict when the entry is older than the TTL", func() {
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictWithdrawn, now)

		later := now.Add(libtimetest.ParseDuration("25h"))
		_, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", later, ttl)
		Expect(ok).To(BeFalse())
	})

	It("reports no verdict for an empty CheckedAt", func() {
		cursor := &pkg.Cursor{
			OSVVerdicts: map[string]*pkg.OSVVerdictEntry{
				"CVE-2026-33817": {Withdrawn: true, CheckedAt: ""},
			},
		}

		_, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeFalse())
	})

	It("reports no verdict for an unparsable CheckedAt", func() {
		cursor := &pkg.Cursor{
			OSVVerdicts: map[string]*pkg.OSVVerdictEntry{
				"CVE-2026-33817": {Withdrawn: true, CheckedAt: "banana"},
			},
		}

		_, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeFalse())
	})

	It("reports no verdict for a missing entry", func() {
		cursor := &pkg.Cursor{}

		_, ok := cursor.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeFalse())
	})

	It("does not record an unresolved verdict", func() {
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictUnresolved, now)

		Expect(cursor.OSVVerdicts).To(BeEmpty())
	})

	It("records a verdict on a zero-value cursor without panicking", func() {
		cursor := &pkg.Cursor{}
		Expect(func() {
			cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictWithdrawn, now)
		}).NotTo(Panic())
		Expect(cursor.OSVVerdicts).To(HaveLen(1))
		Expect(cursor.OSVVerdicts["CVE-2026-33817"].CheckedAt).To(Equal("2026-11-24T00:00:00Z"))
	})

	It("round-trips a withdrawn verdict through SaveCursor and LoadCursor", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictWithdrawn, now)
		Expect(pkg.SaveCursor(ctx, path, cursor)).To(Succeed())

		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		verdict, ok := loaded.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeTrue())
		Expect(verdict).To(Equal(pkg.OSVVerdictWithdrawn))
	})

	It("round-trips a legitimate verdict through SaveCursor and LoadCursor", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictLegitimate, now)
		Expect(pkg.SaveCursor(ctx, path, cursor)).To(Succeed())

		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		verdict, ok := loaded.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeTrue())
		Expect(verdict).To(Equal(pkg.OSVVerdictLegitimate))
	})

	It("serialises a legitimate verdict with withdrawn present as false", func() {
		cursor := &pkg.Cursor{}
		cursor.SetVerdict("CVE-2026-33817", pkg.OSVVerdictLegitimate, now)

		data, err := json.Marshal(cursor)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"withdrawn":false`))
	})

	It("loads a cursor JSON with no osv_verdicts key", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		legacy := `{"repos":{"github.com/bborbe/a":{"last_seen_head_sha":"abc"}}}`
		Expect(os.WriteFile(path, []byte(legacy), 0600)).To(Succeed())

		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Repos["github.com/bborbe/a"].LastSeenHeadSHA).To(Equal("abc"))
		_, ok := loaded.CachedVerdict(ctx, "CVE-2026-33817", now, ttl)
		Expect(ok).To(BeFalse())
	})
})
