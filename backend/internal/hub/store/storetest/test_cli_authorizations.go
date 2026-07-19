package storetest

import (
	"testing"
	"time"

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
		rows, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{DeviceCode: deviceCode, UserID: user.ID})
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
			DeviceCode: deviceCode, UserID: user.ID,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)
		time.Sleep(time.Until(expiresAt) + 100*time.Millisecond)

		rows, err = st.DeviceAuthorizations().Consume(ctx, deviceCode)
		require.NoError(t, err)
		assert.Zero(t, rows)
	})
}
