//go:build unix

package solo

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateWorkerState_ReissuesAgainAfterAFailedPersist(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory without its write permission")
	}
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: "original-token", RegisteredBy: "owner"})
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	registration := activeRegistration("owner", keys)
	registration.AuthTokenAccepted = false
	reg := &fakeRegistrar{registration: registration, reissuedToken: "first-replacement"}
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	reg.onReissue = func() { require.NoError(t, os.Chmod(dir, 0o500)) }
	_, _, err = loadWithLease(t, reg, dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrPermission)
	require.NoError(t, os.Chmod(dir, 0o700))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, reg.retiredIDs)
	assert.Zero(t, reg.registered)
	reg.onReissue = nil
	reg.reissuedToken = "second-replacement"
	got, _, err := loadWithLease(t, reg, dir)
	require.NoError(t, err)
	assert.Equal(t, "w-original", got.WorkerID)
	assert.Equal(t, "second-replacement", got.AuthToken)
	assert.Equal(t, "second-replacement", readState(t, path).AuthToken)
	assert.Equal(t, []string{"original-token", "original-token"}, reg.registrationTokens)
	assert.Equal(t, []string{"w-original", "w-original"}, reg.reissuedIDs)
	assert.Empty(t, reg.retiredIDs)
	assert.Zero(t, reg.registered)
}
