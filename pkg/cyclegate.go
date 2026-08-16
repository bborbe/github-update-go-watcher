// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

//counterfeiter:generate -o ../mocks/cycle_gate.go --fake-name CycleGate . CycleGate

// CycleGate enforces "exactly one poll cycle at a time" across the interval
// loop and the forced-cycle HTTP endpoint. It is non-blocking by design: a
// caller that cannot acquire the slot backs off instead of queueing, so a
// burst of forced-cycle requests cannot pile up behind a slow cycle.
type CycleGate interface {
	// TryAcquire reports whether the caller now holds the single cycle slot.
	// A caller that receives true MUST call Release when its cycle finishes.
	TryAcquire() bool
	// Release frees the slot. Calling Release without holding it is a no-op.
	Release()
}

// NewCycleGate returns a CycleGate backed by a capacity-1 channel.
func NewCycleGate() CycleGate {
	return &cycleGate{slot: make(chan struct{}, 1)}
}

type cycleGate struct {
	slot chan struct{}
}

func (g *cycleGate) TryAcquire() bool {
	select {
	case g.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *cycleGate) Release() {
	select {
	case <-g.slot:
	default:
	}
}
