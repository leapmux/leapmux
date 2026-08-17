package hub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
)

// TestAdminServicesAreMounted boots the REAL server wiring (the same
// NewServer the solo/dev/hub binaries use) and asserts every admin.proto
// method answers on the mux — not 404. The auth package's descriptor walk
// cannot see registration: APIAuthService and DelegationService are the
// live precedent, declared in proto, generated, and never mounted as
// Connect handlers. An admin RPC that the interceptor restricts but the
// server does not mount is unreachable; one that the server mounts but
// the interceptor does not restrict is worse. This test pins
// the first half; admin_procedures_test.go pins the second.
func TestAdminServicesAreMounted(t *testing.T) {
	// The probes below go straight to the mux via httptest, never through
	// the listeners startTestServer binds.
	srv := startTestServer(t, &config.Config{})

	handler := srv.server.Handler

	// The probe signal: the mux's "/" fallback serves the SPA for any
	// unmatched path (200 text/html), while a MOUNTED Connect procedure
	// is processed by the Connect handler — an empty unauthenticated POST
	// is refused with a 4xx (decode or the auth gate). So "a 4xx, not
	// the SPA's 200" is exactly the mounted signal; the gate itself is
	// proven by the auth package's interceptor tests.
	probe := func(t *testing.T, path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(nil))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	services := leapmuxv1.File_leapmux_v1_admin_proto.Services()
	require.NotZero(t, services.Len(), "admin.proto exposes no services")
	for i := range services.Len() {
		svc := services.Get(i)
		for j := range svc.Methods().Len() {
			m := svc.Methods().Get(j)
			path := "/" + string(svc.FullName()) + "/" + string(m.Name())
			t.Run(path, func(t *testing.T) {
				code := probe(t, path)
				assert.GreaterOrEqualf(t, code, 400, "%s must be refused, not served", path)
				assert.Lessf(t, code, 500,
					"%s is declared in admin.proto and restricted by the auth interceptor but NOT mounted in NewServer; the SPA fallback's 200 swallowed it", path)
			})
		}
	}

	t.Run("canary: an unmounted path falls through to the SPA", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, probe(t, "/leapmux.v1.NoSuchService/NoSuchMethod"),
			"the SPA fallback serves unmatched paths; if this changes, the mounted-probe signal above needs re-deriving")
	})
}
