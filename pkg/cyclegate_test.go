// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("CycleGate", func() {
	It("first TryAcquire returns true", func() {
		gate := pkg.NewCycleGate()
		Expect(gate.TryAcquire()).To(BeTrue())
	})

	It("second TryAcquire returns false while held", func() {
		gate := pkg.NewCycleGate()
		Expect(gate.TryAcquire()).To(BeTrue())
		Expect(gate.TryAcquire()).To(BeFalse())
	})

	It("after Release, next TryAcquire is true again", func() {
		gate := pkg.NewCycleGate()
		Expect(gate.TryAcquire()).To(BeTrue())
		gate.Release()
		Expect(gate.TryAcquire()).To(BeTrue())
	})

	It("Release without holding does not panic", func() {
		gate := pkg.NewCycleGate()
		gate.Release() // no-op, does not panic
	})

	It("while gate is held, all concurrent attempts fail", func() {
		gate := pkg.NewCycleGate()
		held := gate.TryAcquire()
		Expect(held).To(BeTrue())

		const workers = 10
		var results [workers]bool
		var wg sync.WaitGroup

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx] = gate.TryAcquire()
			}(i)
		}
		wg.Wait()

		// All 10 concurrent attempts should fail while the gate is held
		for idx, r := range results {
			Expect(r).To(BeFalse(), "worker %d should not have acquired", idx)
		}
	})
})
