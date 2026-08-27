package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestIsLoopbackURLRequiresAnHTTPScheme pins the scheme half of the rule.
//
// The accepted value reaches a Location header. A host test alone accepts
// "javascript://127.0.0.1/...": the host IS loopback and the scheme
// executes, so the hostname allowlist reports a URL that never belonged in
// a redirect. A CLI serves its callback over http or https and needs
// nothing else.
func TestIsLoopbackURLRequiresAnHTTPScheme(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://127.0.0.1:54321/cb",
		"https://127.0.0.1:54321/cb",
		"http://localhost:54321/cb",
	} {
		assert.Truef(t, isLoopbackURL(raw), "%q is a served loopback callback", raw)
	}

	for _, raw := range []string{
		"javascript://127.0.0.1/%0aalert(1)",
		"javascript://localhost/",
		"JavaScript://127.0.0.1/",
		"data://localhost/",
		"file://localhost/etc/passwd",
		"ftp://127.0.0.1/cb",
		"//127.0.0.1/cb",
	} {
		assert.Falsef(t, isLoopbackURL(raw),
			"%q has a loopback host but no scheme a CLI callback can use", raw)
	}
}

// TestIsLoopbackURLAgreesWithTheCSPSchemes pins the other half of the
// invariant httpsec/loopback.go states: the redirect and the CSP's
// form-action must accept the same schemes.
//
// A scheme this function accepts while the policy omits it is a CLI login
// that hangs, with nothing in any log to say why. The two now read one
// list, and this test fails if a caller reintroduces a literal.
func TestIsLoopbackURLAgreesWithTheCSPSchemes(t *testing.T) {
	t.Parallel()

	for _, scheme := range httpsec.LoopbackSchemes {
		assert.Truef(t, isLoopbackURL(scheme+"://127.0.0.1:54321/cb"),
			"%q is a scheme the CSP admits, so the redirect must accept it", scheme)
	}
	assert.NotEmpty(t, httpsec.LoopbackSchemes)
}

// TestGateModeForDerivesFromTheMethod pins the rule the four CLI consent
// legs used to each restate for themselves.
//
// A GET is replayable from its own URL, so the gate sends an un-elevated
// caller away, and that caller comes back to exactly that address. The gate
// refuses anything that carries its parameters in the BODY instead:
// handleAuthorize reads the PKCE challenge from the form, and a redirect
// would drop it, so the user would return to a consent page that forgot
// what it consented to.
//
// HEAD is a GET without the body, so it bounces too rather than taking the
// body-carrying default.
func TestGateModeForDerivesFromTheMethod(t *testing.T) {
	t.Parallel()

	for method, want := range map[string]gateMode{
		http.MethodGet:    gateBounce,
		http.MethodHead:   gateBounce,
		http.MethodPost:   gateRefuse,
		http.MethodPut:    gateRefuse,
		http.MethodPatch:  gateRefuse,
		http.MethodDelete: gateRefuse,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(method, "http://hub.example.com/auth/cli/start", nil)
			require.NoError(t, err)
			assert.Equal(t, want, gateModeFor(req))
		})
	}
}
