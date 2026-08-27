package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// The admin surface takes the elevation gate by a hand-written call in each
// handler, exactly as the user surface does, and it had no classification
// record at all. userProcedureElevation walks user.proto only, so an Admin*
// procedure that nobody wired to a gate satisfied every test.
//
// That omission happened, and it happened five times in one change:
// AdminOAuthService's three write verbs, AdminUserService.DeleteUser and
// AdminUserService.UpdateUser all shipped with NO gate while the sign-up
// toggle beside them had one. This record is the tripwire for the next one.
//
// Classifying a procedure here is NOT what protects it. The handler's own
// call is. What this catches is a procedure nobody THOUGHT about.

// adminElevationClass says how one admin procedure relates to the gate.
//
// A separate enum from elevationClass, because the two surfaces admit
// different credentials. The user surface asks "did this SESSION prove a
// factor"; the admin surface has to answer for a command-line credential
// too, and the answer differs per verb.
type adminElevationClass int

const (
	// adminNeedsElevatedSession calls requireElevatedSessionForDurableAuthority:
	// an elevated SESSION, and a bearer is refused outright.
	adminNeedsElevatedSession adminElevationClass = iota
	// adminNeedsElevatedCredential calls requireElevatedActor, directly or
	// through writeUnderElevation: any credential that can carry an
	// elevation, a command-line credential included.
	adminNeedsElevatedCredential
	// adminNeedsElevatedSessionForSomeFields takes the strict session rule
	// for one class of field and the credential rule for the rest.
	adminNeedsElevatedSessionForSomeFields
	// adminNeedsNoElevation reads, or only reduces access.
	adminNeedsNoElevation
)

// adminProcedureElevation records the class of every admin procedure, with
// the reason in the same place as the classification.
//
// It is the sibling of userProcedureElevation, and of the rationale record in
// auth/admin_procedures_test.go, which answers a different question: that one
// says why a procedure is ADMIN-ONLY, this one says what an administrator has
// to prove on top of holding the role.
var adminProcedureElevation = map[string]struct {
	class  adminElevationClass
	reason string
}{
	// --- AdminSettingsService ---
	//
	// Every WRITE takes writeUnderElevation and every READ takes nothing. A
	// hub setting is deployment-wide, and several of these keys are the hub's
	// own security controls.
	leapmuxv1connect.AdminSettingsServiceListSettingsProcedure:        {adminNeedsNoElevation, "a read; it reports which keys HOLD a secret and never a secret value"},
	leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure:       {adminNeedsElevatedCredential, "rewrites instance configuration; the CLI is the documented headless path"},
	leapmuxv1connect.AdminSettingsServiceUpdateSettingSecretProcedure: {adminNeedsElevatedCredential, "writes the encrypted halves (SMTP password, captcha secrets)"},
	leapmuxv1connect.AdminSettingsServiceUpdateSettingsProcedure:      {adminNeedsElevatedCredential, "rewrites several settings at once, both halves, in one transaction"},
	leapmuxv1connect.AdminSettingsServiceResetSettingProcedure:        {adminNeedsElevatedCredential, "returns instance configuration to defaults, which can turn a control off"},
	leapmuxv1connect.AdminSettingsServiceResetSettingsProcedure:       {adminNeedsElevatedCredential, "returns several settings to defaults in one transaction"},

	// --- AdminUserService ---
	//
	// The strict session rule covers the verbs that create DURABLE NEW
	// AUTHORITY. requireElevatedSessionForDurableAuthority states the whole
	// of that rule.
	leapmuxv1connect.AdminUserServiceCreateUserProcedure:    {adminNeedsElevatedSession, "mints an account, optionally an administrator, with a password the caller chooses"},
	leapmuxv1connect.AdminUserServiceSetUserAdminProcedure:  {adminNeedsElevatedSession, "grants administration itself"},
	leapmuxv1connect.AdminUserServiceResetPasswordProcedure: {adminNeedsElevatedSession, "sets any account's password without the old one"},
	leapmuxv1connect.AdminUserServiceUpdateUserProcedure: {adminNeedsElevatedSessionForSomeFields,
		"the email fields are a recovery identity, so they take the strict session rule; a display name or a cleared pending address takes the credential rule"},

	// The credential rule: an elevated command-line credential is admitted,
	// because each of these has a documented headless verb and none of them
	// creates a new way INTO an account.
	leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure: {adminNeedsElevatedCredential, "mints a credential that outlives the session; the mint's own clamp contains the bearer path"},
	leapmuxv1connect.AdminUserServiceDeleteUserProcedure:    {adminNeedsElevatedCredential, "irreversible destruction of an account and everything it owns"},

	// Reads, and revokes that only reduce access.
	leapmuxv1connect.AdminUserServiceListUsersProcedure:             {adminNeedsNoElevation, "a read across accounts"},
	leapmuxv1connect.AdminUserServiceGetUserProcedure:               {adminNeedsNoElevation, "a read of one account"},
	leapmuxv1connect.AdminUserServiceListUserSessionsProcedure:      {adminNeedsNoElevation, "a read of one user's sessions"},
	leapmuxv1connect.AdminUserServiceListSessionsProcedure:          {adminNeedsNoElevation, "a read of every live session"},
	leapmuxv1connect.AdminUserServiceRevokeSessionProcedure:         {adminNeedsNoElevation, "only REDUCES access; a delay is the attacker's gain"},
	leapmuxv1connect.AdminUserServiceRevokeUserSessionsProcedure:    {adminNeedsNoElevation, "only reduces access, for every credential the user holds"},
	leapmuxv1connect.AdminUserServicePurgeExpiredSessionsProcedure:  {adminNeedsNoElevation, "deletes rows that already expired; it grants nothing"},
	leapmuxv1connect.AdminUserServiceListAPITokensProcedure:         {adminNeedsNoElevation, "metadata only; no secret and no hash leaves the store layer"},
	leapmuxv1connect.AdminUserServiceRevokeAPITokenProcedure:        {adminNeedsNoElevation, "only reduces access"},
	leapmuxv1connect.AdminUserServiceListDelegationTokensProcedure:  {adminNeedsNoElevation, "a read of the delegation bearers"},
	leapmuxv1connect.AdminUserServiceRevokeDelegationTokenProcedure: {adminNeedsNoElevation, "only reduces access"},

	// --- AdminWorkerService ---
	//
	// Nothing here moves a credential or a recovery identity. A worker is a
	// machine the hub trusts, and every verb below either reads that trust or
	// withdraws it.
	leapmuxv1connect.AdminWorkerServiceListWorkersProcedure:                  {adminNeedsNoElevation, "a read across workers"},
	leapmuxv1connect.AdminWorkerServiceGetWorkerProcedure:                    {adminNeedsNoElevation, "a read of one worker"},
	leapmuxv1connect.AdminWorkerServiceDeregisterWorkerProcedure:             {adminNeedsNoElevation, "withdraws the hub's trust in one worker; it only reduces access"},
	leapmuxv1connect.AdminWorkerServiceListRegistrationKeysProcedure:         {adminNeedsNoElevation, "a read of the registration keys"},
	leapmuxv1connect.AdminWorkerServiceRevokeRegistrationKeyProcedure:        {adminNeedsNoElevation, "only reduces access"},
	leapmuxv1connect.AdminWorkerServicePurgeExpiredRegistrationKeysProcedure: {adminNeedsNoElevation, "deletes rows that already expired; it grants nothing"},

	// --- AdminOAuthService ---
	//
	// A provider row installs a sign-in route for the WHOLE hub, and one with
	// trust_email set links an incoming identity to any account whose verified
	// address it presents. writeUnderElevation states the rest.
	leapmuxv1connect.AdminOAuthServiceAddOAuthProviderProcedure:        {adminNeedsElevatedCredential, "installs an identity provider, which is a sign-in route for every account"},
	leapmuxv1connect.AdminOAuthServiceListOAuthProvidersProcedure:      {adminNeedsNoElevation, "a read of the provider inventory; the client secret stays encrypted in the store"},
	leapmuxv1connect.AdminOAuthServiceRemoveOAuthProviderProcedure:     {adminNeedsElevatedCredential, "removes a login method for every user"},
	leapmuxv1connect.AdminOAuthServiceSetOAuthProviderEnabledProcedure: {adminNeedsElevatedCredential, "turns a login method on or off for every user"},
}

// TestEveryAdminProcedureIsElevationClassified is the tripwire: a new admin
// method fails this test until somebody states which side of the gate it sits
// on and why.
//
// It checks the RECORD, not the handler. The other half is behavioral --
// TestAdminOAuthService_WritesNeedAnElevatedSession,
// TestAdminUserService_DurableAuthorityVerbsNeedAnElevatedSession,
// TestAdminUserService_CredentialGatedVerbsNeedAProvenFactor and
// TestAdminSettingsService_WriteGate drive the protected verbs through a real
// un-elevated caller. Add a procedure to one and to the other.
func TestEveryAdminProcedureIsElevationClassified(t *testing.T) {
	t.Parallel()

	paths := protoProcedurePaths(t, leapmuxv1.File_leapmux_v1_admin_proto)
	for _, path := range paths {
		entry, ok := adminProcedureElevation[path]
		assert.Truef(t, ok,
			"admin.proto method %q is not elevation-classified; the gate is a hand-written call in each handler, so an unclassified procedure is one nobody decided about -- add it to adminProcedureElevation with a reason", path)
		assert.NotEmptyf(t, entry.reason, "admin.proto method %q has an empty reason", path)
	}

	known := make(map[string]bool, len(paths))
	for _, path := range paths {
		known[path] = true
	}
	for procedure := range adminProcedureElevation {
		assert.Truef(t, known[procedure],
			"procedure %q is elevation-classified but admin.proto no longer declares it; remove the stale entry", procedure)
	}
}

// TestAdminWriteProceduresAreClassifiedProtected reads the classification
// against the SHAPE of the verb, which is the part a reader can get wrong
// twice in the same direction.
//
// Every one of the five gaps this record exists to catch was a WRITE that
// somebody classified in their head as harmless. So the record states its own
// invariant: a verb called Add, Create, Update, Set or Reset changes state
// that other accounts stand on, and none of those may sit in
// adminNeedsNoElevation. The verbs that legitimately need nothing all read
// (List, Get), only reduce access (Revoke, Deregister), or delete rows that
// already expired (Purge).
func TestAdminWriteProceduresAreClassifiedProtected(t *testing.T) {
	t.Parallel()

	creating := []string{"Add", "Create", "Update", "Set", "Reset"}
	for procedure, entry := range adminProcedureElevation {
		method := procedure[strings.LastIndex(procedure, "/")+1:]
		for _, verb := range creating {
			if !strings.HasPrefix(method, verb) {
				continue
			}
			assert.NotEqualf(t, adminNeedsNoElevation, entry.class,
				"procedure %q creates or rewrites state that other accounts stand on, so it may not be classified adminNeedsNoElevation -- wire it to a gate, or rename it if it only reduces access", procedure)
		}
	}
}
