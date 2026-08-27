package service

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// soloUserInfo is the shape auth.LoadSoloUser produces: an administrator with
// NO credential row, because solo mode authenticates every request as one
// bootstrapped account and holds no session and no bearer.
func soloUserInfo() *auth.UserInfo {
	return &auth.UserInfo{
		ID:              userid.MustNew("solo-user"),
		Username:        "solo",
		IsAdmin:         true,
		AuthenticatedAt: time.Now().UTC(),
		Solo:            true,
	}
}

// TestElevationRuleAdmitsSoloMode pins the rule that keeps the whole
// hub-administration surface reachable on a desktop hub.
//
// Solo mode has no ceremony to prove a factor with: there is no sign-in, and
// rejectSoloElevation refuses ElevateSession and both passkey elevation legs
// outright. Its synthetic user also carries no credential row, so every
// credential-shaped test answers "cannot elevate" and the refusal that
// follows -- "sign in from a browser" -- names a sign-in that does not exist.
// That refusal is PERMANENT, and it covers every hub-settings write plus the
// durable-authority verbs, which solo mode is meant to serve: only the
// HiddenInSolo keys are withdrawn from that surface.
func TestElevationRuleAdmitsSoloMode(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	solo := soloUserInfo()

	require.NoError(t, requireElevation(solo, now),
		"solo mode has no factor to prove, so the rule has nothing to decide")
	require.NoError(t, assertElevatedActor(solo, now),
		"the mint's backstop must admit it for the same reason")

	ctx := auth.WithUser(context.Background(), solo)
	actor, err := requireElevatedSessionForDurableAuthority(ctx, now)
	require.NoError(t, err,
		"the strict rule must answer solo BEFORE its session test, which the synthetic user cannot pass")
	assert.Same(t, solo, actor, "the caller needs the actor back to slide and re-read with")
}

// TestElevationRuleStillRefusesAnOrdinaryCallerWithNoCredential guards the
// exemption above from widening into every caller.
//
// The solo answer is keyed on the SoloAuthenticated flag, which LoadSoloUser
// is the only producer of. A caller that merely carries an empty credential --
// a shape a bug or a hand-built fixture can reach -- must still be refused,
// and refused WITHOUT the step-up marker, because no prompt can help it.
func TestElevationRuleStillRefusesAnOrdinaryCallerWithNoCredential(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	bare := &auth.UserInfo{ID: userid.MustNew("usr-1"), IsAdmin: true}

	err := requireElevation(bare, now)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Empty(t, connectErr.Meta().Get(ElevationRequiredHeader),
		"a credential that can never carry a window must not be sent to a prompt")

	ctx := auth.WithUser(context.Background(), bare)
	_, err = requireElevatedSessionForDurableAuthority(ctx, now)
	require.Error(t, err, "the strict rule must refuse a caller that holds no session")
}
