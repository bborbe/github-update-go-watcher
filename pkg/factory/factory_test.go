// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"

	"github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/cdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("Factory", func() {
	Describe("CreateStaticFilters", func() {
		It("skips a Candidate with GoModPresent=false for reason no_gomod", func() {
			f := factory.CreateStaticFilters(nil)
			candidate := filter.Candidate{
				RepoKey:      "github.com/example/repo",
				GoModPresent: false,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(Equal("no_gomod"))
		})

		It("passes a fully-qualifying Candidate", func() {
			f := factory.CreateStaticFilters(nil)
			candidate := filter.Candidate{
				RepoKey:       "github.com/example/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(BeEmpty())
		})
	})

	Describe("CreateGoDevHTTPClient", func() {
		It("has a non-zero timeout", func() {
			client := factory.CreateGoDevHTTPClient()
			Expect(client.Timeout).To(BeNumerically(">", 0))
		})
	})

	Describe("CreateStaticFilters with allowlist", func() {
		It("skips a repo outside the allowlist for reason scope", func() {
			f := factory.CreateStaticFilters([]string{"github.com/allowed/repo"})
			candidate := filter.Candidate{
				RepoKey:       "github.com/other/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(Equal("scope"))
		})

		It("passes a repo within the allowlist", func() {
			f := factory.CreateStaticFilters([]string{"github.com/example/repo"})
			candidate := filter.Candidate{
				RepoKey:       "github.com/example/repo",
				GoModPresent:  true,
				GoModParsable: true,
				GoBehind:      true,
				Consent:       filter.GrantedConsent,
			}
			reason := f.Skip(candidate)
			Expect(reason).To(BeEmpty())
		})
	})

	Describe("DefaultCursorPath", func() {
		It("defaults to the PVC mount point", func() {
			Expect(pkg.DefaultCursorPath).To(Equal("/data/cursor.json"))
		})
	})

	// The controller refuses a create whose target_vault is empty instead of
	// falling back to a default, so an unstamped command is a silent no-op:
	// the publish succeeds and no task file appears (measured 2026-11-12 —
	// 23 publishes, 0 files). These tests pin the stamping that closes it.
	Describe("CreateCreateCommandSender", func() {
		It("stamps the configured vault on a command that carries none", func() {
			ctx := context.Background()
			capture := &capturingCommandObjectSender{}
			sender := factory.CreateCreateCommandSender(capture, "openclaw")

			cmd := task.CreateCommand{
				Title:          "Update Go bborbe-errors 7ea56f7",
				TaskIdentifier: "d1194a62-91d7-5d6f-b96d-bebbe6a15eb4",
			}
			Expect(sender.SendCommand(ctx, cmd)).To(Succeed())

			var got task.CreateCommand
			Expect(capture.last.Command.Data.MarshalInto(ctx, &got)).To(Succeed())
			Expect(got.TargetVault).To(Equal("openclaw"))
		})

		It("leaves an explicit vault untouched", func() {
			ctx := context.Background()
			capture := &capturingCommandObjectSender{}
			sender := factory.CreateCreateCommandSender(capture, "openclaw")

			cmd := task.CreateCommand{
				Title:          "Update Go bborbe-errors 7ea56f7",
				TaskIdentifier: "d1194a62-91d7-5d6f-b96d-bebbe6a15eb4",
				TargetVault:    "trading",
			}
			Expect(sender.SendCommand(ctx, cmd)).To(Succeed())

			var got task.CreateCommand
			Expect(capture.last.Command.Data.MarshalInto(ctx, &got)).To(Succeed())
			Expect(got.TargetVault).To(Equal("trading"))
		})
	})

	Describe("CreateCompleteCommandSender", func() {
		It("stamps the configured vault on a command that carries none", func() {
			ctx := context.Background()
			capture := &capturingCommandObjectSender{}
			sender := factory.CreateCompleteCommandSender(capture, "openclaw")

			cmd := task.CompleteCommand{
				TaskIdentifier: "d1194a62-91d7-5d6f-b96d-bebbe6a15eb4",
			}
			Expect(sender.SendCommand(ctx, cmd)).To(Succeed())

			var got task.CompleteCommand
			Expect(capture.last.Command.Data.MarshalInto(ctx, &got)).To(Succeed())
			Expect(got.TargetVault).To(Equal("openclaw"))
		})
	})
})

// capturingCommandObjectSender records the last CommandObject it is asked to
// send, so a test can read the payload the typed sender actually produced.
type capturingCommandObjectSender struct {
	last cdb.CommandObject
}

func (s *capturingCommandObjectSender) SendCommandObject(
	_ context.Context,
	co cdb.CommandObject,
) error {
	s.last = co
	return nil
}

func (s *capturingCommandObjectSender) SendCommandObjects(
	_ context.Context,
	cos cdb.CommandObjects,
) error {
	if len(cos) > 0 {
		s.last = cos[len(cos)-1]
	}
	return nil
}
