package service

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The gate is a property of the MINT, not of what each handler remembered to
// type. Two surfaces mint the same command-line credential, the classification
// tripwire in user_procedures_internal_test.go cannot reach an Admin*
// procedure, and the consent legs are mux routes rather than Connect
// procedures -- so the omission the gate exists to prevent was possible at
// exactly the place it mattered, and it had already happened once.
func TestMintAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	uid := userid.MustNew("6PwpqVF2YPRs9izaHencG20nazPAhW0aXNAKLBdAnBui")
	session := func(elevatedUntil *time.Time) *auth.UserInfo {
		return &auth.UserInfo{
			ID:         uid,
			Credential: auth.SessionCredential("s-1"),
			Elevation:  auth.NewElevation(&now, elevatedUntil),
		}
	}

	t.Run("a consumed consent grant permits the mint", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, mintedByConsentGrant("code-1").assert(now))
	})

	// /auth/cli/token carries no session at all, so the grant row IS the
	// proof -- but only a caller that actually loaded one can pass it.
	t.Run("an absent grant with no actor is refused", func(t *testing.T) {
		t.Parallel()
		err := mintedByConsentGrant("").assert(now)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})

	t.Run("an elevated session permits the mint", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		require.NoError(t, mintedByActor(session(&until)).assert(now))
	})

	t.Run("an un-elevated session is refused with the prompt marker", func(t *testing.T) {
		t.Parallel()
		err := mintedByActor(session(nil)).assert(now)
		require.Error(t, err)
		var connectErr *connect.Error
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
		assert.Equal(t, "1", connectErr.Meta().Get(ElevationRequiredHeader))
	})

	t.Run("a lapsed elevation is refused", func(t *testing.T) {
		t.Parallel()
		lapsed := now.Add(-time.Minute)
		require.Error(t, mintedByActor(session(&lapsed)).assert(now))
	})

	// A COMMAND-LINE credential takes the same rule as a session: it has a
	// row of its own to stamp now, and it proves the factor in a browser
	// through /auth/cli/elevate-authorization. Admitting it unelevated made
	// possession of the credential file the whole of the check for the mint.
	t.Run("an elevated command-line credential is admitted", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		bearer := &auth.UserInfo{
			ID:         uid,
			Credential: auth.APICredential("t-1"),
			Elevation:  auth.Elevation{ExpiresAt: until},
		}
		require.NoError(t, mintedByActor(bearer).assert(now))
	})

	t.Run("an unelevated command-line credential is refused", func(t *testing.T) {
		t.Parallel()
		bearer := &auth.UserInfo{ID: uid, Credential: auth.APICredential("t-1")}
		require.Error(t, mintedByActor(bearer).assert(now))
	})

	// A DELEGATION bearer can carry no elevation at all -- a worker mints it
	// for an agent that reads untrusted input -- so it is refused whatever
	// the row says.
	t.Run("a delegation bearer is refused", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		delegated := &auth.UserInfo{
			ID:         uid,
			Credential: auth.DelegationCredential("d-1", "w-1"),
			Elevation:  auth.Elevation{ExpiresAt: until},
		}
		require.Error(t, mintedByActor(delegated).assert(now))
	})

	t.Run("no authority at all is refused", func(t *testing.T) {
		t.Parallel()
		require.Error(t, mintAuthorityZeroForTest().assert(now))
	})
}

// mintAuthorityZeroForTest is the shape a new mint site would produce by
// forgetting to state an authority at all.
func mintAuthorityZeroForTest() mintAuthority { return mintAuthority{} }
