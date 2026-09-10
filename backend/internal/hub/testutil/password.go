package testutil

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
)

var fixturePasswordHashes = struct {
	sync.Mutex
	values map[string]func() (string, error)
}{values: make(map[string]func() (string, error))}

// FixturePasswordHash returns a hash with production parameters for a fixture password.
// Reuse each hash within the test process. Password hashing tests must call password.Hash directly.
func FixturePasswordHash(t testing.TB, plain string) string {
	t.Helper()
	fixturePasswordHashes.Lock()
	compute := fixturePasswordHashes.values[plain]
	if compute == nil {
		compute = sync.OnceValues(func() (string, error) { return password.Hash(plain) })
		fixturePasswordHashes.values[plain] = compute
	}
	fixturePasswordHashes.Unlock()

	hash, err := compute()
	require.NoError(t, err)
	return hash
}
