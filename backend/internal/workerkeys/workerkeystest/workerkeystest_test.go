package workerkeystest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func TestKeys(t *testing.T) {
	t.Parallel()

	keys := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	require.NoError(t, workerkeys.Validate(keys))
	assert.Len(t, keys.GetX25519(), contracts.X25519PublicKeySize)
	assert.Len(t, keys.GetMlkem(), contracts.MlkemPublicKeySize)
	assert.Len(t, keys.GetSlhdsa(), contracts.SlhdsaPublicKeySize)
	assert.Equal(t, uint8(workerkeystest.SeededWorkerFill), keys.GetX25519()[0])
	assert.Equal(t, uint8(workerkeystest.SeededWorkerFill), keys.GetMlkem()[0])
	assert.Equal(t, uint8(workerkeystest.SeededWorkerFill), keys.GetSlhdsa()[0])
}

func TestKeysPanicsOnAnInvalidFill(t *testing.T) {
	t.Parallel()

	// 0x7d's low nibble packs ML-KEM coefficients of 0xd7d, above the 3329
	// modulus, so the set is correctly sized but not a usable key.
	assert.Panics(t, func() { workerkeystest.Keys(0x7d) })
}
