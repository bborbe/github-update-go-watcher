// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewGoModPresentFilter returns "no_gomod" for a repo with no go.mod at HEAD.
// Not a failure: most repos in a mixed-language org simply are not Go repos.
func NewGoModPresentFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if !candidate.GoModPresent {
			return "no_gomod"
		}
		return ""
	})
}
