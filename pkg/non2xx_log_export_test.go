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
