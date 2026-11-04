package workerkeys_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func TestOf(t *testing.T) {
	t.Parallel()

	ck, err := noise.GenerateCompositeKeypair()
	require.NoError(t, err)

	keys, err := workerkeys.Of(ck)
	require.NoError(t, err)

	slhdsa, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	assert.Equal(t, ck.X25519Public, keys.GetX25519())
	assert.Equal(t, ck.MlkemPublicKeyBytes(), keys.GetMlkem())
	assert.Equal(t, slhdsa, keys.GetSlhdsa())
	assert.Len(t, keys.GetX25519(), contracts.X25519PublicKeySize)
	assert.Len(t, keys.GetMlkem(), contracts.MlkemPublicKeySize)
	assert.Len(t, keys.GetSlhdsa(), contracts.SlhdsaPublicKeySize)
}

func TestOfRefusesAKeypairItCannotDescribe(t *testing.T) {
	t.Parallel()

	ck, err := noise.GenerateCompositeKeypair()
	require.NoError(t, err)

	t.Run("a nil keypair", func(t *testing.T) {
		keys, err := workerkeys.Of(nil)
		require.Error(t, err)
		assert.Nil(t, keys)
	})

	t.Run("a keypair with no ML-KEM key", func(t *testing.T) {
		broken := *ck
		broken.MlkemDecapsulationKey = nil
		keys, err := workerkeys.Of(&broken)
		require.Error(t, err)
		assert.Nil(t, keys)
		assert.Contains(t, err.Error(), "ML-KEM")
	})

	t.Run("a zero-value SLH-DSA public key", func(t *testing.T) {
		broken := *ck
		broken.SlhdsaPublicKey = noise.CompositeKeypair{}.SlhdsaPublicKey
		keys, err := workerkeys.Of(&broken)
		require.Error(t, err, "a zero-value public key marshals to empty bytes, so Validate must catch it")
		assert.Nil(t, keys)
	})
}

// Validate owns the check for an absent set, and hands each key to the noise
// package in its own position. noise_test.go covers what makes a key valid; a
// bad key in each position here shows that the set reaches that check whole, and
// the error identifies the position.
func TestValidate(t *testing.T) {
	t.Parallel()

	valid := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	clone := func(mutate func(*leapmuxv1.WorkerPublicKeys)) *leapmuxv1.WorkerPublicKeys {
		out := proto.Clone(valid).(*leapmuxv1.WorkerPublicKeys)
		mutate(out)
		return out
	}

	t.Run("accepts a complete set", func(t *testing.T) {
		require.NoError(t, workerkeys.Validate(valid))
	})

	t.Run("refuses an absent set", func(t *testing.T) {
		err := workerkeys.Validate(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	for name, tc := range map[string]struct {
		keys *leapmuxv1.WorkerPublicKeys
		want string
	}{
		"a short x25519 key": {clone(func(k *leapmuxv1.WorkerPublicKeys) { k.X25519 = k.X25519[:31] }), "x25519"},
		"a short mlkem key":  {clone(func(k *leapmuxv1.WorkerPublicKeys) { k.Mlkem = k.Mlkem[:1567] }), "mlkem"},
		"a short slhdsa key": {clone(func(k *leapmuxv1.WorkerPublicKeys) { k.Slhdsa = k.Slhdsa[:63] }), "slhdsa"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			err := workerkeys.Validate(tc.keys)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()

	a := workerkeystest.Keys(0x42)
	b := workerkeystest.Keys(0x42)
	require.True(t, workerkeys.Equal(a, b))

	assert.False(t, workerkeys.Equal(a, workerkeystest.Keys(0x41)), "a different set is not equal")
	assert.False(t, workerkeys.Equal(a, nil), "a missing set is not equal")
	assert.True(t, workerkeys.Equal(nil, nil),
		"the comparison is field-by-field on getters, so two empty sets are equal; Validate is what refuses an empty set")
	assert.False(t, workerkeys.Equal(a, &leapmuxv1.WorkerPublicKeys{
		X25519: a.GetX25519(), Mlkem: a.GetMlkem(), Slhdsa: a.GetSlhdsa()[:63],
	}), "a truncated slhdsa key is not equal")

	// The key set is the three keys; an unknown field is not part of it.
	newer := proto.Clone(a).(*leapmuxv1.WorkerPublicKeys)
	// Field 31, varint 0: one unknown field, which proto.Equal would compare.
	newer.ProtoReflect().SetUnknown([]byte{0xF8, 0x01, 0x00})
	assert.False(t, proto.Equal(a, newer), "the fixture must actually carry an unknown field")
	assert.True(t, workerkeys.Equal(a, newer), "unknown fields are not part of the key set")
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	keys := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	assert.Equal(t,
		noise.CompositeKeyFingerprint(keys.GetX25519(), keys.GetMlkem(), keys.GetSlhdsa()),
		workerkeys.Fingerprint(keys))
	assert.Equal(t, workerkeys.Fingerprint(keys), workerkeys.Fingerprint(workerkeystest.Keys(workerkeystest.SeededWorkerFill)))
	assert.NotEqual(t, workerkeys.Fingerprint(keys), workerkeys.Fingerprint(workerkeystest.Keys(0x43)))
}
