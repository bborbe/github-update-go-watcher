// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package mathutil provides small, dependency-free numeric helpers.
package mathutil

// Clamp returns value constrained to the inclusive range [min, max].
// If min is greater than max the bounds are swapped so the result is
// always well defined.
func Clamp(value, min, max int) int {
	if min > max {
		min, max = max, min
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
