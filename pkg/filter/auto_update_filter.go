// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewAutoUpdateFilter is the per-repo trust gate sourced from
// `.maintainer.yaml: goUpdate.autoUpdate`. It is POSITIVE OPT-IN: only
// autoUpdate: true passes. Absent file, absent goUpdate section, absent key
// and explicit false all yield "auto_update_disabled".
//
// This gate is the only thing that turns this service's attention into agent
// action on somebody else's repository. There is deliberately no flag,
// env var, or code path that disables it or defaults it to true.
//
// An UNPARSABLE `.maintainer.yaml` never reaches this filter: the gatherer
// drops that repo from the cycle before evaluation, so a malformed file is a
// drop, not a consent verdict.
func NewAutoUpdateFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if !candidate.AutoUpdate {
			return "auto_update_disabled"
		}
		return ""
	})
}
