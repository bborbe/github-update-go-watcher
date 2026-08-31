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

// decisionTaskIDNamespace is the UUID5 namespace for decision tasks.
// Frozen and deliberately distinct from taskIDNamespace — decision tasks
// and update tasks must never collide.
var decisionTaskIDNamespace = uuid.MustParse("8a96832c-8007-4d9a-922c-d9cdbdfeca89")

// DeriveDecisionTaskID returns a UUID5 derived deterministically from
// (owner, repo) only — deliberately excluding any HEAD SHA (spec 002
// Desired Behavior 7). A repo that receives new commits while its consent
// remains undecided must not produce a second decision task; the same
// identity is re-emitted every cycle until the repo owner records an
// answer in .maintainer.yaml, and that re-emit is a downstream no-op (spec
// 002 Desired Behavior 8), exactly mirroring the dedup contract
// DeriveTaskID documents for the SHA-keyed update-task identity above.
func DeriveDecisionTaskID(owner, repo string) uuid.UUID {
	seed := fmt.Sprintf("update-go-decision-%s-%s", owner, repo)
	return uuid.NewSHA1(decisionTaskIDNamespace, []byte(seed))
}
