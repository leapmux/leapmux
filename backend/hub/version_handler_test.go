package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/version"
)

// TestVersionHandler_ReturnsJSONIdentity locks the response shape
// `leapmux remote version` consumes. The CLI relies on these exact
// keys, so any future rename here will break that consumer; tests
// guard that contract.
func TestVersionHandler_ReturnsJSONIdentity(t *testing.T) {
	// Save / restore — Value is build-time and may be "dev" in tests.
	prev := version.Value
	version.Value = "test-9.9.9"
	t.Cleanup(func() { version.Value = prev })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	versionHandler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))

	for _, k := range []string{"version", "commit", "branch", "build_time", "formatted"} {
		assert.Contains(t, body, k, "version response must include %q", k)
	}
	assert.Equal(t, "test-9.9.9", body["version"])
	assert.Contains(t, body["formatted"], "test-9.9.9")
}

// TestVersionHandler_DoesNotRequireAuth pins the design choice that
// /version is callable without any auth header. If a future change
// adds a global auth middleware on the hub mux, the test catches it
// before users hit the breakage at runtime.
//
// Implementation note: this is a unit test on the handler function,
// so it isolates the handler from the surrounding mux/middleware
// stack. A higher-level test would need to start the full hub server,
// which is out of scope here. The CLI-side test in
// internal/cli/remote/cmd/version_test.go does exercise an httptest
// server with no auth, providing the round-trip coverage.
func TestVersionHandler_DoesNotRequireAuth(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	// No Authorization, no cookie — must still 200.
	versionHandler(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestVersionHandler_AcceptsAnyMethod documents that the handler
// doesn't gate on HTTP method. Curling with HEAD or POST is harmless
// since the response body is read-only and idempotent. We pin the
// behaviour rather than rejecting non-GET, because rejecting would be
// slightly user-hostile (a `curl -X HEAD` smoke check would 405) for
// no security benefit.
func TestVersionHandler_AcceptsAnyMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/version", nil)
		versionHandler(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "method %s", method)
	}
}
