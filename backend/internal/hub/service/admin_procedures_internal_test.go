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
// AdminIdPService's three write verbs, AdminUserService.DeleteUser and
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
	// adminNeedsElevatedCredential calls requireElevatedActor, directly or
	// through writeUnderElevation: any credential that can carry an
	// elevation, a command-line credential included.
	adminNeedsElevatedCredential adminElevationClass = iota
	// adminNeedsElevationFromInterceptor means the interceptor's window
	// check is the whole elevation requirement. The handler adds no gate of
	// its own: what the procedure does (a read, a revocation, a purge of
	// expired rows) needs nothing beyond the step-up the interceptor already
	// demands for every Admin* procedure.
	adminNeedsElevationFromInterceptor
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
	// Every WRITE takes writeUnderElevation and every READ takes nothing
	// beyond the interceptor. A hub setting is deployment-wide, and several
	// of these keys are the hub's own security controls.
	leapmuxv1connect.AdminSettingsServiceListSettingsProcedure:   {adminNeedsElevationFromInterceptor, "a read; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminSettingsServiceUpdateSettingsProcedure: {adminNeedsElevatedCredential, "rewrites instance configuration, both halves including the encrypted secrets, in one transaction; the CLI is the documented headless path"},
	leapmuxv1connect.AdminSettingsServiceResetSettingsProcedure:  {adminNeedsElevatedCredential, "returns instance configuration to defaults in one transaction, which can turn a control off"},

	// --- AdminNetworkService ---
	//
	// A read of derived state. The WRITE that changes which addresses the hub
	// answers on is AdminSettingsService.UpdateSettings, which is elevated
	// above; this only reports what that write produced, so demanding a fresh
	// factor to LOOK would ask for a proof and grant nothing.
	leapmuxv1connect.AdminNetworkServiceGetListenStatusProcedure: {adminNeedsElevationFromInterceptor, "a read; the interceptor's check is the whole requirement"},

	// --- AdminUserService ---
	//
	// Every write verb requires an elevated administrator credential (browser
	// session or CLI credential with admin:users scope that completed browser
	// step-up elevation) and commits under commitUnderElevation.
	leapmuxv1connect.AdminUserServiceCreateUserProcedure:    {adminNeedsElevatedCredential, "mints an account, optionally an administrator, with a password the caller chooses"},
	leapmuxv1connect.AdminUserServiceSetUserAdminProcedure:  {adminNeedsElevatedCredential, "grants or revokes administration itself"},
	leapmuxv1connect.AdminUserServiceResetPasswordProcedure: {adminNeedsElevatedCredential, "sets any account's password without the old one"},
	leapmuxv1connect.AdminUserServiceUpdateUserProcedure:    {adminNeedsElevatedCredential, "rewrites profile, email, and verification fields under elevation"},

	// The credential rule: an elevated command-line credential is admitted,
	// because each of these has a documented headless verb and none of them
	// creates a new way INTO an account.
	leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure: {adminNeedsElevatedCredential, "mints a credential that outlives the session; the mint's own clamp contains the bearer path"},
	leapmuxv1connect.AdminUserServiceDeleteUserProcedure:    {adminNeedsElevatedCredential, "irreversible destruction of an account and everything it owns"},

	// Reads, and revokes that only reduce access. The interceptor's check is
	// the whole requirement for each.
	leapmuxv1connect.AdminUserServiceListUsersProcedure:             {adminNeedsElevationFromInterceptor, "a read across accounts; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceGetUserProcedure:               {adminNeedsElevationFromInterceptor, "a read of one account; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceListUserSessionsProcedure:      {adminNeedsElevationFromInterceptor, "a read of one user's sessions; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceListSessionsProcedure:          {adminNeedsElevationFromInterceptor, "a read of every live session; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceRevokeSessionProcedure:         {adminNeedsElevationFromInterceptor, "only reduces access; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceRevokeUserSessionsProcedure:    {adminNeedsElevationFromInterceptor, "only reduces access, for every credential the user holds; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServicePurgeExpiredSessionsProcedure:  {adminNeedsElevationFromInterceptor, "deletes rows that already expired; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceListAPITokensProcedure:         {adminNeedsElevationFromInterceptor, "metadata only; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceRevokeAPITokenProcedure:        {adminNeedsElevationFromInterceptor, "only reduces access; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceListDelegationTokensProcedure:  {adminNeedsElevationFromInterceptor, "a read of the delegation bearers; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminUserServiceRevokeDelegationTokenProcedure: {adminNeedsElevationFromInterceptor, "only reduces access; the interceptor's check is the whole requirement"},

	// --- AdminWorkerService ---
	//
	// Nothing here moves a credential or a recovery identity. A worker is a
	// machine the hub trusts, and every verb below either reads that trust or
	// withdraws it. The interceptor's check is the whole requirement for each.
	leapmuxv1connect.AdminWorkerServiceListWorkersProcedure:                  {adminNeedsElevationFromInterceptor, "a read across workers; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminWorkerServiceGetWorkerProcedure:                    {adminNeedsElevationFromInterceptor, "a read of one worker; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminWorkerServiceDeregisterWorkerProcedure:             {adminNeedsElevationFromInterceptor, "withdraws the hub's trust in one worker; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminWorkerServiceListRegistrationKeysProcedure:         {adminNeedsElevationFromInterceptor, "a read of the registration keys; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminWorkerServiceRevokeRegistrationKeyProcedure:        {adminNeedsElevationFromInterceptor, "only reduces access; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminWorkerServicePurgeExpiredRegistrationKeysProcedure: {adminNeedsElevationFromInterceptor, "deletes rows that already expired; the interceptor's check is the whole requirement"},

	// --- AdminIdPService ---
	//
	// A provider row installs a sign-in route for the WHOLE hub, and one with
	// trust_email set links an incoming identity to any account whose verified
	// address it presents. writeUnderElevation states the rest.
	leapmuxv1connect.AdminIdPServiceAddOAuthProviderProcedure:          {adminNeedsElevatedCredential, "installs an identity provider, which is a sign-in route for every account"},
	leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure:        {adminNeedsElevationFromInterceptor, "a read of the provider inventory; the interceptor's check is the whole requirement"},
	leapmuxv1connect.AdminIdPServiceRemoveOAuthProviderProcedure:       {adminNeedsElevatedCredential, "removes a login method for every user"},
	leapmuxv1connect.AdminIdPServiceSetOAuthProviderEnabledProcedure:   {adminNeedsElevatedCredential, "turns a login method on or off for every user"},
	leapmuxv1connect.AdminIdPServiceUpdateOAuthProviderSecretProcedure: {adminNeedsElevatedCredential, "rotates a sign-in provider's client secret in place"},
}

// TestEveryAdminProcedureIsElevationClassified is the tripwire: a new admin
// method fails this test until somebody states which side of the gate it sits
// on and why.
//
// It checks the RECORD, not the handler. The other half is behavioral --
// TestAdminIdPService_WritesNeedAProvenFactor,
// TestAdminUserService_DurableAuthorityVerbsNeedAnElevatedSession,
// TestAdminUserService_CredentialGuardedVerbsNeedAProvenFactor and
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
// adminNeedsElevationFromInterceptor. The verbs that legitimately need
// nothing beyond the interceptor all read (List, Get), only reduce access
// (Revoke, Deregister), or delete rows that already expired (Purge).
func TestAdminWriteProceduresAreClassifiedProtected(t *testing.T) {
	t.Parallel()

	creating := []string{"Add", "Create", "Update", "Set", "Reset"}
	for procedure, entry := range adminProcedureElevation {
		method := procedure[strings.LastIndex(procedure, "/")+1:]
		for _, verb := range creating {
			if !strings.HasPrefix(method, verb) {
				continue
			}
			assert.NotEqualf(t, adminNeedsElevationFromInterceptor, entry.class,
				"procedure %q creates or rewrites state that other accounts stand on, so it may not be classified adminNeedsElevationFromInterceptor -- wire it to a gate, or rename it if it only reduces access", procedure)
		}
	}
}
