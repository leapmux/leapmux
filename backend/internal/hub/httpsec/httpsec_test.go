package httpsec

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

func serve(t *testing.T, h http.Handler) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Result()
}

func TestPolicyHeader(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Content-Security-Policy", Policy{}.Header())
	assert.Equal(t, "Content-Security-Policy-Report-Only", Policy{ReportOnly: true}.Header())
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("sets the policy under the enforcing header", func(t *testing.T) {
		resp := serve(t, Middleware(Policy{CSP: "default-src 'self'"}, okHandler()))
		assert.Equal(t, "default-src 'self'", resp.Header.Get("Content-Security-Policy"))
		assert.Empty(t, resp.Header.Get("Content-Security-Policy-Report-Only"))
	})

	t.Run("sets the policy under the report-only header", func(t *testing.T) {
		resp := serve(t, Middleware(Policy{CSP: "default-src 'self'", ReportOnly: true}, okHandler()))
		assert.Equal(t, "default-src 'self'", resp.Header.Get("Content-Security-Policy-Report-Only"))
		assert.Empty(t, resp.Header.Get("Content-Security-Policy"),
			"a report-only policy must never be sent as an enforcing one")
	})

	// An empty policy means "this caller cannot know its assets". Sending a
	// guessed policy there is an outage rather than a weaker defence, so the
	// header must be absent -- not empty, absent.
	t.Run("sends no CSP header for an empty policy", func(t *testing.T) {
		resp := serve(t, Middleware(Policy{}, okHandler()))
		_, enforced := resp.Header["Content-Security-Policy"]
		_, reported := resp.Header["Content-Security-Policy-Report-Only"]
		assert.False(t, enforced)
		assert.False(t, reported)
	})

	// These two protect EVERY response, not only the app document, so they are
	// set even when there is no policy at all.
	t.Run("always sets nosniff and a referrer policy", func(t *testing.T) {
		for _, p := range []Policy{{}, {CSP: "default-src 'self'"}, {CSP: "x", ReportOnly: true}} {
			resp := serve(t, Middleware(p, okHandler()))
			assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
			assert.Equal(t, "same-origin", resp.Header.Get("Referrer-Policy"))
		}
	})

	t.Run("calls the next handler and keeps its body and status", func(t *testing.T) {
		h := Middleware(Policy{CSP: "default-src 'self'"}, http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte("body"))
			}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusTeapot, rec.Code)
		assert.Equal(t, "body", rec.Body.String())
		assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
	})

	// The headers are written BEFORE next runs, so a handler that commits its
	// status immediately still carries them. A middleware that set them after
	// the call would silently drop every one of them on this path.
	t.Run("sets the headers on a handler that writes its status at once", func(t *testing.T) {
		h := Middleware(Policy{CSP: "default-src 'self'"}, http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "no", http.StatusForbidden)
			}))
		resp := serve(t, h)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, "default-src 'self'", resp.Header.Get("Content-Security-Policy"))
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	})

	// A route that deliberately needs its own value wins. Set replaces, so the
	// inner handler's answer is the one the browser reads.
	t.Run("lets a handler override a header for its own route", func(t *testing.T) {
		h := Middleware(Policy{CSP: "default-src 'self'"}, http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Referrer-Policy", "no-referrer")
			}))
		resp := serve(t, h)
		assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
			"an override of one header must not disturb the others")
	})
}
