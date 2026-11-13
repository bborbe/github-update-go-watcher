// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	base "github.com/bborbe/cqrs/base"
	libkafka "github.com/bborbe/kafka"
)

// TerminalClaimsTopicForTest exposes the unexported topic derivation so the
// external test package can assert the prefixed topic name.
func TerminalClaimsTopicForTest(prefix base.TopicPrefix) libkafka.Topic {
	return terminalClaimsTopic(prefix)
}
