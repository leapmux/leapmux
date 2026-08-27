package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestCallerAPITokenID derives "which credential makes this call" from
// the CALLER's own credential, never from the request body. A client that
// could specify the row would decide which entry the UI marks as "this device",
// and the mark is what stops somebody from revoking the machine they use.
func TestCallerAPITokenID(t *testing.T) {
	t.Parallel()

	uid := userid.MustNew("usr_1")
	assert.Equal(t, "tok-1", callerAPITokenID(&auth.UserInfo{ID: uid, Credential: auth.APICredential("tok-1")}))
	assert.Empty(t, callerAPITokenID(&auth.UserInfo{ID: uid, Credential: auth.SessionCredential("s-1")}),
		"a browser session is not a CLI credential")
	assert.Empty(t, callerAPITokenID(&auth.UserInfo{ID: uid, Credential: auth.DelegationCredential("d-1", "w-1")}),
		"a worker-minted bearer is not one of the account's own devices")
	assert.Empty(t, callerAPITokenID(&auth.UserInfo{ID: uid}), "solo mode carries no credential at all")
}

// TestMyAPITokenToProto pins the mapper's two decisions: which fields cross,
// and when the mapper sets `current`.
func TestMyAPITokenToProto(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	used := created.Add(time.Hour)
	refresh := created.Add(90 * 24 * time.Hour)
	access := created.Add(time.Hour)
	row := store.APIToken{
		ID:               "tok-1",
		UserID:           "usr_1",
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "alice@laptop",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		// The app's REGISTERED ceiling, which the oauth_clients join always
		// supplies -- it is total, so a row with a blank one is a fixture the
		// store cannot produce. Here it is wide enough to cut nothing, so the
		// assertion below reads the consent; TestMyAPITokenToProto_ReportsWhat
		// TheCredentialReaches drives the case where it cuts.
		ClientScopes:     authscope.NonAdminGrant().String(),
		SecretHash:       []byte("secret"),
		RefreshHash:      []byte("refresh"),
		CreatedAt:        created,
		LastUsedAt:       &used,
		ExpiresAt:        &access,
		RefreshExpiresAt: &refresh,
	}

	out := myAPITokenToProto(row, "tok-1")
	assert.Equal(t, "tok-1", out.GetId())
	assert.Equal(t, "alice@laptop", out.GetInstallationName())
	assert.Equal(t, oauthapp.ControlCLIClientID, out.GetClientId())
	// The GRANT is rendered as sorted tokens, so a person reading the
	// connected-app list can see what the app can actually do.
	assert.Equal(t, []string{
		"account:read", "account:write", "agent:read", "agent:write",
		"file:read", "git:read", "git:write", "terminal:read", "terminal:write",
		"tunnel:open", "worker:admin", "worker:read",
		"workspace:read", "workspace:write",
	}, out.GetGrantedScopes())
	assert.True(t, out.GetCurrent())
	assert.Equal(t, created, out.GetCreatedAt().AsTime())
	assert.Equal(t, used, out.GetLastUsedAt().AsTime())
	assert.Equal(t, refresh, out.GetRefreshExpiresAt().AsTime())
	assert.Nil(t, out.GetExpiresAt(),
		"a renewing credential must not report an access expiry that moves at every rotation")

	// A different caller marks nothing, and an EMPTY caller id must not
	// match a row whose id is also empty by accident.
	assert.False(t, myAPITokenToProto(row, "tok-2").GetCurrent())
	assert.False(t, myAPITokenToProto(store.APIToken{ID: ""}, "").GetCurrent())

	// The absent optional stamps stay absent rather than rendering as the
	// zero instant, which a client would show as 1 January year one.
	bare := myAPITokenToProto(store.APIToken{ID: "tok-3", CreatedAt: created}, "")
	assert.Nil(t, bare.GetLastUsedAt())
	assert.Nil(t, bare.GetRefreshExpiresAt())
	assert.Nil(t, bare.GetExpiresAt())
}

// TestMyAPITokenToProtoReportsOneDeadline pins the exclusive rule.
//
// The two credential kinds carry different deadlines, and each row must report
// exactly one: a renewing credential reports when the device signs in again,
// and a fixed-lifetime credential reports its whole life. Reporting neither is
// what left a `--ttl`-minted service credential in the account's own listing
// with no deadline at all, indistinguishable from one that never expires.
func TestMyAPITokenToProtoReportsOneDeadline(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	access := created.Add(365 * 24 * time.Hour)

	fixed := myAPITokenToProto(store.APIToken{
		ID:        "tok-ttl",
		CreatedAt: created,
		ExpiresAt: &access,
	}, "")
	assert.Nil(t, fixed.GetRefreshExpiresAt(), "a fixed-lifetime credential has no refresh deadline")
	assert.Equal(t, access, fixed.GetExpiresAt().AsTime(),
		"its access expiry IS its whole life, so it must be reported")
}

// TestMyAPITokenToProtoReportsWhatTheCredentialReaches pins the mapper against
// the app's REGISTERED ceiling, not the stored consent.
//
// This list is what a person reads to decide whether to disconnect an app, and
// an owner who has just narrowed the registration is exactly the person reading
// it. Reporting the column would show them no effect and would name permissions
// the app's very next call is refused.
func TestMyAPITokenToProtoReportsWhatTheCredentialReaches(t *testing.T) {
	t.Parallel()

	out := myAPITokenToProto(store.APIToken{
		ID:               "tok-1",
		UserID:           "usr_1",
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "alice@laptop",
		// CONSENTED to three, and the registration has since lost one of them.
		GrantedScopes: "workspace:read file:read worker:read",
		ClientScopes:  "workspace:read worker:read",
	}, "")

	assert.Equal(t, []string{"worker:read", "workspace:read"}, out.GetGrantedScopes(),
		"a permission the registration no longer allows must not be listed as one the app holds")
}

// TestMyAPITokenToProtoRendersNoPermissionsForAnUnreadableCeiling.
//
// The same answer an unreadable GRANT already gets, and for the same reason: a
// value the hub cannot read must not render as though it were legible, and
// validation already refuses the credential for it.
func TestMyAPITokenToProtoRendersNoPermissionsForAnUnreadableCeiling(t *testing.T) {
	t.Parallel()

	for name, row := range map[string]store.APIToken{
		"the grant drifted":   {GrantedScopes: "workspace:read invented:permission", ClientScopes: "workspace:read"},
		"the ceiling drifted": {GrantedScopes: "workspace:read", ClientScopes: "workspace:read invented:permission"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, myAPITokenToProto(row, "").GetGrantedScopes())
		})
	}
}
