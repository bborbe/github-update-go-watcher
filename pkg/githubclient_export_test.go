// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "net/url"

// SetBaseURL points c at a test server. Test-only.
func SetBaseURL(c GitHubClient, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if sc, ok := c.(*githubClient); ok {
		sc.client.BaseURL = u
	}
	return nil
}
