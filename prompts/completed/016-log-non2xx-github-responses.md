---
status: completed
summary: Added a non-2xx logging RoundTripper for the GitHub API client, wired it into NewGitHubClient, and covered it with 26 specs plus a changelog entry.
execution_id: github-update-go-watcher-non2xx-log-exec-016-log-non2xx-github-responses
dark-factory-version: v0.198.7
created: "2026-10-31T13:30:00Z"
queued: "2026-10-31T14:44:23Z"
started: "2026-10-31T14:46:24Z"
completed: "2026-10-31T14:48:46Z"
---

# Log non-2xx GitHub API responses at the HTTP client boundary

<summary>
- Every GitHub API call that comes back with a non-2xx status now produces exactly one log line, at verbose level 2, from inside the HTTP client.
- Each line carries the request method, host and path, the status code, the rate-limit resource and remaining count, the retry-after header and a short snippet of the response body.
- A missing header or an empty body is reported as the literal word `absent`, so "no value" can be told apart from "empty value".
- Response bodies are truncated to 200 characters plus an ellipsis, and newlines are collapsed, so one response is always one log line.
- Only the first 4096 bytes of the body are buffered; the body handed back to the caller is that prefix followed by the unread remainder, so callers still see the full body and no existing behaviour changes.
- A body read error is not swallowed: it re-surfaces on the caller's own read, after the prefix.
- Closing the restored body closes the original one.
- No credentials are ever logged: the full URL is never printed (it can carry a token in userinfo or a query string) and no request header is printed.
- The line format is identical to the other watchers in the fleet, so log queries work the same way across all of them.
- The behaviour is covered by tests for the status-code predicate, every field, the absent markers, truncation, multi-line bodies, body restoration, credential redaction and the disabled-sink case.
- A changelog entry records the addition.
</summary>

<objective>
Make a GitHub API failure diagnosable from the watcher's own pod logs without guessing: today a non-2xx response surfaces only as a wrapped error with no status code, no rate-limit headers and no body, so primary-quota exhaustion and secondary rate limiting look identical. Wrap the GitHub HTTP client's transport so every non-2xx response emits one structured line, using the exact same format the other watchers in the fleet already emit.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:

- `pkg/githubclient.go` — `NewGitHubClient(httpClient *http.Client) GitHubClient` is the single construction point for the GitHub API client. Its body today is one line: `return &githubClient{client: gogithub.NewClient(httpClient)}`. `githubClient` holds one field, `client *gogithub.Client`. Every GitHub API call in this repo goes through the `*gogithub.Client` built here.
- `pkg/factory/factory.go` — `CreateWatcher(...)` calls `pkg.NewGitHubClient(githubHTTPClient)` (the only caller of `NewGitHubClient`). `main.go` is the only caller of `CreateWatcher`, and it passes the client returned by `auth.ResolveGitHubClient`.
- `pkg/auth/auth.go` — `ResolveGitHubClient(ctx, creds)` returns the GitHub App-authenticated `*http.Client`. Do NOT modify this file: the wrapper belongs at the GitHub client construction point (`NewGitHubClient`), not at the credential exchange, so the transport decorator stays a property of the API client rather than of the credentials.
- `pkg/godevclient.go` / `CreateGoDevHTTPClient()` in `pkg/factory/factory.go` — a separate plain HTTP client for the go.dev lookup. It must stay untouched; the wrapper is for GitHub API traffic only.
- `pkg/pkg_suite_test.go` — the Ginkgo suite lives in `package pkg_test`. Its `TestSuite` is the only bootstrap in the package, so any new spec file must be `package pkg_test` to actually run.
- `pkg/githubclient_export_test.go` — the existing precedent for exposing an unexported symbol to the external test package (`SetBaseURL`). Follow this pattern.
- `pkg/githubclient_test.go` — the existing style for HTTP-level tests: `httptest.NewServer`, nested `Describe`/`Context`/`It`, `Expect(...)`. Its 403 rate-limit cases build the client via `NewGitHubClient(server.Client())`, so after this change they exercise go-github's error-body parsing through the wrapper — they must stay green unchanged.
- `CHANGELOG.md` — the newest release section today is `## v0.7.0` (line 11); there is no `## Unreleased` section. Bullets are `- feat:` / `- fix:` / `- chore:` under a `## vX.Y.Z` heading.

Guides to read before writing code:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — why this line is `glog.V(2).Info` and not `glog.Info`: debug-shaped content is gated behind `V(N)`, V0 is reserved for production-default operator info.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external test package + Ginkgo/Gomega conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` convention.

Libraries already in `go.mod` (do not add dependencies): `github.com/golang/glog v1.2.5`, `github.com/onsi/ginkgo/v2`, `github.com/onsi/gomega`.

This repo has NO `rateCapturingTransport` — do not reference one and do not invent one. The wrapper is the only transport decorator in this package.
</context>

<requirements>

### 1. Create `pkg/non2xx_log.go` — verbatim, package `pkg`

Create the file with exactly this content (license header included; the sibling repo this was reviewed in uses the same content with the same package name, so no adjustment is needed):

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/golang/glog"
)

// non2xxBodyLimit is the maximum number of runes of the response body carried
// in a non-2xx log line; longer bodies are truncated and suffixed with "…".
const non2xxBodyLimit = 200

// non2xxAbsent is logged for a header (or body) the response does not carry,
// so a missing value is distinguishable from an empty one.
const non2xxAbsent = "absent"

// logNon2xxV2 is the production sink for non-2xx lines. The watchers run with
// -v=2, so a V(2) line is visible in pod logs.
func logNon2xxV2(line string) {
	glog.V(2).Info(line)
}

// non2xxLoggingTransport wraps an http.RoundTripper and logs one line per
// non-2xx response.
type non2xxLoggingTransport struct {
	inner http.RoundTripper
	log   func(string)
}

// RoundTrip delegates to the inner transport and, on a non-2xx response,
// emits one line. A transport error or a nil response is passed through
// unchanged and logged by the caller's existing error path.
func (t *non2xxLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if t.log != nil {
		logNon2xxResponse(req, resp, t.log)
	}
	return resp, err
}

// non2xxPeekBytes bounds how much of a non-2xx body is buffered for the
// snippet: enough for non2xxBodyLimit runes of 4-byte UTF-8 plus whitespace.
const non2xxPeekBytes = 4096

// logNon2xxResponse emits one line for a response whose status is outside
// 200-299 — 3xx included, so a redirect hop or a 304 is logged too — carrying
// the signals needed to tell primary-quota exhaustion from secondary rate
// limiting. Nothing is emitted for a 2xx.
//
// Only the first non2xxPeekBytes of the body are read. The body handed back to
// the caller is that prefix followed by the unread remainder of the original
// body, so the caller sees the full body; a read error is not swallowed but
// re-surfaces on the caller's own read, after the prefix.
func logNon2xxResponse(req *http.Request, resp *http.Response, log func(string)) {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return
	}
	var prefix []byte
	if resp.Body != nil {
		var readErr error
		prefix, readErr = io.ReadAll(io.LimitReader(resp.Body, non2xxPeekBytes))
		rest := io.Reader(resp.Body)
		if readErr != nil {
			rest = errReader{err: readErr}
		}
		resp.Body = peekedBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), rest),
			Closer: resp.Body,
		}
	}
	log(non2xxLine(req, resp, prefix))
}

// peekedBody replays a buffered prefix before the rest of the original body
// and closes the original body.
type peekedBody struct {
	io.Reader
	io.Closer
}

// errReader returns err on every read.
type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

// non2xxLine formats the non-2xx line. Only method, host and path of the
// request are logged — never the full URL (userinfo, query tokens) and never a
// request header.
func non2xxLine(req *http.Request, resp *http.Response, body []byte) string {
	method, host, path := non2xxAbsent, non2xxAbsent, non2xxAbsent
	if req != nil {
		method = req.Method
		if req.URL != nil {
			host = orAbsent(req.URL.Host)
			path = orAbsent(req.URL.Path)
		}
	}
	return fmt.Sprintf(
		"github non-2xx method=%s host=%s path=%s status=%d resource=%s remaining=%s retry_after=%s body=%s",
		method,
		host,
		path,
		resp.StatusCode,
		orAbsent(resp.Header.Get("X-RateLimit-Resource")),
		orAbsent(resp.Header.Get("X-RateLimit-Remaining")),
		orAbsent(resp.Header.Get("Retry-After")),
		bodySnippet(body),
	)
}

func orAbsent(v string) string {
	if v == "" {
		return non2xxAbsent
	}
	return v
}

// bodySnippet collapses whitespace so the line stays one line, and truncates
// to non2xxBodyLimit runes with a trailing "…".
func bodySnippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return non2xxAbsent
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	if utf8.RuneCountInString(s) <= non2xxBodyLimit {
		return s
	}
	runes := []rune(s)
	return string(runes[:non2xxBodyLimit]) + "…"
}
```

Do not change the prefix string `github non-2xx`, the field order `method= host= path= status= resource= remaining= retry_after= body=`, the constant values (`200`, `"absent"`), or the log level (`glog.V(2)`). The format is shared with the rest of the fleet and must stay byte-identical.

### 2. Install the wrapper in `pkg/githubclient.go` — inside `NewGitHubClient`

This is the single construction point: `NewGitHubClient` is called only by `factory.CreateWatcher`, which is called only by `main.go` with the client from `auth.ResolveGitHubClient`. Every GitHub API call in the repo passes through it, and no other code path builds a GitHub API client. There is exactly one install site — do not add a second one, and do not touch `pkg/auth/auth.go` or `CreateGoDevHTTPClient`.

Change `NewGitHubClient` from:

```go
// NewGitHubClient returns the production GitHubClient backed by the given
// HTTP client (authenticated via GitHub App installation token).
func NewGitHubClient(httpClient *http.Client) GitHubClient {
	return &githubClient{client: gogithub.NewClient(httpClient)}
}
```

to (keep the existing doc comment; extend it with the second paragraph):

```go
// NewGitHubClient returns the production GitHubClient backed by the given
// HTTP client (authenticated via GitHub App installation token).
//
// The client's transport is wrapped so every non-2xx GitHub API response emits
// one structured log line (see non2xx_log.go), which is what makes primary
// quota exhaustion distinguishable from secondary rate limiting in pod logs.
func NewGitHubClient(httpClient *http.Client) GitHubClient {
	inner := httpClient.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	httpClient.Transport = &non2xxLoggingTransport{
		inner: inner,
		log:   logNon2xxV2,
	}
	return &githubClient{client: gogithub.NewClient(httpClient)}
}
```

Rules for this step:
- `nil` transport MUST become `http.DefaultTransport`; assigning a wrapper with a nil inner transport panics on the first request.
- `log` MUST be `logNon2xxV2` (the `glog.V(2)` sink), not `fmt.Println`, not `log.Printf`, not a new sink function.
- Do not change the `GitHubClient` interface, the `githubClient` struct, or any method on it.
- Do not add a config flag, environment variable or verbosity check around the wrapper — the `V(2)` gate inside `logNon2xxV2` is the only gate.

### 3. Create `pkg/non2xx_log_export_test.go` — test-only shims, `package pkg`

The Ginkgo suite is `package pkg_test` (`pkg/pkg_suite_test.go`), so every unexported symbol the specs touch needs an exported test-only shim — the same pattern as `pkg/githubclient_export_test.go`'s `SetBaseURL`. The specs drive the unexported transport, call `logNon2xxResponse` and `non2xxLine` directly, and build an `errReader`; none of those are reachable from `pkg_test` without a shim. Create the file with exactly:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"io"
	"net/http"
)

// Non2xxLoggingTransportForTest returns the non-2xx logging transport wrapping
// inner. Test-only: the Ginkgo suite runs in package pkg_test, so the
// unexported transport needs an exported constructor.
func Non2xxLoggingTransportForTest(inner http.RoundTripper, log func(string)) http.RoundTripper {
	return &non2xxLoggingTransport{
		inner: inner,
		log:   log,
	}
}

// Non2xxPeekBytesForTest exposes the peek window so a spec can build a body
// larger than it. Test-only.
const Non2xxPeekBytesForTest = non2xxPeekBytes

// LogNon2xxResponseForTest calls the unexported logger directly, for the
// hand-built *http.Response cases. Test-only.
func LogNon2xxResponseForTest(req *http.Request, resp *http.Response, log func(string)) {
	logNon2xxResponse(req, resp, log)
}

// Non2xxLineForTest calls the unexported formatter directly. Test-only.
func Non2xxLineForTest(req *http.Request, resp *http.Response, body []byte) string {
	return non2xxLine(req, resp, body)
}

// ErrReaderForTest returns a reader that fails with err on every read, so a
// spec can simulate a body that breaks mid-stream. Test-only.
func ErrReaderForTest(err error) io.Reader {
	return errReader{err: err}
}
```

Do not put the specs in `package pkg` — no `TestSuite` bootstrap exists there, so the specs would compile and never run.

### 4. Create `pkg/non2xx_log_test.go` — replicate every case, `package pkg_test`

Replicate all fifteen spec blocks below (three `DescribeTable`s and twelve `It`s — 24 executed specs). The differences from the reference test are mechanical: `package pkg_test`, the `pkg.` import, the transport built through `pkg.Non2xxLoggingTransportForTest(...)` instead of the sibling repo's `rateCapturingTransport` (which does not exist here), and every direct call to an unexported symbol routed through the `pkg/non2xx_log_export_test.go` shim — `pkg.LogNon2xxResponseForTest`, `pkg.Non2xxLineForTest`, `pkg.ErrReaderForTest`, `pkg.Non2xxPeekBytesForTest`. Do not call `logNon2xxResponse`, `non2xxLine`, `errReader` or `non2xxPeekBytes` by name from this file: it is `package pkg_test` and they are not visible there.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("non-2xx response logging", func() {
	var (
		lines []string
		sink  func(string)
	)
	BeforeEach(func() {
		lines = nil
		sink = func(l string) { lines = append(lines, l) }
	})

	roundTrip := func(status int, headers map[string]string, body string) *http.Response {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		DeferCleanup(srv.Close)
		tr := pkg.Non2xxLoggingTransportForTest(srv.Client().Transport, sink)
		req, err := http.NewRequest("GET", srv.URL+"/repos/o/r/pulls", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	DescribeTable("emit predicate",
		func(status int, wantLine bool) {
			roundTrip(status, nil, "x")
			if wantLine {
				Expect(lines).To(HaveLen(1))
				Expect(lines[0]).To(HavePrefix("github non-2xx "))
			} else {
				Expect(lines).To(BeEmpty())
			}
		},
		Entry("200 emits nothing", http.StatusOK, false),
		Entry("204 emits nothing", http.StatusNoContent, false),
		Entry("304 emits a line", http.StatusNotModified, true),
		Entry("403 emits a line", http.StatusForbidden, true),
		Entry("404 emits a line", http.StatusNotFound, true),
		Entry("429 emits a line", http.StatusTooManyRequests, true),
		Entry("500 emits a line", http.StatusInternalServerError, true),
	)

	It("carries all fields when the headers are present", func() {
		roundTrip(http.StatusTooManyRequests, map[string]string{
			"X-RateLimit-Resource":  "core",
			"X-RateLimit-Remaining": "0",
			"Retry-After":           "60",
		}, `{"message":"API rate limit exceeded"}`)
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(ContainSubstring("method=GET "))
		Expect(lines[0]).To(ContainSubstring(" path=/repos/o/r/pulls "))
		Expect(lines[0]).To(ContainSubstring(" status=429 "))
		Expect(lines[0]).To(ContainSubstring(" resource=core "))
		Expect(lines[0]).To(ContainSubstring(" remaining=0 "))
		Expect(lines[0]).To(ContainSubstring(" retry_after=60 "))
		Expect(lines[0]).To(HaveSuffix(` body={"message":"API rate limit exceeded"}`))
	})

	DescribeTable("logs the literal absent for a missing header",
		func(missing string, field string) {
			headers := map[string]string{
				"X-RateLimit-Resource":  "core",
				"X-RateLimit-Remaining": "10",
				"Retry-After":           "5",
			}
			delete(headers, missing)
			roundTrip(http.StatusForbidden, headers, "denied")
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]).To(ContainSubstring(" " + field + "=absent "))
		},
		Entry("X-RateLimit-Resource", "X-RateLimit-Resource", "resource"),
		Entry("X-RateLimit-Remaining", "X-RateLimit-Remaining", "remaining"),
		Entry("Retry-After", "Retry-After", "retry_after"),
	)

	It("logs body=absent for an empty body", func() {
		roundTrip(http.StatusBadGateway, nil, "")
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=absent"))
	})

	DescribeTable("truncates the body snippet",
		func(n int, wantEllipsis bool) {
			roundTrip(http.StatusInternalServerError, nil, strings.Repeat("a", n))
			Expect(lines).To(HaveLen(1))
			snippet := lines[0][strings.Index(lines[0], " body=")+len(" body="):]
			if wantEllipsis {
				Expect(snippet).To(Equal(strings.Repeat("a", 200) + "…"))
			} else {
				Expect(snippet).To(Equal(strings.Repeat("a", n)))
			}
		},
		Entry("200 chars kept whole", 200, false),
		Entry("201 chars truncated", 201, true),
	)

	It("keeps the line on one line for a multi-line body", func() {
		roundTrip(http.StatusInternalServerError, nil, "line one\nline two\r\n\tline three")
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).NotTo(ContainSubstring("\n"))
		Expect(lines[0]).To(HaveSuffix(" body=line one line two line three"))
	})

	It("restores the response body for the caller", func() {
		resp := roundTrip(http.StatusNotFound, nil, `{"message":"Not Found"}`)
		read, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(read)).To(Equal(`{"message":"Not Found"}`))
	})

	It("never logs credentials from the request", func() {
		const sentinel = "ghs_SENTINELtoken123"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		DeferCleanup(srv.Close)
		tr := pkg.Non2xxLoggingTransportForTest(srv.Client().Transport, sink)
		u := strings.Replace(srv.URL, "http://", "http://x-access-token:"+sentinel+"@", 1)
		req, err := http.NewRequest("GET", u+"/repos/o/r?access_token="+sentinel, nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+sentinel)
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).NotTo(ContainSubstring("SENTINEL"))
		Expect(lines[0]).NotTo(ContainSubstring("Bearer"))
		Expect(lines[0]).NotTo(ContainSubstring("access_token"))
	})

	It("restores a body larger than the peek window in full", func() {
		big := strings.Repeat("x", pkg.Non2xxPeekBytesForTest*3+7)
		resp := roundTrip(http.StatusInternalServerError, nil, big)
		read, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(read)).To(Equal(big))
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=" + strings.Repeat("x", 200) + "…"))
	})

	It("re-surfaces a body read error to the caller instead of swallowing it", func() {
		readErr := errors.New("connection reset mid-body")
		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{},
			Body: io.NopCloser(
				io.MultiReader(strings.NewReader("partial"), pkg.ErrReaderForTest(readErr)),
			),
		}
		pkg.LogNon2xxResponseForTest(newNon2xxRequest(), resp, sink)
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=partial"))
		read, err := io.ReadAll(resp.Body)
		Expect(string(read)).To(Equal("partial"))
		Expect(err).To(MatchError(readErr))
	})

	It("closes the original body when the caller closes the restored one", func() {
		closed := false
		resp := &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{},
			Body:       closeSpy{Reader: strings.NewReader("nf"), closed: &closed},
		}
		pkg.LogNon2xxResponseForTest(newNon2xxRequest(), resp, sink)
		Expect(resp.Body.Close()).To(Succeed())
		Expect(closed).To(BeTrue())
	})

	It("logs body=absent when the response has no body", func() {
		pkg.LogNon2xxResponseForTest(
			newNon2xxRequest(),
			&http.Response{StatusCode: http.StatusNotModified, Header: http.Header{}},
			sink,
		)
		Expect(lines).To(HaveLen(1))
		Expect(
			lines[0],
		).To(HaveSuffix(" status=304 resource=absent remaining=absent retry_after=absent body=absent"))
	})

	It("logs absent for a nil request and for a request without a URL", func() {
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
		Expect(
			pkg.Non2xxLineForTest(nil, resp, nil),
		).To(HavePrefix("github non-2xx method=absent host=absent path=absent status=403 "))
		Expect(
			pkg.Non2xxLineForTest(&http.Request{Method: "GET"}, resp, nil),
		).To(HavePrefix("github non-2xx method=GET host=absent path=absent status=403 "))
	})

	It("replaces invalid UTF-8 in the body snippet", func() {
		resp := &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}}
		Expect(
			pkg.Non2xxLineForTest(nil, resp, []byte{'o', 'k', 0xff, 0xfe}),
		).To(HaveSuffix(" body=ok?"))
	})

	It("emits nothing when log is nil", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		DeferCleanup(srv.Close)
		tr := pkg.Non2xxLoggingTransportForTest(srv.Client().Transport, nil)
		req, err := http.NewRequest("GET", srv.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(BeEmpty())
	})
})

func newNon2xxRequest() *http.Request {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/o/r", nil)
	Expect(err).NotTo(HaveOccurred())
	return req
}

// closeSpy records whether Close was called.
type closeSpy struct {
	io.Reader
	closed *bool
}

func (c closeSpy) Close() error {
	*c.closed = true
	return nil
}
```

All fifteen spec blocks are required (three `DescribeTable`s, twelve `It`s — 24 executed specs): emit predicate (7 entries), all-fields, absent headers (3 entries), absent body, truncation (2 entries), single-line body, body restoration, credential redaction, large-body restoration, read-error re-surfacing, original-body close, nil-body absent, nil-request absent, invalid-UTF-8 replacement, nil sink.

### 5. Create `pkg/non2xx_wiring_test.go` — the production client actually carries the wrapper

The specs in requirement 4 test the transport in isolation, which would still pass if `NewGitHubClient` forgot to install it. This is the boundary test: it proves the wrapper sits on the path production uses. Put it in a new file rather than appending to `pkg/githubclient_test.go`, so the two edits cannot conflict.

Create the file with exactly:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("github client transport wiring", func() {
	It("installs the non-2xx logging transport on the production client", func() {
		httpClient := &http.Client{}
		_ = pkg.NewGitHubClient(httpClient)
		Expect(httpClient.Transport).NotTo(BeNil())
		Expect(httpClient.Transport).To(BeAssignableToTypeOf(pkg.Non2xxLoggingTransportForTest(nil, nil)))
	})

	It("keeps the client working after the transport is wrapped", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[]`)
		}))
		DeferCleanup(srv.Close)
		client := pkg.NewGitHubClient(srv.Client())
		Expect(pkg.SetBaseURL(client, srv.URL+"/")).To(Succeed())
		_, err := client.HasOpenUpdatePR(context.Background(), pkg.Repo{Owner: "o", Name: "r"})
		Expect(err).NotTo(HaveOccurred())
	})
})
```

Notes on this file:
- `srv.Client()` already carries a non-nil `*http.Transport` (httptest sets one for every server), so this spec exercises the normal wrap path, not the nil branch. The `nil → http.DefaultTransport` branch is covered by the first `It`, which passes `&http.Client{}`. Keep both as written.
- The `"/"` suffix on the base URL is required by `SetBaseURL` and matches how `pkg/githubclient_test.go` already builds its clients (`pkg.SetBaseURL(client, server.URL+"/")`).
- `HasOpenUpdatePR` returns `(bool, error)`; a `200` with body `[]` must return `(false, nil)`. If the real signature or `pkg.Repo`'s fields differ when you read `pkg/githubclient.go` and `pkg/repo.go`, follow the real code — the assertions are the contract, not the literal call.
- The `It` blocks must stay inside the `Describe` — a bare `It` at file scope panics at suite start.

### 6. `CHANGELOG.md` — new `## Unreleased` section above `## v0.7.0`

Insert, directly above the `## v0.7.0` heading and separated by a blank line:

```markdown
## Unreleased

- feat: log every non-2xx GitHub API response at the HTTP client boundary (`-v=2`), one line per response carrying `method`, `host`, `path`, `status`, `X-RateLimit-Resource`, `X-RateLimit-Remaining`, `Retry-After` and a 200-rune body snippet — so primary-quota exhaustion and secondary rate limiting are distinguishable in pod logs without a repro. Missing headers and empty bodies log the literal `absent`; the body is restored for the caller; the full URL and request headers are never logged.
```

Do not add a version heading, do not rename or reorder existing sections, do not edit any released section.

### 7. Self-check before finishing

Re-run `<verification>` and confirm every command passes. Then walk the acceptance criteria: one line per non-2xx response, none for 2xx, `absent` for missing header/body, 200-rune truncation with `…`, body restored, no URL or header in the output, wrapper installed once in `NewGitHubClient`, changelog entry under `## Unreleased`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do not add any dependency; `github.com/golang/glog`, Ginkgo and Gomega are already in `go.mod`.
- Stay on `github.com/golang/glog`. Do NOT introduce `log/slog`, `log.Printf`, `fmt.Printf` or a `slog` logger anywhere in this change.
- Never log `req.URL.String()`, `req.URL.RawQuery`, `req.URL.User`, or any request header. Only `req.Method`, `req.URL.Host` and `req.URL.Path` may appear.
- The line prefix `github non-2xx` and the field names `method=`, `host=`, `path=`, `status=`, `resource=`, `remaining=`, `retry_after=`, `body=` are fixed across the fleet — do not reorder or rename them.
- The transport must never swallow a transport error and never return a nil response where the inner transport returned one; `RoundTrip` returns `(resp, err)` unchanged in the error/nil case.
- The response body must be restored before returning as `peekedBody{Reader: io.MultiReader(bytes.NewReader(prefix), rest), Closer: original}`, so callers keep reading the full body and closing the restored body closes the original one. Never close the original body inside the peek.
- Do not add a timeout, retry or context to the wrapper: the request already carries its context and the client already carries its timeout. The transport only observes and delegates.
- A 2xx response must not have its body read at all — the wrapper is transparent on the success path.
- Only one install site: `NewGitHubClient` in `pkg/githubclient.go`. Do not wrap in `pkg/auth/auth.go`, `factory.CreateWatcher`, `main.go`, or the go.dev client.
- Do not modify `pkg/githubclient.go`'s `GitHubClient` interface or any of its methods.
- Existing tests must still pass unchanged.
- All paths in this prompt are repo-relative.
</constraints>

<verification>
Run from the repo root.

```
make precommit
```

Must exit 0 (format, generate, test, check, addlicense).

```
grep -n "github non-2xx method=%s host=%s path=%s status=%d resource=%s remaining=%s retry_after=%s body=%s" pkg/non2xx_log.go
```

Must print exactly one match — the format string is byte-identical to the fleet's.

```
grep -n "non2xxLoggingTransport" pkg/githubclient.go
```

Must match at least once — the wrapper is installed at the single construction point.

```
grep -c "func NewGitHubClient" pkg/githubclient.go
```

Must print exactly `1`.

```
! grep -q "req.URL.String()" pkg/non2xx_log.go && echo NO_FULL_URL
! grep -q "req.Header" pkg/non2xx_log.go && echo NO_REQUEST_HEADERS
! grep -q "slog" pkg/non2xx_log.go && echo NO_SLOG
! grep -q "rateCapturingTransport" pkg/non2xx_log.go pkg/non2xx_log_test.go && echo NO_STALE_TRANSPORT_REF
```

Each line must print its marker (the `!` form is required — a `grep -c` that prints `0` still exits non-zero).

```
grep -c "glog.V(2).Info(line)" pkg/non2xx_log.go
```

Must print exactly `1` — the sink is the `V(2)` gate, nothing else.

```
grep -n "^package pkg_test" pkg/non2xx_log_test.go
grep -n "^package pkg$" pkg/non2xx_log_export_test.go
grep -c "github client transport wiring" pkg/non2xx_wiring_test.go
```

Each must match once — the specs run under the existing `pkg_test` suite, the constructor lives in `pkg`, and the wiring spec file exists.

```
go test -mod=mod ./pkg/ -run TestSuite -count=1
```

Must exit 0.

```
test "$(go test -mod=mod ./pkg/ -run TestSuite -count=1 -v 2>&1 | awk '/Ran [0-9]+ of [0-9]+ Specs/{print $2; exit}')" -ge 258 && echo SPECS_RAN
```

Must print `SPECS_RAN`. The suite runs 232 specs before this change and 258 after — the 24 specs in `non2xx_log_test.go` plus the 2 wiring specs in `non2xx_wiring_test.go`. A bare exit-code check would still pass if the new specs compiled but never registered (a `package pkg` spec file with no `TestSuite` bootstrap is exactly that failure); the count is what catches it.

```
awk '/^## /{print NR": "$0}' CHANGELOG.md | head -2
```

The first two `## ` headings must be `## Unreleased` then `## v0.7.0` — Unreleased sits above the newest release.

```
grep -c '^## v' CHANGELOG.md
```

Must print exactly `21` — the same count as before the change, so no release section added, removed or renamed (`## Unreleased` does not match `^## v`).
</verification>
