// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/bborbe/maintainer/maintainerconfig"
	"github.com/golang/glog"
	gogithub "github.com/google/go-github/v84/github"
)

// ErrRateLimited is returned when the GitHub API responds with a primary or
// abuse rate-limit error. Callers abort the whole cycle on this sentinel
// (poll_cycle_total{result="rate_limited"}) rather than retrying in a loop.
var ErrRateLimited = stderrors.New("github rate limited")

// maxContentBytes caps every file this client decodes. The contents of
// go.mod and .maintainer.yaml in any observed repo are attacker-controlled,
// so the API-reported Size is checked BEFORE decoding.
const maxContentBytes = 1024 * 1024

// maxListPages bounds repo-list pagination so a self-referential or
// misbehaving `next` link cannot loop the cycle forever.
const maxListPages = 100

//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient

// GitHubClient is the read-only upstream surface for the Go-update watcher.
// Nothing in this interface writes to an observed repository.
type GitHubClient interface {
	// ListRepos returns the non-archived repositories under owner that the
	// authenticated GitHub App installation can access — public AND private.
	// Forks are included: a fork carrying its own goUpdate.autoUpdate flag has
	// given its own consent (spec Non-goal: forks are not gated separately).
	// Enumeration goes through GET /installation/repositories (Apps.ListRepos),
	// NOT GET /users/{u}/repos, because the latter silently omits private repos
	// under an installation token. Pagination is internal and capped at
	// maxListPages; the returned slice is the full set.
	ListRepos(ctx context.Context, owner string) ([]Repo, error)

	// GetHeadSHA returns the full HEAD SHA of repo's default branch.
	GetHeadSHA(ctx context.Context, repo Repo) (string, error)

	// GetGoMod returns the raw bytes of go.mod at HEAD of repo's default
	// branch. Returns (nil, nil) when the file does not exist (HTTP 404) —
	// the caller maps a nil slice to skip reason "no_gomod". Returns
	// (nil, ErrRateLimited) on rate limiting. Every other failure (network,
	// 5xx, oversize, base64 decode) returns a wrapped error and drops the repo.
	GetGoMod(ctx context.Context, repo Repo) ([]byte, error)

	// GetMaintainerConfig returns the parsed `.maintainer.yaml` at HEAD of
	// repo's default branch. This file is the repo owner's consent record.
	//
	//   - (parsed config, nil) on a valid YAML document, including empty input
	//     and documents with the `goUpdate:` key absent (both zero-value).
	//   - (zero-value, nil) on HTTP 404 (file absent) — reads as NOT opted in.
	//   - (zero-value, ErrRateLimited) on primary or abuse rate limiting.
	//   - (zero-value, wrapped error) on every other failure including 5xx,
	//     oversize files, base64 decode failures, and YAML parse failures.
	//
	// Malformed YAML MUST NOT be silently treated as autoUpdate:false — it is
	// an error so the repo is dropped from the cycle rather than recorded as a
	// consent verdict.
	GetMaintainerConfig(ctx context.Context, repo Repo) (maintainerconfig.MaintainerConfig, error)
}

// NewGitHubClient returns the production GitHubClient backed by the given
// HTTP client (authenticated via GitHub App installation token).
func NewGitHubClient(httpClient *http.Client) GitHubClient {
	return &githubClient{client: gogithub.NewClient(httpClient)}
}

type githubClient struct {
	client *gogithub.Client
}

// isRateLimitError reports whether err is a GitHub rate-limit signal
// (primary or secondary/abuse).
func isRateLimitError(err error) bool {
	var rl *gogithub.RateLimitError
	var arl *gogithub.AbuseRateLimitError
	return stderrors.As(err, &rl) || stderrors.As(err, &arl)
}

// isNotFound reports whether err is a GitHub 404 response.
func isNotFound(err error) bool {
	var ghErr *gogithub.ErrorResponse
	return stderrors.As(err, &ghErr) && ghErr.Response != nil &&
		ghErr.Response.StatusCode == http.StatusNotFound
}

// wrapRateLimitErr maps rate-limit responses onto ErrRateLimited and wraps
// everything else with context.
func (c *githubClient) wrapRateLimitErr(
	ctx context.Context,
	err error,
	msg string,
	args ...interface{},
) error {
	if isRateLimitError(err) {
		return ErrRateLimited
	}
	return errors.Wrapf(ctx, err, msg, args...)
}

// mapGitHubRepos maps GitHub repository objects to our Repo type, dropping
// entries that are archived, owned by another login, or have no name.
func mapGitHubRepos(repos []*gogithub.Repository, owner string) []Repo {
	result := make([]Repo, 0, len(repos))
	for _, repo := range repos {
		if repo.GetArchived() {
			continue
		}
		if repo.GetOwner().GetLogin() != owner {
			continue
		}
		if repo.GetName() == "" {
			continue
		}
		result = append(result, Repo{
			Owner:         repo.GetOwner().GetLogin(),
			Name:          repo.GetName(),
			DefaultBranch: repo.GetDefaultBranch(),
		})
	}
	return result
}

// ListRepos implements GitHubClient.
func (c *githubClient) ListRepos(ctx context.Context, owner string) ([]Repo, error) {
	opts := &gogithub.ListOptions{PerPage: 100, Page: 1}
	var (
		repos   []Repo
		total   int
		private int
		page    int
	)
	for {
		select {
		case <-ctx.Done():
			return nil, errors.Wrap(ctx, ctx.Err(), "context cancelled during ListRepos")
		default:
		}

		page++
		result, resp, err := c.client.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, c.wrapRateLimitErr(
				ctx, err, "list installation repos for %s page=%d", owner, opts.Page,
			)
		}

		for _, r := range result.Repositories {
			total++
			if r.GetPrivate() {
				private++
			}
		}

		repos = append(repos, mapGitHubRepos(result.Repositories, owner)...)

		if resp == nil || resp.NextPage == 0 {
			break
		}
		if page >= maxListPages {
			return nil, errors.Errorf(
				ctx, "list installation repos for %s exceeded %d pages", owner, maxListPages,
			)
		}
		opts.Page = resp.NextPage
	}

	glog.Infof(
		"github-update-go-watcher listed installation repos owner=%s total=%d private=%d in_scope=%d",
		owner,
		total,
		private,
		len(repos),
	)
	return repos, nil
}

// GetHeadSHA implements GitHubClient.
func (c *githubClient) GetHeadSHA(ctx context.Context, repo Repo) (string, error) {
	if repo.DefaultBranch == "" {
		return "", errors.Errorf(
			ctx,
			"repo %s has empty DefaultBranch — cannot fetch HEAD SHA",
			repo.String(),
		)
	}
	branch, _, err := c.client.Repositories.GetBranch(
		ctx, repo.Owner, repo.Name, repo.DefaultBranch, 1,
	)
	if err != nil {
		return "", c.wrapRateLimitErr(
			ctx, err, "get branch %s@%s", repo.String(), repo.DefaultBranch,
		)
	}
	return branch.GetCommit().GetSHA(), nil
}

// GetGoMod implements GitHubClient.
func (c *githubClient) GetGoMod(ctx context.Context, repo Repo) ([]byte, error) {
	opts := &gogithub.RepositoryContentGetOptions{Ref: repo.DefaultBranch}
	fileContent, _, _, err := c.client.Repositories.GetContents(
		ctx, repo.Owner, repo.Name, "go.mod", opts,
	)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		if isRateLimitError(err) {
			return nil, ErrRateLimited
		}
		return nil, errors.Wrapf(
			ctx, err, "get go.mod %s ref=%s", repo.String(), repo.DefaultBranch,
		)
	}
	if fileContent == nil {
		return nil, nil
	}
	if fileContent.GetSize() > maxContentBytes {
		return nil, errors.Errorf(
			ctx, "go.mod %s too large: %d bytes (max %d)",
			repo.String(), fileContent.GetSize(), maxContentBytes,
		)
	}
	decoded, err := fileContent.GetContent()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "decode go.mod %s", repo.String())
	}
	if len(decoded) > maxContentBytes {
		return nil, errors.Errorf(
			ctx, "go.mod %s decoded too large: %d bytes (max %d)",
			repo.String(), len(decoded), maxContentBytes,
		)
	}
	return []byte(decoded), nil
}

// GetMaintainerConfig implements GitHubClient.
func (c *githubClient) GetMaintainerConfig(
	ctx context.Context,
	repo Repo,
) (maintainerconfig.MaintainerConfig, error) {
	opts := &gogithub.RepositoryContentGetOptions{Ref: repo.DefaultBranch}
	fileContent, _, _, err := c.client.Repositories.GetContents(
		ctx, repo.Owner, repo.Name, ".maintainer.yaml", opts,
	)
	if err != nil {
		if isNotFound(err) {
			return maintainerconfig.MaintainerConfig{}, nil
		}
		if isRateLimitError(err) {
			return maintainerconfig.MaintainerConfig{}, ErrRateLimited
		}
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx, err, "get .maintainer.yaml %s ref=%s", repo.String(), repo.DefaultBranch,
		)
	}
	if fileContent == nil {
		return maintainerconfig.MaintainerConfig{}, nil
	}
	if fileContent.GetSize() > maxContentBytes {
		return maintainerconfig.MaintainerConfig{}, errors.Errorf(
			ctx, ".maintainer.yaml %s too large: %d bytes (max %d)",
			repo.String(), fileContent.GetSize(), maxContentBytes,
		)
	}
	decoded, err := fileContent.GetContent()
	if err != nil {
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx, err, "decode .maintainer.yaml %s", repo.String(),
		)
	}
	if len(decoded) > maxContentBytes {
		return maintainerconfig.MaintainerConfig{}, errors.Errorf(
			ctx, ".maintainer.yaml %s decoded too large: %d bytes (max %d)",
			repo.String(), len(decoded), maxContentBytes,
		)
	}
	cfg, err := maintainerconfig.Parse(ctx, []byte(decoded))
	if err != nil {
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx, err, "parse .maintainer.yaml %s", repo.String(),
		)
	}
	return cfg, nil
}
