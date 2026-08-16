// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"regexp"
	"strconv"

	"github.com/bborbe/errors"
)

// goReleasePattern matches a go.dev release version string:
// "go" followed by major.minor with an optional patch (e.g. go1.27, go1.26.5).
var goReleasePattern = regexp.MustCompile(`^go(\d+)\.(\d+)(?:\.(\d+))?$`)

// goDirectivePattern matches a go.mod `go` directive value: major.minor with an
// optional patch (e.g. 1.26, 1.26.6). A two-part value normalises to patch 0.
var goDirectivePattern = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?$`)

// Version is a parsed Go version. Patch defaults to 0 when the source string
// omits it (e.g. "1.26" -> patch 0). Raw preserves the original string.
type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

// ParseGoRelease parses a go.dev release version string (e.g. go1.26.6, go1.27).
func ParseGoRelease(ctx context.Context, s string) (Version, error) {
	return parseVersion(ctx, goReleasePattern, s, "go release version")
}

// ParseGoDirective parses a go.mod go directive value (e.g. 1.26.6, 1.26).
func ParseGoDirective(ctx context.Context, s string) (Version, error) {
	return parseVersion(ctx, goDirectivePattern, s, "go directive version")
}

func parseVersion(
	ctx context.Context,
	re *regexp.Regexp,
	s, kind string,
) (Version, error) {
	matches := re.FindStringSubmatch(s)
	if matches == nil {
		return Version{}, errors.Errorf(ctx, "invalid %s %q", kind, s)
	}

	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "parse major version from %q", s)
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "parse minor version from %q", s)
	}
	patch := 0
	if matches[3] != "" {
		patch, err = strconv.Atoi(matches[3])
		if err != nil {
			return Version{}, errors.Wrapf(ctx, err, "parse patch version from %q", s)
		}
	}

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   s,
	}, nil
}

// Compare returns a negative value when v < other, zero when equal, and a
// positive value when v > other. The ordering is by major, then minor, then
// patch.
func (v Version) Compare(other Version) int {
	if d := v.Major - other.Major; d != 0 {
		return d
	}
	if d := v.Minor - other.Minor; d != 0 {
		return d
	}
	return v.Patch - other.Patch
}

// Less reports whether v is strictly less than other.
func (v Version) Less(other Version) bool {
	return v.Compare(other) < 0
}

// String returns the canonical three-part form "go<major>.<minor>.<patch>".
func (v Version) String() string {
	return "go" + v.Number()
}

// Number returns the three-part version number without the "go" prefix
// (e.g. "1.26.0"). This is the exact form emitted as current_go and latest_go
// message fields.
func (v Version) Number() string {
	return strconv.Itoa(v.Major) + "." +
		strconv.Itoa(v.Minor) + "." +
		strconv.Itoa(v.Patch)
}
