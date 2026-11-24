// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command staleignore-exercise drives the real stale-ignore pass against the
// real GitHub and OSV APIs and writes its own transcript. It exists so the
// alias case can be proven live rather than argued from unit tests.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/github-update-go-watcher/pkg"
	"github.com/bborbe/github-update-go-watcher/pkg/factory"
)

// githubAPIBase is the REST base used only for the repo lookup. The pass
// itself reads through pkg.NewGitHubClient.
const githubAPIBase = "https://api.github.com"

// peekLimit bounds how much of an OSV response body the recorder buffers before
// restoring it for the caller.
const peekLimit = 64 << 10

func main() {
	repoFlag := flag.String("repo", "", "host-qualified repo, github.com/<owner>/<name>")
	transcriptFlag := flag.String(
		"transcript",
		"",
		"path to write the transcript; empty means stdout",
	)
	flag.Parse()
	os.Exit(run(*repoFlag, *transcriptFlag))
}

func run(repoArg, transcriptPath string) int {
	out, closeOut, err := openTranscript(transcriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open transcript: %v\n", err)
		return 2
	}
	defer closeOut()
	transcript := &transcriptWriter{out: out}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		transcript.printf("error: GITHUB_TOKEN is not set\n")
		return 2
	}

	ctx := context.Background()
	owner, name, err := parseRepo(ctx, repoArg)
	if err != nil {
		transcript.printf("error: %v\n", err)
		return 2
	}
	transcript.printf("repo=github.com/%s/%s\n", owner, name)

	githubHTTP := &http.Client{Transport: &bearerTransport{token: token}}
	defaultBranch, err := lookupDefaultBranch(ctx, githubHTTP, owner, name)
	if err != nil {
		transcript.printf("error: resolve repo %s/%s: %v\n", owner, name, err)
		return 1
	}

	latestGo, err := pkg.NewGoDevClient(
		factory.CreateGoDevHTTPClient(),
		pkg.DefaultGoDevURL,
	).LatestStable(ctx)
	if err != nil {
		transcript.printf("error: resolve latest go: %v\n", err)
		return 1
	}

	osvHTTP := factory.CreateOSVHTTPClient()
	osvHTTP.Transport = &recordingTransport{inner: http.DefaultTransport, out: transcript}
	osvClient := pkg.NewOSVClient(
		osvHTTP,
		pkg.DefaultOSVVulnBaseURL,
		pkg.DefaultOSVIndexURL,
		libtime.NewCurrentDateTime(),
	)

	registry := prometheus.NewRegistry()
	metrics := pkg.NewMetrics(registry)
	publisher := &printingPublisher{out: transcript}
	pass := pkg.NewStaleIgnorePass(
		pkg.NewGitHubClient(githubHTTP),
		osvClient,
		publisher,
		metrics,
		nil,
		libtime.NewCurrentDateTime(),
	)

	repo := pkg.Repo{Owner: owner, Name: name, DefaultBranch: defaultBranch}
	cursor := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
	reason := pass.Scan(ctx, cursor, []pkg.Repo{repo}, latestGo)

	writeIgnoreScanMetrics(transcript, registry)
	transcript.printf("abort_reason=%s\n", reason)
	exitCode := 0
	if reason != "" {
		exitCode = 1
	}
	transcript.printf("exit_code=%d\n", exitCode)
	return exitCode
}

func openTranscript(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0600,
	) // #nosec G304 -- operator-supplied transcript path
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func parseRepo(ctx context.Context, value string) (string, string, error) {
	trimmed := strings.TrimPrefix(value, "https://")
	trimmed = strings.TrimPrefix(trimmed, "github.com/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.Errorf(
			ctx,
			"invalid repo %q: want github.com/<owner>/<name>",
			value,
		)
	}
	return parts[0], parts[1], nil
}

func lookupDefaultBranch(
	ctx context.Context,
	client *http.Client,
	owner string,
	name string,
) (string, error) {
	repoURL := fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, owner, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "create repo request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "get repo %s/%s", owner, name)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", errors.Errorf(ctx, "repo not found %s/%s", owner, name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf(ctx, "repo lookup returned status %d", resp.StatusCode)
	}
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode repo response")
	}
	if body.DefaultBranch == "" {
		return "", errors.Errorf(ctx, "repo %s/%s has empty default_branch", owner, name)
	}
	return body.DefaultBranch, nil
}

func writeIgnoreScanMetrics(transcript *transcriptWriter, registry *prometheus.Registry) {
	families, err := registry.Gather()
	if err != nil {
		transcript.printf("error: gather metrics: %v\n", err)
		return
	}
	for _, family := range families {
		if family.GetName() != "github_update_go_watcher_ignore_scan_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			result := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "result" {
					result = label.GetValue()
				}
			}
			transcript.printf(
				"metric ignore_scan_total result=%s value=%g\n",
				result,
				metric.GetCounter().GetValue(),
			)
		}
	}
}

// transcriptWriter serialises writes from the transport and the publisher.
type transcriptWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (t *transcriptWriter) printf(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, format, args...)
}

// bearerTransport adds a GitHub user token to every request.
type bearerTransport struct {
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// recordingTransport records one line per OSV request: URL, status and the
// withdrawn value parsed out of the response body.
type recordingTransport struct {
	inner http.RoundTripper
	out   *transcriptWriter
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	var prefix []byte
	if resp.Body != nil {
		var readErr error
		prefix, readErr = io.ReadAll(io.LimitReader(resp.Body, peekLimit))
		rest := io.Reader(resp.Body)
		if readErr != nil {
			rest = errReader{err: readErr}
		}
		resp.Body = &peekedBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), rest),
			Closer: resp.Body,
		}
	}
	t.out.printf(
		"osv url=%s status=%d withdrawn=%s\n",
		req.URL.String(),
		resp.StatusCode,
		withdrawnLabel(prefix),
	)
	return resp, err
}

func withdrawnLabel(body []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return "absent"
	}
	raw, ok := fields["withdrawn"]
	if !ok {
		return "absent"
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "present"
	}
	if value == "" {
		return "present"
	}
	return value
}

// peekedBody replays a buffered prefix before the rest of the original body.
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

// printingPublisher prints the stale-ignore command as JSON. It prints nothing
// for the other two publish methods.
type printingPublisher struct {
	out *transcriptWriter
}

func (p *printingPublisher) PublishCreate(context.Context, pkg.Candidate) bool {
	return false
}

func (p *printingPublisher) PublishDecision(context.Context, pkg.Candidate) bool {
	return false
}

func (p *printingPublisher) PublishStaleIgnore(
	_ context.Context,
	candidate pkg.Candidate,
	finding pkg.StaleIgnoreFinding,
	generation int,
) bool {
	cmd := pkg.BuildStaleIgnoreCommand(
		candidate,
		finding,
		generation,
		pkg.TaskConfig{Stage: "exercise"},
	)
	encoded, err := json.Marshal(cmd)
	if err != nil {
		p.out.printf("error: marshal command: %v\n", err)
		return false
	}
	p.out.printf("task=%s\n", string(encoded))
	return true
}
