// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

import (
	"context"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
	"gopkg.in/yaml.v3"
)

// Consent is the three-valued outcome of reading `.maintainer.yaml:
// goUpdate.autoUpdate` for one repo (spec 002 Desired Behavior 1).
//
//   - GrantedConsent — the key is present and explicitly boolean true.
//   - RefusedConsent — the key is present and explicitly boolean false.
//   - UndecidedConsent — the file is absent, the goUpdate section is
//     absent, the autoUpdate key is absent, or the key holds any
//     non-boolean value (string, integer, null, etc). Nothing except an
//     explicit boolean true may ever produce GrantedConsent (Security).
//
// The zero value (Consent("")) is deliberately not one of the three named
// constants and is never returned by ParseConsent on a nil error. Any
// caller holding a zero or otherwise-unrecognised Consent must treat it as
// UndecidedConsent — fail closed, never fail open.
type Consent string

const (
	GrantedConsent   Consent = "granted"
	RefusedConsent   Consent = "refused"
	UndecidedConsent Consent = "undecided"
)

// AvailableConsents lists every Consent value the system accepts.
// Validate() ranges over this collection.
var AvailableConsents = Consents{
	GrantedConsent,
	RefusedConsent,
	UndecidedConsent,
}

// String implements fmt.Stringer.
func (c Consent) String() string {
	return string(c)
}

// Validate reports an error unless c is a member of AvailableConsents.
func (c Consent) Validate(ctx context.Context) error {
	if !AvailableConsents.Contains(c) {
		return errors.Wrapf(ctx, validation.Error, "unknown consent %q", c)
	}
	return nil
}

// Consents is a collection of Consent values.
type Consents []Consent

// Contains reports whether consent is a member of the collection.
func (c Consents) Contains(consent Consent) bool {
	return collection.Contains(c, consent)
}

// maintainerDoc is the minimal shape ParseConsent needs to reach the
// goUpdate.autoUpdate node as a raw yaml.Node, so it can tell "absent" from
// "present and false" apart -- see ParseConsent doc for why this cannot go
// through github.com/bborbe/maintainer/maintainerconfig.Parse instead.
type maintainerDoc struct {
	GoUpdate struct {
		AutoUpdate yaml.Node `yaml:"autoUpdate"`
	} `yaml:"goUpdate"`
}

// ParseConsent reads raw `.maintainer.yaml` bytes and returns the tri-state
// Consent verdict (spec 002 Desired Behavior 1).
//
// This intentionally does NOT reuse
// github.com/bborbe/maintainer/maintainerconfig.Parse. That package decodes
// straight into a typed bool field: Go zero-value semantics make "key
// absent" and "key present but false" both decode as false, with no way to
// tell them apart -- and maintainerconfig is a shared, externally-owned
// schema consumed by multiple independently-deployed bots, so it cannot be
// extended for this one repo's tri-state need. ParseConsent instead walks
// the raw yaml.Node tree so it can see whether the node exists at all and
// what YAML tag the resolver gave it. yaml.v3's implicit resolver only ever
// tags a plain, unquoted true/True/TRUE/false/False/FALSE as !!bool; a
// quoted string, an integer, yes/no, or an explicit null all resolve to a
// different tag and therefore can never reach the granted/refused branches
// below.
//
// Returns:
//   - (GrantedConsent, nil) only when goUpdate.autoUpdate resolves to the
//     YAML !!bool tag with value true/True/TRUE.
//   - (RefusedConsent, nil) only when goUpdate.autoUpdate resolves to the
//     YAML !!bool tag with value false/False/FALSE.
//   - (UndecidedConsent, nil) when the document is empty, the goUpdate
//     section is absent, the autoUpdate key is absent, or the key is
//     present with any non-boolean value.
//   - (Consent(""), non-nil error) when content is not valid YAML at all.
//     The caller (GetMaintainerConfig / gatherCandidate) MUST treat a
//     non-nil error as a drop-before-evaluation, exactly like today's
//     unparsable-go.mod path -- never read the zero-value Consent as a
//     verdict (spec 002 Desired Behavior 2, AC7).
func ParseConsent(ctx context.Context, content []byte) (Consent, error) {
	if len(content) == 0 {
		return UndecidedConsent, nil
	}

	var doc maintainerDoc
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return Consent(""), errors.Wrapf(ctx, err, "parse .maintainer.yaml")
	}

	node := doc.GoUpdate.AutoUpdate
	if node.Kind == 0 {
		// Key (or the whole goUpdate section, or the whole document) never
		// appeared -- the field stayed at its zero value.
		return UndecidedConsent, nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		// Present but not a plain boolean scalar. Never a refusal, never a
		// grant -- Non-goals forbids defaulting any non-true value to consent.
		return UndecidedConsent, nil
	}
	switch node.Value {
	case "true", "True", "TRUE":
		return GrantedConsent, nil
	case "false", "False", "FALSE":
		return RefusedConsent, nil
	default:
		return UndecidedConsent, nil
	}
}
