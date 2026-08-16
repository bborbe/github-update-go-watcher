// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

//counterfeiter:generate -o ../../mocks/cursor_reader.go --fake-name CursorReader . CursorReader

// CursorReader is the minimal read surface SHAUnchangedFilter needs. Declared
// locally (Hollywood principle) so this package never imports pkg.Cursor.
type CursorReader interface {
	// LastSeenSHA returns the recorded HEAD for repoKey, or "" if unseen.
	LastSeenSHA(repoKey string) string
}

// NewSHAUnchangedFilter returns "sha_unchanged" when Candidate.HeadSHA equals
// the recorded HEAD for the same repo. A cold cursor always passes; later
// cycles only pass once HEAD advances.
//
// A forced cycle omits this filter from the chain entirely — every other gate
// still applies (spec Desired Behavior 8).
func NewSHAUnchangedFilter(cursor CursorReader) TaskCreationFilter {
	return TaskCreationFilterFunc(func(candidate Candidate) string {
		if candidate.HeadSHA != "" && candidate.HeadSHA == cursor.LastSeenSHA(candidate.RepoKey) {
			return "sha_unchanged"
		}
		return ""
	})
}
