package noise

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPrintInteropVector generates a test vector that can be used to verify
// the TypeScript hybrid Noise_NK implementation against the Go implementation.
//
// To use: run `go test -run TestPrintInteropVector -v` and copy the output
// into a TypeScript test that verifies the same values.
func TestPrintInteropVector(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	fmt.Printf("x25519PublicKey: %s\n", hex.EncodeToString(workerKey.X25519Public))
	fmt.Printf("x25519PrivateKey: %s\n", hex.EncodeToString(workerKey.X25519Private))
	fmt.Printf("mlkemPublicKey (len=%d): %s\n", len(workerKey.MlkemPublicKeyBytes()), hex.EncodeToString(workerKey.MlkemPublicKeyBytes()))
	fmt.Printf("slhdsaPublicKey (len=%d): %s\n", len(slhdsaPubBytes), hex.EncodeToString(slhdsaPubBytes))

	// Go initiator creates message1.
	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)
	fmt.Printf("message1 (len=%d): %s\n", len(msg1), hex.EncodeToString(msg1))

	// Go responder processes message1.
	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)
	fmt.Printf("message2 (len=%d): %s\n", len(msg2), hex.EncodeToString(msg2))

	// Go initiator completes handshake.
	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	// Encrypt a test message initiator -> responder.
	testMsg := []byte("hello")
	ct, err := initiatorSession.Encrypt(testMsg)
	require.NoError(t, err)
	fmt.Printf("encrypted 'hello' (initiator->responder): %s\n", hex.EncodeToString(ct))

	// Verify responder can decrypt.
	pt, err := workerSession.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, testMsg, pt)
}
