package captcha

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
)

// protectedProcedureRationale records, for every captcha-protected
// procedure, why anonymous automation must pre-pay proof-of-work before
// its handler runs. This is the captcha sibling of the auth package's
// publicProcedureRationale tripwire: protectedProcedures names the doors
// that make an unauthenticated caller run Argon2 or create users, so an
// entry without a written reason is unaudited.
var protectedProcedureRationale = map[string]string{
	leapmuxv1connect.AuthServiceLoginProcedure:               "runs Argon2 verification for an anonymous caller",
	leapmuxv1connect.AuthServiceSignUpProcedure:              "creates a user (Argon2 hash, optional SMTP) for an anonymous caller",
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure: "consumes a single-use pending id and creates a user for an anonymous caller",
}

// captchaExemptRationale records, for every OTHER public procedure, why it
// is deliberately NOT captcha-protected. Together the two maps force the
// author of the next public procedure to decide, at test time, which side
// it is on -- forgetting the protectedProcedures entry otherwise compiles,
// passes the auth package's rationale test, and ships the procedure
// unprotected.
var captchaExemptRationale = map[string]string{
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:                      "cheap pre-login read",
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:                  "cheap pre-login read",
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure:              "read keyed by a single-use pending id; no expensive action",
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:                 "issues the ALTCHA challenges themselves; protecting it would be circular",
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
		note, ok := protectedProcedureRationale[procedure]
		assert.Truef(t, ok, "protected procedure %q has no rationale; record why automation must pre-pay proof-of-work", procedure)
		assert.NotEmptyf(t, note, "protected procedure %q has an empty rationale", procedure)
		assert.Truef(t, public[procedure],
			"protected procedure %q is not in the auth interceptor's publicProcedures; the captcha check assumes an anonymous caller", procedure)
	}
	for procedure := range protectedProcedureRationale {
		assert.Truef(t, func() bool { _, ok := protectedProcedures[procedure]; return ok }(),
			"%q has a protection rationale but is no longer protected; drop the entry", procedure)
	}
	// The forget-direction tripwire: every public procedure must be
	// classified on exactly one side.
	for procedure := range public {
		_, protected := protectedProcedures[procedure]
		_, exempted := captchaExemptRationale[procedure]
		assert.Truef(t, protected != exempted,
			"public procedure %q is unclassified or double-classified: either add it to protectedProcedures with a rationale, or record why it is exempt in captchaExemptRationale", procedure)
	}

	// Every protected procedure also carries an action name (the string
	// its clients mint provider tokens under; reCAPTCHA verifies it
	// server-side), so a new protected procedure cannot silently reach
	// Verify with an empty action.
	for procedure := range protectedProcedures {
		action, ok := procedureActions[procedure]
		assert.Truef(t, ok, "protected procedure %q has no action in procedureActions; add the action its clients execute", procedure)
		assert.NotEmptyf(t, action, "protected procedure %q maps to an empty action", procedure)
	}
	assert.Len(t, procedureActions, len(protectedProcedures), "procedureActions and protectedProcedures must stay in lockstep")
}
