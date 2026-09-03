package auth

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
)

// ScopeRequirement is what one Connect procedure demands of a SCOPED
// credential.
//
// The polarity is the whole design. adminProcedures is an ALLOWLIST, so a miss
// there fails OPEN -- which is why enforceAdmin had to move to a name prefix.
// A REQUIREMENT map's miss is the strictest possible answer instead: the zero
// value is "nobody classified this", and enforceScope refuses it to every
// scoped credential. So a procedure added tomorrow is unreachable by an app
// until somebody assigns it, and no prefix trick is needed here.
//
// It is a struct rather than a bare leapmuxv1.Scope because three of the four
// answers are not scopes at all -- unassigned, public and never -- and folding
// them into the enum would put values in scope.proto that no account can grant.
type ScopeRequirement struct {
	kind  requirementKind
	scope leapmuxv1.Scope
}

type requirementKind uint8

const (
	// reqUnassigned is the ZERO VALUE, which is what a map miss yields. It
	// denies every scoped credential, so a new procedure is fail-closed from
	// the moment it is declared.
	reqUnassigned requirementKind = iota
	// reqScope demands one named scope.
	reqScope
	// reqPublic records a procedure the interceptor waives entirely: the
	// handler authenticates itself with a credential of its own (a worker
	// registration key, a worker auth_token), or the procedure bootstraps a
	// session. The scope rung is UNREACHABLE for these, because
	// publicProcedures short-circuits first -- so this value is a record. It
	// still denies, which is the fail-closed answer if that ordering ever
	// changes: an app credential has no business calling Login or Register.
	reqPublic
	// reqNever records a deliberate refusal. See ScopeNever.
	reqNever
	// reqNotHubServed records a procedure the Hub's Connect mux never mounts.
	// See ScopeNotHubServed. It denies exactly as reqNever does; the separate
	// value keeps the two RECORDS apart, so a test can assert what the hub
	// refuses on purpose without also matching what the hub never serves.
	reqNotHubServed
)

// Requires states that a procedure demands one named scope. It panics on a
// non-grantable value, so a bad entry in the table below is a boot failure
// rather than a runtime denial nobody notices.
func Requires(scope leapmuxv1.Scope) ScopeRequirement {
	if !authscope.IsGrantable(scope) {
		panic(fmt.Sprintf("procedure scope %s is not grantable", scope))
	}
	return ScopeRequirement{kind: reqScope, scope: scope}
}

// ScopePublic records a procedure on publicProcedures. See reqPublic.
var ScopePublic = ScopeRequirement{kind: reqPublic}

// ScopeNever records a procedure NO grant reaches, whatever an account ticks
// on a consent screen.
//
// It marks the account's AUTHENTICATOR and CREDENTIAL surface, and the line is
// this: a scope may reach an action that changes a factor the account already
// has, because that action separately demands proof of the existing factor. A
// scope may NEVER reach an action that ADDS a factor, that manages ANOTHER
// app's credential, or that MINTS a credential -- each of those creates
// authority which outlives the app's connection, so disconnecting the app
// would no longer withdraw what it was given.
//
// It is a recorded value rather than an absence because an absence cannot
// state a reason. A reader of the table learns the procedure was considered
// and refused, not that somebody forgot it.
var ScopeNever = ScopeRequirement{kind: reqNever}

// ScopeNotHubServed records a procedure the Hub's Connect mux does not mount,
// so no request ever reaches the scope rung through it.
//
// Two service families are in this state and both are legitimate:
// ControlIPCService is the Worker's own local unix-socket surface, and
// WorkerPrivateService is dispatched by method NAME inside a Noise channel
// rather than as a Connect procedure. Recording them keeps the descriptor walk
// TOTAL -- a declared procedure with no entry fails the walk -- and
// server_scope_coverage_test.go cross-checks the claim against the real mount
// list, so mounting one of these on the hub turns this record into a test
// failure instead of an unclassified procedure.
var ScopeNotHubServed = ScopeRequirement{kind: reqNotHubServed}

// Scope reports the named scope this requirement demands, if it demands one.
func (r ScopeRequirement) Scope() (leapmuxv1.Scope, bool) {
	return r.scope, r.kind == reqScope
}

// IsAssigned reports whether somebody classified this procedure.
func (r ScopeRequirement) IsAssigned() bool { return r.kind != reqUnassigned }

// IsPublic reports whether this procedure is recorded on publicProcedures.
func (r ScopeRequirement) IsPublic() bool { return r.kind == reqPublic }

// IsNever reports the deliberate refusal. It is false for ScopeNotHubServed,
// which denies for a different reason and is checked against the mount list.
func (r ScopeRequirement) IsNever() bool { return r.kind == reqNever }

// IsHubServed reports whether the Hub's Connect mux is expected to mount this
// procedure. server_scope_coverage_test.go checks the claim against the real
// mount list.
func (r ScopeRequirement) IsHubServed() bool { return r.kind != reqNotHubServed }

// procedureScopes is the requirement of every declared leapmux.v1 procedure.
//
// Every key is a generated leapmuxv1connect constant, so a rename in the proto
// turns a stale entry into a build error rather than a silent hole.
//
// procedure_scopes_test.go walks the proto descriptors and fails on any
// declared method missing from here, and on any entry that matches no declared
// method.
var procedureScopes = map[string]ScopeRequirement{
	// --- AuthService --------------------------------------------------------
	//
	// Almost all of it bootstraps a session, so it is public. The two that need
	// a caller are classified on what they do WITH one.
	leapmuxv1connect.AuthServiceLoginProcedure:                           ScopePublic,
	leapmuxv1connect.AuthServiceSignUpProcedure:                          ScopePublic,
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:                   ScopePublic,
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:               ScopePublic,
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure:           ScopePublic,
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:             ScopePublic,
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:              ScopePublic,
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:               ScopePublic,
	leapmuxv1connect.AuthServiceFinishPasskeyLoginProcedure:              ScopePublic,
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:              ScopePublic,
	leapmuxv1connect.AuthServiceFinishPasskeySignUpProcedure:             ScopePublic,
	leapmuxv1connect.AuthServiceRequestAccountRecoveryProcedure:          ScopePublic,
	leapmuxv1connect.AuthServiceCompleteAccountRecoveryPasswordProcedure: ScopePublic,
	leapmuxv1connect.AuthServiceBeginAccountRecoveryPasskeyProcedure:     ScopePublic,
	leapmuxv1connect.AuthServiceFinishAccountRecoveryPasskeyProcedure:    ScopePublic,
	leapmuxv1connect.AuthServiceGetCurrentUserProcedure:                  Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_READ),
	// A bearer holds no session, so there is nothing here for an app to end.
	// It ends its own credential through RFC 7009 /oauth/revoke instead.
	leapmuxv1connect.AuthServiceLogoutProcedure: ScopeNever,

	// --- UserService: the account's own profile -----------------------------
	leapmuxv1connect.UserServiceUpdateProfileProcedure:     Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE),
	leapmuxv1connect.UserServiceGetTimeoutsProcedure:       Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_READ),
	leapmuxv1connect.UserServiceListUserSettingsProcedure:  Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_READ),
	leapmuxv1connect.UserServiceUpdateUserSettingProcedure: Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE),
	leapmuxv1connect.UserServiceResetUserSettingProcedure:  Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE),
	// Changes a factor the account ALREADY has, and separately demands proof
	// of it: the handler refuses an un-elevated credential, and an app's
	// elevation needs oauth_clients.elevation_allowed, which is off for every
	// app somebody registers. That is why it takes a scope rather than
	// ScopeNever, and it is what keeps the built-in control CLI able to do
	// today exactly what it does today.
	leapmuxv1connect.UserServiceChangePasswordProcedure: Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE),
	// A READ of this account's own connection list, disclosing no secret. It
	// is what `leapmux control auth credentials` prints, and reading which
	// apps you connected is something the account owner can already do.
	leapmuxv1connect.UserServiceListMyAPITokensProcedure: Requires(leapmuxv1.Scope_SCOPE_ACCOUNT_READ),

	// --- UserService: authenticators and credentials, refused to every app ---
	//
	// Each of these ADDS a factor, ENUMERATES the account's factors, or ends
	// ANOTHER app's connection. See ScopeNever.
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure:        ScopeNever,
	leapmuxv1connect.UserServiceVerifyEmailProcedure:               ScopeNever,
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure:   ScopeNever,
	leapmuxv1connect.UserServiceUnlinkOAuthProviderProcedure:       ScopeNever,
	leapmuxv1connect.UserServiceBeginPasskeyRegistrationProcedure:  ScopeNever,
	leapmuxv1connect.UserServiceFinishPasskeyRegistrationProcedure: ScopeNever,
	leapmuxv1connect.UserServiceListPasskeysProcedure:              ScopeNever,
	leapmuxv1connect.UserServiceRenamePasskeyProcedure:             ScopeNever,
	leapmuxv1connect.UserServiceDeletePasskeyProcedure:             ScopeNever,
	leapmuxv1connect.UserServiceDeactivatePasskeyAuthProcedure:     ScopeNever,
	// The SESSION step-up ceremony. An app credential holds no session and
	// elevates through /oauth/step-up, which is the browser stage the
	// elevation_allowed flag governs.
	leapmuxv1connect.UserServiceElevateSessionProcedure:         ScopeNever,
	leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure:  ScopeNever,
	leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure: ScopeNever,
	leapmuxv1connect.UserServiceDropElevationProcedure:          ScopeNever,
	// Ends a credential the caller may not hold. An app disconnects ITSELF
	// through /oauth/revoke, which needs no scope because it presents the
	// token it revokes.
	leapmuxv1connect.UserServiceRevokeMyAPITokenProcedure: ScopeNever,
	// The same refusal one level up: DisconnectApp ends EVERY credential the
	// account holds for one app, so an app that reached it could retire a
	// rival on its owner's behalf.
	leapmuxv1connect.UserServiceDisconnectAppProcedure: ScopeNever,

	// --- AppService ---------------------------------------------------------
	//
	// The whole service sits behind admin:apps: an app that could register an
	// app would choose its own redirect address and its own scope ceiling, and
	// the next consent screen would be the attacker's. Keeping it OUT of the
	// app-reachable vocabulary was what ScopeNever marked here before; the
	// separate admin family gives the same protection to ordinary grants
	// (an app's ceiling may list admin:apps only if an administrator
	// registered it that way) while letting a command-line credential reach
	// the documented `control admin app` verbs.
	//
	// Reading is the same scope rather than a weaker one: the listing carries
	// every registration's redirect addresses and ceilings, which is the
	// reconnaissance an app would want before it forged one.
	leapmuxv1connect.AppServiceRegisterAppProcedure:            Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceListAppsProcedure:               Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceUpdateAppProcedure:              Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceSetAppElevationAllowedProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceVerifyAppProcedure:              Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceRevokeAppProcedure:              Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),
	leapmuxv1connect.AppServiceDeleteAppProcedure:              Requires(leapmuxv1.Scope_SCOPE_ADMIN_APPS),

	// --- WorkspaceService ---------------------------------------------------
	leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:  Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:    Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceListTabsProcedure:        Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceGetTabProcedure:          Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceLocateTabProcedure:       Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceLocateTileProcedure:      Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),

	// --- SectionService: the sidebar's own layout ---------------------------
	leapmuxv1connect.SectionServiceListSectionsProcedure:  Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.SectionServiceCreateSectionProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.SectionServiceRenameSectionProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.SectionServiceDeleteSectionProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.SectionServiceMoveSectionProcedure:   Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	leapmuxv1connect.SectionServiceMoveWorkspaceProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),

	// --- UserCRDT: the layout document --------------------------------------
	//
	// SubmitOps is one procedure that mutates the WHOLE user document, so
	// workspace:write is the honest unit -- every client-submittable op body
	// is layout, and the workspace-level ops are hub-only. What a scope can
	// still narrow inside it is which WORKER a tab may be bound to; see
	// crdt.SubmitInput.
	leapmuxv1connect.UserCRDTGetMaterializedProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
	leapmuxv1connect.UserCRDTSubmitOpsProcedure:       Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),
	// Announcing presence writes this account's live cursor into the document
	// every other client reads. A read-only app watches; it does not appear.
	leapmuxv1connect.UserCRDTUpdatePresenceProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE),

	// --- ChannelService: reaching a worker ----------------------------------
	//
	// Opening the channel is worker:read for all three. What the app may then
	// CALL inside the Noise tunnel is enforced by the worker, from the grant
	// the hub puts in the handshake; see channel.Caller.
	leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKER_READ),
	leapmuxv1connect.ChannelServiceOpenChannelProcedure:              Requires(leapmuxv1.Scope_SCOPE_WORKER_READ),
	leapmuxv1connect.ChannelServiceCloseChannelProcedure:             Requires(leapmuxv1.Scope_SCOPE_WORKER_READ),

	// --- WorkerManagementService: this account's own workers ----------------
	leapmuxv1connect.WorkerManagementServiceListWorkersProcedure:                   Requires(leapmuxv1.Scope_SCOPE_WORKER_READ),
	leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:                     Requires(leapmuxv1.Scope_SCOPE_WORKER_READ),
	leapmuxv1connect.WorkerManagementServiceCreateRegistrationKeyProcedure:         Requires(leapmuxv1.Scope_SCOPE_WORKER_ADMIN),
	leapmuxv1connect.WorkerManagementServiceExtendRegistrationKeyProcedure:         Requires(leapmuxv1.Scope_SCOPE_WORKER_ADMIN),
	leapmuxv1connect.WorkerManagementServiceDeleteRegistrationKeyProcedure:         Requires(leapmuxv1.Scope_SCOPE_WORKER_ADMIN),
	leapmuxv1connect.WorkerManagementServiceDeregisterWorkerProcedure:              Requires(leapmuxv1.Scope_SCOPE_WORKER_ADMIN),
	leapmuxv1connect.WorkerManagementServiceEmailRegistrationInstructionsProcedure: Requires(leapmuxv1.Scope_SCOPE_WORKER_ADMIN),

	// --- Worker-facing procedures: the handler carries its own credential ---
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure:                ScopePublic,
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:                 ScopePublic,
	leapmuxv1connect.WorkerReconcilerServiceListOwnedTabsForWorkerProcedure: ScopePublic,

	// --- AdminSettingsService -----------------------------------------------
	leapmuxv1connect.AdminSettingsServiceListSettingsProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure:       Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminSettingsServiceUpdateSettingSecretProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminSettingsServiceUpdateSettingsProcedure:      Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminSettingsServiceResetSettingProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminSettingsServiceResetSettingsProcedure:       Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),

	// --- AdminNetworkService ------------------------------------------------
	//
	// A READ of derived state: the machine's interfaces, and what the hub's
	// sockets hold. admin:read, like every other admin listing. Writing the
	// address list is AdminSettingsService.UpdateSetting, which carries
	// admin:settings, so the permission an operator needs to CHANGE what the
	// hub answers on is the one that governs every other hub setting.
	leapmuxv1connect.AdminNetworkServiceGetListenStatusProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),

	// --- AdminIdPService: the hub's sign-in providers -----------------------
	//
	// A provider row is a trust anchor for every account, so writing one is
	// the hub's security policy: admin:settings, not admin:users.
	leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure:      Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminIdPServiceAddOAuthProviderProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminIdPServiceRemoveOAuthProviderProcedure:     Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),
	leapmuxv1connect.AdminIdPServiceSetOAuthProviderEnabledProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS),

	// --- AdminUserService ---------------------------------------------------
	leapmuxv1connect.AdminUserServiceListUsersProcedure:            Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminUserServiceGetUserProcedure:              Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminUserServiceListUserSessionsProcedure:     Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminUserServiceListSessionsProcedure:         Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminUserServiceListAPITokensProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminUserServiceListDelegationTokensProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),

	leapmuxv1connect.AdminUserServiceCreateUserProcedure:            Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceUpdateUserProcedure:            Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceDeleteUserProcedure:            Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceSetUserAdminProcedure:          Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceResetPasswordProcedure:         Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceRevokeSessionProcedure:         Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceRevokeUserSessionsProcedure:    Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServicePurgeExpiredSessionsProcedure:  Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceRevokeAPITokenProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	leapmuxv1connect.AdminUserServiceRevokeDelegationTokenProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),
	// MINTS a bearer for any account on the hub, including the caller's own,
	// which is why it needs a second bound the scope alone cannot give: the
	// issued grant is NARROWED to the issuer's own (see resolveIssuedScopes),
	// so a credential can never mint one wider than itself. Without that, an
	// app granted admin:users alone could issue itself tunnel:open.
	leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_USERS),

	// --- AdminWorkerService: workers across every account -------------------
	leapmuxv1connect.AdminWorkerServiceListWorkersProcedure:                  Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminWorkerServiceGetWorkerProcedure:                    Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminWorkerServiceListRegistrationKeysProcedure:         Requires(leapmuxv1.Scope_SCOPE_ADMIN_READ),
	leapmuxv1connect.AdminWorkerServiceDeregisterWorkerProcedure:             Requires(leapmuxv1.Scope_SCOPE_ADMIN_WORKERS),
	leapmuxv1connect.AdminWorkerServiceRevokeRegistrationKeyProcedure:        Requires(leapmuxv1.Scope_SCOPE_ADMIN_WORKERS),
	leapmuxv1connect.AdminWorkerServicePurgeExpiredRegistrationKeysProcedure: Requires(leapmuxv1.Scope_SCOPE_ADMIN_WORKERS),

	// --- Not served by the Hub's Connect mux --------------------------------
	//
	// ControlIPCService is the Worker's local unix socket. WorkerPrivateService
	// is dispatched by method name inside a Noise channel. See ScopeNotHubServed.
	leapmuxv1connect.ControlIPCServiceCallInnerProcedure:                   ScopeNotHubServed,
	leapmuxv1connect.ControlIPCServiceStreamInnerProcedure:                 ScopeNotHubServed,
	leapmuxv1connect.ControlIPCServiceUpdateStreamProcedure:                ScopeNotHubServed,
	leapmuxv1connect.ControlIPCServiceCancelProcedure:                      ScopeNotHubServed,
	leapmuxv1connect.ControlIPCServiceWhoamiProcedure:                      ScopeNotHubServed,
	leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure: ScopeNotHubServed,
	leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:       ScopeNotHubServed,
	leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure:            ScopeNotHubServed,
	leapmuxv1connect.WorkerPrivateServiceRevokeTabPayloadProcedure:         ScopeNotHubServed,
}

// ScopeRequirementFor returns what a procedure demands of a scoped credential.
// A procedure nobody classified answers the zero value, which denies.
func ScopeRequirementFor(procedure string) ScopeRequirement {
	return procedureScopes[procedure]
}

// enforceScope refuses a SCOPED credential a procedure its grant does not
// reach.
//
// It runs FIRST among the authorization rungs, and the order is deliberate.
// It is the cheapest, it applies to every credential kind, and running it
// before enforceAdmin means an app with no admin:read is refused for its own
// grant rather than told whether its user is an administrator.
//
// An UNSCOPED credential passes unconditionally. That is the composition rule
// as code: a scope SUBTRACTS from the user's own authority and never adds to
// it, so this rung can only ever deny. Every other rung -- ownership, the
// admin gate, the email gate, elevation -- still applies afterwards.
func enforceScope(procedure string, userInfo *UserInfo) error {
	if userInfo == nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}
	if userInfo.Scopes.IsUnscoped() {
		return nil
	}
	requirement := ScopeRequirementFor(procedure)
	scope, explicit := requirement.Scope()
	if !explicit {
		// reqUnassigned, reqPublic, reqNever and reqNotHubServed all land
		// here, and every one of them is a refusal for a scoped credential.
		// The message stays uniform, so a denial never discloses WHICH of the
		// four it was -- an app learns that it may not call the procedure, not
		// whether the hub serves it at all.
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("this app's authorization does not cover %s", procedure))
	}
	if !userInfo.Scopes.Allows(scope) {
		return connect.NewError(connect.CodePermissionDenied,
			errors.New(authscope.NotGrantedDenial(scope)))
	}
	return nil
}

// CeilingFor is the widest grant a credential of this kind may EVER carry.
//
// It is applied in loadBearer, at the moment the row is read, rather than only
// at the mint. So a mint bug, a hand-edited row or a restored backup cannot
// produce an over-scoped credential that authenticates: the ceiling is a
// property of the credential kind at every validation.
//
// This is the same argument the AbsoluteTokenLifetime ceiling already makes in
// apiTokenExpired -- read the limit at validation, so no issuer, present or
// future, can write past it.
func CeilingFor(kind BearerKind) authscope.ScopeSet {
	switch kind {
	case BearerKindAPI:
		// An app's ceiling is the whole grantable vocabulary: what it actually
		// holds is what its owner consented to, and the consent screen is the
		// place that narrows it.
		return authscope.EveryGrantableScope()
	case BearerKindDelegation:
		return delegationCeiling
	}
	// An unknown kind reaches nothing. ParseBearer rejects one before this is
	// called, so the branch is defence in depth.
	return authscope.ScopeSet{}
}

// delegationCeiling is what a worker-minted delegation bearer may reach,
// however wide the credential that spawned it was.
//
// It carries the guarantee the deleted delegationAllowedProcedures allowlist
// used to carry: a bearer minted for a process that reads UNTRUSTED INPUT --
// a coding agent acting on a prompt -- can never administer the hub, never
// touch the account's own profile or credentials, and never administer the
// account's workers. What it keeps from that allowlist is the hub surface:
// reading and writing the layout document, reading workspaces and tabs, and
// opening a channel to a worker.
//
// The terminal, file, git, agent and tunnel families widen it for the OTHER
// door this credential opens: the cross-worker channel. A sibling worker's
// dispatcher authenticates that channel with this bearer and restricts every
// inner method to the scope it declares, so a ceiling without them broke
// every cross-worker inner call -- a spawned agent could no longer close a
// tab, read a file or push a branch on another worker, exactly the surface
// the delegation path exists to serve. No hub Connect procedure declares any
// of those scopes (procedureScopes above holds the hub surface alone), so
// they widen no hub reach. The families that DO widen it -- worker:read and
// workspace:write, which reach the worker-management and workspace verbs --
// are recorded in delegation_procedures_test.go's newlyDelegationReachable
// table: the test pins the delegation surface as the old allowlist PLUS that
// recorded widening, and nothing else.
var delegationCeiling = authscope.MustNew(
	leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
	leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE,
	leapmuxv1.Scope_SCOPE_WORKER_READ,
	leapmuxv1.Scope_SCOPE_AGENT_READ,
	leapmuxv1.Scope_SCOPE_AGENT_WRITE,
	leapmuxv1.Scope_SCOPE_TERMINAL_READ,
	leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
	leapmuxv1.Scope_SCOPE_FILE_READ,
	leapmuxv1.Scope_SCOPE_GIT_READ,
	leapmuxv1.Scope_SCOPE_GIT_WRITE,
	leapmuxv1.Scope_SCOPE_TUNNEL_OPEN,
)
