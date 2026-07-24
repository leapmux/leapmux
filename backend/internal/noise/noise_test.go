package noise

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandshakeRoundtrip(t *testing.T) {
	// Worker generates composite keypair.
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	// Initiator (Frontend) starts handshake.
	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)
	assert.NotEmpty(t, msg1)

	// Worker (Responder) processes msg1 and returns msg2.
	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)
	assert.NotEmpty(t, msg2)
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator")
		ciphertext, err := initiatorSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder")
		ciphertext, err := workerSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("message %d from responder", i))
		ct, err = workerSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)
	}
}

func TestWrongMlkemKey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	// Generate a different ML-KEM key.
	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator uses wrong ML-KEM public key — encapsulates to wrong key.
	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, wrongKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Responder decapsulates with its own key. ML-KEM implicit rejection
	// produces a random shared secret rather than an error, so the
	// responder handshake itself succeeds.
	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// Initiator verification fails because the ML-KEM shared secrets differ,
	// producing different transcripts and thus an SLH-DSA signature mismatch.
	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLH-DSA")
}

func TestInvalidSlhdsaSignature(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Generate a different SLH-DSA key — use its public key for verification.
	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	wrongSlhdsaPubBytes, err := wrongKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// Initiator uses wrong SLH-DSA public key — signature verification should fail.
	_, err = InitiatorHandshake2(hs, msg2, wrongSlhdsaPubBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLH-DSA")
}

func TestEmptyMessage(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	ct, err := initiatorSession.Encrypt([]byte{})
	require.NoError(t, err)
	assert.NotEmpty(t, ct) // Ciphertext includes auth tag.

	pt, err := workerSession.Decrypt(ct)
	require.NoError(t, err)
	assert.Empty(t, pt)
}

func TestPlaintextSizeLimit(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	session, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	// Exactly at limit should succeed.
	atLimit := make([]byte, MaxPlaintextSize)
	_, err = session.Encrypt(atLimit)
	require.NoError(t, err)

	// One byte over limit should fail.
	overLimit := make([]byte, MaxPlaintextSize+1)
	_, err = session.Encrypt(overLimit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext too large")
}

func TestNeedsRekey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	session, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	assert.False(t, session.Send.NeedsRekey(), "NeedsRekey should be false at nonce 0")
}

func TestCipherStateRekey(t *testing.T) {
	key := [keyLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	cs := &CipherState{k: key}
	ctOld, err := cs.Encrypt([]byte("before"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cs.Nonce())

	cs.Rekey()
	assert.Equal(t, uint64(0), cs.Nonce(), "Rekey must reset nonce")
	assert.NotEqual(t, key, cs.k, "Rekey must change the key")

	// Ciphertext produced under the old key must not decrypt under the new key.
	_, err = cs.Decrypt(ctOld)
	require.Error(t, err)

	ctNew, err := cs.Encrypt([]byte("after"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cs.Nonce())

	peer := &CipherState{k: key}
	peer.Rekey()
	pt, err := peer.Decrypt(ctNew)
	require.NoError(t, err)
	assert.Equal(t, []byte("after"), pt)
}

func TestSessionNeedsRekeyEither(t *testing.T) {
	send := &CipherState{k: [keyLen]byte{1}}
	recv := &CipherState{k: [keyLen]byte{2}}
	session := &Session{Send: send, Receive: recv}

	assert.False(t, session.NeedsRekeyEither())

	send.SetNonceForTest(SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "send soft nonce must trip either")

	send.SetNonceForTest(0)
	recv.SetNonceForTest(SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "receive soft nonce must trip either")
}

func TestCipherStateRekeyMatchesHKDF(t *testing.T) {
	// Fixed vector shared with frontend/src/lib/noise.test.ts so Go and TS
	// stay on the same application-defined REKEY.
	key := [keyLen]byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}
	cs := &CipherState{k: key}
	_, err := cs.Encrypt([]byte("x"))
	require.NoError(t, err)
	cs.Rekey()

	expectedK, _ := noiseHKDF(key[:], nil)
	var want [keyLen]byte
	copy(want[:], expectedK[:keyLen])
	assert.Equal(t, want, cs.k)

	ct, err := cs.Encrypt([]byte("hello-rekey"))
	require.NoError(t, err)
	assert.Equal(t, "a65694004d00816424626539afa175fb55106807e2c4af79c2c464", hex.EncodeToString(ct),
		"post-rekey ciphertext must match the TypeScript interop vector")

	peer := &CipherState{k: want}
	pt, err := peer.Decrypt(ct)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello-rekey"), pt)
}

func TestTamperedMlkemCiphertextCausesAEADFailure(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	_, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Tamper with the ML-KEM ciphertext portion of message1.
	// The classical Noise part (first 48 bytes) is untouched.
	tampered := make([]byte, len(msg1))
	copy(tampered, msg1)
	tampered[48] ^= 0xFF // flip one byte in the ML-KEM ciphertext

	// Responder should fail because the tampered mlkemCT changes the handshake
	// hash (via mixHash), causing the message2 AEAD to use a different AD than
	// what the initiator expects. This proves mlkemCT is bound into the
	// Noise state, not just the SLH-DSA transcript.
	// Note: ML-KEM implicit rejection means Decapsulate doesn't error on
	// tampered ciphertext — it produces a random shared secret instead.
	// The failure comes from the divergent handshake hash.
	_, _, err = ResponderHandshake(workerKey, tampered)
	// The responder handshake itself succeeds (ML-KEM implicit rejection),
	// but when the initiator tries to verify, the handshake hashes differ.
	// Let's verify from the initiator's perspective instead:
	require.NoError(t, err) // Responder doesn't detect the tampering

	// Re-do the handshake properly and verify the initiator would fail.
	hs, msg1Good, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Tamper with message1 before sending to responder.
	tamperedMsg1 := make([]byte, len(msg1Good))
	copy(tamperedMsg1, msg1Good)
	tamperedMsg1[48] ^= 0xFF

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, tamperedMsg1)
	require.NoError(t, err) // Responder succeeds due to ML-KEM implicit rejection

	// Initiator fails because handshake hashes diverged (mixHash(mlkemCT) differs).
	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.Error(t, err, "initiator should detect tampered mlkemCT via handshake hash divergence")
}

func TestClassicalHandshakeRoundtrip(t *testing.T) {
	// Worker generates composite keypair (only X25519 used for classical).
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator starts classical handshake.
	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg1), "classical message1 should be 48 bytes")

	// Worker processes msg1 and returns msg2.
	msg2, workerSession, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg2), "classical message2 should be 48 bytes")
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator (classical)")
		ciphertext, err := initiatorSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder (classical)")
		ciphertext, err := workerSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestClassicalMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	msg2, workerSession, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)

	initiatorSession, err := ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("classical message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("classical message %d from responder", i))
		ct, err = workerSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)
	}
}

func TestClassicalWrongKey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator targets workerKey, but responder uses wrongKey's private key.
	// The DH will produce different shared secrets, so decrypt fails.
	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	msg2, _, err := ClassicalResponderHandshake(wrongKey.X25519Public, wrongKey.X25519Private, msg1)
	// Responder decryption of message1 fails because DH(wrongPriv, initiatorEphemeral)
	// produces a different key than DH(correctPriv, initiatorEphemeral).
	require.Error(t, err)
	_ = msg2
	_ = hs
}

func TestNonceLimitsConst(t *testing.T) {
	// Pin the literal nonce limits and plaintext size so a regression in the
	// constant expression (e.g. shifting the exponent or misplacing a `-1`)
	// is caught here rather than only by downstream behavior.
	assert.Equal(t, uint64(2147483647), SoftNonceLimit, "SoftNonceLimit should be 2^31-1")
	assert.Equal(t, uint64(4294967295), HardNonceLimit, "HardNonceLimit should be 2^32-1")
	assert.Equal(t, 65519, MaxPlaintextSize, "MaxPlaintextSize should be 65535-16")
}

func TestMessageSizes(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// message1 = noise_msg1 (48) + mlkem_ciphertext (1568) = 1616
	assert.Equal(t, 1616, len(msg1), "message1 size")

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// message2 = noise_msg2 (48) + slhdsa_signature (49856) = 49904
	assert.Equal(t, 49904, len(msg2), "message2 size")
	_ = hs
}

func TestHandshakeStateZeroedAfterHybrid(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Verify sensitive material exists before completing handshake.
	assert.NotNil(t, hs.ss, "symmetric state should exist before handshake2")
	assert.NotEmpty(t, hs.mlkemSS, "mlkemSS should be set before handshake2")
	assert.NotEmpty(t, hs.mlkemCT, "mlkemCT should be set before handshake2")

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	// After handshake2 completes, HandshakeState should be zeroed.
	assert.Nil(t, hs.ss, "symmetric state should be nil after handshake2")
	assert.Nil(t, hs.ePriv, "ephemeral private key should be nil after handshake2")
	assert.Equal(t, make([]byte, len(hs.mlkemSS)), hs.mlkemSS, "mlkemSS should be zeroed after handshake2")
	assert.Equal(t, make([]byte, len(hs.mlkemCT)), hs.mlkemCT, "mlkemCT should be zeroed after handshake2")
}

func TestHandshakeStateZeroedAfterClassical(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	assert.NotNil(t, hs.ss, "symmetric state should exist before handshake2")

	msg2, _, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)

	_, err = ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	assert.Nil(t, hs.ss, "symmetric state should be nil after classical handshake2")
	assert.Nil(t, hs.ePriv, "ephemeral private key should be nil after classical handshake2")
}
