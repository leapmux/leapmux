package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refreshRequest builds a parsed token-endpoint request the flight key can
// read the presented client credentials from.
func refreshRequest(t *testing.T, form string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, r.ParseForm())
	return r
}

// TestRefreshFlightKeySeparatesClientCredentials pins the follower-auth half
// of the refresh singleflight: only the leader's closure runs the RFC 6749
// section 6 client authentication, so a follower whose credentials the leader
// never saw must NOT share the leader's flight -- it would receive the
// leader's rotated pair without its own authentication ever running. The key
// carries a digest of the presented credentials so each distinct presentation
// runs its own flight (and its own refusal).
func TestRefreshFlightKeySeparatesClientCredentials(t *testing.T) {
	t.Parallel()

	parsed := parsedRefreshBearer{tokenID: "token-1", secretHash: []byte{1, 2, 3}}
	legit := refreshRequest(t, "client_id=app&client_secret=right")
	again := refreshRequest(t, "client_id=app&client_secret=right")
	attacker := refreshRequest(t, "client_id=app&client_secret=wrong")
	nobody := refreshRequest(t, "client_id=app")

	assert.Equal(t, refreshFlightKey(parsed, "", legit), refreshFlightKey(parsed, "", again),
		"the same presentation still collapses onto one flight, keeping the rotation single-use")
	assert.NotEqual(t, refreshFlightKey(parsed, "", legit), refreshFlightKey(parsed, "", attacker),
		"a different secret must run its own flight, where its own authentication refuses it")
	assert.NotEqual(t, refreshFlightKey(parsed, "", legit), refreshFlightKey(parsed, "", nobody),
		"a missing secret must run its own flight, where its own authentication refuses it")

	// The Basic form of the SAME credentials shares the flight with the form
	// body spelling, matching how authenticateClientOpts reads either.
	basic := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	basic.SetBasicAuth("app", "right")
	assert.Equal(t, refreshFlightKey(parsed, "", legit), refreshFlightKey(parsed, "", basic))

	// The raw secret never appears in the key: only its digest does.
	assert.NotContains(t, refreshFlightKey(parsed, "", legit), "right")
}
