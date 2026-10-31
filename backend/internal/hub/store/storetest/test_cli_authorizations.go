package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCLIAuthorizations(t *testing.T) {
	// The create binds PENDING itself, because the column has no DEFAULT: a
	// caller cannot state another starting point.
	t.Run("a new device grant is pending", func(t *testing.T) {
		st := s.NewStore(t)
		deviceCode := id.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: time.Now().Add(time.Hour),
		}))

		row, err := st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING, row.State)
		assert.Empty(t, row.UserID, "nobody approved the grant yet")
		assert.Empty(t, row.GrantedScopes, "a pending grant grants nothing")
	})

	// Consume and ConsumeApprovedForUserClient bind the APPROVED ordinal, and
	// a pending or denied grant must never become a credential.
	t.Run("only an approved device grant is consumed", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-consume-user")
		now := time.Now().UTC()
		create := func() (deviceCode, userCode string) {
			deviceCode, userCode = id.Generate(), verifycode.Generate()
			require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
				ClientID:   oauthapp.ControlCLIClientID,
				DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: now.Add(time.Hour),
			}))
			return deviceCode, userCode
		}
		approve := func(userCode string) {
			rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
				UserCode: userCode, UserID: userid.MustNew(user.ID),
			}, now)
			require.NoError(t, err)
			require.Equal(t, int64(1), rows)
		}
		pending, _ := create()
		denied, deniedUserCode := create()
		rows, err := st.DeviceAuthorizations().DenyByUserCode(ctx, deniedUserCode)
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)
		approved, approvedUserCode := create()
		approve(approvedUserCode)
		disconnected, disconnectedUserCode := create()
		approve(disconnectedUserCode)

		rows, err = st.DeviceAuthorizations().Consume(ctx, pending, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a pending grant must not be consumed")
		rows, err = st.DeviceAuthorizations().Consume(ctx, denied, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a denied grant must not be consumed")
		rows, err = st.DeviceAuthorizations().Consume(ctx, approved, now)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows, "an approved grant is consumed")

		// The disconnect spends the one approved grant still unconsumed.
		rows, err = st.DeviceAuthorizations().ConsumeApprovedForUserClient(ctx, oauthapp.ControlCLIClientID, userid.MustNew(user.ID), now)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
		for code, wantConsumed := range map[string]bool{pending: false, denied: false, approved: true, disconnected: true} {
			row, err := st.DeviceAuthorizations().Get(ctx, code)
			require.NoError(t, err)
			assert.Equal(t, wantConsumed, row.ConsumedAt != nil, "consumed_at of %s", code)
		}
	})

	t.Run("subsecond-live device grant can be approved", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-subsecond-user")
		deviceCode := id.Generate()
		now := time.Date(2040, 1, 2, 3, 4, 5, 500_000_000, time.UTC)
		expiresAt := now.Truncate(time.Second).Add(950 * time.Millisecond)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{DeviceCode: deviceCode, UserID: userid.MustNew(user.ID)}, now)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
	})

	// Both approval methods check expiry against the caller's clock.
	// A missing SQL parameter previously made this predicate always true on SQLite, PostgreSQL, and MySQL.
	// TiDB instead rejected the datetime, so every CLI device authorization returned HTTP 500.
	t.Run("device grant approval judges liveness on the caller clock", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-clock-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))
		afterExpiry := time.Now().UTC().Add(48 * time.Hour)

		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(user.ID),
		}, afterExpiry)
		require.NoError(t, err)
		assert.Zero(t, rows, "Approve must refuse a grant that is dead at the caller's clock")

		rows, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		}, afterExpiry)
		require.NoError(t, err)
		assert.Zero(t, rows, "ApproveByUserCode must refuse a grant that is dead at the caller's clock")

		// Control: the same row, at the caller's own clock.
		rows, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows, "control: the same grant approves at a live clock")
	})

	t.Run("expired approved device grant cannot be consumed", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-user")
		deviceCode := id.Generate()
		now := time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)
		expiresAt := now.Add(1500 * time.Millisecond)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(user.ID),
		}, now)
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)

		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode, expiresAt)
		require.NoError(t, err)
		assert.Zero(t, rows, "the grant expires at its deadline")

		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode, expiresAt.Add(time.Millisecond))
		require.NoError(t, err)
		assert.Zero(t, rows, "the grant stays expired after its deadline")

		// A refused consume must preserve the row. The same grant remains usable before expiry.
		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode, expiresAt.Add(-time.Millisecond))
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
	})

	// An approval must identify its user. Refuse a zero user ID instead of writing SQL NULL.
	// NULL is valid for a pending row. An update that filters only by device or user code could report success without an approver.
	// The browser would report authorization, but the CLI would receive authorization_pending until the grant expired.
	t.Run("device grant cannot be approved by an unminted user", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-zero-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))

		_, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.UserID{},
		}, time.Now().UTC())
		require.ErrorIs(t, err, store.ErrInvalidArgument)
		_, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.UserID{},
		}, time.Now().UTC())
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		// The row must be untouched -- still pending, still approvable.
		row, err := st.DeviceAuthorizations().GetByUserCode(ctx, userCode)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING, row.State, "a refused approval must not have marked the row approved")

		// Control: the same row, approved by a real user.
		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows, "control: a real user approves the same row")
	})

	// Approve a pending grant only once. A repeated form submission or a second approver must change nothing.
	// Otherwise, the second approval could replace user_id and granted_scopes before consumption.
	// The last approver would receive the credential while the first approver saw a successful authorization.
	// This remains possible until the next poll, or until expiry if no client polls.
	t.Run("an approved device grant cannot be approved again", func(t *testing.T) {
		st := s.NewStore(t)
		first := SeedUser(t, st, "device-auth-first-approver")
		second := SeedUser(t, st, "device-auth-second-approver")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))
		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(first.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)

		rows, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(second.ID), GrantedScopes: "admin:read",
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "ApproveByUserCode must refuse a grant that is already approved")

		rows, err = st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(second.ID), GrantedScopes: "admin:read",
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "Approve must refuse a grant that is already approved")

		row, err := st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		assert.Equal(t, first.ID, row.UserID, "the first approver keeps the grant")
		assert.Empty(t, row.GrantedScopes, "a refused approval must not widen the grant")
	})

	// A denial moves the grant from PENDING to DENIED, and it is final. The
	// approve and deny statements match a pending row only, so a DENIED grant
	// can never become APPROVED for the rest of its life.
	t.Run("a denied device grant cannot be approved", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-denied-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   oauthapp.ControlCLIClientID,
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))
		denied, err := st.DeviceAuthorizations().DenyByUserCode(ctx, userCode)
		require.NoError(t, err)
		require.Equal(t, int64(1), denied)
		row, err := st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		require.Equal(t, leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_DENIED, row.State, "the deny moves the grant to DENIED")

		denied, err = st.DeviceAuthorizations().DenyByUserCode(ctx, userCode)
		require.NoError(t, err)
		assert.Zero(t, denied, "a second deny matches no pending grant")

		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "ApproveByUserCode must refuse a denied grant")

		rows, err = st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "Approve must refuse a denied grant")

		row, err = st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_DENIED, row.State, "the grant stays denied")
		assert.Empty(t, row.UserID, "a refused approval records no user")
	})
}
