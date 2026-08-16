// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewGoBehindFilter returns "go_current" when the repo's declared Go version
// is NOT strictly behind current stable — i.e. equal to stable and ahead of
// stable both read as "nothing to do here".
func NewGoBehindFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if !candidate.GoBehind {
			return "go_current"
		}
		return ""
	})
}
