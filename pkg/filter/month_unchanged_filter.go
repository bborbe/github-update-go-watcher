// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewMonthUnchangedFilter returns "month_unchanged" when the cursor already
// records the same calendar month for the repo. It is the monthly pass's dedup
// gate: the first cycle of a month passes every eligible repo, later cycles in
// that month skip with this literal reason.
//
// The literal matters: verification asserts it by name, so a differently
// spelled reason fails the check rather than passing as "an equivalent".
func NewMonthUnchangedFilter(cursor CursorReader, month string) TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if cursor.LastMonthlyMonth(candidate.RepoKey) == month {
			return "month_unchanged"
		}
		return ""
	})
}
