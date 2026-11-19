package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The consent-route check must be exact on the path: only the two pages
// consentLeg serves with GET count, so a longer path like
// /oauth/authorizeX and the token endpoint stay out, and a query or a
// fragment does not change the page.
func TestConsentRouteMatchesTheServedPathExactly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		redirect string
		want     bool
	}{
		{"the authorize page", "/oauth/authorize", true},
		{"the authorize page with a query", "/oauth/authorize?client_id=abc", true},
		{"the authorize page with a fragment", "/oauth/authorize#done", true},
		{"the device page", "/oauth/device", true},
		{"the device page with a query", "/oauth/device?user_code=1234-5678", true},
		{"a longer path beside the authorize page", "/oauth/authorizeX", false},
		{"a longer path under the authorize page", "/oauth/authorize/extra", false},
		{"the trailing slash of the authorize page", "/oauth/authorize/", false},
		{"the token endpoint", "/oauth/token", false},
		{"the consent endpoint, served with POST only", "/oauth/consent", false},
		{"the home page", "/", false},
		{"an empty redirect", "", false},
		{"the authorize page named inside a query", "/login?to=/oauth/authorize", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isConsentRoute(tt.redirect))
		})
	}
}
