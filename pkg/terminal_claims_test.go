// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("TerminalClaims", func() {
	var (
		ctx   context.Context
		store pkg.TerminalClaims
	)

	BeforeEach(func() {
		ctx = context.Background()
		store = pkg.NewTerminalClaims(pkg.NewMemDB())
	})

	It("reports nothing for an identifier never observed", func() {
		allowed, err := store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed).To(BeFalse())
	})

	It("allows the clear exactly once per identifier", func() {
		Expect(store.Observe(ctx, "task-1")).To(Succeed())

		first, err := store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(BeTrue())

		second, err := store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(BeFalse(),
			"the entitlement is consumed, so a second cycle cannot end the claim again")
	})

	It("does not re-arm the entitlement when observed again", func() {
		Expect(store.Observe(ctx, "task-1")).To(Succeed())
		allowed, err := store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed).To(BeTrue())

		// A second terminal event for the same identifier — the re-emitted task
		// stopping again at the same HEAD — must not re-open the entitlement.
		Expect(store.Observe(ctx, "task-1")).To(Succeed())
		allowed, err = store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed).To(BeFalse())
	})

	It("tracks identifiers independently", func() {
		Expect(store.Observe(ctx, "task-1")).To(Succeed())

		allowed, err := store.ClaimClearAllowed(ctx, "task-2")
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed).To(BeFalse())

		allowed, err = store.ClaimClearAllowed(ctx, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed).To(BeTrue())
	})
})
