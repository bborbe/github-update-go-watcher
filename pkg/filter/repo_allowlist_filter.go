// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

import (
	"strings"

	"github.com/bborbe/maintainer/repoallowlist"
)

// ParseRepoAllowlist parses a comma-separated allowlist string into
// host-qualified repo keys (e.g. "github.com/bborbe/disk-status").
// Whitespace is trimmed and empty entries dropped. nil on empty input, which
// repoallowlist.IsAllowed treats as allow-all within the configured owner.
func ParseRepoAllowlist(raw string) []string {
	if raw == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		result = append(result, entry)
	}
	return result
}

// NewRepoAllowlistFilter returns the operator-scope gate: "scope" for any
// Candidate whose RepoKey is not permitted by the allowlist.
func NewRepoAllowlistFilter(allowlist []string) TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if !repoallowlist.IsAllowed(allowlist, candidate.RepoKey) {
			return "scope"
		}
		return ""
	})
}
