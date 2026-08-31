// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"

	"github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/mocks"
	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("taskpublisher", func() {
	var (
		ctx       context.Context
		sender    *fakeTaskPublisherSender
		metrics   *mocks.Metrics
		publisher pkg.TaskPublisher
		candidate pkg.Candidate
	)

	BeforeEach(func() {
		ctx = context.Background()
		sender = &fakeTaskPublisherSender{}
		metrics = &mocks.Metrics{}
		candidate = pkg.Candidate{
			Repo: pkg.Repo{
				Owner:         "bborbe",
				Name:          "disk-status",
				DefaultBranch: "main",
			},
			HeadSHA:       "d630ef3526cfc57fbdccd9ba53c5c3a02945e407",
			GoModPresent:  true,
			GoModParsable: true,
			CurrentGo: pkg.Version{
				Major: 1,
				Minor: 24,
				Patch: 0,
				Raw:   "1.24",
			},
			LatestGo: pkg.Version{
				Major: 1,
				Minor: 26,
				Patch: 6,
				Raw:   "1.26.6",
			},
			Consent: filter.GrantedConsent,
		}
	})

	JustBeforeEach(func() {
		publisher = pkg.NewTaskPublisher(sender, metrics, pkg.TaskConfig{Stage: "dev"})
	})

	Describe("PublishCreate", func() {
		Context("on send success", func() {
			It("returns true", func() {
				Expect(publisher.PublishCreate(ctx, candidate)).To(BeTrue())
			})

			It("captures one command", func() {
				publisher.PublishCreate(ctx, candidate)
				Expect(len(sender.SentCommands)).To(Equal(1))
			})

			It("IncPublished called with create", func() {
				publisher.PublishCreate(ctx, candidate)
				Expect(metrics.IncPublishedCallCount()).To(Equal(1))
				label := metrics.IncPublishedArgsForCall(0)
				Expect(label).To(Equal("create"))
			})
		})

		Context("on send error", func() {
			BeforeEach(func() {
				sender.SendError = errors.New("send failed")
			})

			It("returns false", func() {
				Expect(publisher.PublishCreate(ctx, candidate)).To(BeFalse())
			})

			It("IncPublished called with error", func() {
				publisher.PublishCreate(ctx, candidate)
				Expect(metrics.IncPublishedCallCount()).To(Equal(1))
				label := metrics.IncPublishedArgsForCall(0)
				Expect(label).To(Equal("error"))
			})
		})
	})
})

// fakeTaskPublisherSender implements task.CreateCommandSender for testing.
type fakeTaskPublisherSender struct {
	SentCalls    int
	SentCommands []task.CreateCommand
	SendError    error
}

func (f *fakeTaskPublisherSender) SendCommand(
	ctx context.Context,
	cmd task.CreateCommand,
) error {
	f.SentCalls++
	f.SentCommands = append(f.SentCommands, cmd)
	return f.SendError
}
