package service

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/require"
)

func TestSessionElevationRequiresAnExplicitFactor(t *testing.T) {
	_, err := grantSessionElevation(t.Context(), nil, nil, "session", userid.MustNew("owner"), time.Now(), nil)
	require.ErrorContains(t, err, "requires a verified factor")
	current := &store.User{ID: "owner", FirstCredentialExempt: true, PasswordHash: "verified hash"}
	for _, proof := range []sessionElevationProof{
		passwordElevationProof{},
		passwordElevationProof{passwordHash: "another hash"},
		passwordElevationProof{passwordHash: current.PasswordHash, authGeneration: 1},
		providerElevationProof{},
		passkeyElevationProof{},
		passkeyElevationProof{identity: webauthn.VerifiedCredentialIdentity{RowID: "row"}},
	} {
		require.ErrorIs(t, proof.validate(t.Context(), nil, current), errElevationProofChanged)
	}
}
