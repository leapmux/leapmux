package auth

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// declaredProcedurePaths walks every leapmux.v1 service in the proto registry
// and returns the Connect procedure path of every method.
//
// The WHOLE registry, not one file. A service declared in a proto nobody
// thought to walk would otherwise be unclassified with every test green, which
// is exactly the hole TestNoAdminServiceOutsideAdminProto exists to close for
// the admin gate.
func declaredProcedurePaths(t *testing.T) []string {
	t.Helper()
	var out []string
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			if !strings.HasPrefix(string(svc.FullName()), "leapmux.v1.") {
				continue
			}
			for j := range svc.Methods().Len() {
				out = append(out, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	require.NotEmpty(t, out, "the descriptor walk found no leapmux.v1 methods; it proved nothing")
	return out
}

// TestEveryDeclaredProcedureIsScopeClassified is THE tripwire.
//
// A miss in procedureScopes denies every scoped credential, so an unclassified
// procedure is fail-closed rather than an auth bypass -- which is the whole
// reason the map states a REQUIREMENT rather than an allowance. What a miss
// still costs is a working app: the procedure is unreachable and nobody wrote
// down whether it should be. This turns that into a build failure at the moment
// the method is declared.
func TestEveryDeclaredProcedureIsScopeClassified(t *testing.T) {
	for _, path := range declaredProcedurePaths(t) {
		assert.Truef(t, ScopeRequirementFor(path).IsAssigned(),
			"procedure %q has no procedureScopes entry; it is refused to every scoped credential until one exists -- "+
				"assign Requires(<scope>), ScopePublic, ScopeNever or ScopeNotHubServed", path)
	}
}

// TestNoStaleProcedureScopeEntries is the other direction: an entry that
// matches no declared method is a dead record, and a dead record is how a map
// starts describing a different set from the one that exists.
func TestNoStaleProcedureScopeEntries(t *testing.T) {
	declared := make(map[string]bool)
	for _, path := range declaredProcedurePaths(t) {
		declared[path] = true
	}
	for procedure := range procedureScopes {
		assert.Truef(t, declared[procedure],
			"procedureScopes entry %q matches no declared method; remove it", procedure)
	}
}

// scopeRequirementRationale records WHY each non-scope classification is what
// it is.
//
// The three non-scope answers all DENY, so none of them can be wrong in a way a
// request reveals -- which is exactly why each one needs a stated reason. A
// reader must be able to tell "considered and refused" from "nobody looked".
var scopeRequirementRationale = map[string]string{
	// --- ScopeNever: the account's authenticators and credentials ----------
	leapmuxv1connect.AuthServiceLogoutProcedure:                    "a bearer holds no session; an app ends its own credential through /oauth/revoke",
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure:        "the recovery address is a route to a new factor",
	leapmuxv1connect.UserServiceVerifyEmailProcedure:               "completes an email change, which is a factor route",
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure:   "completes an email change, which is a factor route",
	leapmuxv1connect.UserServiceUnlinkOAuthProviderProcedure:       "removes a sign-in method from the account",
	leapmuxv1connect.UserServiceBeginPasskeyRegistrationProcedure:  "ADDS an authenticator that outlives the app's connection",
	leapmuxv1connect.UserServiceFinishPasskeyRegistrationProcedure: "ADDS an authenticator that outlives the app's connection",
	leapmuxv1connect.UserServiceListPasskeysProcedure:              "enumerates the account's authenticators",
	leapmuxv1connect.UserServiceRenamePasskeyProcedure:             "manages the account's authenticators",
	leapmuxv1connect.UserServiceDeletePasskeyProcedure:             "manages the account's authenticators",
	leapmuxv1connect.UserServiceDeactivatePasskeyAuthProcedure:     "manages the account's authenticators",
	leapmuxv1connect.UserServiceElevateSessionProcedure:            "a SESSION ceremony; an app credential elevates through /oauth/step-up",
	leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure:     "a SESSION ceremony; an app credential elevates through /oauth/step-up",
	leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure:    "a SESSION ceremony; an app credential elevates through /oauth/step-up",
	leapmuxv1connect.UserServiceDropElevationProcedure:             "a SESSION ceremony; an app credential elevates through /oauth/step-up",
	leapmuxv1connect.UserServiceRevokeMyAPITokenProcedure:          "ends ANOTHER app's connection; an app revokes itself through /oauth/revoke",
	leapmuxv1connect.UserServiceDisconnectAppProcedure:             "ends every credential the account holds for one app, so an app could retire a rival",

	// --- ScopeNever: the app registry itself ------------------------------

	// --- ScopeNotHubServed: not on the Hub's Connect mux -------------------
	leapmuxv1connect.ControlIPCServiceCallInnerProcedure:                   "the Worker's local unix socket, not a Hub procedure",
	leapmuxv1connect.ControlIPCServiceStreamInnerProcedure:                 "the Worker's local unix socket, not a Hub procedure",
	leapmuxv1connect.ControlIPCServiceUpdateStreamProcedure:                "the Worker's local unix socket, not a Hub procedure",
	leapmuxv1connect.ControlIPCServiceCancelProcedure:                      "the Worker's local unix socket, not a Hub procedure",
	leapmuxv1connect.ControlIPCServiceWhoamiProcedure:                      "the Worker's local unix socket, not a Hub procedure",
	leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure: "dispatched by method name inside a Noise channel, not a Hub procedure",
	leapmuxv1connect.WorkerPrivateServiceRegisterFileTabPathProcedure:      "dispatched by method name inside a Noise channel, not a Hub procedure",
	leapmuxv1connect.WorkerPrivateServiceGetFileTabPathProcedure:           "dispatched by method name inside a Noise channel, not a Hub procedure",
	leapmuxv1connect.WorkerPrivateServiceRevokeFileTabPathProcedure:        "dispatched by method name inside a Noise channel, not a Hub procedure",
}

// TestNonScopeClassificationsAreRationaleClassified is the bidirectional
// tripwire, in the house shape of adminProcedureRationale.
//
// ScopePublic is excluded: TestPublicProceduresAreRecordedAsScopePublic already
// pins that set against publicProcedures itself, which is a stronger check than
// a prose note.
func TestNonScopeClassificationsAreRationaleClassified(t *testing.T) {
	for procedure, requirement := range procedureScopes {
		if _, named := requirement.Scope(); named || requirement.IsPublic() {
			continue
		}
		note, ok := scopeRequirementRationale[procedure]
		assert.Truef(t, ok,
			"procedure %q is classified as a refusal but no reason is recorded; add one to scopeRequirementRationale", procedure)
		assert.NotEmptyf(t, note, "procedure %q has an empty refusal reason", procedure)
	}
	for procedure := range scopeRequirementRationale {
		requirement := ScopeRequirementFor(procedure)
		_, named := requirement.Scope()
		assert.Falsef(t, named,
			"procedure %q now demands a named scope but is still listed as a refusal; remove the stale entry", procedure)
		assert.Truef(t, requirement.IsAssigned(),
			"procedure %q has a refusal reason but no classification at all", procedure)
	}
}

// maximallyScopedCredential holds every grantable scope: the widest grant an
// account can consent to. It is what "refused to every grant" is tested against.
func maximallyScopedCredential() *UserInfo {
	return &UserInfo{
		ID:         userid.MustNew("u-app"),
		IsAdmin:    true,
		Credential: APICredential("a-1"),
		Scopes:     authscope.EveryGrantableScope(),
	}
}

func unscopedCredential() *UserInfo {
	return &UserInfo{
		ID:         userid.MustNew("u-session"),
		IsAdmin:    true,
		Credential: SessionCredential("sess-1"),
		Scopes:     authscope.UnscopedGrant(),
	}
}

// TestUnassignedProcedureIsDeniedByTheMapMiss is the direct analogue of
// TestEnforceAdminDeniesByPrefixNotByMap, and it pins the POLARITY that makes
// the prefix trick unnecessary here.
//
// adminProcedures is an allowlist, so its miss fails OPEN -- which is why
// enforceAdmin had to move to a name prefix. procedureScopes states a
// REQUIREMENT, so its miss is the strictest answer available: refused to every
// scoped credential, admitted only to an unscoped one, which is the account
// itself signed in.
func TestUnassignedProcedureIsDeniedByTheMapMiss(t *testing.T) {
	const unlisted = "/leapmux.v1.WorkspaceService/ProcedureNobodyClassifiedYet"
	require.False(t, ScopeRequirementFor(unlisted).IsAssigned(),
		"the fixture must be absent from the map, or it proves nothing")

	err := enforceScope(unlisted, maximallyScopedCredential())
	require.Error(t, err, "an unclassified procedure must be refused to every scoped credential")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	assert.NoError(t, enforceScope(unlisted, unscopedCredential()),
		"an unscoped credential is the account itself; a scope subtracts from its authority and never adds to it")
}

// TestScopeNeverIsRefusedToEveryGrant pins the recorded refusal.
func TestScopeNeverIsRefusedToEveryGrant(t *testing.T) {
	maximal := maximallyScopedCredential()
	never := 0
	for procedure, requirement := range procedureScopes {
		if !requirement.IsNever() {
			continue
		}
		never++
		assert.Errorf(t, enforceScope(procedure, maximal),
			"%q is ScopeNever, so no grant may reach it", procedure)
		assert.NoErrorf(t, enforceScope(procedure, unscopedCredential()),
			"%q must stay reachable by the account itself; only an APP is refused", procedure)
	}
	assert.Positive(t, never, "no ScopeNever entries at all; the assertion proved nothing")
}

// TestPublicAndNotHubServedAreRefusedToEveryGrant covers the other two
// non-scope answers.
//
// A public procedure is unreachable through this rung -- publicProcedures
// short-circuits first -- so this pins the FAIL-CLOSED answer if that ordering
// ever changes: an app credential has no business calling Login or Register.
func TestPublicAndNotHubServedAreRefusedToEveryGrant(t *testing.T) {
	maximal := maximallyScopedCredential()
	for procedure, requirement := range procedureScopes {
		if !requirement.IsPublic() && requirement.IsHubServed() {
			continue
		}
		assert.Errorf(t, enforceScope(procedure, maximal),
			"%q must be refused to a scoped credential", procedure)
	}
}

// TestEachScopeAdmitsItsProceduresAndRefusesANamedNeighbour is the per-scope
// table.
//
// Each row names ONE neighbour it must refuse, and the neighbours are chosen to
// be the pairs a reader would expect to be conflated: reading terminal output
// versus typing into a shell, reading git state versus pushing, reading the
// hub's administration versus rewriting its settings.
func TestEachScopeAdmitsItsProceduresAndRefusesANamedNeighbour(t *testing.T) {
	cases := []struct {
		scope    leapmuxv1.Scope
		admits   []string
		refuses  string
		refusing string
	}{{
		scope:    leapmuxv1.Scope_SCOPE_TERMINAL_READ,
		admits:   nil,
		refuses:  leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		refusing: "reading terminal output is a monitoring dashboard; it must not reach the layout document",
	}, {
		scope:    leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		admits:   []string{leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, leapmuxv1connect.UserCRDTGetMaterializedProcedure},
		refuses:  leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		refusing: "a read of the layout must not mutate it",
	}, {
		scope:    leapmuxv1.Scope_SCOPE_WORKER_READ,
		admits:   []string{leapmuxv1connect.ChannelServiceOpenChannelProcedure, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure},
		refuses:  leapmuxv1connect.WorkerManagementServiceDeregisterWorkerProcedure,
		refusing: "connecting to a worker must not deregister one",
	}, {
		scope:    leapmuxv1.Scope_SCOPE_ADMIN_READ,
		admits:   []string{leapmuxv1connect.AdminSettingsServiceListSettingsProcedure, leapmuxv1connect.AdminUserServiceListUsersProcedure},
		refuses:  leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure,
		refusing: "reading the hub's administration must not rewrite its security policy",
	}, {
		scope:    leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		admits:   []string{leapmuxv1connect.AdminUserServiceCreateUserProcedure},
		refuses:  leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure,
		refusing: "this is the gap the four admin scopes exist to close: user administration is not settings administration",
	}, {
		scope:    leapmuxv1.Scope_SCOPE_ACCOUNT_READ,
		admits:   []string{leapmuxv1connect.AuthServiceGetCurrentUserProcedure, leapmuxv1connect.UserServiceListMyAPITokensProcedure},
		refuses:  leapmuxv1connect.UserServiceUpdateProfileProcedure,
		refusing: "a read of the profile must not write it",
	}}

	for _, tc := range cases {
		set := authscope.MustNew(tc.scope).Close()
		holder := &UserInfo{
			ID:         userid.MustNew("u-app"),
			IsAdmin:    true,
			Credential: APICredential("a-1"),
			Scopes:     set,
		}
		for _, procedure := range tc.admits {
			assert.NoErrorf(t, enforceScope(procedure, holder), "%s must admit %q", tc.scope, procedure)
		}
		assert.Errorf(t, enforceScope(tc.refuses, holder),
			"%s must refuse %q: %s", tc.scope, tc.refuses, tc.refusing)
	}
}

// TestTerminalReadDoesNotImplyTerminalWrite states the most important boundary
// in the vocabulary directly, rather than as a consequence of the table.
//
// Reading terminal output is a monitoring dashboard. Typing into a shell is
// arbitrary code execution on the account's machine. The closure adds the READ
// half of each write pair and never the reverse, and this is the assertion that
// says so.
func TestTerminalReadDoesNotImplyTerminalWrite(t *testing.T) {
	read := authscope.MustNew(leapmuxv1.Scope_SCOPE_TERMINAL_READ).Close()
	assert.True(t, read.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_READ))
	assert.False(t, read.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE),
		"terminal:read must never imply terminal:write")

	write := authscope.MustNew(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE).Close()
	assert.True(t, write.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_READ),
		"terminal:write implies terminal:read: a client that types must see what it typed into")
}

// TestTunnelOpenStandsAlone pins that a network pivot cannot be reached through
// any other scope.
//
// tunnel:open is not "worker access": it is arbitrary TCP egress from inside
// the account's private network. Folding it into file:read would hide that
// behind "read my files", so no other scope may imply it.
func TestTunnelOpenStandsAlone(t *testing.T) {
	for _, scope := range authscope.Grantable() {
		if scope == leapmuxv1.Scope_SCOPE_TUNNEL_OPEN {
			continue
		}
		assert.Falsef(t, authscope.MustNew(scope).Close().Allows(leapmuxv1.Scope_SCOPE_TUNNEL_OPEN),
			"%s must not imply tunnel:open; arbitrary network egress needs its own consent", scope)
	}
}

// TestEnforceScopeRefusesAnUnauthenticatedCaller pins the nil arm. Both callers
// sit behind a user-resolving branch, so it is unreachable -- which is why it
// needs a test: an unreachable arm that fails OPEN is invisible until something
// makes it reachable.
func TestEnforceScopeRefusesAnUnauthenticatedCaller(t *testing.T) {
	err := enforceScope(leapmuxv1connect.AuthServiceGetCurrentUserProcedure, nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestZeroScopedCredentialReachesNothing pins the fail-closed zero value at the
// rung rather than in the set type: a UserInfo whose Scopes nobody filled must
// reach no procedure at all.
func TestZeroScopedCredentialReachesNothing(t *testing.T) {
	empty := &UserInfo{
		ID:         userid.MustNew("u-app"),
		IsAdmin:    true,
		Credential: APICredential("a-1"),
	}
	for procedure := range procedureScopes {
		assert.Errorf(t, enforceScope(procedure, empty),
			"a credential with no grant must not reach %q", procedure)
	}
}

// TestRequiresPanicsOnANonGrantableScope pins that a bad table entry is a BOOT
// failure rather than a runtime denial nobody notices.
func TestRequiresPanicsOnANonGrantableScope(t *testing.T) {
	assert.Panics(t, func() { Requires(leapmuxv1.Scope_SCOPE_NEVER) })
	assert.Panics(t, func() { Requires(leapmuxv1.Scope_SCOPE_ALL) })
	assert.Panics(t, func() { Requires(leapmuxv1.Scope_SCOPE_UNSPECIFIED) })
}

// TestScopeDenialNamesNoInternalState pins the message.
//
// A refusal must not disclose WHICH of the four non-scope answers it was: an
// app learns that it may not call the procedure, not whether the hub serves it
// at all. So the unassigned, public, never and not-hub-served arms share one
// sentence, and only the named-scope arm says more -- because there, naming the
// missing permission is the remedy.
func TestScopeDenialNamesNoInternalState(t *testing.T) {
	scoped := maximallyScopedCredential()
	messages := map[string]bool{}
	for procedure, requirement := range procedureScopes {
		if _, named := requirement.Scope(); named {
			continue
		}
		err := enforceScope(procedure, scoped)
		require.Errorf(t, err, "%q must be refused", procedure)
		messages[strings.ReplaceAll(err.Error(), procedure, "<procedure>")] = true
	}
	assert.Len(t, messages, 1,
		"every non-scope refusal must share one sentence, or the message discloses which kind it was")
}

// TestAppServiceProceduresRequireAdminApps pins the decision that made the
// documented `control admin app` verbs reachable: the whole AppService sits
// behind admin:apps. Before it, every verb was ScopeNever and a CLI credential
// -- necessarily scoped -- was refused at this rung before requireElevatedOwner
// could run, contradicting the flow the docs describe.
func TestAppServiceProceduresRequireAdminApps(t *testing.T) {
	for _, procedure := range []string{
		leapmuxv1connect.AppServiceRegisterAppProcedure,
		leapmuxv1connect.AppServiceListAppsProcedure,
		leapmuxv1connect.AppServiceUpdateAppProcedure,
		leapmuxv1connect.AppServiceSetAppElevationAllowedProcedure,
		leapmuxv1connect.AppServiceVerifyAppProcedure,
		leapmuxv1connect.AppServiceRevokeAppProcedure,
		leapmuxv1connect.AppServiceDeleteAppProcedure,
	} {
		assert.Equalf(t, leapmuxv1.Scope_SCOPE_ADMIN_APPS,
			ScopeRequirementFor(procedure).scope,
			"%s must sit behind admin:apps", procedure)
	}

	// And a credential holding admin:apps passes the rung for exactly these.
	holder := &UserInfo{IsAdmin: true, Scopes: mustScopeSet("admin:read admin:apps")}
	assert.NoError(t, enforceScope(leapmuxv1connect.AppServiceListAppsProcedure, holder))
	without := &UserInfo{IsAdmin: true, Scopes: mustScopeSet("admin:read")}
	assert.Error(t, enforceScope(leapmuxv1connect.AppServiceListAppsProcedure, without),
		"admin:read alone must not reach the app catalogue")
}

func mustScopeSet(tokens string) authscope.ScopeSet {
	set, err := authscope.Parse(tokens)
	if err != nil {
		panic(err)
	}
	return set
}
