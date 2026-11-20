package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestStepUpMutationAuth_AdmitsSoloModeThroughTheFactorBranch pins the
// admission the gate documents: a delegation bearer is refused, and solo
// mode is exempted by auth.RequireElevation on the factor branch.
//
// Solo mode has no ceremony to prove a factor with, so the factor branch
// must not refuse it. The first-credential branch cannot admit it either --
// the synthetic user holds no session to rest a sign-in instant on -- so the
// factor branch's exemption is the only admission solo mode has.
func TestStepUpMutationAuth_AdmitsSoloModeThroughTheFactorBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)
	svc := &UserService{store: st}

	now := time.Now().UTC()
	solo := &auth.UserInfo{
		ID:       userid.MustNew(user.ID),
		Username: "solo",
		IsAdmin:  true,
		Solo:     true,
	}
	require.False(t, solo.Elevated(now),
		"precondition: the synthetic solo user carries no window and holds no row to stamp")

	admission, err := svc.stepUpMutationAuth(ctx, entryStepUp(solo), user)
	require.NoError(t, err,
		"solo mode has no ceremony to prove a factor with, so auth.RequireElevation exempts it")
	assert.False(t, admission.firstCredential,
		"admitted on the factor branch, not the first-credential rule")
}
