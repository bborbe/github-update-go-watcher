// Copyright (c) 2023 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

package main_test

import (
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("Main", func() {
	It("Compiles", func() {
		var err error
		_, err = gexec.Build(".", "-mod=mod", "-buildvcs=false")
		Expect(err).NotTo(HaveOccurred())
	})

	It("defaults the cursor path to the PVC mount point", func() {
		// pkg is imported to access DefaultCursorPath
		Expect(pkg.DefaultCursorPath).To(Equal("/data/cursor.json"))
	})

	It("composes the terminal-claims consumer into service.Run", func() {
		data, err := os.ReadFile("main.go")
		Expect(err).NotTo(HaveOccurred())
		source := string(data)

		start := strings.Index(source, "service.Run(")
		Expect(start).To(BeNumerically(">=", 0), "service.Run call not found in main.go")
		end := strings.Index(source[start:], "\n\t)")
		Expect(end).To(BeNumerically(">=", 0), "service.Run call is not terminated")
		block := source[start : start+end]

		// Scoped to the call block, not the whole file: a constructor that is
		// declared and tested but never passed to service.Run would otherwise
		// pass, and the fix would be inert in production.
		Expect(block).To(ContainSubstring("NewTerminalClaimsConsumer"),
			"the terminal-claims consumer must be a run.Func passed to service.Run")
	})

	It("does not declare the removed scaffold flags", func() {
		data, err := os.ReadFile("main.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).NotTo(ContainSubstring("DATADIR"))
		Expect(string(data)).NotTo(ContainSubstring("BATCH_SIZE"))
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
}
