package captcha

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
)

// protectedProcedureRationale records, for every captcha-protected PUBLIC
// procedure, why anonymous automation must pre-pay proof-of-work before
// its handler runs. This is the captcha sibling of the auth package's
// publicProcedureRationale tripwire: protectedProcedures lists the doors
// that make an unauthenticated caller run Argon2 or create users, so an
// entry without a written reason is unaudited.
var protectedProcedureRationale = map[string]string{
	leapmuxv1connect.AuthServiceLoginProcedure:                           "runs Argon2 verification for an anonymous caller",
	leapmuxv1connect.AuthServiceSignUpProcedure:                          "creates a user (Argon2 hash, optional SMTP) for an anonymous caller",
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:             "consumes a single-use pending id and creates a user for an anonymous caller",
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:               "starts a passkey assertion ceremony for an anonymous caller",
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:              "starts a passkey registration ceremony for an anonymous caller",
	leapmuxv1connect.AuthServiceRequestAccountRecoveryProcedure:          "issues a recovery email for an anonymous caller",
	leapmuxv1connect.AuthServiceCompleteAccountRecoveryPasswordProcedure: "runs Argon2 and rotates credentials for an anonymous caller with a recovery token",
	leapmuxv1connect.AuthServiceBeginAccountRecoveryPasskeyProcedure:     "charges the recovery token's attempt budget and resolves the account for an anonymous caller",
}

// protectedAuthenticatedRationale is the authenticated sibling of the map
// above: these procedures sit behind the session gate rather than the
// anonymous one, and are protected because a scripted session can charge
// them cheaply -- the guess budget costs nothing to burn, and the resend
// path drives an SMTP send toward an address the cooldown gate alone no
// longer fully paces.
var protectedAuthenticatedRationale = map[string]string{
	leapmuxv1connect.UserServiceVerifyEmailProcedure:             "charges the per-code wrong-guess budget, which a script burns for free",
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure: "drives an SMTP send toward the cooldown gate",
}

// captchaExemptRationale records, for every OTHER public procedure, why it
// is deliberately NOT captcha-protected. Together the two maps force the
// author of the next public procedure to decide, at test time, which side
// it is on -- forgetting the protectedProcedures entry otherwise compiles,
// passes the auth package's rationale test, and ships the procedure
// unprotected.
var captchaExemptRationale = map[string]string{
	leapmuxv1connect.AuthServiceSetInitialSoloPasswordProcedure:             "solo disables captcha; ratelimit.OpSoloPasswordSetup caps its Argon2 hash by address instead",
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:                      "cheap pre-login read",
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:                  "cheap pre-login read",
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure:              "read keyed by a single-use pending id; no expensive action",
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:                 "issues the ALTCHA challenges themselves; protecting it would be circular",
	leapmuxv1connect.AuthServiceFinishPasskeyLoginProcedure:                 "consumes a short-lived ceremony session; expensive work is in Begin",
	leapmuxv1connect.AuthServiceFinishPasskeySignUpProcedure:                "consumes a short-lived ceremony session; expensive work is in Begin",
	leapmuxv1connect.AuthServiceFinishAccountRecoveryPasskeyProcedure:       "consumes a short-lived ceremony session the captcha'd Begin minted",
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure:                "caller is a worker process with a registration key, not a human form",
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:                 "caller is a worker process with an auth_token, not a human form",
	leapmuxv1connect.WorkerReconcilerServiceListOwnedTabsForWorkerProcedure: "caller is a worker process with an auth_token, not a human form",
}

func TestProtectedProceduresAreClassified(t *testing.T) {
	assert.NotEmpty(t, protectedProcedures, "no protected procedures; the tripwire is vacuous")

	public := make(map[string]bool)
	for _, p := range auth.PublicProcedures() {
		public[p] = true
	}

	for procedure := range protectedProcedures {
		_, publicRationale := protectedProcedureRationale[procedure]
		_, authRationale := protectedAuthenticatedRationale[procedure]
		assert.Truef(t, publicRationale || authRationale,
			"protected procedure %q has no rationale; record why automation must pre-pay proof-of-work", procedure)
		assert.Falsef(t, publicRationale && authRationale,
			"protected procedure %q is classified as both public and authenticated; pick one", procedure)
		// A PUBLIC rationale claims the anonymous door; the auth
		// interceptor's list must agree. An authenticated rationale makes
		// no such claim -- the session gate is the entry, not the captcha.
		if publicRationale {
			assert.Truef(t, public[procedure],
				"protected procedure %q has a public rationale but is not in the auth interceptor's publicProcedures", procedure)
		}
	}
	for procedure := range protectedProcedureRationale {
		assert.Truef(t, func() bool { _, ok := protectedProcedures[procedure]; return ok }(),
			"%q has a protection rationale but is no longer protected; drop the entry", procedure)
	}
	for procedure := range protectedAuthenticatedRationale {
		assert.Truef(t, func() bool { _, ok := protectedProcedures[procedure]; return ok }(),
			"%q has an authenticated protection rationale but is no longer protected; drop the entry", procedure)
	}
	// The forget-direction tripwire: every public procedure must be
	// classified on exactly one side.
	for procedure := range public {
		_, protected := protectedProcedures[procedure]
		_, exempted := captchaExemptRationale[procedure]
		assert.Truef(t, protected != exempted,
			"public procedure %q is unclassified or double-classified: either add it to protectedProcedures with a rationale, or record why it is exempt in captchaExemptRationale", procedure)
	}

	// Every protected procedure carries a non-empty action in its own
	// entry (the string its clients mint provider tokens under; reCAPTCHA
	// verifies it server-side), so a new protected procedure cannot
	// silently reach Verify with an empty action.
	for procedure, proc := range protectedProcedures {
		assert.NotEmptyf(t, proc.action, "protected procedure %q maps to an empty action", procedure)
	}
}
