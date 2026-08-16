// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultGoDevURL is the go.dev release-list endpoint returning the JSON array
// of releases (including unstable ones — the client filters to stable).
//
// Spec Non-goal: this URL is NOT configurable. Tests inject the GoDevClient
// interface, never a URL.
const DefaultGoDevURL = "https://go.dev/dl/?mode=json"

//counterfeiter:generate -o ../mocks/go_dev_client.go --fake-name GoDevClient . GoDevClient

// GoDevClient resolves the current stable Go release.
type GoDevClient interface {
	// LatestStable returns the maximum stable Go version reported by go.dev.
	// It returns an error when the request fails, the response is non-200,
	// the body is not the expected JSON array, or no entry carries a version
	// string matching go<major>.<minor>[.<patch>]. The caller aborts the whole
	// cycle on any error (poll_cycle_total{result="go_version_error"}).
	LatestStable(ctx context.Context) (Version, error)
}

// GoDevRelease is the subset of the go.dev release JSON the watcher consumes.
type GoDevRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// NewGoDevClient returns the production GoDevClient backed by the given HTTP
// client and URL (always DefaultGoDevURL in production wiring).
func NewGoDevClient(httpClient *http.Client, url string) GoDevClient {
	return &goDevClientImpl{
		httpClient: httpClient,
		url:        url,
	}
}

type goDevClientImpl struct {
	httpClient *http.Client
	url        string
}

func (c *goDevClientImpl) LatestStable(ctx context.Context) (Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "create request %s", c.url)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "get %s", c.url)
	}
	glog.V(2).Infof("go.dev request completed url=%s status=%d", c.url, resp.StatusCode)
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close go.dev response body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return Version{}, errors.Errorf(ctx, "go.dev returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "read go.dev response body")
	}
	var releases []GoDevRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return Version{}, errors.Wrapf(ctx, err, "parse go.dev JSON")
	}
	var max Version
	for _, r := range releases {
		if !r.Stable {
			continue
		}
		v, err := ParseGoRelease(ctx, r.Version)
		if err != nil {
			glog.V(2).Infof("skip unparseable go.dev version %q: %v", r.Version, err)
			continue
		}
		if max.Raw == "" || max.Less(v) {
			max = v
		}
	}
	if max.Raw == "" {
		return Version{}, errors.Errorf(ctx, "no stable go version found in go.dev response")
	}
	return max, nil
}
