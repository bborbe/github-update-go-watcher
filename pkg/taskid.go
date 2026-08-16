// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"

	"github.com/google/uuid"
)

// taskIDNamespace is the UUID5 namespace for github-update-go tasks.
// Frozen: changing it would break the task controller's dedup and re-file
// every open work item.
var taskIDNamespace = uuid.MustParse("8a9e45ee-da1f-4939-a3b5-11201f600a1a")

// DeriveTaskID returns a UUID5 derived deterministically from
// (owner, repo, headSHA) via the seed "update-go-<owner>-<repo>-<headSHA>"
// (spec Desired Behavior 6).
//
// Same repo at the same HEAD always yields the same identifier, so a re-emit
// is a downstream no-op; a new HEAD yields a new identifier, so a new commit
// correctly produces a fresh work item.
func DeriveTaskID(owner, repo, headSHA string) uuid.UUID {
	seed := fmt.Sprintf("update-go-%s-%s-%s", owner, repo, headSHA)
	return uuid.NewSHA1(taskIDNamespace, []byte(seed))
}
