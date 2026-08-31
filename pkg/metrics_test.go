// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("Metrics", func() {
	var reg *prometheus.Registry

	BeforeEach(func() {
		reg = prometheus.NewRegistry()
	})

	Describe("NewMetrics", func() {
		It("registers all four metric families", func() {
			m := pkg.NewMetrics(reg)
			Expect(m).NotTo(BeNil())

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			names := make([]string, len(families))
			for i, f := range families {
				names[i] = f.GetName()
			}
			Expect(names).To(ContainElement("github_update_go_watcher_poll_cycle_total"))
			Expect(names).To(ContainElement("github_update_go_watcher_published_total"))
			Expect(names).To(ContainElement("github_update_go_watcher_repos_scanned_total"))
			Expect(names).To(ContainElement("github_update_go_watcher_filter_skipped_total"))
		})

		It("pre-initialises all PollCycleResults label values to 0", func() {
			pkg.NewMetrics(reg)
			collectAndCount := testutil.CollectAndCount(reg)
			Expect(collectAndCount).To(BeNumerically(">=", 4))
		})

		It("pre-initialises all PublishStatuses label values to 0", func() {
			pkg.NewMetrics(reg)
			collectAndCount := testutil.CollectAndCount(reg)
			Expect(collectAndCount).To(BeNumerically(">=", 4))
		})

		It("pre-initialises all FilterSkipReasons label values to 0", func() {
			pkg.NewMetrics(reg)
			collectAndCount := testutil.CollectAndCount(reg)
			Expect(collectAndCount).To(BeNumerically(">=", 4))
		})

		It("IncFilterSkipped moves the correct series to 1", func() {
			m := pkg.NewMetrics(reg)
			m.IncFilterSkipped("scope")

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_filter_skipped_total" {
					for _, m := range f.GetMetric() {
						for _, label := range m.GetLabel() {
							if label.GetName() == "reason" && label.GetValue() == "scope" {
								Expect(m.GetCounter().GetValue()).To(Equal(1.0))
								return
							}
						}
					}
				}
			}
			Fail("filter_skipped_total metric or scope label not found")
		})

		It("all other filter skip reasons remain at 0 after IncFilterSkipped(scope)", func() {
			m := pkg.NewMetrics(reg)
			m.IncFilterSkipped("scope")

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_filter_skipped_total" {
					for _, m := range f.GetMetric() {
						for _, label := range m.GetLabel() {
							if label.GetName() == "reason" && label.GetValue() != "scope" {
								Expect(m.GetCounter().GetValue()).To(Equal(0.0))
							}
						}
					}
				}
			}
		})

		It("pre-initialises auto_update_undecided to 0", func() {
			pkg.NewMetrics(reg)

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_filter_skipped_total" {
					for _, metric := range f.GetMetric() {
						for _, label := range metric.GetLabel() {
							if label.GetName() == "reason" &&
								label.GetValue() == "auto_update_undecided" {
								Expect(metric.GetCounter().GetValue()).To(Equal(0.0))
								return
							}
						}
					}
				}
			}
			Fail("filter_skipped_total metric or auto_update_undecided label not found")
		})

		It("two distinct registries both succeed (no package-level registration)", func() {
			reg2 := prometheus.NewRegistry()
			Expect(func() {
				_ = pkg.NewMetrics(reg)
				_ = pkg.NewMetrics(reg2)
			}).NotTo(Panic())
		})
	})

	Describe("Metrics interface", func() {
		It("IncPollCycle records the result label", func() {
			m := pkg.NewMetrics(reg)
			m.IncPollCycle("success")

			families, _ := reg.Gather()
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_poll_cycle_total" {
					for _, m := range f.GetMetric() {
						for _, label := range m.GetLabel() {
							if label.GetName() == "result" && label.GetValue() == "success" {
								Expect(m.GetCounter().GetValue()).To(Equal(1.0))
								return
							}
						}
					}
				}
			}
			Fail("poll_cycle_total metric or success label not found")
		})

		It("IncPublished records the status label", func() {
			m := pkg.NewMetrics(reg)
			m.IncPublished("create")

			families, _ := reg.Gather()
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_published_total" {
					for _, m := range f.GetMetric() {
						for _, label := range m.GetLabel() {
							if label.GetName() == "status" && label.GetValue() == "create" {
								Expect(m.GetCounter().GetValue()).To(Equal(1.0))
								return
							}
						}
					}
				}
			}
			Fail("published_total metric or create label not found")
		})

		It("IncReposScanned increments the counter", func() {
			m := pkg.NewMetrics(reg)
			m.IncReposScanned(5)

			families, _ := reg.Gather()
			for _, f := range families {
				if f.GetName() == "github_update_go_watcher_repos_scanned_total" {
					Expect(f.GetMetric()[0].GetCounter().GetValue()).To(Equal(5.0))
					return
				}
			}
			Fail("repos_scanned_total metric not found")
		})
	})
})
