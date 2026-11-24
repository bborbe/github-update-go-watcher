---
status: completed
spec: [008-stale-ignore-signal]
summary: Added OSVClient with withdrawn/legitimate/unresolved verdicts, Go-index alias resolution with lazy TTL caching, and a cursor-backed verdict cache with tests and recorded fixtures.
execution_id: github-update-go-watcher-staleignore-exec-021-spec-008-osv-verdict
dark-factory-version: v0.198.17
created: "2026-11-24T15:50:00Z"
queued: "2026-11-24T14:37:42Z"
started: "2026-11-24T14:41:25Z"
completed: "2026-11-24T14:44:54Z"
---

# Resolve an advisory ID against OSV, with alias resolution

<summary>
- The watcher can ask OSV whether a single advisory ID has been withdrawn, and gets one of exactly three answers: withdrawn, legitimate, or unresolved.
- An ID whose own OSV record 404s is resolved through the Go vulnerability index, so a CVE alias merged into a `GO-*` record is still answered — this is the case that covers 168 repos today.
- Alias resolution takes "any" aliased record that is withdrawn, not the first, because one advisory can legitimately alias several records.
- A 404 that resolves to no record, or to records with no withdrawal, is unresolved — never silently "legitimate".
- Any transport error, unexpected status or unparsable body is unresolved, so an upstream outage can never produce a stale verdict and never causes an emission.
- The Go index is fetched at most once per process per day and only after an ID has actually 404'd, so a healthy fleet pays no index cost.
- Resolved verdicts are cached in the cursor with a day-long TTL, so a steady fleet costs about one OSV request per distinct advisory per day; unresolved IDs are retried next cycle.
- A cached verdict whose timestamp is missing or unparsable is treated as absent, so a corrupt entry re-checks rather than wedging a repo.
- The OSV client sends no credentials and is built on its own plain HTTP client, never the GitHub App-authenticated one.
- Everything is covered by tests driven off recorded real payloads, so an OSV schema change fails a test instead of silently disabling the signal.
</summary>

<objective>
Turn "this repo suppresses advisory X" into a verdict the watcher can act on: is X still a live advisory, or has OSV withdrawn it? Only a withdrawn verdict may ever cause an emission, and the answer must survive the dominant real-world case where the ID a repo carries is an alias whose own record no longer exists.
</objective>

<context>
Read these files before editing — every anchor below was verified against them:

- `pkg/godevclient.go` — the template for this client: an interface with a `//counterfeiter:generate` directive, an exported constructor, a private struct holding the HTTP client plus its URLs, `http.NewRequestWithContext`, `defer` body close with a `glog.Warningf` on close failure, a non-200 status error, `io.ReadAll`, `json.Unmarshal`, and `errors.Wrapf(ctx, err, "…")` from `github.com/bborbe/errors`. Follow it; do not invent a different HTTP style.
- `pkg/godevclient.go` — `DefaultGoDevURL` shows the "one frozen default URL constant" convention. The OSV base URL and index URL get the same treatment.
- `pkg/cursor.go` — `Cursor` (`:25`) and `RepoState` (`:30`) are the persisted-memory schema; `LoadCursor`/`SaveCursor` are the only readers/writers. Every new field is `omitempty` so a cursor written by the previous binary loads and cold-starts.
- `pkg/watcher.go` — `inFlightExpired` (`:481`) is the existing precedent for parsing an RFC3339 string with `libtime.ParseDateTime(ctx, value)` and treating an unparsable value as "not set". `InFlightTTL = 14 * libtime.Day` (`:324`) is the precedent for a `libtime.Duration` TTL constant.
- `libtime` (`github.com/bborbe/time v1.27.25`): `libtime.Hour` / `libtime.Day` are `libtime.Duration` constants; `libtime.CurrentDateTimeGetter` is `interface{ Now() DateTime }`; `libtime.CurrentDateTime` adds `SetNow(now DateTime)`; `libtime.DateTime` has `Sub(HasTime) Duration`, `Format(layout string) string` and `IsZero() bool`; `libtime.ParseDateTime(ctx, value) (*DateTime, error)` returns a POINTER. Use these; never `time.Now()` directly.
- `pkg/pkg_suite_test.go` — the Ginkgo suite is `package pkg_test`, so every spec file is `package pkg_test` and every symbol it touches must be exported.
- `docs/dod.md` — `make precommit` green, new functions tested, Ginkgo/Gomega, `mocks/` regenerated (never hand-edited), `github.com/bborbe/errors`, no absolute paths.

Guides to read before writing code (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — the clock is injected, never read from the system.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — per-request diagnostics are `V(2)`; a failure that matters operationally is `Warningf`.

This repo has NO HTTP retry helper and NO generic fetcher — do not reference one and do not invent one. Each request is one attempt; the caller retries on the next cycle.
</context>

<requirements>

### 1. Create `pkg/osvclient.go` — package `pkg`, the verdict type, the client and its constants

```go
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
) OSVClient
```

The private implementation struct holds `httpClient`, `vulnBaseURL`, `indexURL`, `currentDateTimeGetter`, and the index cache: `index []osvIndexEntry`, `indexFetchedAt libtime.DateTime`, `indexLoaded bool`. Document on the struct that it is not safe for concurrent use — the poll cycle is serialised by `CycleGate`, exactly as the cursor's single-writer comment says.

Add the index entry type (unexported) with the shape the index actually serves:

```go
// osvIndexEntry is one entry of https://vuln.go.dev/index/vulns.json: a flat
// array of {id, modified, aliases}.
type osvIndexEntry struct {
	ID      string   `json:"id"`
	Modified string  `json:"modified"`
	Aliases []string `json:"aliases"`
}
```

### 2. `CheckWithdrawn` — the exact resolution order

Implement it as a thin public method over small helpers (the linters cap a function at 80 lines / 50 statements, gocognit 20, nestif 4):

1. **Fetch the record.** `GET <vulnBaseURL>/<url.PathEscape(id)>` via `http.NewRequestWithContext` + `httpClient.Do`. Always `defer` close the body and `glog.Warningf` a close failure, exactly as `goDevClientImpl.LatestStable` does. Log one `glog.V(2).Infof` line per request naming the URL path, the status and the ID — never the token (there is none) and never the body.
2. **200** → read the body through `io.LimitReader(resp.Body, osvVulnBodyLimit+1)`; if more than `osvVulnBodyLimit` bytes arrive, that is an `OSVVerdictUnresolved` with an error naming the limit. Decode into a small private type holding only a `Withdrawn string` field tagged `json:"withdrawn"` — nothing else in the record is needed. A non-empty `Withdrawn` → `OSVVerdictWithdrawn, nil`; empty → `OSVVerdictLegitimate, nil`. A decode error → `OSVVerdictUnresolved` + wrapped error.
3. **404** → resolve through the index (step 4). Every other non-2xx status, a transport error, or a nil response → `OSVVerdictUnresolved` + wrapped error.
4. **Index resolution** (`resolveViaIndex(ctx, id) (OSVVerdict, error)`):
   - Load the index with `loadIndex(ctx)`. If it fails → `OSVVerdictUnresolved` + error.
   - Select every entry whose `Aliases` contains `id` exactly. Zero matches → `OSVVerdictUnresolved, nil`.
   - For each matched entry, fetch `<vulnBaseURL>/<url.PathEscape(entry.ID)>` with the same 8 MiB cap and decode the same `withdrawn` field. **If any** fetch reports a non-empty `withdrawn`, return `OSVVerdictWithdrawn, nil` immediately.
   - If a matched record's fetch fails, keep walking the remaining matches; only if no match is withdrawn does the outcome become `OSVVerdictUnresolved` — return it with a wrapped error describing that the aliases could not be resolved.
   - If every matched record answered 200 with no `withdrawn`, return `OSVVerdictUnresolved, nil` — the rule is that a 404 resolving to records with no withdrawal is unresolved, never stale and never legitimate.
   - The verdict is always about the QUERIED `id`, never about the alias that produced it.
5. **`loadIndex(ctx)`** — returns the cached index when `indexLoaded && !indexFetchedAt.IsZero() && now.Sub(&indexFetchedAt) < goIndexTTL`, where `now := currentDateTimeGetter.Now()`. Otherwise fetch `indexURL`, cap the body at `osvIndexBodyLimit` (same over-limit rule), decode `[]osvIndexEntry`, store it, set `indexFetchedAt = now` and `indexLoaded = true`. A fetch or decode failure leaves the cache untouched (so the next 404 retries) and returns the wrapped error.
6. The index is fetched ONLY from the 404 path. A run in which every ID answers 200 must issue zero index requests — that is a test, not a comment.

Never retry, never sleep, never add a per-request timeout inside the client: the injected `http.Client` carries the timeout (the factory sets 10s in the next prompt) and the request carries the context. Do not set any header beyond what `http.NewRequestWithContext` sets; in particular do not add an `Authorization` header.

### 3. Extend the cursor with the verdict cache — `pkg/cursor.go`

`osvVerdictTTL` is NOT declared here — prompt 3 is its only consumer, and declaring it in this prompt leaves an unused unexported constant that fails `unused` lint.

Add the entry type and the map field:

```go
// OSVVerdictEntry is one cached OSV verdict: whether the advisory was withdrawn
// and when the verdict was recorded (RFC3339, UTC). Only resolved verdicts are
// cached, so a present entry with Withdrawn=false means "legitimate".
//
// Withdrawn deliberately carries no omitempty: an entry that serialised without
// the key would read back as a different verdict than the one stored.
type OSVVerdictEntry struct {
	Withdrawn bool   `json:"withdrawn"`
	CheckedAt string `json:"checked_at"`
}
```

On `Cursor`, add after `Repos`:

```go
	// OSVVerdicts caches resolved OSV verdicts by advisory ID (spec 008 DB7).
	// omitempty so a cursor written by the previous binary loads, and so a
	// cursor with no verdicts serialises to nothing. An unresolved ID is never
	// stored here — it is retried on the next cycle.
	OSVVerdicts map[string]*OSVVerdictEntry `json:"osv_verdicts,omitempty"`
```

Do NOT initialise the map in `LoadCursor`; every access must be nil-safe (a nil map reads fine, and `SetVerdict` allocates on first write). Do NOT touch `SaveCursor`.

Add the two helpers as methods on `*Cursor`:

```go
// CachedVerdict returns the cached verdict for id when the entry exists and is
// younger than ttl. An absent entry, an empty or unparsable CheckedAt, and an
// entry at or past ttl all report false so the ID is re-resolved.
func (c *Cursor) CachedVerdict(
	ctx context.Context,
	id string,
	now libtime.DateTime,
	ttl libtime.Duration,
) (OSVVerdict, bool)

// SetVerdict records a resolved verdict for id at now (RFC3339, UTC). An
// unresolved verdict is never recorded — the caller re-asks next cycle.
func (c *Cursor) SetVerdict(id string, verdict OSVVerdict, now libtime.DateTime)
```

`CachedVerdict` maps a stored entry to `OSVVerdictWithdrawn` when `Withdrawn` is true and `OSVVerdictLegitimate` when false. Parse `CheckedAt` with `libtime.ParseDateTime(ctx, entry.CheckedAt)`; on error return `("", false)` and log one `glog.V(2)` line naming the ID and the bad value (never the whole cursor). Compare with `now.Sub(parsed) < ttl`.

`SetVerdict` returns without writing when `verdict == OSVVerdictUnresolved` or `verdict == ""`; otherwise allocates `OSVVerdicts` if nil and stores an entry with `Withdrawn: verdict == OSVVerdictWithdrawn` and `CheckedAt: now.Format(time.RFC3339)`.

### 4. Commit the recorded fixtures — `pkg/testdata/osv/`

These are the real payloads the feature turns on; a shape change in OSV must fail a test rather than silently disable the pass.

`pkg/testdata/osv/GO-2026-4923.json`
```json
{
  "id": "GO-2026-4923",
  "modified": "2026-04-08T13:33:56Z",
  "published": "2026-03-02T00:00:00Z",
  "aliases": ["CVE-2026-33817"],
  "withdrawn": "2026-04-08T13:33:56Z"
}
```

`pkg/testdata/osv/CVE-2026-33817-404.json` — the body OSV serves for the merged alias, recorded so the test asserts the real shape:
```json
{"code":404,"message":"Not Found"}
```

`pkg/testdata/osv/index.json` — the slice of `vuln.go.dev/index/vulns.json` around that alias, plus one unrelated entry:
```json
[
  {"id": "GO-2026-4923", "modified": "2026-04-08T13:33:56Z", "aliases": ["CVE-2026-33817"]},
  {"id": "GO-2026-1000", "modified": "2026-01-01T00:00:00Z", "aliases": ["CVE-2026-0001"]}
]
```

### 5. Create `pkg/osvclient_test.go` — `package pkg_test`, `httptest`-driven

Build the client as `pkg.NewOSVClient(server.Client(), server.URL+"/v1/vulns", server.URL+"/index/vulns.json", clock)` where `clock` is a settable `libtime.CurrentDateTime` (`libtime.NewCurrentDateTime()` + `SetNow`), so TTL behaviour is deterministic. Serve the fixtures from `pkg/testdata/osv/` and count requests per path in the handler. Cover:

- **AC4** — `GO-2026-4923.json` (withdrawn set) → `OSVVerdictWithdrawn`; the same record with the `withdrawn` key removed → `OSVVerdictLegitimate`.
- **AC5** — the alias case end to end: `/v1/vulns/CVE-2026-33817` answers 404 with the recorded body; `/index/vulns.json` serves `index.json`; `/v1/vulns/GO-2026-4923` serves the withdrawn record. `CheckWithdrawn(ctx, "CVE-2026-33817")` must return `OSVVerdictWithdrawn`. The verdict is about the QUERIED id — assert the returned verdict, and assert the handler saw a request for `CVE-2026-33817` first and for `GO-2026-4923` after.
- **AC5, any-not-first** — an index whose `CVE-2026-33817` alias list has TWO entries, the first a non-withdrawn record and the second the withdrawn `GO-2026-4923`: still `OSVVerdictWithdrawn`. Build this index inline in the handler; keep `index.json` as the recorded real slice.
- **AC6** — a 404 whose index has no matching alias → `OSVVerdictUnresolved` with a nil error; a 404 whose alias resolves to a record with no `withdrawn` → `OSVVerdictUnresolved`; a 500 → `OSVVerdictUnresolved` with an error; a transport error (server closed) → `OSVVerdictUnresolved` with an error; a 200 with a non-JSON body → `OSVVerdictUnresolved` with an error.
- **Index cost** — with every ID answering 200, the handler must receive ZERO `/index/vulns.json` requests. With two IDs that both 404, the index must be requested exactly ONCE.
- **Index TTL** — after one 404 resolution, advance the clock past `24h` and force another 404: the index is requested again. Advance the clock by less than `24h`: it is not.
- **Body cap** — a 200 whose body exceeds the 8 MiB limit returns `OSVVerdictUnresolved` and an error naming the limit. One cap test is enough; the index fetch must go through the same over-limit check.
- **No credentials** — assert the handler saw no `Authorization` header on any OSV request.

### 6. Create `pkg/cursor_osvverdicts_test.go` — `package pkg_test`, the verdict cache

Use a temp dir + `pkg.LoadCursor`/`pkg.SaveCursor` (the existing `pkg/cursor_test.go` shows the pattern) plus a direct `&pkg.Cursor{}` where a round-trip is not the point. Cover:

- A recorded withdrawn verdict is returned by `CachedVerdict` within the TTL, and a recorded legitimate verdict likewise.
- A verdict older than `24h` reports `(_, false)`.
- An empty `CheckedAt`, an unparsable `CheckedAt`, and a missing entry all report `(_, false)`.
- `SetVerdict` with `OSVVerdictUnresolved` writes nothing (the map stays empty).
- `SetVerdict` on a zero-value `&pkg.Cursor{}` (nil map) does not panic and produces a readable entry.
- JSON round-trip: `SetVerdict` → `SaveCursor` → `LoadCursor` → `CachedVerdict` returns the same verdict; a legitimate verdict must serialise with `withdrawn` present as `false` (assert on the marshalled bytes, not just the decoded struct — an omitted key would read back as a different verdict).
- A cursor JSON written WITHOUT the `osv_verdicts` key still loads (`LoadCursor` succeeds, `CachedVerdict` reports false).

### 7. Self-check before finishing

Re-run `<verification>` and confirm every command passes. Then walk the acceptance criteria: withdrawn-vs-legitimate on a 200, the alias-404 path returning the QUERIED id, "any alias withdrawn", each unresolved case returning `OSVVerdictUnresolved` with no stale verdict, zero index requests when nothing 404s, the index TTL, and the cursor cache TTL and round-trip.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add any dependency. The client uses `net/http`, `net/url`, `encoding/json`, `io`, `github.com/bborbe/errors`, `github.com/golang/glog` and `github.com/bborbe/time` — all already direct deps.
- Do NOT import `bborbe/github-update-go-agent` — it is a deployable service, and pulling it in drags its whole dependency graph into the watcher.
- The OSV client must send NO credentials. It takes an `*http.Client` argument; the caller injects a plain client with its own 10s timeout (wired in the next prompt). Never reuse the GitHub App-authenticated client, and never add an `Authorization` header.
- Any upstream failure is `OSVVerdictUnresolved`, never stale. Unresolved never emits and is never cached.
- Cap both response bodies before decoding (8 MiB record, 64 MiB index) and never echo a body into a log line — status, URL path, byte count and ID only.
- URL-escape the advisory ID before it enters a request path (`url.PathEscape`) — IDs come from attacker-controlled repo files.
- Do NOT classify or remove an ignore, do NOT decide emission, do NOT touch `Poll`, `processRepos`, `completeMergedUpdates`, `pkg/filter/*`, or the emission claim.
- Do NOT change `Poll`'s schedule or add a goroutine, ticker or background refresh. The index is fetched lazily on the request path only.
- Cursor compatibility: every new `Cursor`/`RepoState` field is `omitempty`; a cursor written by the previous binary must load and cold-start.
- No new env var, chart value, PVC, topic or Config lane — the URLs and both TTLs are package constants.
- Never hand-edit anything under `mocks/` — `make precommit` regenerates it, which is how `mocks/osv_client.go` appears.
- Every new `.go` file starts with the BSD license header used by the existing files.
- Error handling follows `github.com/bborbe/errors`; time comes from the injected `libtime.CurrentDateTimeGetter`, never `time.Now()`.
- Existing tests must still pass unchanged.
- All paths in this prompt are repo-relative.
</constraints>

<verification>
Run from the repo root. Every `make` call MUST pass `ROOTDIR=/workspace` — `.dark-factory.yaml` sets `workflow: worktree`, the container masks `.git`, and `Makefile.variables` derives `ROOTDIR` from `git rev-parse --show-toplevel`, so an unqualified `make` dies before running any target.

```
ROOTDIR=/workspace make precommit
```

Must exit 0 (ensure, format, generate, test, check, addlicense).

```
ROOTDIR=/workspace go test -mod=mod ./pkg/ -run TestSuite -count=1
```

Must exit 0.

```
grep -c 'func (fake \*OSVClient) CheckWithdrawn' mocks/osv_client.go
```

Must print exactly `1` — the fake was generated from the `//counterfeiter:generate` directive (a `grep -c` here is safe because the expected count is non-zero).

```
grep -c 'osv_verdicts,omitempty' pkg/cursor.go
```

Must print exactly `1`.

```
grep -c 'OSVVerdictWithdrawn\|OSVVerdictLegitimate\|OSVVerdictUnresolved' pkg/osvclient.go
```

Must print at least `3`.

```
! grep -q 'Authorization' pkg/osvclient.go && echo NO_AUTH_HEADER
! grep -q 'time.Now()' pkg/osvclient.go pkg/cursor.go && echo NO_SYSTEM_CLOCK
! grep -q 'fmt.Errorf' pkg/osvclient.go && echo NO_FMT_ERRORF
! grep -q '/Users/' pkg/osvclient_test.go pkg/cursor_osvverdicts_test.go && echo NO_ABSOLUTE_PATHS
```

Each line must print its marker.

```
test -f pkg/testdata/osv/GO-2026-4923.json && test -f pkg/testdata/osv/CVE-2026-33817-404.json && test -f pkg/testdata/osv/index.json && echo OSV_FIXTURES_PRESENT
```

Must print `OSV_FIXTURES_PRESENT`.

```
ROOTDIR=/workspace go vet -mod=mod ./pkg/ && echo VET_OK
```

Must print `VET_OK`.
</verification>
