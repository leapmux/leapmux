package auth

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// adminProcedureRationale records WHY each admin-only procedure is
// admin-only, in the house shape of publicProcedureRationale and
// delegationProcedureScope. Grouped by the blast radius the gate protects.
var adminProcedureRationale = map[string]string{
	// Instance-wide configuration: every user of the hub lives under
	// whatever these write.
	leapmuxv1connect.AdminSettingsServiceListSettingsProcedure:        "reads every instance setting including secret presence",
	leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure:       "rewrites instance-wide configuration (sign-up, SMTP, limits)",
	leapmuxv1connect.AdminSettingsServiceUpdateSettingSecretProcedure: "writes the encrypted halves (SMTP password, captcha secrets)",
	leapmuxv1connect.AdminSettingsServiceUpdateSettingsProcedure:      "rewrites several instance settings at once, both halves, in one transaction",
	leapmuxv1connect.AdminSettingsServiceResetSettingProcedure:        "returns instance configuration to defaults, cross-key validated",
	leapmuxv1connect.AdminSettingsServiceResetSettingsProcedure:       "returns several instance settings to defaults in one transaction",
	leapmuxv1connect.AdminNetworkServiceGetListenStatusProcedure:      "describes the host's network interfaces and which addresses the hub answers on",

	// Credential and identity administration: these mint or destroy other
	// users' credentials, which is the definition of administration.
	leapmuxv1connect.AdminUserServiceListUsersProcedure:             "enumerates every account on the hub",
	leapmuxv1connect.AdminUserServiceGetUserProcedure:               "reads any user's profile including pending email state",
	leapmuxv1connect.AdminUserServiceCreateUserProcedure:            "mints accounts, optionally admin, bypassing sign-up policy",
	leapmuxv1connect.AdminUserServiceUpdateUserProcedure:            "rewrites any user's profile, email, and verification state",
	leapmuxv1connect.AdminUserServiceDeleteUserProcedure:            "soft-deletes users and revokes every credential they hold",
	leapmuxv1connect.AdminUserServiceSetUserAdminProcedure:          "grants and revokes administrator privilege itself",
	leapmuxv1connect.AdminUserServiceResetPasswordProcedure:         "sets any user's password without the old one; account takeover if ungated",
	leapmuxv1connect.AdminUserServiceListUserSessionsProcedure:      "enumerates one user's active sessions",
	leapmuxv1connect.AdminUserServiceListSessionsProcedure:          "enumerates every live session across users",
	leapmuxv1connect.AdminUserServiceRevokeSessionProcedure:         "kills any single session",
	leapmuxv1connect.AdminUserServiceRevokeUserSessionsProcedure:    "kills every credential a user holds",
	leapmuxv1connect.AdminUserServicePurgeExpiredSessionsProcedure:  "bulk store mutation of the session table",
	leapmuxv1connect.AdminUserServiceListAPITokensProcedure:         "enumerates bearer tokens across users",
	leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure:         "mints a bearer for any user; the secret crosses once",
	leapmuxv1connect.AdminUserServiceRevokeAPITokenProcedure:        "revokes any bearer token",
	leapmuxv1connect.AdminUserServiceListDelegationTokensProcedure:  "enumerates delegation bearers across users",
	leapmuxv1connect.AdminUserServiceRevokeDelegationTokenProcedure: "revokes any agent's delegation credential",

	// Cross-user worker administration; the per-user surface stays in
	// WorkerManagementService behind ownership checks.
	leapmuxv1connect.AdminWorkerServiceListWorkersProcedure:                  "lists workers across every user with owner names",
	leapmuxv1connect.AdminWorkerServiceGetWorkerProcedure:                    "reads any worker's registration state",
	leapmuxv1connect.AdminWorkerServiceDeregisterWorkerProcedure:             "force-deregisters any user's worker",
	leapmuxv1connect.AdminWorkerServiceListRegistrationKeysProcedure:         "enumerates registration keys across users",
	leapmuxv1connect.AdminWorkerServiceRevokeRegistrationKeyProcedure:        "revokes any registration key",
	leapmuxv1connect.AdminWorkerServicePurgeExpiredRegistrationKeysProcedure: "bulk store mutation of the key table",

	// Authentication infrastructure: a rogue provider row is a credential
	// harvesting endpoint.
	leapmuxv1connect.AdminIdPServiceAddOAuthProviderProcedure:        "installs an OAuth trust anchor (client secret custody)",
	leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure:      "reads the provider inventory including client ids",
	leapmuxv1connect.AdminIdPServiceRemoveOAuthProviderProcedure:     "removes a login method for every user",
	leapmuxv1connect.AdminIdPServiceSetOAuthProviderEnabledProcedure: "toggles a login method for every user",
}

// TestAdminProceduresAreRationaleClassified is the bidirectional tripwire
// in the house shape: every restricted procedure needs a non-empty
// rationale, and every rationale entry must still be restricted.
func TestAdminProceduresAreRationaleClassified(t *testing.T) {
	for procedure := range adminProcedures {
		note, ok := adminProcedureRationale[procedure]
		assert.Truef(t, ok,
			"admin procedure %q is not rationale-classified; record why it is admin-only in adminProcedureRationale", procedure)
		assert.NotEmptyf(t, note, "admin procedure %q has an empty rationale", procedure)
	}
	for procedure := range adminProcedureRationale {
		assert.Truef(t, adminProcedures[procedure],
			"procedure %q is rationale-classified but no longer restricted; remove the stale entry", procedure)
	}
}

// adminProtoProcedurePaths walks admin.proto's service descriptors and
// builds the fully-qualified Connect procedure path for every method.
func adminProtoProcedurePaths(t *testing.T) []string {
	t.Helper()
	services := leapmuxv1.File_leapmux_v1_admin_proto.Services()
	var out []string
	for i := range services.Len() {
		svc := services.Get(i)
		for j := range svc.Methods().Len() {
			m := svc.Methods().Get(j)
			out = append(out, "/"+string(svc.FullName())+"/"+string(m.Name()))
		}
	}
	require.NotEmpty(t, out, "admin.proto exposes no methods; the descriptor walk has nothing to check")
	return out
}

// TestAdminGateCoversEveryAdminProtoMethod walks the admin.proto service
// descriptors and asserts the RECORD covers exactly that method set.
//
// A missing entry no longer means "mounts unrestricted" — enforceAdmin
// denies on the Admin* prefix, so a new admin RPC is gated the moment it
// is declared. What a missing entry means now is an UNDOCUMENTED gate: a
// procedure nobody stated a reason for. A stale entry still means a dead
// record.
func TestAdminGateCoversEveryAdminProtoMethod(t *testing.T) {
	for _, path := range adminProtoProcedurePaths(t) {
		assert.Truef(t, adminProcedures[path],
			"admin.proto method %q has no adminProcedures entry; the prefix gate already restricts it, but its reason is unrecorded — add the entry and a rationale", path)
	}
	for procedure := range adminProcedures {
		found := false
		for _, path := range adminProtoProcedurePaths(t) {
			if path == procedure {
				found = true
				break
			}
		}
		assert.Truef(t, found,
			"adminProcedures entry %q matches no admin.proto method; remove it (or the constant is stale)", procedure)
	}
}

// TestAdminProceduresDisjointFromOtherGates pins that no admin procedure is
// reachable through the public waiver or by a delegation bearer.
//
// The delegation half used to read an allowlist. It now reads the CEILING that
// replaced it, which is a stronger statement: an allowlist bounded the
// procedures a delegation bearer could call and left the grant on its row
// unbounded, so a mint bug produced an over-scoped credential that still
// authenticated. The ceiling bounds the GRANT at every validation, so a
// delegation bearer cannot hold an admin scope at all.
func TestAdminProceduresDisjointFromOtherGates(t *testing.T) {
	delegated := &UserInfo{
		ID:         userid.MustNew("u-delegation"),
		IsAdmin:    true,
		Credential: DelegationCredential("d-1", "w-1"),
		// The widest grant a delegation row could ever carry, after the
		// read-time narrowing loadBearer applies.
		Scopes: authscope.UnscopedGrant().NarrowTo(CeilingFor(BearerKindDelegation)),
	}
	for procedure := range adminProcedures {
		assert.Falsef(t, publicProcedures[procedure],
			"admin procedure %q is also public; that is an auth bypass", procedure)
		assert.Errorf(t, enforceScope(procedure, delegated),
			"admin procedure %q is reachable by a delegation bearer; any spawned agent would reach it", procedure)
		assert.Falsef(t, unverifiedAllowedProcedures[procedure],
			"admin procedure %q is on the unverified allowlist; it stays behind the admin gate only", procedure)
	}
}

// TestAdminProtoServicesAreExactlyFive guards against a sixth admin
// service appearing in the proto without the gate (and its tripwires)
// learning about it.
func TestAdminProtoServicesAreExactlyFive(t *testing.T) {
	want := map[protoreflect.FullName]bool{
		"leapmux.v1.AdminSettingsService": true,
		"leapmux.v1.AdminUserService":     true,
		"leapmux.v1.AdminWorkerService":   true,
		"leapmux.v1.AdminIdPService":      true,
		"leapmux.v1.AdminNetworkService":  true,
	}
	got := map[protoreflect.FullName]bool{}
	services := leapmuxv1.File_leapmux_v1_admin_proto.Services()
	for i := range services.Len() {
		got[services.Get(i).FullName()] = true
	}
	assert.Equal(t, want, got,
		"the admin proto service set changed; extend adminProcedures, adminProcedureRationale, and the hub-server mount test")
}

// TestAdminProceduresAbsentFromHubRPCRegistry pins the worker-IPC bridge
// boundary: hubrpc.Registry is a TYPING device ("not for security" — its
// own comment says so), and anything listed in it is callable by any
// worker-spawned agent holding a delegation bearer. No admin procedure may
// ever be registered there.
func TestAdminProceduresAbsentFromHubRPCRegistry(t *testing.T) {
	for procedure := range adminProcedures {
		_, listed := hubrpc.Registry[procedure]
		assert.Falsef(t, listed,
			"admin procedure %q is in hubrpc.Registry; the worker IPC bridge would proxy it for any spawned agent — admin commands build a hub client directly instead", procedure)
	}
}

// TestNoAdminServiceOutsideAdminProto closes the gap the two walks above
// leave open.
//
// Both of them read File_leapmux_v1_admin_proto ONLY, so a service named
// Admin* declared in any other proto file satisfies every check while
// mounting unrestricted. This walks the whole registry instead: the
// Admin* naming rule is already treated as authoritative (see
// TestAdminProtoServicesAreExactlyFour), so a service that claims the
// name must live where the gate looks for it.
func TestNoAdminServiceOutsideAdminProto(t *testing.T) {
	const adminProtoPath = "leapmux/v1/admin.proto"
	seen := false
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Path() == adminProtoPath {
			seen = true
		}
		services := fd.Services()
		for i := range services.Len() {
			name := services.Get(i).Name()
			if !strings.HasPrefix(string(name), "Admin") {
				continue
			}
			assert.Equalf(t, adminProtoPath, fd.Path(),
				"service %q is named Admin* but lives in %q; the admin gate's tripwires only walk %s, so it would mount UNRESTRICTED with every test green — move it there or rename it",
				services.Get(i).FullName(), fd.Path(), adminProtoPath)
		}
		return true
	})
	require.True(t, seen, "admin.proto is not in the registry; the walk proved nothing")
}

// TestEnforceAdminDeniesByPrefixNotByMap pins that the ENFORCEMENT is the
// prefix and the map is only the rationale record.
//
// The map used to be the gate, which fails OPEN: an Admin*Service method
// nobody adds to it mounts unrestricted, and the walks above catch that
// only when somebody runs the suite. The fixture below is exactly that
// case — a well-formed admin procedure path with no map entry — and the
// gate must refuse it for a non-admin.
func TestEnforceAdminDeniesByPrefixNotByMap(t *testing.T) {
	const unlisted = "/leapmux.v1.AdminUserService/ProcedureNobodyListedYet"
	require.False(t, adminProcedures[unlisted],
		"the fixture must be absent from the map, or it proves nothing")

	plain := &UserInfo{IsAdmin: false}
	admin := &UserInfo{IsAdmin: true}

	err := enforceAdmin(unlisted, plain)
	require.Error(t, err, "an unlisted Admin*Service procedure must still be denied")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.NoError(t, enforceAdmin(unlisted, admin), "an admin passes every admin procedure")

	// A non-admin service keeps passing for everyone.
	assert.NoError(t, enforceAdmin(leapmuxv1connect.AuthServiceGetCurrentUserProcedure, plain))
	// And a service whose name merely CONTAINS "Admin" later in the path
	// is not caught by the prefix.
	assert.NoError(t, enforceAdmin("/leapmux.v1.WorkspaceService/AdminishThing", plain))
}

// TestAdminProceduresAllCarryTheGatePrefix keeps the record and the
// enforcement from describing different sets: every procedure the map
// documents must be one the prefix actually denies.
func TestAdminProceduresAllCarryTheGatePrefix(t *testing.T) {
	for procedure := range adminProcedures {
		assert.Truef(t, strings.HasPrefix(procedure, adminProcedurePrefix),
			"adminProcedures lists %q, which enforceAdmin's prefix %q does not cover", procedure, adminProcedurePrefix)
	}
}
