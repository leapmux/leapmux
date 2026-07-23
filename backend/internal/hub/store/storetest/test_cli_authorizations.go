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
		orgID := SeedOrg(t, st, "device-auth-subsecond-org")
		user := SeedUser(t, st, orgID, "device-auth-subsecond-user")
		deviceCode := id.Generate()
		expiresAt := time.Now().UTC().Truncate(time.Second).Add(950 * time.Millisecond)
		if time.Until(expiresAt) < 400*time.Millisecond {
			time.Sleep(time.Until(expiresAt) + 50*time.Millisecond)
			expiresAt = time.Now().UTC().Truncate(time.Second).Add(950 * time.Millisecond)
		}
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{DeviceCode: deviceCode, UserID: userid.MustNew(user.ID)})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
	})

	t.Run("expired approved device grant cannot be consumed", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "device-auth-org")
		user := SeedUser(t, st, orgID, "device-auth-user")
		deviceCode := id.Generate()
		expiresAt := time.Now().Add(1500 * time.Millisecond)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: expiresAt,
		}))
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)
		time.Sleep(time.Until(expiresAt) + 100*time.Millisecond)

		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode)
		require.NoError(t, err)
		assert.Zero(t, rows)
	})

	// An approval names WHO approved, so an unminted approver must be refused
	// rather than written as SQL NULL.
	//
	// NULL is the legitimate state of a PENDING row, which is exactly why this
	// bites: the UPDATE filters on the device/user code alone, so it would match
	// and report one row affected. The browser would say "device authorized"
	// while the row stayed effectively unapproved, and the polling CLI -- which
	// answers authorization_pending for a blank user_id -- would keep waiting
	// until the grant expired, told the opposite of what happened.
	t.Run("device grant cannot be approved by an unminted user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "device-auth-zero-org")
		user := SeedUser(t, st, orgID, "device-auth-zero-user")
		deviceCode := id.Generate()
		userCode := verifycode.Generate()
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: time.Now().Add(time.Hour),
		}))

		_, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
			DeviceCode: deviceCode, UserID: userid.UserID{},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
		_, err = st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.UserID{},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		// The row must be untouched -- still pending, still approvable.
		row, err := st.DeviceAuthorizations().GetByUserCode(ctx, userCode)
		require.NoError(t, err)
		assert.Zero(t, row.Approved, "a refused approval must not have marked the row approved")

		// Control: the same row, approved by a real user.
		rows, err := st.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: userCode, UserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows, "control: a real user approves the same row")
	})
}
