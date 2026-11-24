// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// taskIDNamespace is the UUID5 namespace for github-update-go tasks.
// Frozen: changing it would break the task controller's dedup and re-file
// every open work item.
var taskIDNamespace = uuid.MustParse("8a9e45ee-da1f-4939-a3b5-11201f600a1a")

// DeriveTaskID returns a UUID5 derived deterministically from
// (owner, repo, goVersion, headSHA) via the seed
// "update-go-<owner>-<repo>-<goVersion>-<headSHA>".
//
// Same repo at the same HEAD and the same target Go version always yields the
// same identifier, so a re-emit is a downstream no-op; a new HEAD OR a new
// target Go version yields a new identifier, so a new commit and a new stable
// Go release each correctly produce a fresh work item.
func DeriveTaskID(owner, repo, goVersion, headSHA string) uuid.UUID {
	seed := fmt.Sprintf("update-go-%s-%s-%s-%s", owner, repo, goVersion, headSHA)
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

// staleIgnoreTaskIDNamespace is the UUID5 namespace for stale-ignore tasks.
// Frozen and deliberately distinct from taskIDNamespace and
// decisionTaskIDNamespace: a stale-ignore task must never collide with an
// update task or a decision task.
var staleIgnoreTaskIDNamespace = uuid.MustParse("4f2a7c1e-9b3d-4c58-8e21-6d0b7a5c9f13")

// DeriveStaleIgnoreTaskID returns a UUID5 over the seed
// "stale-ignore-<owner>-<repo>-g<generation>-<ids>", where ids is the sorted
// stale set joined by ",".
//
// The generation is part of the identity and only ever increases, so a re-emit
// past staleReemitInterval mints a record that does not yet exist and therefore
// actually runs. Resetting the generation to 1 on a set change would be wrong:
// an oscillating set ({A} -> {A,B} -> {A}) would re-derive the first emission's
// identifier, the controller would dedup it, and the still-stale A would never
// be re-filed.
func DeriveStaleIgnoreTaskID(owner, repo string, generation int, ids []string) uuid.UUID {
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	seed := fmt.Sprintf(
		"stale-ignore-%s-%s-g%d-%s",
		owner,
		repo,
		generation,
		strings.Join(sorted, ","),
	)
	return uuid.NewSHA1(staleIgnoreTaskIDNamespace, []byte(seed))
}
