package solo

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestLoadOrCreateWorkerStatePreservesIdentityAfterAReissueConflict(t *testing.T) {
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: "original-token", RegisteredBy: "owner"})
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	registration := activeRegistration("owner", keys)
	registration.AuthTokenAccepted = false
	registrar := &fakeRegistrar{registration: registration, reissueErr: store.ErrConflict}
	state, composite, err := loadWithLease(t, registrar, dir)
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Nil(t, state)
	assert.Nil(t, composite)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "identity and private key bytes must remain unchanged")
	assert.Empty(t, registrar.retiredIDs)
	assert.Zero(t, registrar.registered)
	assert.Equal(t, []string{"w-original"}, registrar.reissuedIDs)
	restored := readState(t, path)
	assert.Equal(t, "w-original", restored.WorkerID)
}
