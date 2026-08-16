// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewGoModParsableFilter returns "gomod_unparsable" when go.mod exists but
// carries no readable <major>.<minor>[.<patch>] go directive. This filter
// assumes GoModPresentFilter ran first, so it only fires for present-but-
// unreadable files.
func NewGoModParsableFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if candidate.GoModPresent && !candidate.GoModParsable {
			return "gomod_unparsable"
		}
		return ""
	})
}
