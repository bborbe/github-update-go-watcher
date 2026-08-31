// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewAutoUpdateFilter is the per-repo trust gate sourced from
// `.maintainer.yaml: goUpdate.autoUpdate` (spec 002). It is POSITIVE
// OPT-IN: only Consent == GrantedConsent passes.
//
//   - GrantedConsent  -> "" (passes)
//   - RefusedConsent  -> "auto_update_disabled" — owner explicitly opted out
//   - UndecidedConsent, or any other/invalid Consent value (including the
//     zero value) -> "auto_update_undecided" — fails closed
//
// This gate is the only thing that turns this service's attention into
// agent action on somebody else's repository. There is deliberately no
// flag, env var, or code path that disables it or defaults any non-granted
// value to consent.
//
// An UNPARSABLE `.maintainer.yaml` never reaches this filter: the gatherer
// drops that repo from the cycle before evaluation, so a malformed file is
// a drop, not a consent verdict.
func NewAutoUpdateFilter() TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		switch candidate.Consent {
		case GrantedConsent:
			return ""
		case RefusedConsent:
			return "auto_update_disabled"
		default:
			// UndecidedConsent, and any other/invalid Consent value
			// (including the zero value), fails closed here.
			return "auto_update_undecided"
		}
	})
}
