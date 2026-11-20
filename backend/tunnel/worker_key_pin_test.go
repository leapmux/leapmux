package tunnel

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// compositeHexOf returns the exact composite hex encoding of a generated key
// set: x25519 || mlkem || slhdsa, hex-encoded -- the encoding
// NewExpectedWorkerKeyPin decodes.
func compositeHexOf(t *testing.T) (string, *noiseutil.CompositeKeypair) {
	t.Helper()
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(ck)
	require.NoError(t, err)
	composite := append(append(append([]byte{}, keys.GetX25519()...), keys.GetMlkem()...), keys.GetSlhdsa()...)
	return hex.EncodeToString(composite), ck
}

func TestNewExpectedWorkerKeyPinDecodesTheExactComposite(t *testing.T) {
	compositeHex, ck := compositeHexOf(t)

	pin, err := NewExpectedWorkerKeyPin(compositeHex)
	require.NoError(t, err)

	expected, err := workerkeys.Of(ck)
	require.NoError(t, err)
	assert.True(t, workerkeys.Equal(pin.keys, expected))
}

func TestNewExpectedWorkerKeyPinRefusesMalformedExpectations(t *testing.T) {
	validHex, _ := compositeHexOf(t)
	validBytes, err := hex.DecodeString(validHex)
	require.NoError(t, err)

	// An all-zero X25519 key is sized correctly but every handshake refuses it.
	lowOrder := append([]byte{}, validBytes...)
	for i := 0; i < contracts.X25519PublicKeySize; i++ {
		lowOrder[i] = 0
	}

	tests := []struct {
		name  string
		hex   string
		errIs string
	}{
		{"empty", "", "want"},
		{"not hex", "zz", "decode the expected worker keys"},
		{"truncated set", validHex[:len(validHex)-2], "want"},
		{"low-order x25519 point", hex.EncodeToString(lowOrder), "unusable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pin, err := NewExpectedWorkerKeyPin(tt.hex)
			require.Error(t, err)
			assert.Nil(t, pin)
			assert.Contains(t, err.Error(), tt.errIs)
		})
	}
}

func TestExpectedWorkerKeyPinResolvesOnlyTheExpectedSet(t *testing.T) {
	compositeHex, ck := compositeHexOf(t)
	pin, err := NewExpectedWorkerKeyPin(compositeHex)
	require.NoError(t, err)

	expected, err := workerkeys.Of(ck)
	require.NoError(t, err)
	commit, err := pin.Resolve(context.Background(), "worker-a",
		expected.GetX25519(), expected.GetMlkem(), expected.GetSlhdsa())
	require.NoError(t, err)
	assert.Nil(t, commit, "an immutable expectation records nothing")
}

func TestExpectedWorkerKeyPinRefusesADifferentSet(t *testing.T) {
	compositeHex, ck := compositeHexOf(t)
	pin, err := NewExpectedWorkerKeyPin(compositeHex)
	require.NoError(t, err)

	otherHex, other := compositeHexOf(t)
	require.NotEqual(t, compositeHex, otherHex)
	presented, err := workerkeys.Of(other)
	require.NoError(t, err)

	commit, err := pin.Resolve(context.Background(), "worker-a",
		presented.GetX25519(), presented.GetMlkem(), presented.GetSlhdsa())
	require.Error(t, err)
	assert.Nil(t, commit)

	var mismatch *WorkerKeyMismatchError
	require.ErrorAs(t, err, &mismatch)
	expected, err := workerkeys.Of(ck)
	require.NoError(t, err)
	assert.Equal(t, workerkeys.Fingerprint(expected), mismatch.ExpectedFingerprint)
	assert.Equal(t, workerkeys.Fingerprint(presented), mismatch.PresentedFingerprint)
}

func TestExpectedWorkerKeyPinMismatchSurvivesWrapping(t *testing.T) {
	compositeHex, _ := compositeHexOf(t)
	pin, err := NewExpectedWorkerKeyPin(compositeHex)
	require.NoError(t, err)

	otherHex, _ := compositeHexOf(t)
	presented, err := hex.DecodeString(otherHex)
	require.NoError(t, err)
	x, m, s := splitComposite(t, presented)

	// OpenChannel wraps the pin error; the typed mismatch must survive it so
	// the desktop layer can claim a mismatch transition.
	wrapped := fmt.Errorf("worker key pin: %w",
		fmt.Errorf("open channel: %w", mustResolveErr(t, pin, x, m, s)))

	var mismatch *WorkerKeyMismatchError
	require.ErrorAs(t, wrapped, &mismatch)
	assert.NotEmpty(t, mismatch.ExpectedFingerprint)
	assert.NotEmpty(t, mismatch.PresentedFingerprint)
	assert.NotEqual(t, mismatch.ExpectedFingerprint, mismatch.PresentedFingerprint)
}

// splitComposite splits the composite encoding at the generated key sizes.
func splitComposite(t *testing.T, raw []byte) (x, m, s []byte) {
	t.Helper()
	require.Len(t, raw, contracts.X25519PublicKeySize+contracts.MlkemPublicKeySize+contracts.SlhdsaPublicKeySize)
	return raw[:contracts.X25519PublicKeySize],
		raw[contracts.X25519PublicKeySize : contracts.X25519PublicKeySize+contracts.MlkemPublicKeySize],
		raw[contracts.X25519PublicKeySize+contracts.MlkemPublicKeySize:]
}

func mustResolveErr(t *testing.T, pin *ExpectedWorkerKeyPin, x, m, s []byte) error {
	t.Helper()
	commit, err := pin.Resolve(context.Background(), "worker-a", x, m, s)
	require.Error(t, err)
	assert.Nil(t, commit)
	return err
}
