// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
)

// Repo identifies a GitHub repository within the watcher's scope.
type Repo struct {
	Owner         string
	Name          string
	DefaultBranch string // typically "master" or "main"; cached to avoid a per-cycle lookup
}

// Key returns the host-qualified repo key consumed by the repo allowlist
// (e.g. "github.com/bborbe/disk-status").
func (r Repo) Key() string {
	return fmt.Sprintf("github.com/%s/%s", r.Owner, r.Name)
}

// String returns the short "owner/name" form used in the emitted task fields
// and in log lines.
func (r Repo) String() string {
	return fmt.Sprintf("%s/%s", r.Owner, r.Name)
}
