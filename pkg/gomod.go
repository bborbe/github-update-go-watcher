// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// ParseGoModVersion extracts the `go` directive from raw go.mod bytes and
// returns it as a normalised Version. It is deliberately narrow: it scans for
// the first line whose first field is exactly "go" and parses that line's
// second field via ParseGoDirective. A file with no `go` directive, a
// directive with no value, or a value that is not <major>.<minor>[.<patch>]
// all return an error — the caller maps that to skip reason "gomod_unparsable".
//
// Security: content is attacker-controlled (any observed repo can write any
// go.mod). Only the extracted numeric triple ever leaves this function; raw
// bytes are never propagated into an emitted message or an error message
// longer than the offending token.
func ParseGoModVersion(ctx context.Context, content []byte) (Version, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" {
			return ParseGoDirective(ctx, fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return Version{}, errors.Errorf(ctx, "scanner error reading go.mod: %v", err)
	}
	return Version{}, errors.Errorf(ctx, "no go directive found in go.mod")
}

// ParseGoModVersionDefault extracts the `go` directive from raw go.mod bytes,
// falling back to defaultValue when the content has no readable directive.
//
// Callers that must distinguish "unparsable" from "absent" need ParseGoModVersion:
// this variant collapses both onto defaultValue, which is why the watcher's own
// filter chain uses the erroring form to produce skip reason "gomod_unparsable".
func ParseGoModVersionDefault(ctx context.Context, content []byte, defaultValue Version) Version {
	version, err := ParseGoModVersion(ctx, content)
	if err != nil {
		glog.V(3).Infof("parse go.mod version failed, using default: %v", err)
		return defaultValue
	}
	return version
}
