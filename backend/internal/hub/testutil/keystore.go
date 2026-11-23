package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

// TestKeystore returns a fresh random keystore for tests that open a store
// directly (sqlite.Open, storeopen.Open, etc.).
func TestKeystore(t *testing.T) *keystore.Keystore {
	t.Helper()
	ks, err := keystore.NewRandom()
	require.NoError(t, err)
	return ks
}
