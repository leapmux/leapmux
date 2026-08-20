package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// TestIsLoopbackURLAcceptsExactlyTheSharedHostSet ties the redirect rule to the
// list the CSP's `form-action` derives from.
//
// Three places needed the same answer and each carried its own literal, with a
// comment claiming they matched and no test connecting them. A set that widens
// in one place and not the others is either a hole (the policy admits a host the
// redirect refuses) or an outage (the redirect offers one the policy blocks),
// and neither surfaces until a `leapmux control auth login` hangs.
func TestIsLoopbackURLAcceptsExactlyTheSharedHostSet(t *testing.T) {
	t.Parallel()

	for _, host := range httpsec.LoopbackHosts {
		hostInURL := host
		if host == "::1" {
			hostInURL = "[::1]"
		}
		assert.Truef(t, isLoopbackURL("http://"+hostInURL+":54321/cb"),
			"the policy admits %q, so the redirect must accept it", host)
	}

	// EXACTLY that set. A host the policy does not admit must be refused here,
	// or the redirect offers a hop the browser then blocks.
	for _, raw := range []string{
		"http://evil.test/cb",
		"http://127.0.0.1.evil.test/cb",
		"http://localhost.evil.test/cb",
		"http://169.254.169.254/cb",
		"https://example.com/cb",
		"not a url at all",
		"",
	} {
		assert.Falsef(t, isLoopbackURL(raw), "%q is not a loopback redirect target", raw)
	}
}
