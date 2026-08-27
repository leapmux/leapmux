package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCLIAuthorizations(t *testing.T) {
	t.Run("subsecond-live device grant can be approved", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-subsecond-user")
		deviceCode := id.Generate()
		expiresAt := time.Now().UTC().Truncate(time.Second).Add(950 * time.Millisecond)
		if time.Until(expiresAt) < 400*time.Millisecond {
			time.Sleep(time.Until(expiresAt) + 50*time.Millisecond)
			expiresAt = time.Now().UTC().Truncate(time.Second).Add(950 * time.Millisecond)
		}
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{DeviceCode: deviceCode, UserID: userid.MustNew(user.ID)}, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
	})

	// Both approve verbs judge grant liveness on the caller's clock. The
	// omission that left this unbound was a silent always-true predicate on
	// sqlite/postgres/mysql and a hard `Incorrect datetime value` on TiDB,
	// so every CLI device authorization on a TiDB hub answered 500.
	t.Run("device grant approval judges liveness on the caller clock", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-clock-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
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
		expiresAt := time.Now().Add(1500 * time.Millisecond)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)
		time.Sleep(time.Until(expiresAt) + 100*time.Millisecond)

		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows)
	})

	// An approval identifies WHO approved, so an unminted approver must be
	// refused rather than written as SQL NULL.
	//
	// NULL is the legitimate state of a PENDING row, which is exactly why this
	// is dangerous: the UPDATE filters on the device/user code alone, so it
	// would match and report one row affected. The browser would say "device
	// authorized" while the row stayed effectively unapproved, and the polling
	// CLI, which answers authorization_pending for a blank user_id, would keep
	// waiting until the grant expired, told the opposite of what happened.
	t.Run("device grant cannot be approved by an unminted user", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-zero-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
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
		assert.Zero(t, row.Approved, "a refused approval must not have marked the row approved")

		// Control: the same row, approved by a real user.
		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows, "control: a real user approves the same row")
	})

	// An approval is once. The statements match a PENDING row only, so the
	// second POST -- a double click, a re-submitted form, or a second person
	// who received the code -- changes nothing.
	//
	// Without that guard the second approval overwrote user_id and
	// admin_scope on a grant nobody consumed yet, so the credential reached
	// whoever approved LAST while the first approver read "Device
	// authorized". The window is one poll interval normally, and the whole
	// grant TTL for a code nobody polls.
	t.Run("an approved device grant cannot be approved again", func(t *testing.T) {
		st := s.NewStore(t)
		first := SeedUser(t, st, "device-auth-first-approver")
		second := SeedUser(t, st, "device-auth-second-approver")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))
		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(first.ID),
		}, time.Now().UTC())
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)

		rows, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(second.ID), AdminScope: true,
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "ApproveByUserCode must refuse a grant that is already approved")

		rows, err = st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(second.ID), AdminScope: true,
		}, time.Now().UTC())
		require.NoError(t, err)
		assert.Zero(t, rows, "Approve must refuse a grant that is already approved")

		row, err := st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		assert.Equal(t, first.ID, row.UserID, "the first approver keeps the grant")
		assert.False(t, row.AdminScope, "a refused approval must not widen the scope")
	})

	// A denial is final. The approve statements match a pending row only, so
	// approved = 2 can never return to 1 for the rest of the grant's life.
	t.Run("a denied device grant cannot be approved", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "device-auth-denied-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))
		denied, err := st.DeviceAuthorizations().Deny(ctx, deviceCode)
		require.NoError(t, err)
		require.Equal(t, int64(1), denied)

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

		row, err := st.DeviceAuthorizations().Get(ctx, deviceCode)
		require.NoError(t, err)
		assert.Equal(t, int64(2), row.Approved, "the grant stays denied")
		assert.Empty(t, row.UserID, "a refused approval records no user")
	})
}
