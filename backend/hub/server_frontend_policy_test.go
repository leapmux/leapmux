package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// resolveFrontend returns the handler and its policy from ONE function,
// because the script-src hashes are derived from the assets actually mounted.
// A policy built for a different frontend than the one being served refuses
// the app's own script, which is a blank page rather than a weaker defence --
// so each arm is pinned here.
func TestResolveFrontend(t *testing.T) {
	t.Parallel()

	// An injected frontend brings assets this process cannot read. A guessed
	// policy would be the outage above, so the correct answer is none.
	t.Run("an injected handler is served with no policy", func(t *testing.T) {
		injected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("injected"))
		})

		got, policy, err := resolveFrontend(injected, "")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, policy.CSP, "assets we cannot read must not be given a guessed policy")

		rec := httptest.NewRecorder()
		got.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, "injected", rec.Body.String(), "the injected handler must be the one mounted")
	})

	// An injected handler wins over a dev-frontend URL. Both set at once is a
	// caller's mistake, and the explicit override is the more specific intent.
	t.Run("an injected handler wins over a dev frontend URL", func(t *testing.T) {
		injected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		_, policy, err := resolveFrontend(injected, "http://localhost:4328")
		require.NoError(t, err)
		assert.Empty(t, policy.CSP)
		assert.False(t, policy.ReportOnly)
	})

	// Vite's HMR client injects inline scripts and evaluates source maps, so
	// an ENFORCED policy stops hot reload. A developer who cannot reload turns
	// the header off rather than fixing one directive, which is how a project
	// ends up with no policy at all.
	t.Run("a dev frontend is proxied under a report-only policy", func(t *testing.T) {
		got, policy, err := resolveFrontend(nil, "http://localhost:4328")
		require.NoError(t, err)
		require.NotNil(t, got)

		assert.True(t, policy.ReportOnly, "an enforced policy would break Vite HMR")
		assert.Equal(t, "Content-Security-Policy-Report-Only", policy.Header())
		assert.NotEmpty(t, policy.CSP, "report-only still surfaces a violation in the console")
	})

	t.Run("reports a dev frontend URL it cannot parse", func(t *testing.T) {
		_, _, err := resolveFrontend(nil, "://not a url")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dev proxy")
	})

	// The shipped path. The policy is enforced and derived from the embedded
	// assets, so it carries at least one script hash.
	t.Run("the embedded frontend is served under the enforced derived policy", func(t *testing.T) {
		got, policy, err := resolveFrontend(nil, "")
		require.NoError(t, err)
		require.NotNil(t, got)

		assert.False(t, policy.ReportOnly, "the shipped policy must bite")
		assert.Equal(t, "Content-Security-Policy", policy.Header())
		require.NotEmpty(t, policy.CSP)
		assert.Contains(t, policy.CSP, "'sha256-",
			"the policy must carry the hash derived from the embedded index.html")

		// Scoped to script-src on purpose. A substring test against the whole
		// policy matches style-src's own 'unsafe-inline', which the terminal
		// renderer requires, so it would pass for the wrong reason.
		assert.NotContains(t, directiveOf(t, policy.CSP, "script-src"), "'unsafe-inline'",
			"script-src must not fall back to 'unsafe-inline'")
	})

	// The three arms must not collapse onto one policy. A refactor that made
	// every arm return the same value would still pass each case above.
	t.Run("each arm yields a distinct policy", func(t *testing.T) {
		_, injectedPolicy, err := resolveFrontend(http.NotFoundHandler(), "")
		require.NoError(t, err)
		_, devPolicy, err := resolveFrontend(nil, "http://localhost:4328")
		require.NoError(t, err)
		_, embeddedPolicy, err := resolveFrontend(nil, "")
		require.NoError(t, err)

		assert.NotEqual(t, injectedPolicy, devPolicy)
		assert.NotEqual(t, devPolicy, embeddedPolicy)
		assert.NotEqual(t, injectedPolicy, embeddedPolicy)
	})
}

// directiveOf returns the named directive's sources, and fails when the
// directive is absent -- so a renamed or dropped directive reports itself
// rather than passing a NotContains assertion for the wrong reason.
func directiveOf(t *testing.T, policy, name string) string {
	t.Helper()
	for _, d := range strings.Split(policy, ";") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(d), name+" "); ok {
			return after
		}
	}
	require.Failf(t, "missing directive", "policy %q holds no %s directive", policy, name)
	return ""
}

// The middleware is wired around the WHOLE mux, so a route that is not the
// frontend carries the headers too. The hub renders HTML outside the frontend
// handler (the device-code and PKCE callback pages), and those pages need the
// same treatment as the app document.
func TestSecurityHeadersWrapTheWholeMux(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"test"}`))
	})

	_, policy, err := resolveFrontend(nil, "")
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	httpsec.Middleware(policy, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", rec.Header().Get("Referrer-Policy"))
	assert.True(t, strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "script-src "),
		"a non-frontend route carries the same policy the document does")
}
