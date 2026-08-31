---
status: completed
spec: [002-consent-tristate-undecided]
summary: Introduced tri-state filter.Consent (granted/refused/undecided) with filter.ParseConsent reading .maintainer.yaml as a raw yaml.Node, rewired GetMaintainerConfig to return filter.Consent, collapsed gatherCandidate's AutoUpdate to consent==GrantedConsent, and mechanically retargeted all tests
execution_id: github-update-go-watcher-tristate-exec-007-spec-002-consent-parsing
dark-factory-version: dev
created: "2026-08-31T10:33:16Z"
queued: "2026-08-31T11:22:11Z"
started: "2026-08-31T11:26:17Z"
completed: "2026-08-31T11:33:47Z"
---

<summary>

- The watcher's read of a repo's `.maintainer.yaml` consent file becomes three-valued instead of a plain yes/no.
- A new, independent "granted / refused / undecided" type is introduced and is the single place that decides what counts as consent.
- Reading the raw YAML now distinguishes "the owner said no" from "nobody has ever said anything" — today both collapse into the same false value.
- An unparsable `.maintainer.yaml` keeps behaving exactly as it does today: the repo is dropped from the cycle before any consent verdict is produced, never silently read as a decision.
- No behavior visible outside this prompt changes yet — the filter chain, the emitted tasks and the skip reasons are all identical to today after this prompt lands. This prompt only replaces what the "consent" value IS; the next prompt changes what happens with it.
- New unit tests prove that nothing except an explicit YAML boolean `true` can ever be read as granted — not a string `"true"`, not an integer `1`, not `yes`, not an absent file.
- Existing tests are mechanically retargeted to the new type with zero change to what they assert.

</summary>

<objective>

Introduce a tri-state `Consent` type (`granted` / `refused` / `undecided`) in `pkg/filter`, plus a `ParseConsent` function that reads raw `.maintainer.yaml` bytes and returns one of the three verdicts without ever reading absence as consent. Wire `pkg/githubclient.go`'s `GetMaintainerConfig` to return this new type instead of the shared `maintainerconfig.MaintainerConfig` struct, preserving the existing "malformed YAML is a drop, not a verdict" contract exactly. This prompt does not change any filter, watcher-branching, or task-emission behavior — `pkg.Candidate.AutoUpdate` stays a `bool` for now, computed as `consent == filter.GrantedConsent`, so every existing observable behavior is unchanged after this prompt.

</objective>

<context>

Read before editing:
- `pkg/filter/filter.go` — the `Candidate` struct and package doc this prompt's new type will later plug into (not modified by this prompt, only referenced).
- `pkg/githubclient.go` — `GetMaintainerConfig`'s exact current implementation and doc comment (both quoted in full in Requirements below — verified against this exact file).
- `pkg/githubclient_test.go` — the existing `Describe("GetMaintainerConfig", ...)` block (5 `Context`s: "404 not found", "valid config with autoUpdate true", "empty document (no goUpdate block)", "malformed YAML", "rate-limited response").
- `pkg/watcher.go` — `gatherCandidate`'s exact current body (quoted in full below).
- `pkg/watcher_test.go` — the full file. It has exactly 18 occurrences of `maintainerconfig.MaintainerConfig{...}` (or the bare zero value) passed to `GetMaintainerConfigReturns`, enumerated exhaustively in Requirements below.
- `github.com/bborbe/maintainer@v0.50.2/maintainerconfig/maintainerconfig.go` (in `$(go env GOMODCACHE)`) — confirms `GoUpdateConfig struct { AutoUpdate bool \`yaml:"autoUpdate"\` }` is a plain-bool decode with no way to distinguish "key absent" from "key present and false". This package is a shared, externally-owned schema consumed by other independently-deployed bots and must not be modified or extended.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` — the exact typed-constant + `Available*` collection + `Validate` pattern this prompt's `Consent` type must follow.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping idioms used throughout this repo.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-validation-framework-guide.md` — Ginkgo/Gomega and `Validate(ctx)` conventions already in use in this repo.

</context>

<requirements>

## 1. New file `pkg/filter/consent.go`

Follow the go-enum-type-pattern.md structure exactly (typed newtype, `<Value><TypeName>`-named constants, `Available*` collection, `String()`, `Validate(ctx)` via `Contains`, plural collection type with `Contains`).

```go
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
```

## 2. New file `pkg/filter/consent_test.go`

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

var _ = Describe("Consent", func() {
	Describe("String", func() {
		It("returns the raw value", func() {
			Expect(filter.GrantedConsent.String()).To(Equal("granted"))
		})
	})

	Describe("Validate", func() {
		It("accepts every AvailableConsents member", func() {
			ctx := context.Background()
			for _, c := range filter.AvailableConsents {
				Expect(c.Validate(ctx)).To(Succeed())
			}
		})

		It("rejects an unknown value", func() {
			ctx := context.Background()
			Expect(filter.Consent("bogus").Validate(ctx)).To(HaveOccurred())
		})

		It("rejects the zero value", func() {
			ctx := context.Background()
			Expect(filter.Consent("").Validate(ctx)).To(HaveOccurred())
		})
	})
})

var _ = Describe("ParseConsent", func() {
	Describe("three-outcome matrix", func() {
		DescribeTable("returns the correct verdict",
			func(content []byte, expected filter.Consent) {
				ctx := context.Background()
				consent, err := filter.ParseConsent(ctx, content)
				Expect(err).NotTo(HaveOccurred())
				Expect(consent).To(Equal(expected))
			},
			Entry("explicit true",
				[]byte("goUpdate:\n  autoUpdate: true"), filter.GrantedConsent),
			Entry("explicit false",
				[]byte("goUpdate:\n  autoUpdate: false"), filter.RefusedConsent),
			Entry("absent file",
				[]byte(nil), filter.UndecidedConsent),
			Entry("absent goUpdate section",
				[]byte("other: value"), filter.UndecidedConsent),
			Entry("absent autoUpdate key",
				[]byte("goUpdate:\n  somethingElse: true"), filter.UndecidedConsent),
		)
	})

	// NOTE FOR HUMAN AUDIT (open question, resolve at prompt-audit time):
	// spec 002 AC8's evidence text reads "each asserts a non-empty skip
	// reason" for a table that includes "malformed YAML" as one of its 8
	// rows. ParseConsent operates BELOW the filter/skip-reason layer -- it
	// returns a (Consent, error) pair, not a skip-reason string -- and
	// spec 002 AC7 requires this exact malformed-YAML input to produce a
	// DROP (IncFilterSkipped call count 0), not any skip reason at all.
	// Read literally at this layer, AC7 and AC8 would contradict each
	// other for the identical input. The table below resolves the tension
	// by reading AC8's "non-empty skip reason" as "the parse outcome is
	// not granted": every non-malformed row asserts a Consent other than
	// GrantedConsent with a nil error (which becomes skip reason
	// auto_update_undecided once it reaches the filter chain, wired in a
	// later prompt), and the malformed row asserts a non-nil error (which
	// becomes a drop with step maintainer_config in gatherCandidate,
	// never reaching the filter chain at all -- consistent with AC7). If a
	// different reading of AC8 was intended, flag this table for revision.
	Describe("never grants except an explicit boolean true (AC8, Security)", func() {
		DescribeTable("outcome is never granted",
			func(content []byte, expectError bool) {
				ctx := context.Background()
				consent, err := filter.ParseConsent(ctx, content)

				Expect(consent).NotTo(Equal(filter.GrantedConsent))

				if expectError {
					Expect(err).To(HaveOccurred())
					return
				}
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("absent file", []byte(nil), false),
			Entry("absent goUpdate section", []byte("other: value"), false),
			Entry("absent autoUpdate key",
				[]byte("goUpdate:\n  somethingElse: true"), false),
			Entry("autoUpdate as string \"true\"",
				[]byte("goUpdate:\n  autoUpdate: \"true\""), false),
			Entry("autoUpdate as integer 1",
				[]byte("goUpdate:\n  autoUpdate: 1"), false),
			Entry("autoUpdate as yes",
				[]byte("goUpdate:\n  autoUpdate: yes"), false),
			Entry("autoUpdate as explicit null",
				[]byte("goUpdate:\n  autoUpdate: null"), false),
			Entry("malformed YAML",
				[]byte("goUpdate:\n  autoUpdate: ["), true),
		)
	})
})
```

## 3. `pkg/githubclient.go` — change `GetMaintainerConfig`'s return type

- Remove the import `"github.com/bborbe/maintainer/maintainerconfig"`.
- Add the import `"github.com/bborbe/github-update-go-watcher/pkg/filter"`.
- Change the `GitHubClient` interface method signature from:
  ```go
  GetMaintainerConfig(ctx context.Context, repo Repo) (maintainerconfig.MaintainerConfig, error)
  ```
  to:
  ```go
  GetMaintainerConfig(ctx context.Context, repo Repo) (filter.Consent, error)
  ```
  Update the method's doc comment (currently the block starting `// GetMaintainerConfig returns the parsed...`) to describe the new tri-state contract:
  ```go
  // GetMaintainerConfig returns the parsed consent verdict from
  // `.maintainer.yaml` at HEAD of repo's default branch (spec 002 Desired
  // Behavior 1). This file is the repo owner's consent record.
  //
  //   - (filter.GrantedConsent, nil) — autoUpdate is explicitly boolean true.
  //   - (filter.RefusedConsent, nil) — autoUpdate is explicitly boolean false.
  //   - (filter.UndecidedConsent, nil) — the file is absent (HTTP 404), the
  //     goUpdate section is absent, the autoUpdate key is absent, or the key
  //     holds any non-boolean value.
  //   - (filter.Consent(""), ErrRateLimited) on primary or abuse rate
  //     limiting.
  //   - (filter.Consent(""), wrapped error) on every other failure including
  //     5xx, oversize files, base64 decode failures, and YAML parse
  //     failures.
  //
  // Malformed YAML MUST NOT be silently treated as UndecidedConsent — it is
  // an error so the repo is dropped from the cycle rather than recorded as a
  // consent verdict.
  GetMaintainerConfig(ctx context.Context, repo Repo) (filter.Consent, error)
  ```
- Rewrite the `(*githubClient) GetMaintainerConfig` implementation, replacing every `maintainerconfig.MaintainerConfig{}` return with the correct `filter.Consent` value and swapping the `maintainerconfig.Parse` call for `filter.ParseConsent`:
  ```go
  func (c *githubClient) GetMaintainerConfig(
  	ctx context.Context,
  	repo Repo,
  ) (filter.Consent, error) {
  	opts := &gogithub.RepositoryContentGetOptions{Ref: repo.DefaultBranch}
  	fileContent, _, _, err := c.client.Repositories.GetContents(
  		ctx, repo.Owner, repo.Name, ".maintainer.yaml", opts,
  	)
  	if err != nil {
  		if isNotFound(err) {
  			return filter.UndecidedConsent, nil
  		}
  		if isRateLimitError(err) {
  			return filter.Consent(""), ErrRateLimited
  		}
  		return filter.Consent(""), errors.Wrapf(
  			ctx, err, "get .maintainer.yaml %s ref=%s", repo.String(), repo.DefaultBranch,
  		)
  	}
  	if fileContent == nil {
  		return filter.UndecidedConsent, nil
  	}
  	if fileContent.GetSize() > maxContentBytes {
  		return filter.Consent(""), errors.Errorf(
  			ctx, ".maintainer.yaml %s too large: %d bytes (max %d)",
  			repo.String(), fileContent.GetSize(), maxContentBytes,
  		)
  	}
  	decoded, err := fileContent.GetContent()
  	if err != nil {
  		return filter.Consent(""), errors.Wrapf(
  			ctx, err, "decode .maintainer.yaml %s", repo.String(),
  		)
  	}
  	if len(decoded) > maxContentBytes {
  		return filter.Consent(""), errors.Errorf(
  			ctx, ".maintainer.yaml %s decoded too large: %d bytes (max %d)",
  			repo.String(), len(decoded), maxContentBytes,
  		)
  	}
  	consent, err := filter.ParseConsent(ctx, []byte(decoded))
  	if err != nil {
  		return filter.Consent(""), errors.Wrapf(
  			ctx, err, "parse .maintainer.yaml %s", repo.String(),
  		)
  	}
  	return consent, nil
  }
  ```

The mock `mocks/github_client.go` is fully regenerated by `make precommit`'s `generate` step (counterfeiter, driven by the `//counterfeiter:generate` directive already present above the `GitHubClient` interface) — do not hand-edit any file under `mocks/`.

## 4. `pkg/githubclient_test.go` — rewrite `Describe("GetMaintainerConfig", ...)`

Add the import `"github.com/bborbe/github-update-go-watcher/pkg/filter"` to the existing import block.

Update each of the 5 existing `Context`s' final assertion to check the returned `filter.Consent` directly instead of `cfg.GoUpdate.AutoUpdate`:

- `Context("404 not found", ...)`: replace
  ```go
  cfg, err := client.GetMaintainerConfig(ctx, repo)
  Expect(err).To(Succeed())
  Expect(cfg.GoUpdate.AutoUpdate).To(BeFalse())
  ```
  with
  ```go
  consent, err := client.GetMaintainerConfig(ctx, repo)
  Expect(err).To(Succeed())
  Expect(consent).To(Equal(filter.UndecidedConsent))
  ```
- `Context("valid config with autoUpdate true", ...)`: same rename, final assertion becomes `Expect(consent).To(Equal(filter.GrantedConsent))`.
- `Context("empty document (no goUpdate block)", ...)`: same rename, final assertion becomes `Expect(consent).To(Equal(filter.UndecidedConsent))`.
- `Context("malformed YAML", ...)`: replace
  ```go
  cfg, err := client.GetMaintainerConfig(ctx, repo)
  Expect(err).To(HaveOccurred())
  Expect(cfg.GoUpdate.AutoUpdate).To(BeFalse())
  ```
  with
  ```go
  consent, err := client.GetMaintainerConfig(ctx, repo)
  Expect(err).To(HaveOccurred())
  Expect(consent).To(Equal(filter.Consent("")))
  ```
- `Context("rate-limited response", ...)`: unchanged (it never inspects the returned value, only `errors.Is(err, pkg.ErrRateLimited)`); rename its discarded first return value only if the current code names it (it currently uses `_, err := ...`, so no change needed there).

Add one new `Context`, sibling to the existing 5, filling a gap the existing suite does not cover — an explicit `autoUpdate: false`:

```go
Context("explicit autoUpdate false", func() {
	BeforeEach(func() {
		encoded := base64.StdEncoding.EncodeToString(
			[]byte("goUpdate:\n  autoUpdate: false"),
		)
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]interface{}{
				"type":     "file",
				"encoding": "base64",
				"size":     len(encoded),
				"content":  encoded,
			}
			json.NewEncoder(w).Encode(resp)
		})
		server = httptest.NewServer(handler)
		client = pkg.NewGitHubClient(server.Client())
		err := pkg.SetBaseURL(client, server.URL+"/")
		Expect(err).To(Succeed())
	})

	AfterEach(func() {
		server.Close()
	})

	It("returns refused consent and nil error", func() {
		repo := pkg.Repo{Owner: "myowner", Name: "myrepo", DefaultBranch: "main"}
		consent, err := client.GetMaintainerConfig(ctx, repo)
		Expect(err).To(Succeed())
		Expect(consent).To(Equal(filter.RefusedConsent))
	})
})
```

This new `Context` must be placed inside the existing `Describe("GetMaintainerConfig", func() { ... })` block, following the same `httptest.Server` + `pkg.SetBaseURL` pattern already used by the sibling `Context`s in this file.

## 5. `pkg/watcher.go` — `gatherCandidate` interim collapse (Candidate.AutoUpdate stays bool in this prompt)

Current body (verified):
```go
cfg, err := w.ghClient.GetMaintainerConfig(ctx, repo)
if err != nil {
	if stderrors.Is(err, ErrRateLimited) {
		return Candidate{}, "rate_limited", false
	}
	return dropRepo(repo, "maintainer_config", err)
}

candidate := Candidate{
	Repo:       repo,
	HeadSHA:    headSHA,
	LatestGo:   latestGo,
	AutoUpdate: cfg.GoUpdate.AutoUpdate,
}
```
Replace with:
```go
consent, err := w.ghClient.GetMaintainerConfig(ctx, repo)
if err != nil {
	if stderrors.Is(err, ErrRateLimited) {
		return Candidate{}, "rate_limited", false
	}
	return dropRepo(repo, "maintainer_config", err)
}

candidate := Candidate{
	Repo:     repo,
	HeadSHA:  headSHA,
	LatestGo: latestGo,
	// Candidate.AutoUpdate stays a bool in this prompt (renamed to a
	// filter.Consent field in a later prompt). This collapse is a
	// deliberately temporary bridge so nothing observable changes yet.
	AutoUpdate: consent == filter.GrantedConsent,
}
```
`filter` is already imported in this file (`"github.com/bborbe/github-update-go-watcher/pkg/filter"`), so no import change is needed here.

## 6. `pkg/watcher_test.go` — mechanical retargeting (zero behavior change)

Remove the import `"github.com/bborbe/maintainer/maintainerconfig"` from the import block once every use below is converted (the `filter` import already present at `"github.com/bborbe/github-update-go-watcher/pkg/filter"` stays; the stdlib `"errors"` import stays, it is still used by the AC11 Context and others).

There are exactly 18 places in this file that pass a `maintainerconfig.MaintainerConfig{...}` value (or its bare zero value) to `GetMaintainerConfigReturns`. Convert every one, verbatim, per this table (anchor by the named `Context`/`DescribeTable`/`It`, not by line number — the file changes shape as you edit it):

| Location | Old first argument | New first argument |
|---|---|---|
| `Context("AC5 consent gate")`, `DescribeTable("consent matrix")`, `Entry("maintainer file absent", ...)` | `maintainerconfig.MaintainerConfig{}` | `filter.UndecidedConsent` |
| same table, `Entry("goUpdate section absent", ...)` | `maintainerconfig.MaintainerConfig{}` | `filter.UndecidedConsent` |
| same table, `Entry("autoUpdate key absent", ...)` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{}}` | `filter.UndecidedConsent` |
| same table, `Entry("autoUpdate false", ...)` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: false}}` | `filter.RefusedConsent` |
| same table, `Entry("autoUpdate true", ...)` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true}}` | `filter.GrantedConsent` |
| `Context("AC6 version table")` `BeforeEach` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true}}` | `filter.GrantedConsent` |
| `Context("AC7 LatestStable called exactly once per cycle")` `BeforeEach` | same as above | `filter.GrantedConsent` |
| `Context("AC9 rate limit preserves cursor")`, `DescribeTable("rate limit at each step")`, `Entry("GetMaintainerConfig", ...)` | `maintainerconfig.MaintainerConfig{}, pkg.ErrRateLimited` | `filter.Consent(""), pkg.ErrRateLimited` |
| `Context("AC10 per-repo drop logs and continues")` `BeforeEach` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true}}` | `filter.GrantedConsent` |
| `Context("AC11 unparsable maintainer config is not auto_update_disabled")` `BeforeEach` | `maintainerconfig.MaintainerConfig{}, errors.New("parse error")` | `filter.Consent(""), errors.New("parse error")` |
| `Context("AC12 cursor records HEAD and skips on re-run")` `BeforeEach` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true}}` | `filter.GrantedConsent` |
| `Context("forced cycle bypasses SHAUnchangedFilter")` `BeforeEach` | same as above | `filter.GrantedConsent` |
| same `Context`, `It("force=true still respects consent gate")` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: false}}` | `filter.RefusedConsent` |
| `Context("publish failure still ends with success")` `BeforeEach` | `maintainerconfig.MaintainerConfig{GoUpdate: maintainerconfig.GoUpdateConfig{AutoUpdate: true}}` | `filter.GrantedConsent` |
| `Context("cancellation mid-cycle")` `BeforeEach` | same as above | `filter.GrantedConsent` |
| `Context("metric label containment")`, `It("IncPollCycle labels are in PollCycleResults")` | same as above | `filter.GrantedConsent` |
| same `Context`, `It("IncFilterSkipped labels are in FilterSkipReasons")` | same as above | `filter.GrantedConsent` |
| `Context("merge-detection auto-completes task on merged update PR")` `BeforeEach` | same as above | `filter.GrantedConsent` |

`Context("corrupt cursor cold-starts")` and `Context("AC8 goDevClient error aborts before ListRepos")` have no `GetMaintainerConfigReturns` call — leave untouched.

Also rewrite the `DescribeTable("consent matrix", ...)` function signature and body inside `Context("AC5 consent gate")` — this is a pure, outcome-preserving type conversion in THIS prompt (the reason-string behavior itself changes in a later prompt):

```go
DescribeTable("consent matrix",
	func(consent filter.Consent, expectPublish bool) {
		ghClient.GetMaintainerConfigReturns(consent, nil)
		publisher.PublishCreateReturns(true)
		metrics.IncFilterSkippedStub = func(string) {}

		err := watcher.Poll(context.Background(), false)
		Expect(err).NotTo(HaveOccurred())

		if expectPublish {
			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
		} else {
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			if consent != filter.GrantedConsent {
				Expect(metrics.IncFilterSkippedCallCount()).To(BeNumerically(">", 0))
				arg := metrics.IncFilterSkippedArgsForCall(0)
				Expect(arg).To(Equal("auto_update_disabled"))
			}
		}
	},
	Entry("maintainer file absent", filter.UndecidedConsent, false),
	Entry("goUpdate section absent", filter.UndecidedConsent, false),
	Entry("autoUpdate key absent", filter.UndecidedConsent, false),
	Entry("autoUpdate false", filter.RefusedConsent, false),
	Entry("autoUpdate true", filter.GrantedConsent, true),
)
```
This is intentionally faithful to today's actual runtime behavior (the `AutoUpdateFilter` has not been touched by this prompt, so every non-granted consent still produces `"auto_update_disabled"`, not `"auto_update_undecided"`) — a later prompt restructures this exact table a second time once the filter itself changes.

Tighten `Context("AC11 unparsable maintainer config is not auto_update_disabled")`'s `It("no publish and no auto_update_disabled recorded", ...)` to the strict assertion spec 002's AC7 evidence actually requires (call count 0, not "never happened to be auto_update_disabled"):

Replace:
```go
It("no publish and no auto_update_disabled recorded", func() {
	err := watcher.Poll(context.Background(), false)
	Expect(err).NotTo(HaveOccurred())
	Expect(publisher.PublishCreateCallCount()).To(Equal(0))
	// IncFilterSkipped should not have been called with auto_update_disabled
	for i := 0; i < metrics.IncFilterSkippedCallCount(); i++ {
		arg := metrics.IncFilterSkippedArgsForCall(i)
		Expect(arg).NotTo(Equal("auto_update_disabled"))
	}
})
```
with:
```go
It("no publish and IncFilterSkipped never called (drop, not a skip verdict)", func() {
	err := watcher.Poll(context.Background(), false)
	Expect(err).NotTo(HaveOccurred())
	Expect(publisher.PublishCreateCallCount()).To(Equal(0))
	// AC7 (spec 002): an unparsable .maintainer.yaml is dropped before the
	// filter chain ever runs -- IncFilterSkipped must not be called at
	// all, not merely called with some other reason.
	Expect(metrics.IncFilterSkippedCallCount()).To(Equal(0))
})
```

</requirements>

<constraints>

- Weakening the consent gate is out of scope. Absent consent must still never file an update task and never cause any write to the observed repo.
- Defaulting absent consent to true, under any flag, env var, or code path, is forbidden.
- Behaviour for `autoUpdate: true` and explicit `autoUpdate: false` must remain unchanged by this prompt.
- No new configuration list, no flag, no env var. The set of repos evaluated is unchanged.
- Filter chain order stays frozen: 1 scope, 2 no_gomod, 3 gomod_unparsable, 4 go_current, 5 auto_update_disabled, 6 sha_unchanged. This prompt does not touch the chain at all.
- An unparsable `.maintainer.yaml` MUST keep today's exact behavior: the gatherer drops the repo before evaluation (step `maintainer_config`), never producing a consent verdict of any kind.
- Do not hand-edit any file under `mocks/` — it is fully regenerated by `make precommit`'s `generate` step from the `//counterfeiter:generate` directives already present in `pkg/githubclient.go`.
- Do not hand-edit `go.mod` — `make precommit`'s `ensure` step (`go mod tidy && go mod verify`) will promote `gopkg.in/yaml.v3`, `github.com/bborbe/collection`, and `github.com/bborbe/validation` from indirect to direct dependencies automatically once `pkg/filter/consent.go` imports them.
- Decision-task identity, decision-task type/assignee, and the skip-with-emit pathway are explicitly out of scope for this prompt — they land in a later prompt.

</constraints>

<verification>

```
make precommit
```
must exit 0. This runs `ensure` (go mod tidy/verify), `format`, `generate` (regenerates all counterfeiter mocks from scratch), `test` (`go test -mod=mod -p=1 -cover ...`), `check` (lint, vet, errcheck, vulncheck, osv-scanner, gosec, trivy), and `addlicense`.

Confirm zero remaining references to `maintainerconfig` in `pkg/watcher.go`, `pkg/watcher_test.go`, and `pkg/githubclient.go`:
```
grep -rn "maintainerconfig" pkg/watcher.go pkg/watcher_test.go pkg/githubclient.go
```
must print nothing.

</verification>
