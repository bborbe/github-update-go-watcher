// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package auth resolves GitHub App installation credentials to an HTTP client.
// I/O (JWT exchange + installation-token fetch) happens here, which is why it
// lives outside pkg/factory (the factory package is pure composition).
package auth

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/bborbe/maintainer/githubapp"
	"github.com/golang/glog"
)

// Credentials carries the inputs needed for GitHub App auth. Read from the
// binary's argument struct by the caller. PEMKey is the long-lived secret:
// it arrives by environment only and is never logged.
type Credentials struct {
	AppID          int64
	InstallationID int64
	PEMKey         []byte
}

// ResolveGitHubClient returns an *http.Client authenticated as the GitHub App
// installation.
//
// Rules:
//   - All three fields set -> App auth.
//   - Any subset set without the other two -> error naming the MISSING env var
//     names only (never the value of PEM_KEY).
//   - Nothing set -> error.
func ResolveGitHubClient(ctx context.Context, creds Credentials) (*http.Client, error) {
	appPartial := creds.AppID != 0 || creds.InstallationID != 0 || len(creds.PEMKey) != 0
	appComplete := creds.AppID != 0 && creds.InstallationID != 0 && len(creds.PEMKey) != 0

	if appPartial && !appComplete {
		var missing []string
		if creds.AppID == 0 {
			missing = append(missing, "APP_ID")
		}
		if creds.InstallationID == 0 {
			missing = append(missing, "INSTALLATION_ID")
		}
		if len(creds.PEMKey) == 0 {
			missing = append(missing, "PEM_KEY")
		}
		return nil, errors.Errorf(
			ctx,
			"watcher auth: partial GitHub App config — missing %v; set all three or none",
			missing,
		)
	}

	if appComplete {
		glog.Infof(
			"watcher auth mode=github-app app_id=%d installation_id=%d",
			creds.AppID, creds.InstallationID,
		)
		client, err := githubapp.NewClient(
			ctx, githubapp.Config{
				AppID:          creds.AppID,
				InstallationID: creds.InstallationID,
				PEM:            creds.PEMKey,
			},
		)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "create github app client")
		}
		return client, nil
	}

	return nil, errors.Errorf(
		ctx,
		"watcher auth: GitHub App credentials not configured — set APP_ID, INSTALLATION_ID, and PEM_KEY",
	)
}
