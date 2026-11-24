// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bborbe/agent/command/task"
	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/cqrs/cdb"
	libtime "github.com/bborbe/time"
	libtimetest "github.com/bborbe/time/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/github-update-go-watcher/mocks"
	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("staleIgnorePass", func() {
	var (
		ctx             context.Context
		ghClient        *mocks.GitHubClient
		osvClient       *mocks.OSVClient
		publisher       *mocks.TaskPublisher
		metrics         *mocks.Metrics
		clock           libtime.CurrentDateTime
		pass            pkg.StaleIgnorePass
		cursor          *pkg.Cursor
		repo            pkg.Repo
		allowlist       []string
		latestGo        pkg.Version
		ignoreContents  map[string][]byte
		startNow        libtime.DateTime
		makefileContent string
	)

	BeforeEach(func() {
		ctx = context.Background()
		repo = pkg.Repo{Owner: "bborbe", Name: "run", DefaultBranch: "main"}
		allowlist = []string{repo.Key()}
		latestGo = pkg.Version{Major: 1, Minor: 27, Patch: 2, Raw: "1.27.2"}
		startNow = libtimetest.ParseDateTime("2026-11-24T12:00:00Z")

		ghClient = &mocks.GitHubClient{}
		ghClient.GetHeadSHAReturns("d630ef3526cfc57fbdccd9ba53c5c3a02945e407", nil)
		ghClient.GetGoModReturns([]byte("go 1.24"), nil)
		ghClient.GetMaintainerConfigReturns(filter.GrantedConsent, nil)
		ghClient.GetFileReturns(nil, nil)

		osvClient = &mocks.OSVClient{}
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictLegitimate, nil)

		publisher = &mocks.TaskPublisher{}
		publisher.PublishStaleIgnoreReturns(true)
		metrics = &mocks.Metrics{}

		clock = libtime.NewCurrentDateTime()
		clock.SetNow(startNow)

		pass = pkg.NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, allowlist, clock)
		cursor = &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		ignoreContents = map[string][]byte{}
		makefileContent = ""
	})

	// setSurfaces installs the ignore surfaces the pass will read. A path not in
	// the map is absent.
	setSurfaces := func(contents map[string][]byte) {
		ignoreContents = contents
		ghClient.GetFileStub = func(_ context.Context, _ pkg.Repo, path string) ([]byte, error) {
			return ignoreContents[path], nil
		}
	}

	setMakefile := func(content string) {
		makefileContent = content
		ghClient.GetFileStub = func(_ context.Context, _ pkg.Repo, path string) ([]byte, error) {
			if path == "Makefile" {
				return []byte(makefileContent), nil
			}
			return nil, nil
		}
	}

	scan := func() string {
		return pass.Scan(ctx, cursor, []pkg.Repo{repo}, latestGo)
	}

	state := func() *pkg.RepoState {
		return cursor.Repos[repo.Key()]
	}

	It("AC7: emits nothing when every ignore is legitimate", func() {
		setSurfaces(map[string][]byte{
			"Makefile":           mustRead("testdata/ignore/makefile-continuation.txt"),
			"Makefile.precommit": mustRead("testdata/ignore/makefile-single.txt"),
			".trivyignore":       mustRead("testdata/ignore/trivyignore.txt"),
			".osv-scanner.toml":  mustRead("testdata/ignore/osv-scanner.toml"),
		})
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictLegitimate, nil)

		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(0))
		Expect(ignoreScanCount(metrics, "clean")).To(Equal(1))
		Expect(state().IgnoreStaleIDs).To(BeEmpty())
		Expect(state().IgnoreScannedAt).NotTo(BeEmpty())
	})

	It("AC8: emits exactly one task for one stale ID with the frozen envelope", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)

		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))

		_, candidate, finding, generation := publisher.PublishStaleIgnoreArgsForCall(0)
		Expect(generation).To(Equal(1))
		Expect(finding.IDs).To(Equal([]string{"GO-2026-1111"}))
		Expect(finding.Surfaces["GO-2026-1111"]).To(Equal([]string{"Makefile"}))

		cfg := pkg.TaskConfig{Stage: "dev"}
		cmd := pkg.BuildStaleIgnoreCommand(candidate, finding, generation, cfg)

		Expect(cmd.Title).To(Equal("Remove Stale Ignores bborbe-run"))
		Expect(cmd.Frontmatter["task_type"]).To(Equal("github-update-go"))
		Expect(cmd.Frontmatter["assignee"]).To(Equal("github-update-go-agent"))
		Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
		Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
		Expect(cmd.Frontmatter["stage"]).To(Equal("dev"))
		Expect(cmd.Frontmatter["repo"]).To(Equal("bborbe/run"))
		Expect(cmd.Frontmatter["clone_url"]).To(Equal("git@github.com:bborbe/run.git"))
		Expect(cmd.Frontmatter["ref"]).To(Equal("d630ef3526cfc57fbdccd9ba53c5c3a02945e407"))
		Expect(cmd.Frontmatter["current_go"]).To(Equal("1.24.0"))
		Expect(cmd.Frontmatter["latest_go"]).To(Equal("1.27.2"))
		Expect(cmd.Frontmatter["title"]).To(Equal(cmd.Title))
		Expect(string(cmd.TaskIdentifier)).To(Equal(
			pkg.DeriveStaleIgnoreTaskID("bborbe", "run", generation, finding.IDs).String(),
		))
		Expect(cmd.Body).To(ContainSubstring("GO-2026-1111"))
		Expect(cmd.Body).To(ContainSubstring("Makefile"))
		Expect(cmd.Body).To(ContainSubstring("withdrawn"))
	})

	It("AC8: the built command passes Validate", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)
		Expect(scan()).To(Equal(""))

		_, candidate, finding, generation := publisher.PublishStaleIgnoreArgsForCall(0)
		cmd := pkg.BuildStaleIgnoreCommand(
			candidate,
			finding,
			generation,
			pkg.TaskConfig{Stage: "dev"},
		)
		Expect(cmd.Validate(ctx)).To(Succeed())
	})

	It("AC8: the typed sender stamps target_vault at send time", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)
		Expect(scan()).To(Equal(""))

		_, candidate, finding, generation := publisher.PublishStaleIgnoreArgsForCall(0)
		cmd := pkg.BuildStaleIgnoreCommand(
			candidate,
			finding,
			generation,
			pkg.TaskConfig{Stage: "dev"},
		)

		capture := &capturingCommandObjectSender{}
		sender := factory.CreateCreateCommandSender(capture, "openclaw")
		Expect(sender.SendCommand(ctx, cmd)).To(Succeed())

		var got task.CreateCommand
		Expect(capture.last.Command.Data.MarshalInto(ctx, &got)).To(Succeed())
		Expect(got.TargetVault).To(Equal("openclaw"))
	})

	It("AC9: re-emits after the interval with a new identifier and generation", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)

		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		firstID := emittedID(publisher, 0)
		Expect(state().IgnoreStaleIDs).To(Equal("GO-2026-1111"))
		Expect(state().IgnoreStaleGeneration).To(Equal(1))
		Expect(state().IgnoreStaleEmittedAt).To(Equal(startNow.Format("2006-01-02T15:04:05Z07:00")))

		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(state().IgnoreStaleGeneration).To(Equal(1))

		// Past the rescan throttle but before the re-emit interval: the pass
		// scans again and still emits nothing for the unchanged set.
		clock.SetNow(startNow.Add(25 * libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(state().IgnoreStaleGeneration).To(Equal(1))

		clock.SetNow(startNow.Add(8 * libtime.Day))
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(2))
		secondID := emittedID(publisher, 1)
		Expect(state().IgnoreStaleGeneration).To(Equal(2))
		Expect(secondID).NotTo(Equal(firstID))
	})

	It("AC9: an oscillating set never reuses an identifier", func() {
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)

		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		Expect(scan()).To(Equal(""))
		firstID := emittedID(publisher, 0)

		clock.SetNow(startNow.Add(25 * libtime.Hour))
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111 GO-2026-2222\n")
		Expect(scan()).To(Equal(""))
		secondID := emittedID(publisher, 1)

		clock.SetNow(startNow.Add(50 * libtime.Hour))
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		Expect(scan()).To(Equal(""))
		thirdID := emittedID(publisher, 2)

		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(3))
		Expect(firstID).NotTo(Equal(secondID))
		Expect(secondID).NotTo(Equal(thirdID))
		Expect(thirdID).NotTo(Equal(firstID))
		Expect(state().IgnoreStaleGeneration).To(Equal(3))
	})

	It("AC9: a failed publish leaves every cursor field alone", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)
		publisher.PublishStaleIgnoreReturns(false)

		Expect(scan()).To(Equal(""))
		Expect(state().IgnoreStaleIDs).To(BeEmpty())
		Expect(state().IgnoreStaleGeneration).To(Equal(0))
		Expect(state().IgnoreStaleEmittedAt).To(BeEmpty())
		Expect(state().IgnoreScannedAt).To(BeEmpty())

		// The repo is selected again on the next cycle.
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(2))
	})

	It("AC9: a lost cursor write re-derives the same identifier", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)

		Expect(scan()).To(Equal(""))
		firstID := emittedID(publisher, 0)
		firstGeneration := state().IgnoreStaleGeneration

		// Fresh cursor: the previous write never landed.
		cursor = &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		Expect(scan()).To(Equal(""))
		secondID := emittedID(publisher, 1)
		secondGeneration := state().IgnoreStaleGeneration

		Expect(firstGeneration).To(Equal(1))
		Expect(secondGeneration).To(Equal(1))
		Expect(secondID).To(Equal(firstID))
	})

	It("round-trips the four ignore fields through SaveCursor/LoadCursor", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		original := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				repo.Key(): {
					IgnoreScannedAt:       "2026-11-24T12:00:00Z",
					IgnoreStaleIDs:        "GO-2026-1111,GO-2026-2222",
					IgnoreStaleGeneration: 3,
					IgnoreStaleEmittedAt:  "2026-11-23T12:00:00Z",
				},
			},
		}
		Expect(pkg.SaveCursor(ctx, path, original)).To(Succeed())
		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		loadedState := loaded.Repos[repo.Key()]
		Expect(loadedState.IgnoreScannedAt).To(Equal("2026-11-24T12:00:00Z"))
		Expect(loadedState.IgnoreStaleIDs).To(Equal("GO-2026-1111,GO-2026-2222"))
		Expect(loadedState.IgnoreStaleGeneration).To(Equal(3))
		Expect(loadedState.IgnoreStaleEmittedAt).To(Equal("2026-11-23T12:00:00Z"))
	})

	It("loads a cursor written by the previous binary and selects the repo", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		content := `{"repos":{"` + repo.Key() + `":{"last_seen_head_sha":"abc"}}}`
		Expect(os.WriteFile(path, []byte(content), 0600)).To(Succeed())
		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Repos[repo.Key()].IgnoreScannedAt).To(BeEmpty())

		cursor = loaded
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(1))
	})

	It("AC10: maxIgnoreScansPerCycle binds and leaves the rest unscanned", func() {
		allowlist = nil
		pass = pkg.NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, allowlist, clock)
		repos := make([]pkg.Repo, 0, 12)
		for i := 0; i < 12; i++ {
			repos = append(repos, pkg.Repo{
				Owner:         "bborbe",
				Name:          fmt.Sprintf("repo%02d", i),
				DefaultBranch: "main",
			})
		}
		Expect(pass.Scan(ctx, cursor, repos, latestGo)).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(10))

		unselected := 0
		for _, candidateRepo := range repos {
			if cursor.Repos[candidateRepo.Key()] == nil {
				unselected++
			}
		}
		Expect(unselected).To(Equal(2))
	})

	It("AC10: ignoreRescanInterval binds", func() {
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(1))

		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(1))

		clock.SetNow(startNow.Add(25 * libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(2))
	})

	It("AC10: an ineligible repo still advances its scan timestamp", func() {
		ghClient.GetGoModReturns(nil, nil)
		Expect(scan()).To(Equal(""))
		Expect(state().IgnoreScannedAt).NotTo(BeEmpty())
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(1))

		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(1))
	})

	It("skips a repo whose consent is not granted and advances its timestamp", func() {
		ghClient.GetMaintainerConfigReturns(filter.RefusedConsent, nil)
		Expect(scan()).To(Equal(""))
		Expect(state().IgnoreScannedAt).NotTo(BeEmpty())
		Expect(ghClient.GetFileCallCount()).To(Equal(0))
	})

	It("never gathers a repo outside the allowlist", func() {
		allowlist = []string{"github.com/other/repo"}
		pass = pkg.NewStaleIgnorePass(ghClient, osvClient, publisher, metrics, allowlist, clock)
		Expect(scan()).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(0))
		Expect(cursor.Repos).To(BeEmpty())
	})

	It("a dropped repo leaves its state alone and is retried later", func() {
		ghClient.GetFileStub = func(_ context.Context, _ pkg.Repo, path string) ([]byte, error) {
			if path == ".trivyignore" {
				return nil, os.ErrPermission
			}
			return nil, nil
		}
		Expect(scan()).To(Equal(""))
		Expect(ignoreScanCount(metrics, "error")).To(Equal(1))
		Expect(state().IgnoreScannedAt).To(BeEmpty())

		setSurfaces(nil)
		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(state().IgnoreScannedAt).NotTo(BeEmpty())
	})

	It("unresolved never emits and is not cached", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictUnresolved, nil)

		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(0))
		Expect(ignoreScanCount(metrics, "unresolved")).To(Equal(1))
		Expect(cursor.OSVVerdicts).NotTo(HaveKey("GO-2026-1111"))
	})

	It("an unresolved verdict keeps the recorded stale set so a retry does not re-emit", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		emitted := state().IgnoreStaleIDs
		generation := state().IgnoreStaleGeneration
		Expect(emitted).To(Equal("GO-2026-1111"))

		// A transient OSV failure on the next scan must not look like the
		// set shrinking: the record survives, so the retry below is a no-op.
		cursor.OSVVerdicts = map[string]*pkg.OSVVerdictEntry{}
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictUnresolved, nil)
		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(state().IgnoreStaleIDs).To(Equal(emitted))
		Expect(state().IgnoreStaleGeneration).To(Equal(generation))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))

		// The same set resolving again is still unchanged, so still no emit.
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)
		clock.SetNow(startNow.Add(2 * libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(state().IgnoreStaleGeneration).To(Equal(generation))
	})

	It("a partial unresolved result does not emit a shrunk set", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111 GO-2026-2222\n")
		osvClient.CheckWithdrawnStub = func(_ context.Context, id string) (pkg.OSVVerdict, error) {
			return pkg.OSVVerdictWithdrawn, nil
		}
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(state().IgnoreStaleIDs).To(Equal("GO-2026-1111,GO-2026-2222"))

		// GO-2026-2222 becomes unclassifiable: every ID we could still prove
		// stale is already recorded, so the set has not shrunk.
		cursor.OSVVerdicts = map[string]*pkg.OSVVerdictEntry{}
		osvClient.CheckWithdrawnStub = func(_ context.Context, id string) (pkg.OSVVerdict, error) {
			if id == "GO-2026-2222" {
				return pkg.OSVVerdictUnresolved, nil
			}
			return pkg.OSVVerdictWithdrawn, nil
		}
		clock.SetNow(startNow.Add(libtime.Hour))
		Expect(scan()).To(Equal(""))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(state().IgnoreStaleIDs).To(Equal("GO-2026-1111,GO-2026-2222"))
	})

	It("reuses a fresh cached verdict without calling OSV", func() {
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		cursor.OSVVerdicts = map[string]*pkg.OSVVerdictEntry{
			"GO-2026-1111": {
				Withdrawn: false,
				CheckedAt: startNow.Format("2006-01-02T15:04:05Z07:00"),
			},
		}

		Expect(scan()).To(Equal(""))
		Expect(osvClient.CheckWithdrawnCallCount()).To(Equal(0))
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(0))
		Expect(ignoreScanCount(metrics, "clean")).To(Equal(1))
	})

	It("returns an empty reason on a cancelled context", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		Expect(pass.Scan(cancelled, cursor, []pkg.Repo{repo}, latestGo)).To(Equal(""))
		Expect(ghClient.GetHeadSHACallCount()).To(Equal(0))
	})

	It("wiring: a full Poll runs the pass and publishes one stale-ignore task", func() {
		reg := prometheus.NewRegistry()
		realMetrics := pkg.NewMetrics(reg)
		cursorPath := filepath.Join(GinkgoT().TempDir(), "cursor.json")

		goDevClient := &mocks.GoDevClient{}
		goDevClient.LatestStableReturns(latestGo, nil)

		ghClient.ListReposReturns([]pkg.Repo{repo}, nil)
		ghClient.GetGoModReturns([]byte("go 1.27.2"), nil)
		ghClient.HasOpenUpdatePRReturns(false, nil)
		setMakefile("VULNCHECK_IGNORE = GO-2026-1111\n")
		osvClient.CheckWithdrawnReturns(pkg.OSVVerdictWithdrawn, nil)

		publisher.PublishCreateReturns(true)
		completeSender := new(taskmocks.TaskCompleteCommandSender)
		terminalClaims := &mocks.TerminalClaims{}
		terminalClaims.ClearableReturns(false, nil)

		w := pkg.NewWatcher(
			ghClient,
			goDevClient,
			publisher,
			completeSender,
			realMetrics,
			cursorPath,
			"bborbe",
			filter.TaskCreationFilterList{
				filter.NewRepoAllowlistFilter(allowlist),
				filter.NewGoModPresentFilter(),
				filter.NewGoModParsableFilter(),
				filter.NewGoBehindFilter(),
				filter.NewAutoUpdateFilter(),
			},
			clock,
			terminalClaims,
			osvClient,
			allowlist,
		)
		Expect(w.Poll(ctx, false)).To(Succeed())
		Expect(publisher.PublishStaleIgnoreCallCount()).To(Equal(1))
		Expect(publisher.PublishCreateCallCount()).To(Equal(0))
		Expect(metricValue(reg, "github_update_go_watcher_poll_cycle_total", "result", "success")).
			To(Equal(1.0))
	})
})

func mustRead(path string) []byte {
	content, err := os.ReadFile(path)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return content
}

func ignoreScanCount(metrics *mocks.Metrics, result string) int {
	count := 0
	for i := 0; i < metrics.IncIgnoreScanCallCount(); i++ {
		if metrics.IncIgnoreScanArgsForCall(i) == result {
			count++
		}
	}
	return count
}

func emittedID(publisher *mocks.TaskPublisher, index int) string {
	_, candidate, finding, generation := publisher.PublishStaleIgnoreArgsForCall(index)
	return pkg.DeriveStaleIgnoreTaskID(
		candidate.Repo.Owner,
		candidate.Repo.Name,
		generation,
		finding.IDs,
	).String()
}

func metricValue(
	reg *prometheus.Registry,
	name string,
	labelName string,
	labelValue string,
) float64 {
	families, err := reg.Gather()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == labelName && label.GetValue() == labelValue {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return -1
}

// capturingCommandObjectSender records the last CommandObject it is asked to
// send. Copied from pkg/factory/factory_test.go:146 because that type lives in
// another package and cannot be imported.
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
