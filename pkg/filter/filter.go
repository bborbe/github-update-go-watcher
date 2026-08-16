// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package filter implements the TaskCreationFilter chain — the predicates that
// decide whether a Go-update work item should be filed for one observed repo.
//
// Chain order (frozen; the first non-empty reason wins):
//
//  1. RepoAllowlistFilter  -> "scope"                — operator-configured scope
//  2. GoModPresentFilter   -> "no_gomod"             — repo has no go.mod
//  3. GoModParsableFilter  -> "gomod_unparsable"     — go directive unreadable
//  4. GoBehindFilter       -> "go_current"           — at or ahead of stable
//  5. AutoUpdateFilter     -> "auto_update_disabled" — owner has not opted in
//  6. SHAUnchangedFilter   -> "sha_unchanged"        — HEAD already reported
//
// Filters 1-5 are cycle-invariant and built once at wiring time.
// SHAUnchangedFilter is composed in per cycle because it needs a fresh
// CursorReader, and it is omitted entirely on a forced cycle.
package filter

//counterfeiter:generate -o ../../mocks/task_creation_filter.go --fake-name TaskCreationFilter . TaskCreationFilter

// Candidate is the filter-evaluation input. It mirrors the watcher's per-repo
// observation as a local type so this package never imports pkg (pkg imports
// filter; the reverse would be an import cycle).
type Candidate struct {
	// RepoKey is the host-qualified key "github.com/<owner>/<name>".
	RepoKey string
	// HeadSHA is the full HEAD SHA of the default branch.
	HeadSHA string
	// GoModPresent is false when the repo has no go.mod at all.
	GoModPresent bool
	// GoModParsable is false when go.mod exists but carries no readable
	// <major>.<minor>[.<patch>] go directive.
	GoModParsable bool
	// GoBehind is true when the declared Go version is strictly behind the
	// current stable release. The comparison itself lives in pkg.Version;
	// only the verdict crosses into this package.
	GoBehind bool
	// AutoUpdate is `.maintainer.yaml: goUpdate.autoUpdate`. True is the ONLY
	// value that passes the consent gate.
	AutoUpdate bool
}

// TaskCreationFilter decides whether a single Candidate should be skipped.
// Implementations return the metric-label reason for the skip, or "" to pass
// through. Returning the reason (rather than a bool) means the caller never
// re-evaluates the predicates to work out which counter to bump.
type TaskCreationFilter interface {
	// Skip returns the skip reason (metric label) or "" to pass through.
	Skip(candidate Candidate) string
}

// TaskCreationFilterFunc adapts a function to the TaskCreationFilter interface.
type TaskCreationFilterFunc func(candidate Candidate) string

// Skip implements TaskCreationFilter for the function adapter.
func (f TaskCreationFilterFunc) Skip(candidate Candidate) string {
	return f(candidate)
}

// TaskCreationFilterList is a slice composite returning the first non-empty
// reason from its members. An empty slice never skips.
type TaskCreationFilterList []TaskCreationFilter

// Skip returns the first non-empty reason from any contained filter,
// short-circuiting on the first hit.
func (fs TaskCreationFilterList) Skip(candidate Candidate) string {
	for _, f := range fs {
		if reason := f.Skip(candidate); reason != "" {
			return reason
		}
	}
	return ""
}
