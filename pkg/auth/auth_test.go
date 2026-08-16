// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package auth_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg/auth"
)

var _ = Describe("ResolveGitHubClient", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("no credentials set", func() {
		It("returns error mentioning APP_ID", func() {
			creds := auth.Credentials{}
			_, err := auth.ResolveGitHubClient(ctx, creds)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("APP_ID"))
		})
	})

	Context("only AppID set", func() {
		It("returns error naming INSTALLATION_ID and PEM_KEY", func() {
			creds := auth.Credentials{
				AppID: 123,
			}
			_, err := auth.ResolveGitHubClient(ctx, creds)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("INSTALLATION_ID"))
			Expect(err.Error()).To(ContainSubstring("PEM_KEY"))
			// PEM content must not appear in error message
			Expect(err.Error()).ToNot(ContainSubstring("-----"))
		})
	})

	Context("all three set with invalid PEM", func() {
		It("returns an error without containing the PEM bytes", func() {
			creds := auth.Credentials{
				AppID:          123,
				InstallationID: 456,
				PEMKey:         []byte("not-a-valid-pem"),
			}
			_, err := auth.ResolveGitHubClient(ctx, creds)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring("not-a-valid-pem"))
		})
	})
})
