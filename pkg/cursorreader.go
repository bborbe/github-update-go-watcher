// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

// NewCursorReader exposes a filter-compatible read view over a Cursor.
func NewCursorReader(c *Cursor) filter.CursorReader {
	return &cursorReader{c: c}
}

type cursorReader struct{ c *Cursor }

func (r *cursorReader) LastSeenSHA(repoKey string) string {
	if r.c == nil || r.c.Repos == nil {
		return ""
	}
	entry := r.c.Repos[repoKey]
	if entry == nil {
		return ""
	}
	return entry.LastSeenHeadSHA
}
