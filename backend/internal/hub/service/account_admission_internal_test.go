package service

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupAdmissionRefusesAStaleInitialRequestAfterClaimRelease(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	svc := NewAuthService(AuthServiceDeps{
		SessionHasher: hubtestutil.TestSecretHasher(), Store: st, Config: &config.Config{},
		Lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil),
	})
	require.NoError(t, svc.InitializeAccountState(t.Context()))
	for range 2 {
		user, _, err := svc.createSignupUser(t.Context(), signupInitialAdministrator, createUserTxParams{
			username: "stale-initial", passwordHash: "hash", isAdmin: true, firstCredentialExempt: true,
		})
		require.ErrorIs(t, err, errSetupAlreadyComplete)
		assert.Nil(t, user)
		assert.False(t, svc.setupClaim.Load(), "refusing stale setup must release its claim")
	}
	count, err := st.Users().Count(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}
