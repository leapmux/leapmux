package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A hand-written call inside each handler enforces the elevation gate, and no
// interceptor rung can replace it: the first-credential sibling rule needs
// the account's shape (a user row plus a passkey count) before it can say
// whether a caller must elevate at all, and an interceptor admits or refuses
// -- it cannot say "the handler decides". Four of the legs are not Connect
// procedures either; they are the /oauth/* consent pages, whose refusal
// has a third shape (redirect a GET, refuse a POST) that a rung cannot
// express.
//
// So the omission an interceptor would make impossible is possible here: a
// new sensitive RPC that nobody wires to requireElevation ships with no
// gate, and nothing says so. This record is the substitute. It is the same
// bidirectional tripwire shape the auth package already uses for the public,
// delegation and admin gates (auth/admin_procedures_test.go), and it fails
// the suite when user.proto grows a method that nobody classified.
//
// Classifying a procedure here is NOT what protects it. The handler's own call
// is. What this catches is a procedure nobody THOUGHT about.

// elevationClass says how one UserService procedure relates to the gate.
type elevationClass int

const (
	// protectedByRequireElevation calls requireElevation directly.
	protectedByRequireElevation elevationClass = iota
	// protectedByPasskeyManagementAuth runs through the fork that chooses
	// between elevation and the first-credential sibling rule.
	protectedByPasskeyManagementAuth
	// grantsOrDropsElevation is a factor path, so it cannot require what it
	// exists to produce.
	grantsOrDropsElevation
	// unprotectedOnPurpose reads nothing sensitive, or only reduces access.
	unprotectedOnPurpose
)

// userProcedureElevation records the class of every UserService procedure,
// with the reason in the same place as the classification.
var userProcedureElevation = map[string]struct {
	class  elevationClass
	reason string
}{
	// The factor paths. Requiring elevation here would be a deadlock.
	leapmuxv1connect.UserServiceElevateSessionProcedure:         {grantsOrDropsElevation, "proves a password and grants the window"},
	leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure:  {grantsOrDropsElevation, "mints the step-up assertion options"},
	leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure: {grantsOrDropsElevation, "verifies the step-up assertion and grants the window"},
	leapmuxv1connect.UserServiceDropElevationProcedure:          {grantsOrDropsElevation, "ends the window; only ever reduces access"},

	// Sensitive, and protected by a hand-written call. Each moves a credential
	// or a recovery identity.
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure:        {protectedByRequireElevation, "the account email receives the account-recovery link"},
	leapmuxv1connect.UserServiceUnlinkOAuthProviderProcedure:       {protectedByRequireElevation, "detaching a provider removes a login method the owner cannot re-attach"},
	leapmuxv1connect.UserServiceChangePasswordProcedure:            {protectedByPasskeyManagementAuth, "sets or replaces the password; an account with nothing to elevate with takes the sibling rule"},
	leapmuxv1connect.UserServiceBeginPasskeyRegistrationProcedure:  {protectedByPasskeyManagementAuth, "peeks the admission before the browser prompt"},
	leapmuxv1connect.UserServiceFinishPasskeyRegistrationProcedure: {protectedByPasskeyManagementAuth, "attaches a durable credential"},
	leapmuxv1connect.UserServiceRenamePasskeyProcedure:             {protectedByPasskeyManagementAuth, "relabels a credential the owner uses to decide what to remove"},
	leapmuxv1connect.UserServiceDeletePasskeyProcedure:             {protectedByPasskeyManagementAuth, "removes a login method"},
	leapmuxv1connect.UserServiceDeactivatePasskeyAuthProcedure:     {protectedByPasskeyManagementAuth, "removes every passkey at once"},

	// Deliberately unprotected, each for a stated reason.
	leapmuxv1connect.UserServiceListMyAPITokensProcedure:         {unprotectedOnPurpose, "metadata only; no secret and no hash leaves the store layer"},
	leapmuxv1connect.UserServiceRevokeMyAPITokenProcedure:        {unprotectedOnPurpose, "only REDUCES access; a delay is the attacker's gain"},
	leapmuxv1connect.UserServiceDisconnectAppProcedure:           {unprotectedOnPurpose, "ends every credential one app holds; it only REDUCES access, and it is the remedy on realizing an app is malicious"},
	leapmuxv1connect.UserServiceUpdateProfileProcedure:           {unprotectedOnPurpose, "a username and a display name are not credentials"},
	leapmuxv1connect.UserServiceListPasskeysProcedure:            {unprotectedOnPurpose, "reads the owner's own credential metadata"},
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure: {unprotectedOnPurpose, "re-sends to the address already on the account; it moves nothing"},
	leapmuxv1connect.UserServiceVerifyEmailProcedure:             {unprotectedOnPurpose, "the emailed code IS the proof; requiring elevation on top would demand a factor to finish proving a factor"},
	leapmuxv1connect.UserServiceListUserSettingsProcedure:        {unprotectedOnPurpose, "reads the caller's own preferences"},
	leapmuxv1connect.UserServiceUpdateUserSettingProcedure:       {unprotectedOnPurpose, "writes the caller's own preference"},
	leapmuxv1connect.UserServiceResetUserSettingProcedure:        {unprotectedOnPurpose, "returns the caller's own preference to its default"},
	leapmuxv1connect.UserServiceGetTimeoutsProcedure:             {unprotectedOnPurpose, "reads instance timeouts every client needs to render"},
}

// protoProcedurePaths walks one proto file's service descriptors and builds
// the Connect procedure path for every method, in the shape
// auth/admin_procedures_test.go uses.
//
// It takes the file descriptor rather than hard-coding one, because TWO
// classification records read it: this one over user.proto, and
// adminProcedureElevation over admin.proto. Two copies of the walk would let
// one record check a method set the other does not.
func protoProcedurePaths(t *testing.T, file protoreflect.FileDescriptor) []string {
	t.Helper()
	services := file.Services()
	var out []string
	for i := range services.Len() {
		svc := services.Get(i)
		for j := range svc.Methods().Len() {
			m := svc.Methods().Get(j)
			out = append(out, "/"+string(svc.FullName())+"/"+string(m.Name()))
		}
	}
	require.NotEmptyf(t, out, "%s exposes no methods; the descriptor walk has nothing to check", file.Path())
	return out
}

// TestEveryUserProcedureIsElevationClassified is the tripwire: a new
// UserService method fails this test until somebody states which side of the
// gate it sits on and why.
//
// It checks the RECORD, not the handler, and it cannot check the handler
// from here: the classification is a decision, and only a request can show
// what a handler does with one. TestGatedRPCs_TellASessionToElevate is the
// other half -- it drives every procedure classified as protected through a
// real un-elevated session and asserts the refusal and its marker. Add a
// procedure to one and to the other.
func TestEveryUserProcedureIsElevationClassified(t *testing.T) {
	t.Parallel()

	paths := protoProcedurePaths(t, leapmuxv1.File_leapmux_v1_user_proto)
	for _, path := range paths {
		entry, ok := userProcedureElevation[path]
		assert.Truef(t, ok,
			"user.proto method %q is not elevation-classified; the gate is a hand-written call in each handler, so an unclassified procedure is one nobody decided about -- add it to userProcedureElevation with a reason", path)
		assert.NotEmptyf(t, entry.reason, "user.proto method %q has an empty reason", path)
	}

	known := make(map[string]bool, len(paths))
	for _, path := range paths {
		known[path] = true
	}
	for procedure := range userProcedureElevation {
		assert.Truef(t, known[procedure],
			"procedure %q is elevation-classified but user.proto no longer declares it; remove the stale entry", procedure)
	}
}
