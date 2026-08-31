// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

// Candidate is the watcher's per-repo observation: everything needed to
// (a) decide whether to file a work item and (b) populate the emitted message.
//
// Built per cycle by the Watcher in this order, so partial failures degrade
// gracefully:
//  1. Repo         (from ListRepos)
//  2. HeadSHA      (from GetHeadSHA)
//  3. GoModPresent / GoModParsable / CurrentGo (from GetGoMod + ParseGoModVersion)
//  4. Consent      (from GetMaintainerConfig — UndecidedConsent when .maintainer.yaml is absent)
//  5. LatestGo     (resolved once per cycle, identical across every Candidate)
type Candidate struct {
	Repo          Repo
	HeadSHA       string         // full SHA of the default branch HEAD
	GoModPresent  bool           // false when the repo has no go.mod
	GoModParsable bool           // false when go.mod exists but has no readable go directive
	CurrentGo     Version        // zero value unless GoModParsable
	LatestGo      Version        // current stable, resolved once per cycle
	Consent       filter.Consent // .maintainer.yaml: goUpdate.autoUpdate — the consent gate
}

// ShortSHA returns the first 7 chars of HeadSHA, used in the title and body.
func (c Candidate) ShortSHA() string {
	if len(c.HeadSHA) < 7 {
		return c.HeadSHA
	}
	return c.HeadSHA[:7]
}

// GoBehind reports whether the declared Go version is strictly behind current
// stable. Equal-to and ahead-of both report false ("nothing to do").
func (c Candidate) GoBehind() bool {
	return c.GoModParsable && c.CurrentGo.Less(c.LatestGo)
}

// FilterCandidate projects this observation onto the filter package's input.
func (c Candidate) FilterCandidate() filter.Candidate {
	return filter.Candidate{
		RepoKey:       c.Repo.Key(),
		HeadSHA:       c.HeadSHA,
		GoModPresent:  c.GoModPresent,
		GoModParsable: c.GoModParsable,
		GoBehind:      c.GoBehind(),
		Consent:       c.Consent,
	}
}
