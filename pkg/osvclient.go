// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// DefaultOSVVulnBaseURL is the OSV single-record endpoint; the advisory ID is
// appended as one path segment.
const DefaultOSVVulnBaseURL = "https://api.osv.dev/v1/vulns"

// DefaultOSVIndexURL is the flat Go vulnerability index used to resolve an ID
// whose own OSV record is absent (a merged alias).
const DefaultOSVIndexURL = "https://vuln.go.dev/index/vulns.json"

// osvVulnBodyLimit and osvIndexBodyLimit cap the two response bodies. Both
// upstreams are third-party; neither body may be read unbounded.
const (
	osvVulnBodyLimit  = 8 << 20  // 8 MiB
	osvIndexBodyLimit = 64 << 20 // 64 MiB
)

// goIndexTTL bounds how long one process reuses a fetched Go index. The index
// is fetched at most once per process per TTL, and never when no ID has 404'd.
const goIndexTTL = 24 * libtime.Hour

// OSVVerdict is the closed set of outcomes for one advisory ID lookup.
type OSVVerdict string

const (
	// OSVVerdictWithdrawn means OSV reports the advisory as withdrawn — the
	// only verdict that may cause an emission.
	OSVVerdictWithdrawn OSVVerdict = "withdrawn"
	// OSVVerdictLegitimate means OSV answered and the record carries no
	// withdrawal. Cacheable.
	OSVVerdictLegitimate OSVVerdict = "legitimate"
	// OSVVerdictUnresolved means the question could not be answered: an
	// unreachable or failing upstream, an unparsable body, or a 404 that the
	// index could not resolve to a withdrawn record. Never cacheable, never an
	// emission.
	OSVVerdictUnresolved OSVVerdict = "unresolved"
)

//counterfeiter:generate -o ../mocks/osv_client.go --fake-name OSVClient . OSVClient

// OSVClient resolves a single advisory ID against OSV.
type OSVClient interface {
	// CheckWithdrawn returns the verdict for id.
	//
	//   - (OSVVerdictWithdrawn, nil) — the record carries a non-empty
	//     top-level `withdrawn` timestamp, or a 404 resolved through the Go
	//     index to an aliased record that carries one.
	//   - (OSVVerdictLegitimate, nil) — OSV answered 200 and the record has no
	//     `withdrawn` value.
	//   - (OSVVerdictUnresolved, err) — a transport, status or decode failure,
	//     or a 404 the index could not resolve to a withdrawn record. The error
	//     is for logging only: the caller counts it and continues, it never
	//     aborts a cycle. A 404 that resolves to no record at all is
	//     unresolved with a nil error — that is an answer, not a fault.
	CheckWithdrawn(ctx context.Context, id string) (OSVVerdict, error)
}

// NewOSVClient returns the production OSVClient backed by the given HTTP
// client. The client must be a plain client with its own timeout — never the
// GitHub App-authenticated one, which would send an installation token to a
// third-party host.
func NewOSVClient(
	httpClient *http.Client,
	vulnBaseURL string,
	indexURL string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) OSVClient {
	return &osvClientImpl{
		httpClient:            httpClient,
		vulnBaseURL:           vulnBaseURL,
		indexURL:              indexURL,
		currentDateTimeGetter: currentDateTimeGetter,
	}
}

// osvClientImpl is the production OSVClient.
//
// Concurrency: not safe for concurrent use. The poll cycle is serialised by
// CycleGate, so the index cache has a single reader and writer — the same
// single-writer assumption the cursor makes.
type osvClientImpl struct {
	httpClient            *http.Client
	vulnBaseURL           string
	indexURL              string
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	index                 []osvIndexEntry
	indexFetchedAt        libtime.DateTime
	indexLoaded           bool
}

// osvIndexEntry is one entry of https://vuln.go.dev/index/vulns.json: a flat
// array of {id, modified, aliases}.
type osvIndexEntry struct {
	ID       string   `json:"id"`
	Modified string   `json:"modified"`
	Aliases  []string `json:"aliases"`
}

// osvRecord is the subset of an OSV record the watcher consumes.
type osvRecord struct {
	Withdrawn string `json:"withdrawn"`
}

func (c *osvClientImpl) CheckWithdrawn(ctx context.Context, id string) (OSVVerdict, error) {
	status, withdrawn, err := c.fetchRecord(ctx, id)
	if err != nil {
		return OSVVerdictUnresolved, err
	}
	switch status {
	case http.StatusOK:
		if withdrawn {
			return OSVVerdictWithdrawn, nil
		}
		return OSVVerdictLegitimate, nil
	case http.StatusNotFound:
		return c.resolveViaIndex(ctx, id)
	default:
		return OSVVerdictUnresolved, errors.Errorf(ctx, "osv returned status %d id=%s", status, id)
	}
}

// fetchRecord issues one GET for id and returns the status code plus, on 200,
// whether the record is withdrawn. A non-200 status is returned without an
// error; only transport and decode failures produce one.
func (c *osvClientImpl) fetchRecord(ctx context.Context, id string) (int, bool, error) {
	recordURL := c.vulnBaseURL + "/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, recordURL, nil)
	if err != nil {
		return 0, false, errors.Wrapf(ctx, err, "create osv request id=%s", id)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, false, errors.Wrapf(ctx, err, "get osv record id=%s", id)
	}
	if resp == nil {
		return 0, false, errors.Errorf(ctx, "osv returned nil response id=%s", id)
	}
	glog.V(2).
		Infof("osv request completed path=%s status=%d id=%s", req.URL.Path, resp.StatusCode, id)
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close osv response body id=%s: %v", id, cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false, nil
	}
	withdrawn, err := readWithdrawn(ctx, resp.Body, id)
	if err != nil {
		return resp.StatusCode, false, err
	}
	return resp.StatusCode, withdrawn, nil
}

// resolveViaIndex answers a 404 for id by finding every index entry that
// aliases it and asking each aliased record whether it is withdrawn. The
// verdict is always about the queried id, never about the alias that produced
// it. A 404 that resolves to no record, or to records with no withdrawal, is
// unresolved — never stale and never legitimate.
func (c *osvClientImpl) resolveViaIndex(ctx context.Context, id string) (OSVVerdict, error) {
	index, err := c.loadIndex(ctx)
	if err != nil {
		return OSVVerdictUnresolved, err
	}
	var matched []osvIndexEntry
	for _, entry := range index {
		if slices.Contains(entry.Aliases, id) {
			matched = append(matched, entry)
		}
	}
	if len(matched) == 0 {
		return OSVVerdictUnresolved, nil
	}
	var resolveErr error
	for _, entry := range matched {
		status, withdrawn, err := c.fetchRecord(ctx, entry.ID)
		if err != nil {
			resolveErr = err
			continue
		}
		if status != http.StatusOK {
			resolveErr = errors.Errorf(
				ctx,
				"osv alias record returned status %d id=%s alias=%s",
				status,
				entry.ID,
				id,
			)
			continue
		}
		if withdrawn {
			return OSVVerdictWithdrawn, nil
		}
	}
	if resolveErr != nil {
		return OSVVerdictUnresolved, errors.Wrapf(ctx, resolveErr, "resolve osv aliases id=%s", id)
	}
	return OSVVerdictUnresolved, nil
}

// loadIndex returns the cached Go vulnerability index, fetching it when the
// cache is empty or older than goIndexTTL. A fetch or decode failure leaves the
// cache untouched so the next 404 retries.
func (c *osvClientImpl) loadIndex(ctx context.Context) ([]osvIndexEntry, error) {
	now := c.currentDateTimeGetter.Now()
	if c.indexLoaded && !c.indexFetchedAt.IsZero() && now.Sub(&c.indexFetchedAt) < goIndexTTL {
		return c.index, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.indexURL, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "create osv index request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "get osv index")
	}
	if resp == nil {
		return nil, errors.Errorf(ctx, "osv index returned nil response")
	}
	glog.V(2).Infof("osv index request completed path=%s status=%d", req.URL.Path, resp.StatusCode)
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close osv index response body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf(ctx, "osv index returned status %d", resp.StatusCode)
	}
	data, err := readLimitedBody(ctx, resp.Body, osvIndexBodyLimit, "index")
	if err != nil {
		return nil, err
	}
	var index []osvIndexEntry
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse osv index JSON")
	}
	c.index = index
	c.indexFetchedAt = now
	c.indexLoaded = true
	return c.index, nil
}

// readWithdrawn decodes the withdrawn field from one OSV record body.
func readWithdrawn(ctx context.Context, body io.Reader, id string) (bool, error) {
	data, err := readLimitedBody(ctx, body, osvVulnBodyLimit, id)
	if err != nil {
		return false, err
	}
	var record osvRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false, errors.Wrapf(ctx, err, "parse osv JSON id=%s", id)
	}
	return record.Withdrawn != "", nil
}

// readLimitedBody reads at most limit bytes and reports an error when the body
// is larger. The extra byte read is what distinguishes "exactly limit" from
// "over limit" without buffering the whole body.
func readLimitedBody(
	ctx context.Context,
	body io.Reader,
	limit int64,
	id string,
) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read osv response body id=%s", id)
	}
	if int64(len(data)) > limit {
		return nil, errors.Errorf(ctx, "osv response body exceeds limit %d bytes id=%s", limit, id)
	}
	return data, nil
}
