---
status: completed
container: github-update-go-watcher-003-test-build-info-metrics
dark-factory-version: v0.118.0
created: "2026-04-16T19:40:00Z"
queued: "2026-04-16T17:55:40Z"
started: "2026-04-16T19:57:05Z"
completed: "2026-04-16T19:58:54Z"
branch: test/build-info-metrics
---

<summary>
- `pkg/build-info-metrics.go` has a dedicated Ginkgo test file covering all observable behavior
- Calling `SetBuildInfo` with a concrete build timestamp records that timestamp as a Unix number on the Prometheus gauge
- Calling `SetBuildInfo` with `nil` leaves the gauge value untouched (no panic, no stray write)
- The test uses the standard Ginkgo/Gomega style already used elsewhere in the package
- The gauge value is read via `prometheus/client_golang/prometheus/testutil` so the test is hermetic and does not scrape an HTTP endpoint
- `make precommit` passes cleanly after the new test is added
</summary>

<objective>
Add a Ginkgo/Gomega test file for `pkg/build-info-metrics.go` that exercises `NewBuildInfoMetrics` and `SetBuildInfo` for both the non-nil and nil `buildDate` branches, asserting the Prometheus gauge value via `testutil.ToFloat64`.
</objective>

<context>
Read /workspace/CLAUDE.md for project conventions.

Files to read before making changes:
- pkg/build-info-metrics.go — the code under test. Contains the package-level `buildInfo` gauge, the `BuildInfoMetrics` interface, and `SetBuildInfo` with the `if buildDate == nil { return }` guard.
- pkg/pkg_suite_test.go — the existing Ginkgo suite entry point for the `pkg_test` package. The new test file lives in the same `pkg_test` external test package and relies on this suite runner.
- pkg/handler/sentry-alert_test.go — representative pattern for Ginkgo + Gomega tests in this repo (Describe / BeforeEach / It, `. "github.com/onsi/ginkgo/v2"` and `. "github.com/onsi/gomega"` dot-imports, copyright header).
- go.mod — confirm `github.com/prometheus/client_golang`, `github.com/bborbe/time`, `github.com/onsi/ginkgo/v2`, and `github.com/onsi/gomega` are already available as direct or transitive dependencies.

Prometheus gauge testing: the package-level `buildInfo` gauge is unexported, but it is registered on the default Prometheus registry in `init()`. Read its value from an external test package via `github.com/prometheus/client_golang/prometheus/testutil.ToFloat64(collector)`. Because the test file is in `pkg_test` (external), it cannot address `buildInfo` directly — instead obtain the value through the gauge that `SetBuildInfo` writes to, using a small internal-test-only accessor.

To expose the gauge to the external test package, add one accessor in a new file `pkg/build-info-metrics_export_test.go` that is in the internal `package pkg` and re-exports the gauge as `var BuildInfoGaugeForTest = buildInfo`. Files named `*_test.go` are compiled only during testing, so this accessor is test-only and does not leak into production binaries.
</context>

<requirements>
1. Create `pkg/build-info-metrics_export_test.go` with:
   - Copyright header using year `2026`, matching `pkg/build-info-metrics.go` lines 1-3 exactly.
   - `package pkg` (internal test package).
   - Single declaration: `var BuildInfoGaugeForTest = buildInfo` (exports the unexported gauge for external tests).

2. Create `pkg/build-info-metrics_test.go` with:
   - Copyright header using year `2026`, matching `pkg/build-info-metrics.go` lines 1-3 exactly.
   - `package pkg_test` (external test package, same as other `_test.go` files in this suite).
   - Imports: `time`, `. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`, `libtime "github.com/bborbe/time"`, `"github.com/prometheus/client_golang/prometheus/testutil"`, and `"github.com/bborbe/github-update-go-watcher/pkg"`.
   - A single `Describe("BuildInfoMetrics", ...)` block with these `It` cases:

     a. `It("SetBuildInfo records the build timestamp as Unix time on the gauge", ...)`:
        - Instantiate via `metrics := pkg.NewBuildInfoMetrics()`.
        - Build a `libtime.DateTime` representing a fixed moment, e.g. `buildDate := libtime.DateTime(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))`.
        - Call `metrics.SetBuildInfo(&buildDate)`.
        - Read: `got := testutil.ToFloat64(pkg.BuildInfoGaugeForTest)`.
        - Assert: `Expect(got).To(Equal(float64(buildDate.Unix())))`.

     b. `It("SetBuildInfo does not write when buildDate is nil", ...)`:
        - Instantiate via `metrics := pkg.NewBuildInfoMetrics()`.
        - First, set a known sentinel value: call `metrics.SetBuildInfo(&sentinel)` where `sentinel := libtime.DateTime(time.Date(2020, time.June, 15, 0, 0, 0, 0, time.UTC))`.
        - Capture `before := testutil.ToFloat64(pkg.BuildInfoGaugeForTest)` and assert `Expect(before).To(Equal(float64(sentinel.Unix())))`.
        - Call `metrics.SetBuildInfo(nil)` — this must not panic and must not overwrite the gauge.
        - Assert `Expect(testutil.ToFloat64(pkg.BuildInfoGaugeForTest)).To(Equal(before))`.

     c. `It("NewBuildInfoMetrics returns a non-nil BuildInfoMetrics", ...)`:
        - `Expect(pkg.NewBuildInfoMetrics()).NotTo(BeNil())`.

3. Do NOT modify `pkg/build-info-metrics.go` or any other production source file — the tests must work against the existing public surface plus the one `BuildInfoGaugeForTest` re-export.

4. Do NOT introduce a new Prometheus registry — the existing default registry is fine for this test because the gauge is a singleton owned by the package.

5. Run `make precommit` from the repo root — must pass cleanly with exit code 0.

6. Do NOT change the import ordering convention of the repo — keep standard-library imports first, then third-party, then internal, each group separated by a blank line (see `pkg/handler/sentry-alert_test.go` for the pattern).
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Do NOT touch files outside pkg/
- Existing tests must still pass
- Use github.com/bborbe/errors for any new error wrapping (none expected for a test file)
- Do NOT introduce `fmt.Errorf`
- Do NOT add a new Ginkgo suite entry (`TestSuite`) — the existing `pkg/pkg_suite_test.go` is the single suite runner for this package's external test binary
- Do NOT use `time.Now()` in tests — always use fixed `time.Date(...)` values for determinism
</constraints>

<verification>
Run `make precommit` in the repo root — must pass with exit code 0.

Confirm the new files exist and match the expected structure:
- `ls pkg/build-info-metrics_test.go pkg/build-info-metrics_export_test.go`
- `grep -n "BuildInfoGaugeForTest" pkg/*.go` — two matches: the declaration in `build-info-metrics_export_test.go` and the references in `build-info-metrics_test.go`.
</verification>
