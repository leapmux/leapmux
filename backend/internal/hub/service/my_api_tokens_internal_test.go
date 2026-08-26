package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestCallerAPITokenID derives "which credential is making this call" from
// the CALLER's own credential, never from the request body. A client that
// could specify the row would decide which entry the UI marks as "this device",
// and the mark is what stops somebody cutting off the machine they are on.
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
// and when `current` is set.
func TestMyAPITokenToProto(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	used := created.Add(time.Hour)
	refresh := created.Add(90 * 24 * time.Hour)
	access := created.Add(time.Hour)
	row := store.APIToken{
		ID:               "tok-1",
		UserID:           "usr_1",
		ClientType:       "cli",
		ClientName:       "alice@laptop",
		SecretHash:       []byte("secret"),
		RefreshHash:      []byte("refresh"),
		CreatedAt:        created,
		LastUsedAt:       &used,
		ExpiresAt:        &access,
		RefreshExpiresAt: &refresh,
		AdminScope:       true,
	}

	out := myAPITokenToProto(row, "tok-1")
	assert.Equal(t, "tok-1", out.GetId())
	assert.Equal(t, "alice@laptop", out.GetClientName())
	assert.True(t, out.GetAdminScope())
	assert.True(t, out.GetCurrent())
	assert.Equal(t, created, out.GetCreatedAt().AsTime())
	assert.Equal(t, used, out.GetLastUsedAt().AsTime())
	assert.Equal(t, refresh, out.GetRefreshExpiresAt().AsTime())

	// A different caller marks nothing, and an EMPTY caller id must not
	// match a row whose id is also empty by accident.
	assert.False(t, myAPITokenToProto(row, "tok-2").GetCurrent())
	assert.False(t, myAPITokenToProto(store.APIToken{ID: ""}, "").GetCurrent())

	// The absent optional stamps stay absent rather than rendering as the
	// zero instant, which a client would show as 1 January year one.
	bare := myAPITokenToProto(store.APIToken{ID: "tok-3", CreatedAt: created}, "")
	assert.Nil(t, bare.GetLastUsedAt())
	assert.Nil(t, bare.GetRefreshExpiresAt())
}
