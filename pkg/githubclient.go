// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/bborbe/maintainer/maintainerconfig"
)

// ErrRateLimited is returned by any GitHubClient method when the GitHub API
// has rate-limited the watcher. The Watcher treats this as a whole-cycle abort.
var ErrRateLimited = stderrors.New("rate limited")

//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient

// GitHubClient fetches GitHub API data for the watcher.
type GitHubClient interface {
	// ListRepos returns all repos owned by the given GitHub org/login.
	ListRepos(ctx context.Context, owner string) ([]Repo, error)
	// GetHeadSHA returns the full SHA of the default branch HEAD.
	GetHeadSHA(ctx context.Context, repo Repo) (string, error)
	// GetGoMod returns the content of the repo's go.mod file. A absent
	// go.mod returns (nil, nil). Any other error is a real error.
	GetGoMod(ctx context.Context, repo Repo) ([]byte, error)
	// GetMaintainerConfig returns the parsed .maintainer.yaml content.
	// A absent or empty .maintainer.yaml returns (*MaintainerConfig, nil).
	// A parse error returns (*MaintainerConfig, error) so the watcher can
	// drop the repo without conflating it with the opt-in verdict.
	GetMaintainerConfig(
		ctx context.Context,
		repo Repo,
	) (*maintainerconfig.MaintainerConfig, error)
}
